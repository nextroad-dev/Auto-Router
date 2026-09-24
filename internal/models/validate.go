package models

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// providerKeyPattern mirrors the identifier constraint for registry provider
// keys. Provider keys appear in configuration, SQLite primary keys and logs, so
// they are restricted to a conservative lowercase ASCII alphabet.
var providerKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// modelIDPattern mirrors the identifier constraint for logical model IDs. The
// character class is wider than provider keys because real models.dev model IDs
// include path separators (openrouter, groq) and version qualifiers such as
// "@20250514" or "~latest". Observed characters define the allowed set; an
// unexpected character fails loudly instead of being silently rewritten.
var modelIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@~+-]{0,127}$`)

const maxUpstreamModelIDLength = 256

// providerOverridePrefix introduces the explicit "provider:<provider>/<model>"
// request syntax. It is reserved: a logical model ID may not start with it, so
// a client can never be sent to an unintended target by a registry entry.
const providerOverridePrefix = "provider:"

// AutoModelID is the reserved automatic-routing target. Automatic routing is
// implemented in stage 7; until then the value is recognized and refused
// explicitly instead of being treated as an unknown model.
const AutoModelID = "auto"

// ProviderOverridePrefix returns the reserved request-syntax prefix.
func ProviderOverridePrefix() string { return providerOverridePrefix }

// gatewayProviderPattern validates the historical provider identity stored for
// compatibility with old routing records. It is intentionally the same shape
// as a registry provider key so old "<provider>/<model>" values stay predictable.
var gatewayProviderPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidateProviderKey reports whether key is a valid registry provider key.
func ValidateProviderKey(key string) error {
	if !providerKeyPattern.MatchString(key) {
		return errors.New("provider key must match ^[a-z0-9][a-z0-9._-]{0,63}$")
	}
	return nil
}

// ValidateGatewayProvider reports whether name is a valid historical provider
// identity. Case is preserved for compatibility with old Bifrost records.
func ValidateGatewayProvider(name string) error {
	if !gatewayProviderPattern.MatchString(name) {
		return errors.New("gateway_provider must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	return nil
}

// ValidateModelID reports whether id is a valid logical model ID. Two names are
// reserved: "auto" (stage 7 automatic routing) and the "provider:" prefix
// (explicit provider override), so no registry entry can shadow either syntax.
func ValidateModelID(id string) error {
	if id == AutoModelID {
		return fmt.Errorf("model id %q is reserved for automatic routing", AutoModelID)
	}
	if strings.HasPrefix(id, providerOverridePrefix) {
		return fmt.Errorf("model id must not start with the reserved prefix %q", providerOverridePrefix)
	}
	if !modelIDPattern.MatchString(id) {
		return errors.New("model id must match ^[A-Za-z0-9][A-Za-z0-9._:/-@~+]{0,127}$")
	}
	return nil
}

// ValidateBaseURL accepts an absolute http(s) URL with an optional path prefix.
// Credentials, query strings and fragments are rejected so that a base URL can
// never smuggle a secret into logs or into request construction by accident.
func ValidateBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("base_url must be an absolute http(s) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("base_url must use the http or https scheme")
	}
	if parsed.Host == "" {
		return errors.New("base_url must include a host")
	}
	if parsed.User != nil {
		return errors.New("base_url must not contain userinfo")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return errors.New("base_url must not contain a query string")
	}
	if parsed.Fragment != "" {
		return errors.New("base_url must not contain a fragment")
	}
	return nil
}

// ValidateSourceURL accepts the models.dev sync source. It must use https; a
// loopback host may use plain http so integration tests can run a local server
// without certificates.
func ValidateSourceURL(raw string) error {
	return ValidateSecureURL("registry.sync.url", raw)
}

// ValidateSecureURL accepts an outbound service endpoint: an absolute http(s)
// URL without userinfo, query string or fragment. Plain http is accepted only
// for loopback hosts, where the traffic never leaves the machine, so a
// credential sent there cannot be read off the network. field names the setting
// in every error so an operator knows which value to fix; it never includes the
// raw (possibly credentialed) value.
func ValidateSecureURL(field string, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s must be an absolute http(s) URL", field)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%s must include a host", field)
	}
	if parsed.User != nil {
		return fmt.Errorf("%s must not contain userinfo", field)
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return fmt.Errorf("%s must not contain a query string", field)
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("%s must not contain a fragment", field)
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if !IsLoopbackHost(parsed.Hostname()) {
			return fmt.Errorf("%s must use https unless the host is loopback", field)
		}
		return nil
	default:
		return fmt.Errorf("%s must use the http or https scheme", field)
	}
}

// IsLoopbackHost reports whether host names the local machine. It is exported
// so configuration sections can explain a plain-http exception the same way
// ValidateSecureURL implements it.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// SplitIncludeEntry splits a "provider/model" allowlist entry at the first
// slash. Everything after the first slash is the model reference, so model IDs
// that themselves contain a slash (for example meta-llama/Llama-3.3-70B or
// openrouter's anthropic/claude-...) are supported.
func SplitIncludeEntry(entry string) (providerKey string, modelRef string, err error) {
	if entry == "" || entry != strings.TrimSpace(entry) {
		return "", "", errors.New("registry.sync.include entries must not be empty or padded with whitespace")
	}
	index := strings.Index(entry, "/")
	if index < 0 {
		return "", "", fmt.Errorf("registry.sync.include entry %q must be provider/model", entry)
	}
	providerKey, modelRef = entry[:index], entry[index+1:]
	if err := ValidateProviderKey(providerKey); err != nil {
		return "", "", fmt.Errorf("registry.sync.include entry %q has an invalid provider: %w", entry, err)
	}
	if modelRef == "" {
		return "", "", fmt.Errorf("registry.sync.include entry %q must name a model after the slash", entry)
	}
	if err := ValidateModelID(modelRef); err != nil {
		return "", "", fmt.Errorf("registry.sync.include entry %q has an invalid model: %w", entry, err)
	}
	return providerKey, modelRef, nil
}

// ValidateProvider validates a provider record in isolation. Base URLs are
// optional only while a provider is disabled, which is also the state every
// freshly synchronized (credential-less) provider starts in.
func ValidateProvider(p Provider) error {
	if err := ValidateProviderKey(p.Key); err != nil {
		return err
	}
	if p.Kind != "" && !p.Kind.Valid() {
		return fmt.Errorf("kind must be openai, openai_compatible, anthropic, or gemini")
	}
	if err := validateDisplayName(p.DisplayName); err != nil {
		return err
	}
	if err := validateGatewayProviderField(p.GatewayProvider); err != nil {
		return err
	}
	if !p.Source.Valid() {
		return fmt.Errorf("source must be %q or %q", SourceModelsDev, SourceLocal)
	}
	if p.Priority < 0 {
		return errors.New("priority must not be negative")
	}
	if p.BaseURL == "" {
		if p.Enabled {
			return errors.New("base_url is required when the provider is enabled")
		}
		return nil
	}
	return ValidateBaseURL(p.BaseURL)
}

// validateGatewayProviderField accepts a nil (unmapped) pointer and rejects an
// empty or malformed explicit mapping. Storing an empty string instead of NULL
// would make "no mapping" indistinguishable from "mapped to nothing".
func validateGatewayProviderField(value *string) error {
	if value == nil {
		return nil
	}
	if *value == "" {
		return errors.New("gateway_provider must not be empty; omit it to fall back to the provider key")
	}
	return ValidateGatewayProvider(*value)
}

// ValidateModel validates a logical model record in isolation.
func ValidateModel(m Model) error {
	if err := ValidateModelID(m.ID); err != nil {
		return err
	}
	if err := validateDisplayName(m.DisplayName); err != nil {
		return err
	}
	if !m.Source.Valid() {
		return fmt.Errorf("source must be %q or %q", SourceModelsDev, SourceLocal)
	}
	if m.Priority < 0 {
		return errors.New("priority must not be negative")
	}
	return nil
}

// ValidatePair validates a provider/model pair in isolation. Cross-references
// to provider and model rows are checked by NewCatalog and by the importer.
func ValidatePair(p Pair) error {
	if err := ValidateProviderKey(p.ProviderKey); err != nil {
		return err
	}
	if err := ValidateModelID(p.ModelID); err != nil {
		return err
	}
	if p.UpstreamModelID == "" || p.UpstreamModelID != strings.TrimSpace(p.UpstreamModelID) {
		return errors.New("upstream_model_id must be non-empty and not padded with whitespace")
	}
	if len(p.UpstreamModelID) > maxUpstreamModelIDLength {
		return fmt.Errorf("upstream_model_id must not exceed %d bytes", maxUpstreamModelIDLength)
	}
	if strings.ContainsRune(p.UpstreamModelID, '\x00') {
		return errors.New("upstream_model_id must not contain NUL")
	}
	if p.ContextWindow <= 0 {
		return errors.New("context_window must be positive")
	}
	if p.MaxOutput != nil && (*p.MaxOutput <= 0 || *p.MaxOutput > p.ContextWindow) {
		return errors.New("max_output must be greater than zero and at most context_window")
	}
	if !p.Source.Valid() {
		return fmt.Errorf("source must be %q or %q", SourceModelsDev, SourceLocal)
	}
	if p.Priority < 0 {
		return errors.New("priority must not be negative")
	}
	return nil
}

func validateDisplayName(name string) error {
	if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
		return errors.New("display name must be non-empty and not padded with whitespace")
	}
	if strings.ContainsRune(name, '\x00') {
		return errors.New("display name must not contain NUL")
	}
	return nil
}
