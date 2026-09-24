package config

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/models"
)

// The three inbound scopes. They are a closed set: a key that names an unknown
// scope is a configuration mistake, not a permission to be interpreted later.
//
//   - inference allows the two model protocols and the catalogue listing;
//   - admin allows the /admin/v1 management surface;
//   - override allows the explicit "provider:<provider>/<model>" request syntax,
//     which is a privileged escape hatch that bypasses routing policy.
const (
	ScopeInference = "inference"
	ScopeAdmin     = "admin"
	ScopeOverride  = "override"
)

// ScopeValues returns the accepted scopes in a fixed order, so an error message
// or a report never depends on map iteration.
func ScopeValues() []string {
	return []string{ScopeInference, ScopeAdmin, ScopeOverride}
}

// ValidScope reports whether value is one of the two accepted scopes.
func ValidScope(value string) bool {
	switch value {
	case ScopeInference, ScopeAdmin, ScopeOverride:
		return true
	default:
		return false
	}
}

// Authentication defaults for the fixed runtime. Authentication and the
// management surface are always enabled. The owner password and inference
// keys are managed in SQLite, not through startup configuration.
const (
	// DefaultAuthHeader is the header inbound credentials arrive in.
	DefaultAuthHeader = "Authorization"
	// DefaultAuthScheme is the token scheme prefixed to the credential. An
	// empty scheme accepts the bare key.
	DefaultAuthScheme = "Bearer"
	// DefaultAdminPageSize is the page size of a list endpoint that did not ask
	// for one.
	DefaultAdminPageSize = 50
	// DefaultAdminMaxPageSize bounds what a caller may ask for.
	DefaultAdminMaxPageSize = 200
	// DefaultAdminSessionTTL is how long a browser management session stays valid.
	// Twelve hours covers one working day; the value is absolute from creation, so
	// an operator who leaves a tab open overnight is asked to sign in again rather
	// than holding a session that never expires.
	DefaultAdminSessionTTL = 12 * time.Hour
	// MinAdminSessionTTL and MaxAdminSessionTTL bound the configured value. The
	// lower bound keeps a session from expiring between two clicks of a multi-step
	// form; the upper bound keeps a stolen cookie from being useful for a month.
	MinAdminSessionTTL = 5 * time.Minute
	// MaxAdminSessionTTL is the upper bound, expressed in hours so the error
	// message can state it the way an operator thinks about it.
	MaxAdminSessionTTL = 720 * time.Hour
	// maxAdminPageSize is the hard ceiling of both page size settings. It exists
	// so a decimal typo cannot make one admin request read the whole database.
	maxAdminPageSize = 1000
	// maxAuthKeys bounds active database-backed inbound credentials and the
	// constant-time work performed by one authentication attempt.
	maxAuthKeys = 64
	// minAuthKeyLength is the shortest accepted credential. A short key is
	// guessable; the check exists so a placeholder is refused at startup instead of
	// becoming an authentication bypass in production.
	minAuthKeyLength = 16
	// maxAuthKeyLength bounds one credential, so a hostile file cannot make every
	// authentication compare run over a megabyte.
	maxAuthKeyLength = 1024
)

// authKeyEnvNote is retained for the legacy JSON parser only. Normal service
// startup does not read auth.keys from files or environment variables.
const authKeyEnvNote = "auth.keys is file-only; it has no environment variable"

// authKeyNamePattern is the audit-name grammar. A key name is written into the
// process log for every authenticated request, so it is restricted to a
// conservative lowercase alphabet and can never contain a space, a NUL or a
// control character.
var authKeyNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// AuthKey is one legacy configuration credential shape. It is not used by the
// serving runtime, which loads digest-only credentials from SQLite.
type AuthKey struct {
	// Name is the audit identifier of the key. It appears in the process log and
	// in the Admin API; it is never a secret.
	Name string `json:"name"`
	// Key is the credential itself in the legacy file parser.
	Key string `json:"key"`
	// Scopes are the permissions this key carries.
	Scopes []string `json:"scopes"`
}

// HasScope reports whether the key carries one scope.
func (k AuthKey) HasScope(scope string) bool {
	for _, candidate := range k.Scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

// AuthConfig is the legacy `auth` section. Normal serving keeps authentication
// enabled and loads managed key digests from SQLite.
type AuthConfig struct {
	// Enabled is honored only by the legacy configuration parser; normal serving
	// always enables inbound authentication.
	Enabled bool `json:"enabled"`
	// Header is the header carrying the credential. It defaults to Authorization.
	Header string `json:"header"`
	// Scheme is an optional space-separated prefix such as Bearer. An empty scheme
	// accepts the bare credential.
	Scheme string `json:"scheme"`
	// Keys is retained for legacy JSON parsing and is not used by the server.
	Keys []AuthKey `json:"keys"`
}

// AdminConfig contains management bounds and retained legacy enablement. The
// serving runtime fixes the Admin API on and allows only the session TTL to be
// changed through the SQLite-backed settings surface.
type AdminConfig struct {
	// Enabled is honored only by the legacy configuration parser; serving mounts
	// the authenticated Admin API unconditionally after installation.
	Enabled bool `json:"enabled"`
	// PageSize is the list page size a request that asks for nothing gets.
	PageSize int `json:"page_size"`
	// MaxPageSize is the largest page a request may ask for.
	MaxPageSize int `json:"max_page_size"`
	// SessionTTL is how long a browser session created by the login endpoint stays
	// valid. It is the lifetime the cookie advertises and the lifetime the server
	// enforces; both come from this value, so a client cannot extend one without the
	// other.
	SessionTTL Duration `json:"session_ttl"`
}

func defaultAuthConfig() AuthConfig {
	return AuthConfig{
		Enabled: true,
		Header:  DefaultAuthHeader,
		Scheme:  DefaultAuthScheme,
		Keys:    []AuthKey{},
	}
}

func defaultAdminConfig() AdminConfig {
	return AdminConfig{
		Enabled:     true,
		PageSize:    DefaultAdminPageSize,
		MaxPageSize: DefaultAdminMaxPageSize,
		SessionTTL:  Duration(DefaultAdminSessionTTL),
	}
}

// normalize is part of the legacy configuration parser: it fills absent slices
// and expands ${VAR} references. Normal service startup never calls it.
func (a *AuthConfig) normalize(lookup func(string) (string, bool)) error {
	if a.Keys == nil {
		a.Keys = []AuthKey{}
	}
	for i := range a.Keys {
		if a.Keys[i].Scopes == nil {
			a.Keys[i].Scopes = []string{}
		}
		expanded, err := expandEnv(a.Keys[i].Key, lookup)
		if err != nil {
			// The index identifies the offending entry without naming the
			// variable: the message may name the key's audit name, which is not a
			// secret, but never the credential.
			return fmt.Errorf("auth.keys[%d].key: %w", i, err)
		}
		a.Keys[i].Key = expanded
		// A ${VAR} that is present but empty is refused the same way an explicitly
		// empty environment variable is refused everywhere else: an empty
		// credential is not a credential, and accepting it would make every request
		// unauthenticated.
		if expanded == "" {
			return fmt.Errorf("auth.keys[%d].key must not be empty", i)
		}
	}
	return nil
}

// Validate checks the auth section before any listener is bound. Every error
// names a setting and an index or a key name; none of them ever repeats a
// credential.
func (a AuthConfig) Validate() error {
	if !validHeaderName(a.Header) {
		return errors.New("auth.header must be a valid HTTP header name")
	}
	if _, forbidden := forbiddenAuthHeaders[strings.ToLower(a.Header)]; forbidden {
		return fmt.Errorf("auth.header must not be %q; it is managed by the transport", a.Header)
	}
	if strings.ContainsAny(a.Scheme, " \t\r\n\x00") {
		return errors.New("auth.scheme must not contain whitespace or NUL")
	}
	if len(a.Keys) > maxAuthKeys {
		return fmt.Errorf("auth.keys must not contain more than %d keys", maxAuthKeys)
	}
	names := make(map[string]struct{}, len(a.Keys))
	for i, key := range a.Keys {
		if !authKeyNamePattern.MatchString(key.Name) {
			return fmt.Errorf("auth.keys[%d].name must match %s", i, authKeyNamePattern)
		}
		if _, duplicate := names[key.Name]; duplicate {
			return fmt.Errorf("auth.keys[%d].name is declared more than once", i)
		}
		names[key.Name] = struct{}{}
		if err := validateAuthKeyValue(i, key.Key); err != nil {
			return err
		}
		scopes, err := validateAuthScopes(i, key.Scopes)
		if err != nil {
			return err
		}
		if len(scopes) == 0 {
			return fmt.Errorf("auth.keys[%d].scopes must not be empty", i)
		}
	}
	// Credential availability is checked against SQLite during service assembly.
	// A new database intentionally has no inference keys until the owner creates one.
	return nil
}

// validateAuthKeyValue checks one credential. The error never contains the value
// and never states how close it was to the rules: it names the entry and the
// requirement only.
func validateAuthKeyValue(index int, key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("auth.keys[%d].key must not be blank", index)
	}
	if key != strings.TrimSpace(key) {
		return fmt.Errorf("auth.keys[%d].key must not be padded with whitespace", index)
	}
	if strings.ContainsRune(key, '\x00') {
		return fmt.Errorf("auth.keys[%d].key must not contain NUL", index)
	}
	if len(key) < minAuthKeyLength {
		return fmt.Errorf("auth.keys[%d].key must be at least %d characters", index, minAuthKeyLength)
	}
	if len(key) > maxAuthKeyLength {
		return fmt.Errorf("auth.keys[%d].key must not exceed %d characters", index, maxAuthKeyLength)
	}
	return nil
}

// validateAuthScopes checks one scope list and returns its deduplicated members.
// Unknown scopes are refused rather than ignored: a typo in a scope name would
// otherwise silently grant nothing (or, worse, be read as a different scope).
func validateAuthScopes(index int, scopes []string) ([]string, error) {
	seen := make(map[string]struct{}, len(scopes))
	unique := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if !ValidScope(scope) {
			return nil, fmt.Errorf("auth.keys[%d].scopes must contain only %s", index, strings.Join(ScopeValues(), ", "))
		}
		if _, duplicate := seen[scope]; duplicate {
			continue
		}
		seen[scope] = struct{}{}
		unique = append(unique, scope)
	}
	return unique, nil
}

// Validate checks the admin section before any listener is bound.
func (a AdminConfig) Validate() error {
	for _, setting := range []struct {
		name  string
		value int
	}{
		{"admin.page_size", a.PageSize},
		{"admin.max_page_size", a.MaxPageSize},
	} {
		if setting.value <= 0 || setting.value > maxAdminPageSize {
			return fmt.Errorf("%s must be between 1 and %d", setting.name, maxAdminPageSize)
		}
	}
	if a.PageSize > a.MaxPageSize {
		return errors.New("admin.page_size must not exceed admin.max_page_size")
	}
	// The session lifetime is bounded on both ends. A zero value would mean "a
	// session that is already expired", which is a configuration mistake rather
	// than a way to disable login.
	if ttl := time.Duration(a.SessionTTL); ttl < MinAdminSessionTTL || ttl > MaxAdminSessionTTL {
		return fmt.Errorf("admin.session_ttl must be between %s and %s", MinAdminSessionTTL, MaxAdminSessionTTL)
	}
	return nil
}

// AdminMounted reports whether /admin/* may be registered, with a reason when it
// is enabled but refused. The Admin API is registered only when the process also
// authenticates inbound requests: it can rewrite the routing policy and the
// registry, so "enabled" alone is not enough to expose it.
func (c Config) AdminMounted() (bool, string) {
	if !c.Admin.Enabled {
		return false, ""
	}
	if !c.Auth.Enabled {
		return false, "admin.enabled is true but auth.enabled is false; the Admin API is not mounted because it must not be reachable without authentication"
	}
	return true, ""
}

// adminWarnings reports admin states that are valid but likely to surprise the
// operator. It never includes a credential.
func (c Config) adminWarnings() []string {
	var warnings []string
	if c.Admin.Enabled && !c.Auth.Enabled {
		warnings = append(warnings, "admin.enabled is true but auth.enabled is false; /admin/* is not mounted at all")
	}
	if enabled, _ := c.AdminMounted(); enabled && !listenerIsLoopback(c.HTTP.Address) {
		// The endpoint is authenticated; the warning states the exposure, which is
		// the point of turning authentication on in the first place.
		warnings = append(warnings, fmt.Sprintf("the Admin API is mounted on %q, which is not a loopback address; every /admin request is authenticated but the surface is reachable from the network", c.HTTP.Address))
	}
	if c.Auth.Enabled && len(c.Registry.Providers) == 0 {
		warnings = append(warnings, "no Provider is configured; authenticated inference requests will return provider_not_configured until one is added in the WebUI")
	}
	return warnings
}

// listenerIsLoopback reports whether http.address can only be reached from the
// local machine. A malformed address is treated as non-loopback: the warning is
// the safe direction.
func listenerIsLoopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	return models.IsLoopbackHost(host)
}
