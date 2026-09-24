// Package models owns the model registry domain: providers, logical models and
// the provider/model capability pairs the router routes over. It contains no
// SQL and no HTTP. The storage layer loads rows into these immutable catalogs,
// and routing code only ever reads a snapshot.
package models

import (
	"fmt"
	"sort"
)

// Source identifies who owns a registry row. Synchronized rows are refreshed by
// models.dev imports; local rows come from configuration and are never
// overwritten by a sync.
type Source string

// ProviderKind selects the upstream wire protocol used by a provider.
type ProviderKind string

const (
	ProviderOpenAI           ProviderKind = "openai"
	ProviderOpenAICompatible ProviderKind = "openai_compatible"
	ProviderAnthropic        ProviderKind = "anthropic"
	ProviderGemini           ProviderKind = "gemini"
)

// Valid reports whether k names a supported provider protocol.
func (k ProviderKind) Valid() bool {
	switch k {
	case ProviderOpenAI, ProviderOpenAICompatible, ProviderAnthropic, ProviderGemini:
		return true
	default:
		return false
	}
}

const (
	SourceModelsDev Source = "modelsdev"
	SourceLocal     Source = "local"
)

// Valid reports whether the source is a known registry source.
func (s Source) Valid() bool {
	return s == SourceModelsDev || s == SourceLocal
}

// Provider is a routable upstream endpoint. BaseURL and APIKey are local
// overrides: a models.dev sync never fills or replaces them, and the direct
// executor uses them for the selected provider request. GatewayProvider is kept
// only for compatibility with pre-migration registry rows; direct routing ignores
// it.
type Provider struct {
	Key             string
	Kind            ProviderKind
	DisplayName     string
	BaseURL         string
	APIKey          string
	Enabled         bool
	Priority        int
	Source          Source
	GatewayProvider *string
}

// GatewayKey is a deprecated compatibility helper for callers that still read
// migrated gateway metadata. Direct execution never calls it and always selects
// the provider by Key.
func (p Provider) GatewayKey() string {
	if p.GatewayProvider != nil && *p.GatewayProvider != "" {
		return *p.GatewayProvider
	}
	return p.Key
}

// Model is a logical model exposed to clients. A single logical model can be
// served by several providers through its pairs.
type Model struct {
	ID          string
	DisplayName string
	Enabled     bool
	Priority    int
	Source      Source
}

// Pair binds a logical model to one provider with the capabilities observed for
// that combination. Capabilities are deliberately not merged across providers:
// the same model can support tools through one provider and not another.
type Pair struct {
	ProviderKey     string
	ModelID         string
	UpstreamModelID string
	ContextWindow   int
	MaxOutput       *int
	SupportsTools   bool
	SupportsVision  bool
	// SupportsAudioInput is an independent capability, never a synonym for
	// SupportsVision: a text-and-audio model does not accept images, and an image
	// model does not accept audio. A missing flag means "not supported", which is
	// the fail-closed reading of an unknown capability.
	SupportsAudioInput bool
	SupportsReasoning  bool
	Enabled            bool
	Priority           int
	Source             Source
}

// Catalog is an immutable registry snapshot. Every slice is sorted according to
// the deterministic contract documented on Sort; callers must not mutate a
// snapshot obtained from a Store.
//
// Lookups use indices built at construction, so routing can resolve a model,
// provider or candidate list without scanning the whole registry per request.
type Catalog struct {
	Generation uint64
	Providers  []Provider
	Models     []Model
	Pairs      []Pair

	providerIndex   map[string]Provider
	modelIndex      map[string]Model
	pairsByModel    map[string][]Pair
	pairsByProvider map[string][]Pair
}

// NewCatalog validates and sorts a snapshot. It rejects duplicate keys and
// pairs and pairs that reference an unknown provider or model, then copies the
// inputs so a caller cannot mutate the snapshot afterwards.
func NewCatalog(generation uint64, providers []Provider, catalogModels []Model, pairs []Pair) (*Catalog, error) {
	catalog := &Catalog{
		Generation: generation,
		Providers:  append([]Provider(nil), providers...),
		Models:     append([]Model(nil), catalogModels...),
		Pairs:      append([]Pair(nil), pairs...),
	}
	providerIndex := make(map[string]Provider, len(catalog.Providers))
	for _, provider := range catalog.Providers {
		if err := ValidateProvider(provider); err != nil {
			return nil, fmt.Errorf("provider %q: %w", provider.Key, err)
		}
		if _, exists := providerIndex[provider.Key]; exists {
			return nil, fmt.Errorf("provider %q is duplicated", provider.Key)
		}
		providerIndex[provider.Key] = provider
	}
	modelIndex := make(map[string]Model, len(catalog.Models))
	for _, model := range catalog.Models {
		if err := ValidateModel(model); err != nil {
			return nil, fmt.Errorf("model %q: %w", model.ID, err)
		}
		if _, exists := modelIndex[model.ID]; exists {
			return nil, fmt.Errorf("model %q is duplicated", model.ID)
		}
		modelIndex[model.ID] = model
	}
	seenPairs := make(map[[2]string]struct{}, len(catalog.Pairs))
	for _, pair := range catalog.Pairs {
		if err := ValidatePair(pair); err != nil {
			return nil, fmt.Errorf("pair %s/%s: %w", pair.ProviderKey, pair.ModelID, err)
		}
		if _, exists := providerIndex[pair.ProviderKey]; !exists {
			return nil, fmt.Errorf("pair %s/%s references an unknown provider", pair.ProviderKey, pair.ModelID)
		}
		if _, exists := modelIndex[pair.ModelID]; !exists {
			return nil, fmt.Errorf("pair %s/%s references an unknown model", pair.ProviderKey, pair.ModelID)
		}
		key := [2]string{pair.ProviderKey, pair.ModelID}
		if _, exists := seenPairs[key]; exists {
			return nil, fmt.Errorf("pair %s/%s is duplicated", pair.ProviderKey, pair.ModelID)
		}
		seenPairs[key] = struct{}{}
	}
	catalog.Sort()
	return catalog, nil
}

func (c *Catalog) buildIndexes() {
	c.providerIndex = make(map[string]Provider, len(c.Providers))
	for _, provider := range c.Providers {
		c.providerIndex[provider.Key] = provider
	}
	c.modelIndex = make(map[string]Model, len(c.Models))
	for _, model := range c.Models {
		c.modelIndex[model.ID] = model
	}
	c.pairsByModel = make(map[string][]Pair, len(c.Models))
	for _, pair := range c.Pairs {
		c.pairsByModel[pair.ModelID] = append(c.pairsByModel[pair.ModelID], pair)
	}
	c.pairsByProvider = make(map[string][]Pair, len(c.Providers))
	for _, pair := range c.Pairs {
		c.pairsByProvider[pair.ProviderKey] = append(c.pairsByProvider[pair.ProviderKey], pair)
	}
}

// Sort applies the deterministic ordering contract shared by the router and the
// admin surface: pair priority ascending, then provider priority ascending,
// then provider key ascending, then model ID ascending. Providers are ordered
// by priority then key; models by priority then ID.
func (c *Catalog) Sort() {
	providerPriorities := make(map[string]int, len(c.Providers))
	for _, provider := range c.Providers {
		providerPriorities[provider.Key] = provider.Priority
	}
	sort.SliceStable(c.Providers, func(i, j int) bool {
		if c.Providers[i].Priority != c.Providers[j].Priority {
			return c.Providers[i].Priority < c.Providers[j].Priority
		}
		return c.Providers[i].Key < c.Providers[j].Key
	})
	sort.SliceStable(c.Models, func(i, j int) bool {
		if c.Models[i].Priority != c.Models[j].Priority {
			return c.Models[i].Priority < c.Models[j].Priority
		}
		return c.Models[i].ID < c.Models[j].ID
	})
	sort.SliceStable(c.Pairs, func(i, j int) bool {
		a, b := c.Pairs[i], c.Pairs[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		aPriority, bPriority := providerPriorities[a.ProviderKey], providerPriorities[b.ProviderKey]
		if aPriority != bPriority {
			return aPriority < bPriority
		}
		if a.ProviderKey != b.ProviderKey {
			return a.ProviderKey < b.ProviderKey
		}
		return a.ModelID < b.ModelID
	})
	c.buildIndexes()
}

// Provider returns a provider by key.
func (c *Catalog) Provider(key string) (Provider, bool) {
	provider, ok := c.providerIndex[key]
	return provider, ok
}

// Lookup returns a logical model by ID.
func (c *Catalog) Lookup(modelID string) (Model, bool) {
	model, ok := c.modelIndex[modelID]
	return model, ok
}

// PairsForModel returns the routable pairs for a logical model, in the ordering
// contract order. A pair is routable only when the pair, its provider and its
// logical model are all enabled; routing must not silently ignore a disabled
// flag on any of the three.
func (c *Catalog) PairsForModel(modelID string) []Pair {
	model, ok := c.Lookup(modelID)
	if !ok || !model.Enabled {
		return nil
	}
	return c.routablePairs(c.pairsByModel[modelID])
}

// PairsForProvider returns the routable pairs for one provider, in the ordering
// contract order. It backs the explicit provider override, which selects a
// destination the registry already declares rather than inventing one.
func (c *Catalog) PairsForProvider(providerKey string) []Pair {
	provider, ok := c.providerIndex[providerKey]
	if !ok || !provider.Enabled {
		return nil
	}
	return c.routablePairs(c.pairsByProvider[providerKey])
}

// routablePairs filters disabled pairs and pairs whose logical model is
// disabled, preserving the order produced by Sort.
func (c *Catalog) routablePairs(pairs []Pair) []Pair {
	var routable []Pair
	for _, pair := range pairs {
		if !pair.Enabled {
			continue
		}
		if provider, ok := c.providerIndex[pair.ProviderKey]; !ok || !provider.Enabled {
			continue
		}
		if model, ok := c.modelIndex[pair.ModelID]; !ok || !model.Enabled {
			continue
		}
		routable = append(routable, pair)
	}
	return routable
}

// Warnings reports catalog states that are valid but almost certainly a
// configuration mistake, such as an enabled pair whose provider or model is
// disabled. The result is deterministic and bounded.
func (c *Catalog) Warnings() []string {
	var warnings []string
	for _, pair := range c.Pairs {
		if !pair.Enabled {
			continue
		}
		if provider, ok := c.providerIndex[pair.ProviderKey]; ok && !provider.Enabled {
			warnings = append(warnings, fmt.Sprintf("pair %s/%s is enabled but provider %q is disabled", pair.ProviderKey, pair.ModelID, pair.ProviderKey))
		}
		if model, ok := c.modelIndex[pair.ModelID]; ok && !model.Enabled {
			warnings = append(warnings, fmt.Sprintf("pair %s/%s is enabled but model %q is disabled", pair.ProviderKey, pair.ModelID, pair.ModelID))
		}
	}
	return truncateWarnings(warnings)
}

const maxWarnings = 50

func truncateWarnings(warnings []string) []string {
	if len(warnings) <= maxWarnings {
		return warnings
	}
	truncated := append([]string(nil), warnings[:maxWarnings]...)
	return append(truncated, fmt.Sprintf("%d more warnings suppressed", len(warnings)-maxWarnings))
}

// EnabledProviders returns the number of enabled providers.
func (c *Catalog) EnabledProviders() int { return countEnabled(c.Providers) }

// EnabledModels returns the number of enabled logical models.
func (c *Catalog) EnabledModels() int {
	count := 0
	for _, model := range c.Models {
		if model.Enabled {
			count++
		}
	}
	return count
}

// EnabledPairs returns the number of enabled pairs.
func (c *Catalog) EnabledPairs() int {
	count := 0
	for _, pair := range c.Pairs {
		if pair.Enabled {
			count++
		}
	}
	return count
}

// Empty reports whether the catalog holds no providers, models or pairs.
func (c *Catalog) Empty() bool {
	return len(c.Providers) == 0 && len(c.Models) == 0 && len(c.Pairs) == 0
}

func countEnabled(providers []Provider) int {
	count := 0
	for _, provider := range providers {
		if provider.Enabled {
			count++
		}
	}
	return count
}
