package direct

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
	"github.com/nextroad-dev/Auto-Router/internal/providers/adapter"
)

// retainedGenerations is how many catalog generations keep their executor set
// addressable. A request pins the generation it was routed against and resolves
// its executor (including every failover attempt) from that generation, so an
// in-flight request survives this many registry reloads without switching to a
// newer endpoint or credential.
const retainedGenerations = 16

// Options is the transport policy applied to every executor the registry builds.
type Options struct {
	// StreamIdleTimeout bounds the gap between reads of a streaming body. Zero
	// disables it.
	StreamIdleTimeout time.Duration
	// BodyTimeout bounds the gap between reads of a non-stream body. Zero
	// disables it.
	BodyTimeout time.Duration
	// MaxBufferedResponseBytes bounds a converted non-stream response. Zero uses
	// the adapter default.
	MaxBufferedResponseBytes int64
}

// Registry holds the executor sets of the most recent catalog generations. Reads
// are lock-free; a rebuild publishes a complete new state in one atomic swap.
type Registry struct {
	// buildMutex serializes Build so generations are published in order.
	buildMutex sync.Mutex
	options    atomic.Pointer[Options]
	current    atomic.Pointer[registryState]
}

type registryState struct {
	latest *snapshot
	// byGeneration holds latest plus up to retainedGenerations-1 predecessors.
	byGeneration map[uint64]*snapshot
	order        []uint64
}

type snapshot struct {
	generation uint64
	executors  map[string]providers.Executor
}

// NewRegistry returns a registry with an empty, non-nil snapshot.
func NewRegistry() *Registry {
	registry := &Registry{}
	empty := &snapshot{executors: map[string]providers.Executor{}}
	registry.current.Store(&registryState{latest: empty, byGeneration: map[uint64]*snapshot{0: empty}, order: []uint64{0}})
	registry.options.Store(&Options{})
	return registry
}

// Configure sets the transport policy for executors built by later Build calls.
func (r *Registry) Configure(options Options) {
	if r == nil {
		return
	}
	r.options.Store(&options)
}

// Build constructs executors from catalog and atomically publishes them as the
// executor set of catalog.Generation. Providers without a base URL or with an
// invalid endpoint are skipped so one unusable row cannot take healthy providers
// offline.
//
// Build must run before the catalog it describes becomes visible to requests
// (see models.Store.SwapWith), so a request that observes generation N can always
// find executors for generation N.
func (r *Registry) Build(catalog *models.Catalog) {
	if r == nil {
		return
	}
	options := *r.options.Load()
	newExecutors := map[string]providers.Executor{}
	var generation uint64
	if catalog != nil {
		newExecutors = make(map[string]providers.Executor, len(catalog.Providers))
		generation = catalog.Generation
		for _, provider := range catalog.Providers {
			if provider.BaseURL == "" {
				continue
			}
			kind := provider.Kind
			if kind == "" {
				kind = models.ProviderOpenAICompatible
			}
			if !kind.Valid() {
				continue
			}
			executor, err := New(Config{
				BaseURL:           provider.BaseURL,
				APIKey:            provider.APIKey,
				ProviderKey:       provider.Key,
				Kind:              kind,
				StreamIdleTimeout: options.StreamIdleTimeout,
				BodyTimeout:       options.BodyTimeout,
			})
			if err != nil {
				continue
			}
			adapted, err := adapter.New(kind, executor)
			if err != nil {
				executor.CloseIdleConnections()
				continue
			}
			adapted.MaxBufferedBytes = options.MaxBufferedResponseBytes
			newExecutors[provider.Key] = adapted
		}
	}
	next := &snapshot{generation: generation, executors: newExecutors}

	r.buildMutex.Lock()
	defer r.buildMutex.Unlock()
	previous := r.current.Load()
	state := &registryState{
		latest:       next,
		byGeneration: make(map[uint64]*snapshot, retainedGenerations),
	}
	for _, retained := range previous.order {
		if retained == generation {
			continue
		}
		state.order = append(state.order, retained)
	}
	state.order = append(state.order, generation)
	if len(state.order) > retainedGenerations {
		state.order = state.order[len(state.order)-retainedGenerations:]
	}
	for _, retained := range state.order {
		if retained == generation {
			state.byGeneration[retained] = next
			continue
		}
		state.byGeneration[retained] = previous.byGeneration[retained]
	}
	r.current.Store(state)
	// Idle pools of the superseded set are released. Requests still pinned to it
	// keep working: closing idle connections does not stop the transport.
	if old := previous.latest; old != nil && old != next {
		for _, executor := range old.executors {
			if closer, ok := executor.(interface{ CloseIdleConnections() }); ok {
				closer.CloseIdleConnections()
			}
		}
	}
}

// Get returns the executor of the latest generation.
func (r *Registry) Get(providerKey string) (providers.Executor, bool) {
	if r == nil {
		return nil, false
	}
	executor, ok := r.current.Load().latest.executors[providerKey]
	return executor, ok
}

// GetGeneration returns the executor built for one catalog generation. Zero
// means "no generation pinned" and resolves against the latest set. A generation
// that is no longer retained reports false rather than silently falling back to
// a different configuration.
func (r *Registry) GetGeneration(generation uint64, providerKey string) (providers.Executor, bool) {
	if r == nil {
		return nil, false
	}
	state := r.current.Load()
	if generation == 0 {
		executor, ok := state.latest.executors[providerKey]
		return executor, ok
	}
	snap, ok := state.byGeneration[generation]
	if !ok {
		return nil, false
	}
	executor, ok := snap.executors[providerKey]
	return executor, ok
}

// Generation reports the catalog generation represented by the latest set.
func (r *Registry) Generation() uint64 {
	if r == nil {
		return 0
	}
	return r.current.Load().latest.generation
}
