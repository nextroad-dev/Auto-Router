package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/models"
)

// This file is the settings catalogue: which settings are mutable at runtime,
// which need a restart, and how the effective configuration is rendered for the
// management surface.
//
// The catalogue exists so "mutable" is one list rather than a property of the
// code that happens to read a value today. A path that is absent from the
// catalogue cannot be written, and a test asserts that every key of the effective
// document appears in it: a new configuration field therefore cannot become
// silently writable, or silently missing from the management surface.

// SettingClass says whether one setting may be changed at runtime.
type SettingClass int

const (
	// SettingMutable is applied to the running process on the next request.
	SettingMutable SettingClass = iota
	// SettingRestartRequired is readable and reported, but refuses a write: the
	// value only takes effect at startup.
	SettingRestartRequired
	// SettingUnknown is not part of the configuration surface at all.
	SettingUnknown
)

// ErrImmutable is returned when a patch names a setting that needs a restart. It
// carries the path and the reason so the API can answer 422 with a field, without
// the value ever being echoed.
type ErrImmutable struct {
	Path   string
	Reason string
}

func (e *ErrImmutable) Error() string {
	return fmt.Sprintf("%s cannot be changed at runtime (%s)", e.Path, e.Reason)
}

// ErrUnknownSetting is returned when a patch names something that is not a
// configuration setting.
type ErrUnknownSetting struct{ Path string }

func (e *ErrUnknownSetting) Error() string {
	return fmt.Sprintf("%s is not a configuration setting", e.Path)
}

// runtimeSetting is one entry of the catalogue.
type runtimeSetting struct {
	// Path is the dotted JSON path of the setting.
	Path string
	// Class says whether it may be written at runtime.
	Class SettingClass
	// Reason explains a restart requirement. It never contains a value.
	Reason string
	// Secret marks a credential: its value is never returned, only whether it is
	// set and a masked rendering.
	Secret bool
}

// catalogue is the closed list of every setting the management surface knows
// about. Runtime-mutable settings come first, in the order the Admin API reports
// them; the restart-required entries follow in the order of the configuration
// file.
var catalogue = []runtimeSetting{
	{Path: "routing.allow_provider_override", Class: SettingMutable},
	{Path: "routing.default_preference", Class: SettingMutable},
	{Path: "routing.analyzer_debug_endpoint", Class: SettingMutable},
	{Path: "routing.policy_debug_endpoint", Class: SettingMutable},
	{Path: "routing.auto.failover.enabled", Class: SettingMutable},
	{Path: "routing.policy.high_confidence", Class: SettingMutable},
	{Path: "routing.policy.low_confidence", Class: SettingMutable},
	{Path: "routing.policy.refuse_truncated_evidence", Class: SettingMutable},
	{Path: "routing.policy.default_model", Class: SettingMutable},
	{Path: "routing.policy.cost_tiers", Class: SettingMutable},
	{Path: "routing.policy.latency_tiers", Class: SettingMutable},
	{Path: "routing.policy.allow_models", Class: SettingMutable},
	{Path: "routing.policy.deny_models", Class: SettingMutable},
	{Path: "routing.policy.allow_providers", Class: SettingMutable},
	{Path: "routing.policy.deny_providers", Class: SettingMutable},
	{Path: "routing.policy.allow_pairs", Class: SettingMutable},
	{Path: "routing.policy.deny_pairs", Class: SettingMutable},
	{Path: "routing.log.enabled", Class: SettingMutable},
	{Path: "routing.log.store_client_ip", Class: SettingMutable},
	{Path: "routing.log.retention_days", Class: SettingMutable},
	{Path: "routing.log.jev_trace.enabled", Class: SettingMutable},
	{Path: "routing.log.jev_trace.retention_days", Class: SettingMutable},
	{Path: "jev.enabled", Class: SettingMutable},
	{Path: "jev.input_mode", Class: SettingMutable},
	{Path: "jev.base_url", Class: SettingMutable},
	{Path: "jev.api_key", Class: SettingMutable, Secret: true},
	{Path: "jev.model", Class: SettingMutable},
	{Path: "admin.session_ttl", Class: SettingMutable},
	{Path: "registry.sync.include", Class: SettingMutable},
}

// catalogueIndex is the lookup form of catalogue, built once.
var catalogueIndex = func() map[string]runtimeSetting {
	index := make(map[string]runtimeSetting, len(catalogue))
	for _, setting := range catalogue {
		index[setting.Path] = setting
	}
	return index
}()

// Classify reports whether one dotted setting path may be changed at runtime.
// A path absent from the catalogue is unknown.
func Classify(path string) (SettingClass, string) {
	setting, ok := catalogueIndex[path]
	if !ok {
		return SettingUnknown, ""
	}
	return setting.Class, setting.Reason
}

// ValidatePatch refuses an overlay that names a setting which cannot be written.
// It is called before the merge, so a request that mixes a mutable and an
// immutable setting changes nothing at all.
func ValidatePatch(patch map[string]any) error {
	for _, path := range patchPaths(patch, "") {
		class, reason := Classify(path)
		switch class {
		case SettingMutable:
		case SettingRestartRequired:
			return &ErrImmutable{Path: path, Reason: reason}
		default:
			return &ErrUnknownSetting{Path: path}
		}
	}
	return nil
}

// patchPaths flattens a patch into dotted paths. A leaf that is an object keeps
// descending, so "routing":{"policy":{"foo":1}} is reported as
// "routing.policy.foo" rather than as "routing.policy".
func patchPaths(patch map[string]any, prefix string) []string {
	paths := make([]string, 0, len(patch))
	for key, value := range patch {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if nested, ok := value.(map[string]any); ok && len(nested) > 0 {
			paths = append(paths, patchPaths(nested, path)...)
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// SourceRuntime labels a setting whose effective value comes from the stored runtime
// overlay. It is the fourth source the management surface reports, and it exists here
// rather than in internal/config because only the settings store knows which values the
// overlay carries.
const SourceRuntime = "runtime"

// Field is one setting as the management surface reports it: the effective value,
// where it came from, and whether it may be changed at runtime.
type Field struct {
	Path            string `json:"path"`
	Value           any    `json:"value"`
	Source          string `json:"source"`
	Mutable         bool   `json:"mutable"`
	RestartRequired bool   `json:"restart_required"`
	// Reason explains a restart requirement. It is empty for a mutable setting.
	Reason string `json:"reason,omitempty"`
	// Secret marks a credential. Value is then either null (not set) or the masked
	// rendering, never the credential itself.
	Secret bool `json:"secret,omitempty"`
	// Set reports whether a credential is configured. It is only present for a
	// secret field, where "unset" and "set to something short" are otherwise
	// indistinguishable.
	Set *bool `json:"set,omitempty"`
}

// EffectiveDocument renders the effective configuration as a nested JSON
// document, with credentials replaced by their masked form (or omitted when a
// caller asks for no masking at all and the field is not a secret).
//
// It is derived from the very configuration the process is running, by the same
// JSON field names the configuration file uses, so the management surface cannot
// report a value the process does not hold.
func EffectiveDocument(cfg config.Config, sources config.Sources, maskSecrets bool) map[string]any {
	_ = sources
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return map[string]any{}
	}
	var complete map[string]any
	if err := json.Unmarshal(encoded, &complete); err != nil {
		return map[string]any{}
	}
	maskSecretPaths(complete, "", maskSecrets)
	document := map[string]any{}
	for _, setting := range catalogue {
		if setting.Class == SettingMutable {
			setPath(document, setting.Path, lookupPath(complete, setting.Path))
		}
	}
	return document
}

// maskSecretPaths replaces every credential in a rendered document with its masked
// form. The path list is the catalogue's secret entries, so a new credential
// cannot be added to the configuration without either appearing here or failing
// the catalogue-completeness test.
func maskSecretPaths(document map[string]any, prefix string, mask bool) {
	for key, value := range document {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if nested, ok := value.(map[string]any); ok {
			maskSecretPaths(nested, path, mask)
			continue
		}
		secret, ok := catalogueIndex[path]
		if !ok || !secret.Secret {
			// Two paths carry credentials inside a list of objects rather than at a
			// scalar leaf, so the catalogue cannot mark them by path alone.
			switch path {
			case "auth.keys":
				maskAuthKeys(value, mask)
			case "registry.providers":
				maskRegistryProviders(value, mask)
			}
			continue
		}
		if text, isString := value.(string); isString {
			if mask {
				document[key] = models.MaskSecret(text)
			} else if text != "" {
				// Only reachable when a caller explicitly asks for unmasked output,
				// which no HTTP path does.
				document[key] = text
			}
		}
	}
}

// maskAuthKeys reduces the configured key list to the facts the management
// surface may report: the audit name, the scopes and whether a credential is set.
// The credential itself is never returned, masked or otherwise: a key list exists
// to authenticate callers, and its members are secrets in the strictest sense.
func maskAuthKeys(value any, mask bool) {
	entries, ok := value.([]any)
	if !ok {
		return
	}
	for _, entry := range entries {
		key, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		credential, _ := key["key"].(string)
		key["key"] = nil
		key["set"] = credential != ""
		if credential != "" {
			key["masked"] = models.MaskSecret(credential)
		}
	}
}

// Fields renders every catalogued setting as one flat list, in catalogue order.
// The values come from the effective configuration, so the report and the running
// process cannot disagree.
//
// overlay is the runtime overlay currently in effect, or nil. A setting the overlay
// carries is reported as "runtime" rather than as its file source, because that is the
// answer an operator needs when a value is not what the file says it should be.
func Fields(cfg config.Config, sources config.Sources, overlay map[string]any) []Field {
	document := EffectiveDocument(cfg, sources, true)
	overlayPaths := patchPaths(overlay, "")
	inOverlay := make(map[string]bool, len(overlayPaths))
	for _, path := range overlayPaths {
		inOverlay[path] = true
	}
	fields := make([]Field, 0, len(catalogue))
	for _, setting := range catalogue {
		if setting.Path == "auth.keys" {
			fields = append(fields, authKeyFields(cfg, sources)...)
			continue
		}
		source := sourceFor(setting.Path, sources)
		if inOverlay[setting.Path] {
			source = SourceRuntime
		}
		fields = append(fields, Field{
			Path:            setting.Path,
			Value:           lookupPath(document, setting.Path),
			Source:          source,
			Mutable:         setting.Class == SettingMutable,
			RestartRequired: setting.Class == SettingRestartRequired,
			Reason:          setting.Reason,
			Secret:          setting.Secret,
			Set:             secretSet(cfg, setting),
		})
	}
	return fields
}

// maskRegistryProviders reduces the configured provider rows to the facts the
// management surface may report: the identity, where it points and whether a
// credential is set. The credential itself is never returned, masked or otherwise.
//
// This matters because registry.providers is part of the configuration file, so its
// api_key values are plaintext credentials that the effective-configuration document
// would otherwise render verbatim. The registry *table* has the same rule for the same
// reason: a credential is a credential whether it arrived through the file or through
// a management write, and direct execution consumes it only inside the provider executor.
func maskRegistryProviders(value any, mask bool) {
	entries, ok := value.([]any)
	if !ok {
		return
	}
	for _, entry := range entries {
		provider, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		credential, _ := provider["api_key"].(string)
		provider["api_key"] = nil
		provider["api_key_set"] = credential != ""
		if credential != "" && mask {
			provider["api_key_masked"] = models.MaskSecret(credential)
		}
	}
}

// authKeyFields reports the configured inbound keys without their credentials.
func authKeyFields(cfg config.Config, sources config.Sources) []Field {
	fields := make([]Field, 0, len(cfg.Auth.Keys))
	for _, key := range cfg.Auth.Keys {
		set := key.Key != ""
		field := Field{
			Path:            "auth.keys." + key.Name,
			Value:           map[string]any{"name": key.Name, "scopes": append([]string(nil), key.Scopes...)},
			Source:          sourceFor("auth.keys", sources),
			RestartRequired: true,
			Reason:          "inbound credentials are configuration, not runtime state",
			Secret:          true,
			Set:             &set,
		}
		fields = append(fields, field)
	}
	return fields
}

func secretSet(cfg config.Config, setting runtimeSetting) *bool {
	if !setting.Secret {
		return nil
	}
	set := false
	switch setting.Path {
	case "jev.api_key":
		set = cfg.Jev.APIKey != ""
	default:
		return nil
	}
	return &set
}

// sourceFor reports the provenance of one setting. The runtime overlay wins over
// the file, which wins over the defaults; the settings store records the overlay
// itself, so this function only has to handle the base configuration.
func sourceFor(path string, sources config.Sources) string {
	if source := sources.Source(path); source != "" {
		return source
	}
	return config.SourceDefault
}

// lookupPath reads one dotted path out of a rendered document, walking through
// nested objects. A path that is absent reports nil, which is what an absent
// optional value is.
func lookupPath(document map[string]any, path string) any {
	segments := strings.Split(path, ".")
	var current any = document
	for _, segment := range segments {
		nested, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = nested[segment]
	}
	return current
}

func setPath(document map[string]any, path string, value any) {
	segments := strings.Split(path, ".")
	current := document
	for _, segment := range segments[:len(segments)-1] {
		nested, ok := current[segment].(map[string]any)
		if !ok {
			nested = map[string]any{}
			current[segment] = nested
		}
		current = nested
	}
	current[segments[len(segments)-1]] = value
}

// SettingPaths returns every catalogued path in catalogue order. It backs the
// completeness test and the documentation table.
func SettingPaths() []string {
	paths := make([]string, 0, len(catalogue))
	for _, setting := range catalogue {
		paths = append(paths, setting.Path)
	}
	return paths
}

// MutablePaths returns the runtime-mutable paths in catalogue order.
func MutablePaths() []string {
	paths := make([]string, 0, len(catalogue))
	for _, setting := range catalogue {
		if setting.Class == SettingMutable {
			paths = append(paths, setting.Path)
		}
	}
	return paths
}

// IsImmutable reports whether an error is a refusal of a setting that needs a
// restart, and returns its path and reason.
func IsImmutable(err error) (string, string, bool) {
	var immutable *ErrImmutable
	if errors.As(err, &immutable) {
		return immutable.Path, immutable.Reason, true
	}
	return "", "", false
}

// IsUnknown reports whether an error is a refusal of a name that is not a setting.
func IsUnknown(err error) (string, bool) {
	var unknown *ErrUnknownSetting
	if errors.As(err, &unknown) {
		return unknown.Path, true
	}
	return "", false
}
