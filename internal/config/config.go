// Package config loads and validates process configuration. Routing and provider
// settings will be introduced with their owning development stages.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/router/analyzer"
)

// Duration represents a Go duration encoded as a JSON string, e.g. "5s".
type Duration time.Duration

func (d *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return errors.New("duration must be a string such as \"5s\"")
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return errors.New("duration must be a valid Go duration such as \"5s\"")
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

type Config struct {
	HTTP     HTTPConfig     `json:"http"`
	Database DatabaseConfig `json:"database"`
	Log      LogConfig      `json:"log"`
	Registry RegistryConfig `json:"registry"`
	Routing  RoutingConfig  `json:"routing"`
	Jev      JevConfig      `json:"jev"`
	// Auth is the inbound authentication section. It is off by default, which is
	// the stage-8 behavior: no credential is required and /admin/* is not mounted.
	Auth AuthConfig `json:"auth"`
	// Admin is the management API's own section. It is only effective together
	// with auth.enabled.
	Admin AdminConfig `json:"admin"`
}

type HTTPConfig struct {
	Address           string   `json:"address"`
	ReadHeaderTimeout Duration `json:"read_header_timeout"`
	IdleTimeout       Duration `json:"idle_timeout"`
	ReadinessTimeout  Duration `json:"readiness_timeout"`
	ShutdownTimeout   Duration `json:"shutdown_timeout"`
	// MaxRequestBytes bounds how much of a model request is buffered before it
	// is forwarded. Zero is invalid; the default is 16 MiB.
	MaxRequestBytes int64 `json:"max_request_bytes"`
}

// RoutingConfig holds routing behavior switches and the process-level routing
// defaults the request path resolves against.
type RoutingConfig struct {
	// AllowProviderOverride enables the explicit "provider:<provider>/<model>"
	// request syntax. It is off by default: the syntax is a privileged escape
	// hatch until stage 9 adds inbound authentication.
	AllowProviderOverride bool `json:"allow_provider_override"`
	// DefaultPreference is the routing preference that applies when a request
	// does not carry the X-Routing-Preference header. The type comes from the
	// analyzer so the accepted values and their normalization have exactly one
	// definition; an unknown value is rejected before any listener is bound.
	DefaultPreference analyzer.Preference `json:"default_preference"`
	// AnalyzerDebugEndpoint mounts POST /debug/analyze, an offline development
	// tool that reports the extracted features of a request body. It is off by
	// default and is never mounted on a non-loopback listener.
	AnalyzerDebugEndpoint bool `json:"analyzer_debug_endpoint"`
	// Policy is the policy engine's configuration. Its lists and tier tables are
	// file-only, matching the registry convention: an environment variable
	// overrides a scalar setting, never a table.
	Policy RoutingPolicyConfig `json:"policy"`
	// PolicyDebugEndpoint mounts POST /debug/route, an offline development tool
	// that evaluates the policy over a request body and a caller-supplied
	// candidate list. It is off by default and, like the analyzer endpoint, is
	// never mounted on a non-loopback listener.
	PolicyDebugEndpoint bool `json:"policy_debug_endpoint"`
	// Auto is the automatic routing (`model=auto`) section.
	Auto RoutingAutoConfig `json:"auto"`
	// Log is the durable routing log. It is a subsection of routing rather than of
	// the top-level log section because it is written by the routing path and its
	// subject is routing decisions, not process diagnostics.
	Log RoutingLogConfig `json:"log"`
}

type DatabaseConfig struct {
	Path        string   `json:"path"`
	BusyTimeout Duration `json:"busy_timeout"`
}

type LogConfig struct {
	Level string `json:"level"`
}

func Defaults() Config {
	return Config{
		HTTP: HTTPConfig{
			Address:           "127.0.0.1:8080",
			ReadHeaderTimeout: Duration(5 * time.Second),
			IdleTimeout:       Duration(60 * time.Second),
			ReadinessTimeout:  Duration(2 * time.Second),
			ShutdownTimeout:   Duration(10 * time.Second),
			MaxRequestBytes:   maxRequestBytesDefault,
		},
		Database: DatabaseConfig{
			Path:        "data/auto-router.db",
			BusyTimeout: Duration(5 * time.Second),
		},
		Log:      LogConfig{Level: "info"},
		Registry: defaultRegistryConfig(),
		Routing: RoutingConfig{
			AllowProviderOverride: false,
			DefaultPreference:     analyzer.PreferenceBalanced,
			AnalyzerDebugEndpoint: false,
			Policy:                defaultRoutingPolicyConfig(),
			PolicyDebugEndpoint:   false,
			Auto:                  defaultRoutingAutoConfig(),
			Log:                   defaultRoutingLogConfig(),
		},
		Jev:   defaultJevConfig(),
		Auth:  defaultAuthConfig(),
		Admin: defaultAdminConfig(),
	}
}

// RoutingPreferenceEnv is the environment variable that overrides
// routing.default_preference. It is named here so the explicit-empty-value
// rejection and the loader cannot drift apart.
const RoutingPreferenceEnv = "AUTO_ROUTER_ROUTING_DEFAULT_PREFERENCE"

// The two authentication switches are the only members of their sections that
// have an environment variable. The key list, its header and its scheme stay
// file-only, exactly like the registry lists and the policy tables: an
// environment variable overrides a scalar setting, never a table. A credential
// that could be injected through the environment without appearing in the file
// would also be invisible to the operator reading the file.
const (
	// AuthEnabledEnv turns inbound authentication on.
	AuthEnabledEnv = "AUTO_ROUTER_AUTH_ENABLED"
	// AdminEnabledEnv mounts the Admin API. It is only effective together with
	// AuthEnabledEnv.
	AdminEnabledEnv = "AUTO_ROUTER_ADMIN_ENABLED"
)

// Load applies defaults, an optional JSON file, then explicit environment
// overrides. Relative database paths are resolved from the process directory.
// An explicitly provided but empty environment variable is an error, not a
// request to silently fall back to the file or defaults.
// Load returns the fixed process defaults. path is retained for source compatibility
// with offline tools and old launch scripts; runtime JSON and AUTO_ROUTER_* overrides
// are intentionally ignored.
func Load(path string) (Config, error) {
	_ = path
	cfg := Defaults()
	return cfg, cfg.Validate()
}

// load is Load's implementation with an injectable environment lookup. It records
// no provenance; loadTracked is the same code path with a provenance recorder.
func load(path string, lookup func(string) (string, bool)) (Config, error) {
	return loadTracked(path, lookup, nil)
}

// loadTracked applies defaults, the file and then environment overrides. sources
// may be nil, in which case no provenance is recorded: the effective result is
// identical either way, which is what makes Load and LoadWithSources unable to
// disagree.
func loadTracked(path string, lookup func(string) (string, bool), sources *Sources) (Config, error) {
	cfg := Defaults()
	if path != "" {
		if err := loadFile(path, &cfg); err != nil {
			return Config{}, err
		}
	}
	if err := cfg.Registry.normalize(lookup); err != nil {
		return Config{}, err
	}
	if err := cfg.Jev.normalize(lookup); err != nil {
		return Config{}, err
	}
	if err := cfg.Auth.normalize(lookup); err != nil {
		return Config{}, err
	}
	cfg.Routing.Policy.normalize()
	// The routing log's two retention settings are the only non-boolean members of
	// the section, so they are read through the same explicit-empty rejection rule
	// every other scalar uses.
	for _, retention := range []struct {
		name   string
		path   string
		target *int
	}{
		{"AUTO_ROUTER_ROUTING_LOG_RETENTION_DAYS", "routing.log.retention_days", &cfg.Routing.Log.RetentionDays},
		{"AUTO_ROUTER_ROUTING_LOG_JEV_TRACE_RETENTION_DAYS", "routing.log.jev_trace.retention_days", &cfg.Routing.Log.JevTrace.RetentionDays},
	} {
		if value, ok := lookup(retention.name); ok {
			parsed, err := parseRetentionDays(retention.name, value)
			if err != nil {
				return Config{}, err
			}
			*retention.target = parsed
			sources.markEnv(retention.path, retention.name)
		}
	}
	stringsToSet := []struct {
		name   string
		path   string
		target *string
	}{
		{"AUTO_ROUTER_HTTP_ADDRESS", "http.address", &cfg.HTTP.Address},
		{"AUTO_ROUTER_DATABASE_PATH", "database.path", &cfg.Database.Path},
		{"AUTO_ROUTER_LOG_LEVEL", "log.level", &cfg.Log.Level},
		{"AUTO_ROUTER_JEV_BASE_URL", "jev.base_url", &cfg.Jev.BaseURL},
		{"AUTO_ROUTER_JEV_API_KEY", "jev.api_key", &cfg.Jev.APIKey},
		{"AUTO_ROUTER_JEV_MODEL", "jev.model", &cfg.Jev.Model},
		{"AUTO_ROUTER_JEV_AUTH_HEADER", "jev.auth_header", &cfg.Jev.AuthHeader},
		{"AUTO_ROUTER_JEV_AUTH_SCHEME", "jev.auth_scheme", &cfg.Jev.AuthScheme},
		{"AUTO_ROUTER_JEV_INPUT_MODE", "jev.input_mode", &cfg.Jev.InputMode},
		{"AUTO_ROUTER_ROUTING_DEFAULT_PREFERENCE", "routing.default_preference", (*string)(&cfg.Routing.DefaultPreference)},
		{"AUTO_ROUTER_ROUTING_POLICY_DEFAULT_MODEL", "routing.policy.default_model", &cfg.Routing.Policy.DefaultModel},
	}
	for _, env := range stringsToSet {
		if value, ok := lookup(env.name); ok {
			*env.target = value
			sources.markEnv(env.path, env.name)
		}
	}
	// The same rule applies to the Jev endpoint. The Jev API key is different:
	// an empty value is a legal state for a loopback test server, so it is not
	// rejected here (validation still refuses it for a remote endpoint).
	if value, ok := lookup("AUTO_ROUTER_JEV_BASE_URL"); ok && value == "" {
		return Config{}, errors.New("AUTO_ROUTER_JEV_BASE_URL must be an absolute http(s) URL when it is set")
	}
	if value, ok := lookup("AUTO_ROUTER_JEV_MODEL"); ok && value == "" {
		return Config{}, errors.New("AUTO_ROUTER_JEV_MODEL must not be empty when it is set")
	}
	// The input mode is a closed set of three literals. An explicitly empty
	// value means "no mode configured", which is not a state this router has: a
	// silent fall-back to content would be a privacy decision nobody made, so it
	// is refused like an unknown value.
	if value, ok := lookup("AUTO_ROUTER_JEV_INPUT_MODE"); ok && value == "" {
		return Config{}, errors.New("AUTO_ROUTER_JEV_INPUT_MODE must be one of content, redacted, features_only when it is set")
	}
	// An explicitly empty preference must not silently keep the file value: an
	// operator who clears the variable means "no preference configured", which
	// is not a state this router has. The value is validated below like any
	// other, so the error lists the four accepted values.
	if value, ok := lookup(RoutingPreferenceEnv); ok && strings.TrimSpace(value) == "" {
		return Config{}, fmt.Errorf("%s must be one of %s when it is set", RoutingPreferenceEnv, strings.Join(analyzer.PreferenceValues(), ", "))
	}
	// An explicitly empty default model means "no low-confidence fallback
	// configured", which is a legal state. A value padded with whitespace is a
	// mistake: trimming it silently would turn a typo into a different model name.
	if value, ok := lookup("AUTO_ROUTER_ROUTING_POLICY_DEFAULT_MODEL"); ok && value != strings.TrimSpace(value) {
		return Config{}, errors.New("AUTO_ROUTER_ROUTING_POLICY_DEFAULT_MODEL must not be padded with whitespace when it is set")
	}
	durationsToSet := []struct {
		name   string
		path   string
		target *Duration
	}{
		{"AUTO_ROUTER_READ_HEADER_TIMEOUT", "http.read_header_timeout", &cfg.HTTP.ReadHeaderTimeout},
		{"AUTO_ROUTER_IDLE_TIMEOUT", "http.idle_timeout", &cfg.HTTP.IdleTimeout},
		{"AUTO_ROUTER_READINESS_TIMEOUT", "http.readiness_timeout", &cfg.HTTP.ReadinessTimeout},
		{"AUTO_ROUTER_SHUTDOWN_TIMEOUT", "http.shutdown_timeout", &cfg.HTTP.ShutdownTimeout},
		{"AUTO_ROUTER_DATABASE_BUSY_TIMEOUT", "database.busy_timeout", &cfg.Database.BusyTimeout},
		{"AUTO_ROUTER_JEV_TIMEOUT", "jev.timeout", &cfg.Jev.Timeout},
	}
	for _, env := range durationsToSet {
		if value, ok := lookup(env.name); ok {
			parsed, err := time.ParseDuration(value)
			if err != nil {
				return Config{}, fmt.Errorf("%s must be a Go duration such as 5s", env.name)
			}
			*env.target = Duration(parsed)
			sources.markEnv(env.path, env.name)
		}
	}
	for _, boolean := range []struct {
		name   string
		path   string
		target *bool
		label  string
	}{
		{"AUTO_ROUTER_ROUTING_ALLOW_PROVIDER_OVERRIDE", "routing.allow_provider_override", &cfg.Routing.AllowProviderOverride, "true or false"},
		{"AUTO_ROUTER_ROUTING_ANALYZER_DEBUG_ENDPOINT", "routing.analyzer_debug_endpoint", &cfg.Routing.AnalyzerDebugEndpoint, "true or false"},
		{"AUTO_ROUTER_ROUTING_POLICY_REFUSE_TRUNCATED_EVIDENCE", "routing.policy.refuse_truncated_evidence", &cfg.Routing.Policy.RefuseTruncatedEvidence, "true or false"},
		{"AUTO_ROUTER_ROUTING_POLICY_DEBUG_ENDPOINT", "routing.policy_debug_endpoint", &cfg.Routing.PolicyDebugEndpoint, "true or false"},
		{"AUTO_ROUTER_ROUTING_AUTO_FAILOVER", "routing.auto.failover.enabled", &cfg.Routing.Auto.Failover.Enabled, "true or false"},
		{"AUTO_ROUTER_JEV_ENABLED", "jev.enabled", &cfg.Jev.Enabled, "true or false"},
		{"AUTO_ROUTER_JEV_CAPTURE_RAW_IO", "jev.capture_raw_io", &cfg.Jev.CaptureRawIO, "true or false"},
		{"AUTO_ROUTER_ROUTING_LOG_ENABLED", "routing.log.enabled", &cfg.Routing.Log.Enabled, "true or false"},
		{"AUTO_ROUTER_ROUTING_LOG_STORE_CLIENT_IP", "routing.log.store_client_ip", &cfg.Routing.Log.StoreClientIP, "true or false"},
		{"AUTO_ROUTER_ROUTING_LOG_JEV_TRACE_ENABLED", "routing.log.jev_trace.enabled", &cfg.Routing.Log.JevTrace.Enabled, "true or false"},
		{AuthEnabledEnv, "auth.enabled", &cfg.Auth.Enabled, "true or false"},
		{AdminEnabledEnv, "admin.enabled", &cfg.Admin.Enabled, "true or false"},
	} {
		if value, ok := lookup(boolean.name); ok {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return Config{}, fmt.Errorf("%s must be %s", boolean.name, boolean.label)
			}
			*boolean.target = parsed
			sources.markEnv(boolean.path, boolean.name)
		}
	}
	for _, number := range []struct {
		name   string
		path   string
		target *float64
		label  string
	}{
		{"AUTO_ROUTER_ROUTING_POLICY_HIGH_CONFIDENCE", "routing.policy.high_confidence", &cfg.Routing.Policy.HighConfidence, "a number between 0 and 1"},
		{"AUTO_ROUTER_ROUTING_POLICY_LOW_CONFIDENCE", "routing.policy.low_confidence", &cfg.Routing.Policy.LowConfidence, "a number between 0 and 1"},
	} {
		if value, ok := lookup(number.name); ok {
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
				return Config{}, fmt.Errorf("%s must be %s", number.name, number.label)
			}
			*number.target = parsed
			sources.markEnv(number.path, number.name)
		}
	}
	if value, ok := lookup("AUTO_ROUTER_HTTP_MAX_REQUEST_BYTES"); ok {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return Config{}, errors.New("AUTO_ROUTER_HTTP_MAX_REQUEST_BYTES must be a whole number of bytes")
		}
		cfg.HTTP.MaxRequestBytes = parsed
		sources.markEnv("http.max_request_bytes", "AUTO_ROUTER_HTTP_MAX_REQUEST_BYTES")
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

func loadFile(path string, cfg *Config) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()
	const maxConfigBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return fmt.Errorf("read configuration: %w", err)
	}
	if len(data) > maxConfigBytes {
		return errors.New("configuration exceeds 1 MiB")
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return errors.New("configuration must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(cfg); err != nil {
		// The file is named because this error is the one an operator meets when a field
		// is missing from the build they are running: without the path, "unknown field
		// session_ttl" reads as "this file is wrong" when the real answer is often "an
		// older binary is what actually ran". On Windows a shell resolves
		// ./bin/auto-router to bin/auto-router.exe through PATHEXT, so a build produced
		// next to a stale auto-router.exe is silently not the one that ran. Naming the
		// file, and having the process log its own version, is what makes that
		// diagnosable.
		return fmt.Errorf("decode configuration %s: %w", path, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("configuration %s must contain exactly one JSON object", path)
	}
	return nil
}

func (c Config) Validate() error {
	_, port, err := net.SplitHostPort(c.HTTP.Address)
	if err != nil || strings.TrimSpace(c.HTTP.Address) != c.HTTP.Address {
		return errors.New("http.address must be host:port (for example 127.0.0.1:8080)")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return errors.New("http.address port must be between 0 and 65535")
	}
	if strings.TrimSpace(c.Database.Path) == "" || strings.ContainsRune(c.Database.Path, '\x00') {
		return errors.New("database.path must be a non-empty filesystem path or :memory:")
	}
	if strings.HasPrefix(strings.ToLower(c.Database.Path), "file:") {
		return errors.New("database.path must be a filesystem path, not a SQLite URI")
	}
	for _, timeout := range []struct {
		name  string
		value Duration
	}{
		{"http.read_header_timeout", c.HTTP.ReadHeaderTimeout},
		{"http.idle_timeout", c.HTTP.IdleTimeout},
		{"http.readiness_timeout", c.HTTP.ReadinessTimeout},
		{"http.shutdown_timeout", c.HTTP.ShutdownTimeout},
	} {
		if timeout.value <= 0 {
			return fmt.Errorf("%s must be positive", timeout.name)
		}
	}
	if c.HTTP.MaxRequestBytes <= 0 || c.HTTP.MaxRequestBytes > maxRequestBytesLimit {
		return fmt.Errorf("http.max_request_bytes must be between 1 and %d", maxRequestBytesLimit)
	}
	busy := time.Duration(c.Database.BusyTimeout)
	if busy < time.Millisecond || busy%time.Millisecond != 0 || busy > (1<<31-1)*time.Millisecond {
		return errors.New("database.busy_timeout must be a whole number of milliseconds between 1 and 2147483647")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("log.level must be debug, info, warn or error")
	}
	if err := c.Registry.Validate(); err != nil {
		return err
	}
	// The configured value must be one of the four literals exactly. Unlike the
	// request header it is not normalized: a configuration file that says
	// "Quality" is a typo to fix, not an input to interpret, and the error never
	// repeats the offending value.
	if !c.Routing.DefaultPreference.Valid() {
		return fmt.Errorf("routing.default_preference must be one of %s", strings.Join(analyzer.PreferenceValues(), ", "))
	}
	if err := c.Routing.Policy.Validate(); err != nil {
		return err
	}
	if err := c.Routing.Auto.Validate(); err != nil {
		return err
	}
	if err := c.Routing.Log.Validate(); err != nil {
		return err
	}
	if err := c.Jev.Validate(); err != nil {
		return err
	}
	if err := c.Auth.Validate(); err != nil {
		return err
	}
	if err := c.Admin.Validate(); err != nil {
		return err
	}
	return nil
}

// AnalyzerDebugEndpoint reports whether POST /debug/analyze may be mounted. The
// endpoint is an unauthenticated development tool that echoes request features,
// so it is only mounted when configuration enables it and the listener can only
// be reached from the local machine. A refused endpoint returns false with a
// reason so startup can print a WARN explaining the refusal instead of leaving
// an operator to wonder why a documented route answers 404.
func (c Config) AnalyzerDebugEndpoint() (bool, string) {
	if !c.Routing.AnalyzerDebugEndpoint {
		return false, ""
	}
	host, _, err := net.SplitHostPort(c.HTTP.Address)
	if err != nil || strings.TrimSpace(host) == "" {
		return false, fmt.Sprintf("routing.analyzer_debug_endpoint is enabled but http.address %q is not a loopback address, so POST /debug/analyze is not mounted", c.HTTP.Address)
	}
	if !models.IsLoopbackHost(host) {
		return false, fmt.Sprintf("routing.analyzer_debug_endpoint is enabled but http.address %q is not a loopback address, so POST /debug/analyze is not mounted", c.HTTP.Address)
	}
	return true, ""
}

// analyzerWarnings reports analyzer states that are valid but likely to
// surprise the operator. It never includes request content, which it never has
// access to.
func (c Config) analyzerWarnings() []string {
	if !c.Routing.AnalyzerDebugEndpoint {
		return nil
	}
	if enabled, _ := c.AnalyzerDebugEndpoint(); enabled {
		return []string{"routing.analyzer_debug_endpoint is enabled; POST /debug/analyze reports request features without authentication and must not be exposed beyond the local machine"}
	}
	_, reason := c.AnalyzerDebugEndpoint()
	return []string{reason}
}

// PolicyDebugEndpoint reports whether POST /debug/route may be mounted. The
// endpoint is an unauthenticated development tool that reports how the policy
// engine evaluated a request, so it is only mounted when configuration enables it
// and the listener can only be reached from the local machine. A refused endpoint
// returns false with a reason so startup can print a WARN explaining the refusal
// instead of leaving an operator to wonder why a documented route answers 404.
func (c Config) PolicyDebugEndpoint() (bool, string) {
	if !c.Routing.PolicyDebugEndpoint {
		return false, ""
	}
	host, _, err := net.SplitHostPort(c.HTTP.Address)
	if err != nil || strings.TrimSpace(host) == "" || !models.IsLoopbackHost(host) {
		return false, fmt.Sprintf("routing.policy_debug_endpoint is enabled but http.address %q is not a loopback address, so POST /debug/route is not mounted", c.HTTP.Address)
	}
	return true, ""
}

// policyWarnings reports policy states that are valid but likely to surprise the
// operator. It never includes request content, which it never has access to.
func (c Config) policyWarnings() []string {
	var warnings []string
	if c.Routing.PolicyDebugEndpoint {
		if enabled, _ := c.PolicyDebugEndpoint(); enabled {
			warnings = append(warnings, "routing.policy_debug_endpoint is enabled; POST /debug/route reports routing decisions without authentication and must not be exposed beyond the local machine")
		} else {
			_, reason := c.PolicyDebugEndpoint()
			warnings = append(warnings, reason)
		}
	}
	if !c.Routing.Policy.RefuseTruncatedEvidence {
		warnings = append(warnings, "routing.policy.refuse_truncated_evidence is disabled; a request whose analysis was truncated will be routed on the evidence that was observed")
	}
	// An empty tier table is not a warning: it is the shipped default, and "every
	// model is an unknown tier" is a documented, workable state. A default that
	// warns on startup trains operators to ignore warnings.
	return warnings
}
