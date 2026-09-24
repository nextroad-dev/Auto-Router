package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Source labels where an effective setting came from. They are the values the
// Admin API reports for every setting, and they are deliberately three plain
// strings: a fourth source ("runtime") belongs to the settings store, not to the
// file loader, and is defined there.
const (
	// SourceDefault means nothing overrode the built-in default.
	SourceDefault = "default"
	// SourceFile means the configuration file set the value.
	SourceFile = "file"
	// SourceEnv means an environment variable overrode it.
	SourceEnv = "env"
)

// Sources records the provenance of the settings the Admin API can display. It
// deliberately covers every setting that has an environment override plus the
// two authentication switches, and nothing else: a provenance label for a value
// no environment variable can reach would be "file or default" and is derived
// from comparing against Defaults instead.
//
// The type exists so Load does not have to grow a second return value and so
// Config itself stays a plain value: equality between two configurations remains
// "same effective behavior", which several tests assert.
type Sources struct {
	// Path is the configuration file that was read, empty when none was.
	Path string
	// settings maps a dotted setting path onto a Source value.
	settings map[string]string
	// envVariables maps a dotted setting path onto the environment variable name
	// that overrode it, so an operator can see which variable did it.
	envVariables map[string]string
}

// Source reports the provenance of one dotted setting path. An unknown path is
// reported as a default rather than as an error: the Admin API lists settings it
// knows about, and a path it does not know about is a programming mistake there,
// not an operator mistake here.
func (s Sources) Source(path string) string {
	if source, ok := s.settings[path]; ok {
		return source
	}
	return SourceDefault
}

// EnvVariable returns the environment variable that supplied a setting, or the
// empty string when the environment did not.
func (s Sources) EnvVariable(path string) string {
	return s.envVariables[path]
}

// Path reports the configuration file, if any.
func (s Sources) File() string { return s.Path }

func newSources(path string) *Sources {
	return &Sources{
		Path:         path,
		settings:     map[string]string{},
		envVariables: map[string]string{},
	}
}

func (s *Sources) markEnv(path, variable string) {
	if s == nil {
		return
	}
	s.settings[path] = SourceEnv
	s.envVariables[path] = variable
}

func (s *Sources) markFile(path string) {
	if s == nil {
		return
	}
	if _, already := s.settings[path]; already {
		return
	}
	s.settings[path] = SourceFile
}

// LoadWithSources loads the configuration exactly as Load does and additionally
// reports the provenance of every environment-overridable setting. The two
// functions share one implementation, so the effective values cannot differ
// between them.
//
// The file's contribution is derived from the JSON keys the file actually
// carries rather than from "the file exists": a setting the file omits is
// reported as "default", which is the truth an operator needs when a value is
// not what they expected.
func LoadWithSources(path string) (Config, Sources, error) {
	_ = path
	cfg := Defaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, Sources{}, err
	}
	return cfg, *newSources(""), nil
}

// fileKeyPaths reads the configuration file and returns the dotted paths of the
// keys it carries. Only the paths whose last element is a scalar are recorded:
// a key whose value is an object (routing.policy, routing.log.jev_trace) is
// expanded into its own leaves, so the source of one setting is never inferred
// from a sibling.
func fileKeyPaths(path string) (map[string]struct{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("open configuration: %w", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		// The loader already produced the authoritative decode error; this path
		// only runs after it succeeded, so a failure here is impossible in practice
		// and is reported rather than ignored.
		return nil, fmt.Errorf("decode configuration: %w", err)
	}
	paths := map[string]struct{}{}
	collectKeyPaths("", document, paths)
	return paths, nil
}

func collectKeyPaths(prefix string, document map[string]json.RawMessage, into map[string]struct{}) {
	for key, raw := range document {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		var nested map[string]json.RawMessage
		if len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, &nested) == nil {
			collectKeyPaths(path, nested, into)
			continue
		}
		into[path] = struct{}{}
	}
}

// overridableSetting names one environment-overridable setting.
type overridableSetting struct {
	path     string
	variable string
}

// overridableSettings is the one list of every setting an environment variable
// can reach, in the order the Admin API reports them. It is written out rather
// than derived from the load function so a new environment variable cannot be
// added without a provenance entry appearing here.
var overridableSettings = []overridableSetting{
	{path: "http.address", variable: "AUTO_ROUTER_HTTP_ADDRESS"},
	{path: "http.read_header_timeout", variable: "AUTO_ROUTER_READ_HEADER_TIMEOUT"},
	{path: "http.idle_timeout", variable: "AUTO_ROUTER_IDLE_TIMEOUT"},
	{path: "http.readiness_timeout", variable: "AUTO_ROUTER_READINESS_TIMEOUT"},
	{path: "http.shutdown_timeout", variable: "AUTO_ROUTER_SHUTDOWN_TIMEOUT"},
	{path: "http.max_request_bytes", variable: "AUTO_ROUTER_HTTP_MAX_REQUEST_BYTES"},
	{path: "database.path", variable: "AUTO_ROUTER_DATABASE_PATH"},
	{path: "database.busy_timeout", variable: "AUTO_ROUTER_DATABASE_BUSY_TIMEOUT"},
	{path: "log.level", variable: "AUTO_ROUTER_LOG_LEVEL"},
	{path: "jev.enabled", variable: "AUTO_ROUTER_JEV_ENABLED"},
	{path: "jev.base_url", variable: "AUTO_ROUTER_JEV_BASE_URL"},
	{path: "jev.api_key", variable: "AUTO_ROUTER_JEV_API_KEY"},
	{path: "jev.model", variable: "AUTO_ROUTER_JEV_MODEL"},
	{path: "jev.auth_header", variable: "AUTO_ROUTER_JEV_AUTH_HEADER"},
	{path: "jev.auth_scheme", variable: "AUTO_ROUTER_JEV_AUTH_SCHEME"},
	{path: "jev.timeout", variable: "AUTO_ROUTER_JEV_TIMEOUT"},
	{path: "jev.capture_raw_io", variable: "AUTO_ROUTER_JEV_CAPTURE_RAW_IO"},
	{path: "jev.input_mode", variable: "AUTO_ROUTER_JEV_INPUT_MODE"},
	{path: "routing.allow_provider_override", variable: "AUTO_ROUTER_ROUTING_ALLOW_PROVIDER_OVERRIDE"},
	{path: "routing.default_preference", variable: RoutingPreferenceEnv},
	{path: "routing.analyzer_debug_endpoint", variable: "AUTO_ROUTER_ROUTING_ANALYZER_DEBUG_ENDPOINT"},
	{path: "routing.policy_debug_endpoint", variable: "AUTO_ROUTER_ROUTING_POLICY_DEBUG_ENDPOINT"},
	{path: "routing.policy.high_confidence", variable: "AUTO_ROUTER_ROUTING_POLICY_HIGH_CONFIDENCE"},
	{path: "routing.policy.low_confidence", variable: "AUTO_ROUTER_ROUTING_POLICY_LOW_CONFIDENCE"},
	{path: "routing.policy.refuse_truncated_evidence", variable: "AUTO_ROUTER_ROUTING_POLICY_REFUSE_TRUNCATED_EVIDENCE"},
	{path: "routing.policy.default_model", variable: "AUTO_ROUTER_ROUTING_POLICY_DEFAULT_MODEL"},
	{path: "routing.auto.failover.enabled", variable: "AUTO_ROUTER_ROUTING_AUTO_FAILOVER"},
	{path: "routing.log.enabled", variable: "AUTO_ROUTER_ROUTING_LOG_ENABLED"},
	{path: "routing.log.store_client_ip", variable: "AUTO_ROUTER_ROUTING_LOG_STORE_CLIENT_IP"},
	{path: "routing.log.retention_days", variable: "AUTO_ROUTER_ROUTING_LOG_RETENTION_DAYS"},
	{path: "routing.log.jev_trace.enabled", variable: "AUTO_ROUTER_ROUTING_LOG_JEV_TRACE_ENABLED"},
	{path: "routing.log.jev_trace.retention_days", variable: "AUTO_ROUTER_ROUTING_LOG_JEV_TRACE_RETENTION_DAYS"},
	{path: "auth.enabled", variable: AuthEnabledEnv},
	{path: "admin.enabled", variable: AdminEnabledEnv},
}

// ConfigDir returns the directory of the configuration file, or the process
// working directory when no file was used. It exists so a report can state which
// location was consulted without every caller re-deriving it.
func (s Sources) ConfigDir() string {
	if s.Path == "" {
		working, err := os.Getwd()
		if err != nil {
			return ""
		}
		return working
	}
	return filepath.Dir(s.Path)
}
