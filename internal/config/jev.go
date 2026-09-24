package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/router/jev"
)

// The values below were verified against the official TypeSafe API on
// 2026-09-21 (see internal/router/jev/testdata/README.md for the sources):
// System One is a single POST endpoint authenticated with a bearer token. The
// defaults only describe how the credential is transmitted; the model name has
// no default so the operator explicitly chooses the upstream model or alias.
const (
	// DefaultJevAuthHeader is the header the official API authenticates with.
	DefaultJevAuthHeader = "Authorization"
	// DefaultJevAuthScheme is the token scheme the official API expects.
	DefaultJevAuthScheme = "Bearer"
	// DefaultJevTimeout bounds one complete Jev call. The API answers in a few
	// hundred milliseconds and is on the routing path, so five seconds is a
	// generous ceiling.
	DefaultJevTimeout = 5 * time.Second
)

// JevConfig describes the TypeSafe System One client. It is disabled by
// default: a process that never routes automatically has no reason to hold a
// TypeSafe credential or to open a second network dependency.
type JevConfig struct {
	// Enabled turns the client on. It has no effect on the request path in
	// stage 4 beyond failing startup on an unusable configuration.
	Enabled bool `json:"enabled"`
	// BaseURL is the TypeSafe API root. It must use https unless the host is
	// loopback, so a credential can never be sent over plain http remotely.
	BaseURL string `json:"base_url"`
	// APIKey is the TypeSafe bearer token. It supports ${VAR} expansion and is
	// never logged or echoed in an error message.
	APIKey string `json:"api_key"`
	// Model is the System One model name or upstream alias. It is required
	// whenever the client is enabled and has no built-in default.
	Model string `json:"model"`
	// AuthHeader and AuthScheme describe how the token is transmitted. The
	// defaults match the official API and remain overridable.
	AuthHeader string `json:"auth_header"`
	AuthScheme string `json:"auth_scheme"`
	// Timeout bounds one complete call, including reading the response body.
	Timeout Duration `json:"timeout"`
	// CaptureRawIO keeps the raw request and response bytes in memory. It is
	// off by default because the raw request contains the client's prompt text.
	// It is reserved for the stage 8 debug trail.
	CaptureRawIO bool `json:"capture_raw_io"`
	// InputMode selects how much of the conversation the automatic routing path
	// sends to Jev: content (the view as extracted), redacted (the same view
	// after best-effort redaction and bounded excerpts) or features_only (no
	// client text at all, just counts and enumerations). It is orthogonal to
	// Enabled: "Jev is on" and "Jev may see the prompt" are separate decisions.
	InputMode string `json:"input_mode"`
}

// maxJevModelLength matches the registry's model identifier budget.
const maxJevModelLength = 128

func defaultJevConfig() JevConfig {
	return JevConfig{
		Enabled:    false,
		AuthHeader: DefaultJevAuthHeader,
		AuthScheme: DefaultJevAuthScheme,
		Timeout:    Duration(DefaultJevTimeout),
		InputMode:  string(jev.InputModeRedacted),
	}
}

// normalize expands ${VAR} references in the API key. The semantics match the
// registry and provider credentials: an unset variable is an error naming the
// variable, never its value.
func (j *JevConfig) normalize(lookup func(string) (string, bool)) error {
	expanded, err := expandEnv(j.APIKey, lookup)
	if err != nil {
		return fmt.Errorf("jev.api_key: %w", err)
	}
	j.APIKey = expanded
	return nil
}

// ValidateJevModel checks the syntax and length of a System One model identifier.
// Upstream aliases such as "jev-latest" are intentionally accepted and passed
// through unchanged; alias resolution belongs to the upstream service.
func ValidateJevModel(model string) error {
	if model == "" {
		return errors.New("must not be empty")
	}
	if model != strings.TrimSpace(model) {
		return errors.New("must not be padded with whitespace")
	}
	if strings.ContainsRune(model, '\x00') {
		return errors.New("must not contain NUL")
	}
	if len(model) > maxJevModelLength {
		return fmt.Errorf("must not exceed %d bytes", maxJevModelLength)
	}
	return nil
}

// Validate checks the Jev section before any listener is bound. An explicitly
// empty credential is only legal for a loopback endpoint, where the request
// cannot leave the machine; that exception keeps local integration tests
// possible without weakening the remote case.
func (j JevConfig) Validate() error {
	if j.BaseURL != "" {
		if err := models.ValidateSecureURL("jev.base_url", j.BaseURL); err != nil {
			return err
		}
	}
	if j.Model != "" {
		if err := ValidateJevModel(j.Model); err != nil {
			return fmt.Errorf("jev.model: %w", err)
		}
	}
	if !validHeaderName(j.AuthHeader) {
		return errors.New("jev.auth_header must be a valid HTTP header name")
	}
	if _, forbidden := forbiddenAuthHeaders[strings.ToLower(j.AuthHeader)]; forbidden {
		return fmt.Errorf("jev.auth_header must not be %q; it is managed by the transport", j.AuthHeader)
	}
	if strings.ContainsAny(j.AuthScheme, " \t\r\n\x00") {
		return errors.New("jev.auth_scheme must not contain whitespace or NUL")
	}
	if time.Duration(j.Timeout) <= 0 {
		return errors.New("jev.timeout must be positive")
	}
	// The input mode is a closed set of literals and is not normalized: a typo
	// must be fixed rather than interpreted, and the error never repeats it.
	if _, err := jev.ParseInputMode(j.InputMode); err != nil {
		return fmt.Errorf("jev.input_mode %v", err)
	}
	if j.Enabled {
		if j.BaseURL == "" {
			return errors.New("jev.base_url is required when jev.enabled is true")
		}
		if j.Model == "" {
			return errors.New("jev.model is required when jev.enabled is true; set a model name")
		}
		if j.APIKey == "" && !j.loopback() {
			return errors.New("jev.api_key is required when jev.enabled is true; an empty key is only accepted for a loopback base_url")
		}
	}
	return nil
}

// loopback reports whether the endpoint is local. It is only consulted after
// ValidateSecureURL accepted the URL, so a malformed value cannot reach it.
func (j JevConfig) loopback() bool {
	parsed, err := url.Parse(j.BaseURL)
	if err != nil {
		return false
	}
	return models.IsLoopbackHost(parsed.Hostname())
}

// AuthValue renders the credential for the configured header. An empty key
// yields an empty value, which the client treats as "send no credential".
func (j JevConfig) AuthValue() string {
	if j.APIKey == "" {
		return ""
	}
	if j.AuthScheme == "" {
		return j.APIKey
	}
	return j.AuthScheme + " " + j.APIKey
}

// Configured reports whether a usable client can be built from this section.
// Validate guarantees the fields are present whenever Enabled is set, so this
// is only false for a section that was built programmatically and never
// validated.
func (j JevConfig) Configured() bool {
	return j.Enabled && j.BaseURL != "" && j.Model != "" && j.AuthHeader != "" && j.Timeout > 0
}

// DomainInputMode converts the configured literal into the client's own type.
// Validation has already refused anything else, so an invalid value here can
// only come from a programmatically built configuration; it falls back to the
// documented default rather than sending an unknown mode.
func (j JevConfig) DomainInputMode() jev.InputMode {
	mode, err := jev.ParseInputMode(j.InputMode)
	if err != nil {
		return jev.InputModeContent
	}
	return mode
}

// jevWarnings reports Jev states that are valid but likely to surprise the
// operator. It never includes credential values.
func (c Config) jevWarnings() []string {
	if !c.Jev.Enabled {
		return nil
	}
	var warnings []string
	if c.Jev.CaptureRawIO {
		warnings = append(warnings, "jev.capture_raw_io is enabled; raw Jev requests and responses, including the client's prompt text, are captured in memory for debugging")
	}
	if !registryHasRoutablePair(c) {
		warnings = append(warnings, "jev is enabled but the registry has no enabled provider/model pair; there is nothing for Jev to choose between")
	}
	// There is deliberately no warning for jev.input_mode=content. It is the
	// shipped default and the documented behavior of automatic routing, so a
	// warning would fire on every correctly configured deployment and train
	// operators to ignore warnings; the privacy consequences are described in
	// README.md instead.
	return warnings
}

// registryHasRoutablePair reports whether the configured registry declares at
// least one pair whose provider and model are both enabled. A timestamped
// models.dev sync alone leaves every synced provider disabled, so that case
// warns: the operator still has to supply credentials and turn the provider on.
func registryHasRoutablePair(c Config) bool {
	enabledProviders := make(map[string]struct{}, len(c.Registry.Providers))
	for _, provider := range c.Registry.Providers {
		if provider.Enabled {
			enabledProviders[provider.Key] = struct{}{}
		}
	}
	for _, entry := range c.Registry.Models {
		if !entry.Enabled {
			continue
		}
		if _, ok := enabledProviders[entry.Provider]; ok {
			return true
		}
	}
	return false
}
