// Package config defines and validates the process defaults and the runtime-config
// shape used by the Admin API settings overlay.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
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
	// Auth is the authentication configuration shape. Normal serving uses the
	// SQLite-backed credential store and mounts the authenticated Admin API.
	Auth AuthConfig `json:"auth"`
	// Admin contains management limits and the runtime-adjustable session TTL.
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
	// Policy is the policy engine configuration, including its runtime-adjustable
	// lists and relative cost/latency tier tables.
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

// RoutingPreferenceEnv is the historical environment variable name for
// routing.default_preference.
//
// Deprecated: Runtime startup no longer reads AUTO_ROUTER_* variables.
const RoutingPreferenceEnv = "AUTO_ROUTER_ROUTING_DEFAULT_PREFERENCE"

// These historical environment variable names remain exported for source
// compatibility; the serving process no longer reads them.
const (
	// AuthEnabledEnv is the historical auth switch name.
	//
	// Deprecated: Runtime startup no longer reads AUTO_ROUTER_* variables.
	AuthEnabledEnv = "AUTO_ROUTER_AUTH_ENABLED"
	// AdminEnabledEnv is the historical management API switch name.
	//
	// Deprecated: Runtime startup no longer reads AUTO_ROUTER_* variables.
	AdminEnabledEnv = "AUTO_ROUTER_ADMIN_ENABLED"
)

// Load returns the fixed process defaults. path is retained for source compatibility
// with offline tools and old launch scripts; startup settings are managed through
// SQLite-backed runtime overrides. JSON files and AUTO_ROUTER_* variables are ignored.
//
// Deprecated: Use Defaults to obtain the startup configuration. The path argument is
// ignored and file/environment configuration no longer changes serving behavior.
func Load(path string) (Config, error) {
	_ = path
	cfg := Defaults()
	return cfg, cfg.Validate()
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
	// request header it is not normalized: a configured "Quality" is a typo to
	// fix, not an input to interpret, and the error never repeats the value.
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
