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

// ValidateProviderKey reports whether key is a valid registry provider key.
func ValidateProviderKey(key string) error {
	if !providerKeyPattern.MatchString(key) {
		return errors.New("provider key must match ^[a-z0-9][a-z0-9._-]{0,63}$")
	}
	return nil
}

// ValidateModelID reports whether id is a valid logical model ID.
func ValidateModelID(id string) error {
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
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("registry.sync.url must be an absolute http(s) URL")
	}
	if parsed.Host == "" {
		return errors.New("registry.sync.url must include a host")
	}
	if parsed.User != nil {
		return errors.New("registry.sync.url must not contain userinfo")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return errors.New("registry.sync.url must not contain a query string")
	}
	if parsed.Fragment != "" {
		return errors.New("registry.sync.url must not contain a fragment")
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if !isLoopbackHost(parsed.Hostname()) {
			return errors.New("registry.sync.url must use https unless the host is loopback")
		}
		return nil
	default:
		return errors.New("registry.sync.url must use the http or https scheme")
	}
}

func isLoopbackHost(host string) bool {
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
	if err := validateDisplayName(p.DisplayName); err != nil {
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
