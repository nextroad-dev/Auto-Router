// Package settings owns the runtime configuration overlay: the mutable subset of
// the configuration that the Admin API may change without a restart, compiled into
// one immutable snapshot and published atomically.
//
// The contract this package guarantees:
//
//   - One snapshot per read: Current never returns nil and never returns a
//     half-updated value. A reader that calls Current once gets one complete
//     configuration and the policy engine compiled from it; a request in flight
//     keeps using the snapshot it started with.
//   - No new states: a candidate overlay is merged onto the file and default
//     values and then validated and compiled with the very same code the startup
//     path uses (config.Validate and config.PolicyEngine). The management surface
//     cannot express a configuration a configuration file could not.
//   - Compile before commit: validation, compilation and the database write happen
//     in that order. A candidate that fails either check leaves the stored overlay
//     and the published snapshot untouched.
//   - Optimistic concurrency: one write succeeds per stored version. A concurrent
//     writer is told to re-read rather than silently overwriting a colleague's
//     change.
//
// The package is assembled only by cmd/auto-router. Request-path packages read
// their live values through minimal interfaces they define themselves, so this
// package can depend on configuration and policy without any of them depending on
// it.
package settings

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/router/policy"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

// Snapshot is one complete, immutable runtime configuration. Config and Engine
// are always consistent with each other: Engine is the compilation of Config's
// policy section, and both were produced from the same candidate.
type Snapshot struct {
	// Config is the effective configuration: code defaults with the SQLite overlay applied.
	Config config.Config
	// Runtime is an optional prepared component set (for example the Jev client)
	// built and validated before the corresponding settings transaction commits.
	Runtime any
	// Engine is the policy engine compiled from Config. It is never nil for a
	// snapshot produced by this package.
	Engine *policy.Engine
	// Version is the stored overlay version this snapshot was built from. Version
	// zero means "no overlay", which is the state after a reset or before the first
	// write.
	Version int64
	// Overlay is the runtime overlay itself, as a decoded document, or nil when
	// there is none.
	Overlay map[string]any
}

// Store publishes settings snapshots and applies administrator writes.
//
// A Store is safe for concurrent use. Readers call Current without a lock; the
// write path serializes itself with a mutex so two concurrent PATCH requests
// cannot both compile and then race to publish.
type Store struct {
	base    config.Config
	sources config.Sources
	db      *sql.DB
	logger  *slog.Logger
	prepare func(config.Config) (any, error)
	mutex   chan struct{}
	current atomic.Pointer[Snapshot]
	// observers are called once per published snapshot, in registration order, under
	// the write lock. They exist so a component that owns a value directly (the logging
	// writer's atomic switches, for instance) can mirror an accepted change without
	// reading the snapshot per operation. An observer must not block: it runs inside the
	// write that published the snapshot.
	observers []func(Snapshot)
	// engineSource is the single callback that supplies the compiled engine to a
	// reader holding its own reference.
	engineSource func(Snapshot)
}

// Options wires the store.
type Options struct {
	// Base is the effective configuration loaded from defaults, the file and the
	// environment. It is the value every overlay is merged onto.
	Base config.Config
	// Sources is the provenance of the base configuration, reported by the
	// settings endpoint.
	Sources config.Sources
	// DB is the database holding the settings table. It may be nil, in which case
	// the store is read-only and every write is refused rather than silently lost.
	DB *sql.DB
	// Logger receives one INFO line per applied change. Nil disables it.
	Logger *slog.Logger
	// Prepare constructs and validates runtime-dependent clients for a candidate.
	// It runs before persistence; the returned value is published in the same
	// immutable snapshot as the effective configuration.
	Prepare func(config.Config) (any, error)
}

// ErrReadOnly means the store has no database and cannot persist a change.
var ErrReadOnly = errors.New("runtime settings cannot be changed: the process has no database")

// ErrConflict is the write conflict reported when the stored version moved.
var ErrConflict = storage.ErrSettingsConflict

// New builds the store and publishes the initial snapshot. The stored overlay is
// read and compiled here, so a process that cannot serve its own stored settings
// fails at startup rather than on the first request.
func New(ctx context.Context, options Options) (*Store, error) {
	store := &Store{
		base:    options.Base,
		sources: options.Sources,
		db:      options.DB,
		logger:  options.Logger,
		prepare: options.Prepare,
		mutex:   make(chan struct{}, 1),
	}
	if store.logger == nil {
		store.logger = slog.New(slog.DiscardHandler)
	}
	var record storage.SettingsRecord
	found := false
	if store.db != nil {
		loaded, ok, err := storage.LoadSettings(ctx, store.db)
		if err != nil {
			return nil, err
		}
		record, found = loaded, ok
	}
	overlay, err := decodeOverlay(record.JSON)
	if err != nil {
		return nil, fmt.Errorf("the stored runtime settings are unreadable: %w", err)
	}
	snapshot, err := build(options.Base, overlay, record.Version, found)
	if err != nil {
		// A stored overlay that no longer validates is a configuration error the
		// operator has to see. Refusing to start is deliberate: silently dropping
		// the overlay would route traffic on a policy nobody chose.
		return nil, fmt.Errorf("the stored runtime settings are not usable: %w", err)
	}
	if store.prepare != nil {
		snapshot.Runtime, err = store.prepare(snapshot.Config)
		if err != nil {
			return nil, fmt.Errorf("prepare runtime settings: %w", err)
		}
	}
	if found {
		store.logger.Info("runtime settings overlay applied", "version", record.Version)
	}
	store.current.Store(snapshot)
	return store, nil
}

// Observe registers a callback that runs after every published snapshot. It is
// called for the current snapshot too, so a caller can register once and be
// consistent immediately rather than waiting for the next change.
func (s *Store) Observe(observer func(Snapshot)) {
	if s == nil || observer == nil {
		return
	}
	s.lock()
	defer s.unlock()
	s.observers = append(s.observers, observer)
	observer(*s.Current())
}

// SetEngineSource registers the callback that supplies the compiled engine to a
// reader which took a copy of it at construction. It is Observe's specialized form,
// and it exists so a caller cannot accidentally register two different engine
// sources.
func (s *Store) SetEngineSource(observer func(Snapshot)) {
	if s == nil || observer == nil {
		return
	}
	s.lock()
	defer s.unlock()
	s.engineSource = observer
	observer(*s.Current())
}

// notify runs the registered observers after a snapshot was published. It is called
// while the write lock is held, so an observer sees a stable snapshot and cannot
// interleave with a second write.
func (s *Store) notify(snapshot Snapshot) {
	for _, observer := range s.observers {
		observer(snapshot)
	}
	if s.engineSource != nil {
		s.engineSource(snapshot)
	}
}

// Current returns the published snapshot. It never returns nil, so a reader may
// dereference the result without a nil check.
func (s *Store) Current() *Snapshot {
	if s == nil {
		return nil
	}
	snapshot := s.current.Load()
	if snapshot == nil {
		// Only reachable for a zero-value Store built outside New. Reporting the
		// process defaults is more useful than a nil dereference on a request path.
		return &Snapshot{Config: config.Defaults()}
	}
	return snapshot
}

// Change is one accepted settings change: the overlay to store and the facts the
// response reports.
type Change struct {
	// Document is the overlay to persist.
	Document map[string]any
	// Changes lists the dotted setting paths this request changed, in a
	// deterministic order.
	Changes []string
	// Warnings are the effective configuration's warnings after the change.
	Warnings []string
}

// ApplyResult is what one successful write produced.
type ApplyResult struct {
	Version  int64
	Config   config.Config
	Changes  []string
	Warnings []string
	// Effective is the masked effective settings document.
	Effective map[string]any
}

// Apply merges, validates, compiles and then persists an overlay change.
//
// The order is the contract: the candidate is merged and validated first, the
// policy engine is compiled second, and only then is the database written. A
// candidate that fails either check leaves both the stored overlay and the
// published snapshot exactly as they were.
func (s *Store) Apply(ctx context.Context, patch map[string]any) (ApplyResult, error) {
	if s == nil {
		return ApplyResult{}, ErrReadOnly
	}
	// One writer at a time: compiling and publishing is not atomic with respect to
	// another writer, and a lost update is exactly the failure this prevents.
	s.lock()
	defer s.unlock()

	if err := s.requireWritable(); err != nil {
		return ApplyResult{}, err
	}
	// The catalogue is consulted first, so a request that names a setting the process
	// cannot apply at runtime is refused before anything is merged or compiled. The API
	// applies the same check to answer with a field; keeping it here as well means the
	// invariant holds for every caller of this package, not only for the HTTP one.
	if err := ValidatePatch(patch); err != nil {
		return ApplyResult{}, err
	}
	current := s.Current()
	merged, changes, err := mergeOverlay(current.Overlay, patch)
	if err != nil {
		return ApplyResult{}, err
	}
	document, err := json.Marshal(merged)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("encode the runtime settings: %w", err)
	}
	// Compilation happens before the write, so a candidate the policy engine
	// refuses can never reach the database.
	candidate, err := build(s.base, merged, current.Version+1, true)
	if err != nil {
		return ApplyResult{}, err
	}
	if s.prepare != nil {
		candidate.Runtime, err = s.prepare(candidate.Config)
		if err != nil {
			return ApplyResult{}, fmt.Errorf("prepare runtime settings: %w", err)
		}
	}
	version, err := storage.WriteSettings(ctx, s.db, string(document), current.Version)
	if err != nil {
		return ApplyResult{}, err
	}
	candidate.Version = version
	s.current.Store(candidate)
	s.notify(*candidate)
	s.logger.Info("runtime settings changed", "version", version, "settings", changes)
	return ApplyResult{
		Version:   version,
		Config:    candidate.Config,
		Changes:   changes,
		Warnings:  candidate.Config.Warnings(),
		Effective: EffectiveDocument(candidate.Config, s.sources, true),
	}, nil
}

// Reset removes the overlay and republishes the base configuration.
func (s *Store) Reset(ctx context.Context) (ApplyResult, error) {
	if s == nil {
		return ApplyResult{}, ErrReadOnly
	}
	s.lock()
	defer s.unlock()
	if err := s.requireWritable(); err != nil {
		return ApplyResult{}, err
	}
	snapshot, err := build(s.base, nil, 0, false)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("the code defaults are no longer usable: %w", err)
	}
	if s.prepare != nil {
		snapshot.Runtime, err = s.prepare(snapshot.Config)
		if err != nil {
			return ApplyResult{}, fmt.Errorf("prepare default runtime settings: %w", err)
		}
	}
	if err := storage.ResetSettings(ctx, s.db); err != nil {
		return ApplyResult{}, err
	}
	s.current.Store(snapshot)
	s.notify(*snapshot)
	s.logger.Info("runtime settings overlay cleared")
	return ApplyResult{
		Version:   0,
		Config:    snapshot.Config,
		Warnings:  snapshot.Config.Warnings(),
		Effective: EffectiveDocument(snapshot.Config, s.sources, true),
	}, nil
}

// Snapshot returns the effective configuration for a request or report that must
// not race a concurrent write.
func (s *Store) Config() config.Config { return s.Current().Config }

// Base returns the configuration the overlay is merged onto.
func (s *Store) Base() config.Config { return s.base }

// Sources returns the provenance of the base configuration.
func (s *Store) Sources() config.Sources { return s.sources }

// HasOverlay reports whether a runtime overlay is currently in effect.
func (s *Store) HasOverlay() bool {
	snapshot := s.Current()
	return snapshot != nil && len(snapshot.Overlay) > 0
}

func (s *Store) requireWritable() error {
	if s.db == nil {
		return ErrReadOnly
	}
	return nil
}

func (s *Store) lock()   { s.mutex <- struct{}{} }
func (s *Store) unlock() { <-s.mutex }

// decodeOverlay parses a stored overlay document. An empty document means "no
// overlay" rather than an empty map, so HasOverlay reports the truth.
func decodeOverlay(document string) (map[string]any, error) {
	if strings.TrimSpace(document) == "" {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(document)))
	decoder.UseNumber()
	var merged map[string]any
	if err := decoder.Decode(&merged); err != nil {
		return nil, errors.New("the stored overlay is not a JSON object")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("the stored overlay must contain exactly one JSON object")
	}
	if len(merged) == 0 {
		return nil, nil
	}
	return merged, nil
}

// build merges an overlay onto the base configuration, validates it and compiles
// its policy engine. It is the single place where a candidate becomes a snapshot,
// which is why the startup path and every write share it.
func build(base config.Config, overlay map[string]any, version int64, hasOverlay bool) (*Snapshot, error) {
	candidate := base
	if len(overlay) > 0 {
		encoded, err := json.Marshal(overlay)
		if err != nil {
			return nil, fmt.Errorf("encode the runtime overlay: %w", err)
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&candidate); err != nil {
			return nil, fmt.Errorf("the runtime overlay is not accepted: %w", err)
		}
	}
	if err := candidate.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	engine, err := candidate.PolicyEngine()
	if err != nil {
		return nil, err
	}
	snapshot := &Snapshot{Config: candidate, Engine: engine}
	if hasOverlay {
		// A copy keeps the published snapshot independent of the caller's map.
		copied := make(map[string]any, len(overlay))
		for key, value := range overlay {
			copied[key] = value
		}
		snapshot.Overlay = copied
		snapshot.Version = version
	}
	return snapshot, nil
}

// mergeOverlay applies a nested patch onto an existing overlay. The result is a
// deep merge: patching routing.policy.default_model does not drop
// routing.policy.deny_models, and a nested object is kept only where it carries at
// least one value.
//
// The reported change list names the dotted paths the request actually set, in the
// order of the settings the API documents, so a response never depends on Go map
// iteration.
func mergeOverlay(current map[string]any, patch map[string]any) (map[string]any, []string, error) {
	if len(patch) == 0 {
		return nil, nil, errors.New("the request body must contain at least one setting")
	}
	merged := deepCopyMap(current)
	if merged == nil {
		merged = map[string]any{}
	}
	changes := make([]string, 0, len(patch))
	if err := mergeInto(merged, patch, "", &changes); err != nil {
		return nil, nil, err
	}
	sortPaths(changes)
	pruneEmpty(merged)
	return merged, changes, nil
}

func mergeInto(destination, patch map[string]any, prefix string, changes *[]string) error {
	for key, value := range patch {
		// The key is checked here rather than at JSON decode time so the error names
		// the offending path instead of a byte offset. A key that is not a known
		// setting is refused by the typed decode later; this check exists so a key
		// with a JSON-path-like shape cannot even be inspected further.
		if key == "" || strings.ContainsAny(key, "\x00") {
			return errors.New("a setting name is empty or contains NUL")
		}
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		nested, isObject := value.(map[string]any)
		if !isObject {
			destination[key] = value
			*changes = append(*changes, path)
			continue
		}
		if len(nested) == 0 {
			// An empty object is a no-op rather than a request to clear a section:
			// "clear everything under this key" is expressed by naming the settings.
			continue
		}
		existing, _ := destination[key].(map[string]any)
		if existing == nil {
			existing = map[string]any{}
		}
		if err := mergeInto(existing, nested, path, changes); err != nil {
			return err
		}
		destination[key] = existing
	}
	return nil
}

func deepCopyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	copied := make(map[string]any, len(source))
	for key, value := range source {
		if nested, ok := value.(map[string]any); ok {
			copied[key] = deepCopyMap(nested)
			continue
		}
		copied[key] = value
	}
	return copied
}

// pruneEmpty removes the nested objects that carry nothing, so an overlay for a
// single scalar does not persist one empty object per configured section.
func pruneEmpty(document map[string]any) {
	for key, value := range document {
		nested, ok := value.(map[string]any)
		if !ok {
			continue
		}
		pruneEmpty(nested)
		if len(nested) == 0 {
			delete(document, key)
		}
	}
}

// sortPaths orders changed paths so the response is deterministic. Sorting by the
// path is deliberate: two identical requests produce identical output regardless
// of the order their keys arrived in.
func sortPaths(paths []string) {
	for i := 1; i < len(paths); i++ {
		for j := i; j > 0 && paths[j] < paths[j-1]; j-- {
			paths[j], paths[j-1] = paths[j-1], paths[j]
		}
	}
}
