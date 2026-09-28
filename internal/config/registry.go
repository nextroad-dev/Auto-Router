package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/models"
)

// RegistryConfig declares code-default registry seed data and models.dev sync
// settings. Serving startup uses Defaults; registry rows and the mutable sync
// allow-list are managed through SQLite and the Admin API.
type RegistryConfig struct {
	Sync      RegistrySyncConfig `json:"sync"`
	Providers []RegistryProvider `json:"providers"`
	Models    []RegistryModel    `json:"models"`
}

// RegistrySyncConfig describes where and which models to import from models.dev.
type RegistrySyncConfig struct {
	URL     string   `json:"url"`
	Timeout Duration `json:"timeout"`
	Include []string `json:"include"`
}

// RegistryProvider is a local provider override. BaseURL and APIKey are never
// supplied by a models.dev sync; the direct executor uses them for provider
// requests. GatewayProvider remains a legacy registry field for databases and
// configurations created before the direct-provider migration, but is ignored by
// the serving path. It remains only for data and API compatibility.
type RegistryProvider struct {
	Key         string `json:"key"`
	DisplayName string `json:"display_name"`
	BaseURL     string `json:"base_url"`
	APIKey      string `json:"api_key"`
	Enabled     bool   `json:"enabled"`
	Priority    int    `json:"priority"`
	// GatewayProvider is a legacy compatibility field. It is accepted so older
	// registry files can be loaded, but direct execution always uses Key only for
	// routing/logging and sends the pair's provider-native model ID.
	GatewayProvider string `json:"gateway_provider"`
}

// RegistryModel is a local provider/model pair with explicit capabilities. It
// can describe a private model or override a synchronized pair's upstream model
// ID and capabilities. An absent enabled value means disabled, so a partially
// written entry is never silently routable.
type RegistryModel struct {
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	UpstreamModelID   string `json:"upstream_model_id"`
	DisplayName       string `json:"display_name"`
	ContextWindow     int    `json:"context_window"`
	MaxOutput         *int   `json:"max_output"`
	SupportsTools     bool   `json:"supports_tools"`
	SupportsVision    bool   `json:"supports_vision"`
	SupportsReasoning bool   `json:"supports_reasoning"`
	// SupportsAudioInput defaults to false. The mapping is fail-closed: an
	// unknown capability is read as "not supported", so a request that carries
	// audio is never sent to a pair whose audio input was never declared.
	SupportsAudioInput bool `json:"supports_audio_input"`
	Enabled            bool `json:"enabled"`
}

func defaultRegistryConfig() RegistryConfig {
	return RegistryConfig{
		Sync: RegistrySyncConfig{
			URL:     "https://models.dev/api.json",
			Timeout: Duration(30 * time.Second),
			Include: []string{},
		},
		Providers: []RegistryProvider{},
		Models:    []RegistryModel{},
	}
}

// Validate checks the whole registry section before any database work happens.
func (r RegistryConfig) Validate() error {
	if err := models.ValidateSourceURL(r.Sync.URL); err != nil {
		return err
	}
	if time.Duration(r.Sync.Timeout) <= 0 {
		return errors.New("registry.sync.timeout must be positive")
	}
	for i, entry := range r.Sync.Include {
		if _, _, err := models.SplitIncludeEntry(entry); err != nil {
			return fmt.Errorf("registry.sync.include[%d]: %w", i, err)
		}
	}
	declaredProviders := make(map[string]struct{}, len(r.Providers))
	for i, provider := range r.Providers {
		if err := models.ValidateProvider(provider.Domain()); err != nil {
			return fmt.Errorf("registry.providers[%d]: %w", i, err)
		}
		if _, duplicate := declaredProviders[provider.Key]; duplicate {
			return fmt.Errorf("registry.providers[%d]: provider %q is declared more than once", i, provider.Key)
		}
		declaredProviders[provider.Key] = struct{}{}
	}
	for i, entry := range r.Models {
		_, pair := entry.Domain()
		if err := models.ValidateModel(mustModel(entry)); err != nil {
			return fmt.Errorf("registry.models[%d]: %w", i, err)
		}
		if err := models.ValidatePair(pair); err != nil {
			return fmt.Errorf("registry.models[%d]: %w", i, err)
		}
		// Every usable pair needs a provider with a base URL and (usually) a
		// key. A models.dev sync creates disabled provider rows without either,
		// so a pair referencing an undeclared provider can never be routed and
		// is almost always a typo.
		if _, declared := declaredProviders[entry.Provider]; !declared {
			return fmt.Errorf("registry.models[%d]: provider %q is not declared in registry.providers", i, entry.Provider)
		}
	}
	return nil
}

// Domain converts a configured provider to its registry representation.
func (p RegistryProvider) Domain() models.Provider {
	displayName := p.DisplayName
	if displayName == "" {
		displayName = p.Key
	}
	return models.Provider{
		Key:             p.Key,
		DisplayName:     displayName,
		BaseURL:         p.BaseURL,
		APIKey:          p.APIKey,
		Enabled:         p.Enabled,
		Priority:        p.Priority,
		Source:          models.SourceLocal,
		GatewayProvider: normalizeGatewayProvider(p.GatewayProvider),
	}
}

// normalizeGatewayProvider maps an empty (or whitespace-only) mapping to nil,
// which means "use the provider key" in the registry.
func normalizeGatewayProvider(value string) *string {
	if value == "" {
		return nil
	}
	mapped := value
	return &mapped
}

// Domain converts a configured pair to its registry representation. The
// upstream model ID defaults to the logical model ID, which is the common case
// for OpenAI-compatible endpoints.
func (m RegistryModel) Domain() (models.Model, models.Pair) {
	displayName := m.DisplayName
	if displayName == "" {
		displayName = m.Model
	}
	upstream := m.UpstreamModelID
	if upstream == "" {
		upstream = m.Model
	}
	model := models.Model{
		ID:          m.Model,
		DisplayName: displayName,
		Enabled:     m.Enabled,
		Source:      models.SourceLocal,
	}
	maxOutput := m.MaxOutput
	if maxOutput != nil {
		value := *maxOutput
		maxOutput = &value
	}
	pair := models.Pair{
		ProviderKey:        m.Provider,
		ModelID:            m.Model,
		UpstreamModelID:    upstream,
		ContextWindow:      m.ContextWindow,
		MaxOutput:          maxOutput,
		SupportsTools:      m.SupportsTools,
		SupportsVision:     m.SupportsVision,
		SupportsReasoning:  m.SupportsReasoning,
		SupportsAudioInput: m.SupportsAudioInput,
		Enabled:            m.Enabled,
		Source:             models.SourceLocal,
	}
	return model, pair
}

func mustModel(m RegistryModel) models.Model {
	model, _ := m.Domain()
	return model
}

// LocalProviders returns the configured providers in registry form.
func (r RegistryConfig) LocalProviders() []models.Provider {
	providers := make([]models.Provider, 0, len(r.Providers))
	for _, provider := range r.Providers {
		providers = append(providers, provider.Domain())
	}
	return providers
}

// LocalCatalog returns the configured models and pairs in registry form. A
// repeated logical model is folded with last-one-wins semantics, matching the
// storage upsert.
func (r RegistryConfig) LocalCatalog() ([]models.Model, []models.Pair) {
	modelIndex := make(map[string]int, len(r.Models))
	pairs := make([]models.Pair, 0, len(r.Models))
	catalogModels := make([]models.Model, 0, len(r.Models))
	for _, entry := range r.Models {
		model, pair := entry.Domain()
		if position, ok := modelIndex[model.ID]; ok {
			catalogModels[position] = model
		} else {
			modelIndex[model.ID] = len(catalogModels)
			catalogModels = append(catalogModels, model)
		}
		pairs = append(pairs, pair)
	}
	return catalogModels, pairs
}

// Warnings reports configuration that is valid but likely to surprise the
// operator. It never includes credential values.
func (c Config) Warnings() []string {
	var warnings []string
	for _, provider := range c.Registry.Providers {
		if provider.Enabled && provider.APIKey == "" {
			warnings = append(warnings, fmt.Sprintf("registry provider %q is enabled without an api_key; requests will be sent without credentials", provider.Key))
		}
	}
	warnings = append(warnings, c.analyzerWarnings()...)
	warnings = append(warnings, c.policyWarnings()...)
	warnings = append(warnings, c.routingLogWarnings()...)
	warnings = append(warnings, c.jevWarnings()...)
	return append(warnings, c.adminWarnings()...)
}
