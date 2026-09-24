package config

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/router/policy"
)

// RoutingPolicyConfig is the `routing.policy` section: the settings the policy
// engine is compiled from. It is a separate type from the engine's own Config
// because the file uses JSON spellings and validation messages, while the engine
// uses the domain types directly; the conversion is one function (Domain).
//
// Lists and tier tables are file-only, matching the registry convention: an
// environment variable overrides a scalar setting, never a table. That rule exists
// because a list in an environment variable has no syntax this project can validate
// against a file, and a half-overridden table is worse than a fixed one.
type RoutingPolicyConfig struct {
	// Version is the policy schema version. It must equal policy.Version; a file
	// written for a future schema is refused rather than partially applied.
	Version int `json:"version"`
	// HighConfidence and LowConfidence are the band boundaries.
	HighConfidence float64 `json:"high_confidence"`
	LowConfidence  float64 `json:"low_confidence"`
	// RefuseTruncatedEvidence makes a truncated analysis a hard failure instead of
	// a routing input. It defaults to true: "the analyzer did not see the request"
	// must never be read as "the request does not contain it".
	RefuseTruncatedEvidence bool `json:"refuse_truncated_evidence"`
	// DefaultModel is the single low-confidence fallback. It is only used when it
	// passes the same hard filters as every other candidate.
	DefaultModel string `json:"default_model"`
	// CostTiers and LatencyTiers are the operator-declared integer rankings.
	CostTiers    []RoutingTier `json:"cost_tiers"`
	LatencyTiers []RoutingTier `json:"latency_tiers"`
	// The six identifier lists. An empty allow list means "no restriction".
	AllowModels    []string `json:"allow_models"`
	DenyModels     []string `json:"deny_models"`
	AllowProviders []string `json:"allow_providers"`
	DenyProviders  []string `json:"deny_providers"`
	AllowPairs     []string `json:"allow_pairs"`
	DenyPairs      []string `json:"deny_pairs"`
}

// RoutingTier declares the rank of one model or one provider. Exactly one of the
// two targets is set: a tier names either a logical model or a whole provider.
type RoutingTier struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Tier     int    `json:"tier"`
}

func defaultRoutingPolicyConfig() RoutingPolicyConfig {
	defaults := policy.DefaultConfig()
	// The defaults are stated in one place — the policy package — so a deployment
	// that writes them into its file and one that omits them behave identically.
	return RoutingPolicyConfig{
		Version:                 defaults.Version,
		HighConfidence:          defaults.HighConfidence,
		LowConfidence:           defaults.LowConfidence,
		RefuseTruncatedEvidence: defaults.RefuseTruncatedEvidence,
		DefaultModel:            defaults.DefaultModel,
		CostTiers:               []RoutingTier{},
		LatencyTiers:            []RoutingTier{},
		AllowModels:             []string{},
		DenyModels:              []string{},
		AllowProviders:          []string{},
		DenyProviders:           []string{},
		AllowPairs:              []string{},
		DenyPairs:               []string{},
	}
}

// normalize fills absent list and tier fields with empty slices so a decoded
// configuration compares equal to Defaults regardless of which keys were present.
func (p *RoutingPolicyConfig) normalize() {
	if p.CostTiers == nil {
		p.CostTiers = []RoutingTier{}
	}
	if p.LatencyTiers == nil {
		p.LatencyTiers = []RoutingTier{}
	}
	for _, list := range []*[]string{&p.AllowModels, &p.DenyModels, &p.AllowProviders, &p.DenyProviders, &p.AllowPairs, &p.DenyPairs} {
		if *list == nil {
			*list = []string{}
		}
	}
}

// Domain converts the section into the engine's configuration. It performs no
// validation: Validate has already run by the time anything may be compiled, and a
// single conversion keeps the two representations from drifting.
func (p RoutingPolicyConfig) Domain() policy.Config {
	return policy.Config{
		Version:                 p.Version,
		HighConfidence:          p.HighConfidence,
		LowConfidence:           p.LowConfidence,
		RefuseTruncatedEvidence: p.RefuseTruncatedEvidence,
		DefaultModel:            p.DefaultModel,
		CostTiers:               domainTiers(p.CostTiers),
		LatencyTiers:            domainTiers(p.LatencyTiers),
		AllowModels:             copyStrings(p.AllowModels),
		DenyModels:              copyStrings(p.DenyModels),
		AllowProviders:          copyStrings(p.AllowProviders),
		DenyProviders:           copyStrings(p.DenyProviders),
		AllowPairs:              copyStrings(p.AllowPairs),
		DenyPairs:               copyStrings(p.DenyPairs),
	}
}

func domainTiers(tiers []RoutingTier) []policy.Tier {
	converted := make([]policy.Tier, 0, len(tiers))
	for _, tier := range tiers {
		converted = append(converted, policy.Tier{Model: tier.Model, Provider: tier.Provider, Tier: tier.Tier})
	}
	return converted
}

func copyStrings(values []string) []string { return append([]string(nil), values...) }

// Validate checks the policy section before any listener is bound. Every error
// names the setting and never repeats an offending value, so a hostile or
// mistaken configuration cannot push text into a startup log.
func (p RoutingPolicyConfig) Validate() error {
	if p.Version != policy.Version {
		return fmt.Errorf("routing.policy.version must be %d", policy.Version)
	}
	for _, threshold := range []struct {
		name  string
		value float64
	}{
		{"routing.policy.low_confidence", p.LowConfidence},
		{"routing.policy.high_confidence", p.HighConfidence},
	} {
		if math.IsNaN(threshold.value) || math.IsInf(threshold.value, 0) || threshold.value < 0 || threshold.value > 1 {
			return fmt.Errorf("%s must be between 0 and 1", threshold.name)
		}
	}
	if p.LowConfidence >= p.HighConfidence {
		return errors.New("routing.policy.low_confidence must be strictly below routing.policy.high_confidence")
	}
	if p.DefaultModel != "" {
		if err := models.ValidateModelID(p.DefaultModel); err != nil {
			return fmt.Errorf("routing.policy.default_model: %w", err)
		}
	}
	for _, dimension := range []struct {
		name  string
		tiers []RoutingTier
	}{
		{"cost_tiers", p.CostTiers},
		{"latency_tiers", p.LatencyTiers},
	} {
		if err := validateTiers(dimension.name, dimension.tiers); err != nil {
			return err
		}
	}
	for _, list := range []struct {
		name   string
		values []string
		pairs  bool
	}{
		{name: "allow_models", values: p.AllowModels},
		{name: "deny_models", values: p.DenyModels},
		{name: "allow_providers", values: p.AllowProviders},
		{name: "deny_providers", values: p.DenyProviders},
	} {
		if err := validateIdentifiers(list.name, list.values); err != nil {
			return err
		}
	}
	for _, list := range []struct {
		name   string
		values []string
	}{
		{name: "allow_pairs", values: p.AllowPairs},
		{name: "deny_pairs", values: p.DenyPairs},
	} {
		if err := validatePairs(list.name, list.values); err != nil {
			return err
		}
	}
	return nil
}

// validateTiers checks one ranking table. Which identifier rules apply depends on
// the target kind, which is why the two are validated separately rather than by a
// single "non-empty string" rule.
func validateTiers(field string, tiers []RoutingTier) error {
	declared := make(map[string]struct{}, len(tiers))
	for index, tier := range tiers {
		switch {
		case tier.Model == "" && tier.Provider == "":
			return fmt.Errorf("routing.policy.%s[%d] must name either a model or a provider", field, index)
		case tier.Model != "" && tier.Provider != "":
			return fmt.Errorf("routing.policy.%s[%d] must not name both a model and a provider", field, index)
		case tier.Tier < 0:
			return fmt.Errorf("routing.policy.%s[%d].tier must not be negative", field, index)
		}
		key := "model:" + tier.Model
		if tier.Model != "" {
			if err := models.ValidateModelID(tier.Model); err != nil {
				return fmt.Errorf("routing.policy.%s[%d].model: %w", field, index, err)
			}
		} else {
			key = "provider:" + tier.Provider
			if err := models.ValidateProviderKey(tier.Provider); err != nil {
				return fmt.Errorf("routing.policy.%s[%d].provider: %w", field, index, err)
			}
		}
		if _, duplicate := declared[key]; duplicate {
			// The target is reported by kind, never by value: a value echoed into a
			// log line is how a typo becomes a disclosure.
			kind := "model"
			if tier.Provider != "" {
				kind = "provider"
			}
			return fmt.Errorf("routing.policy.%s declares the same %s tier more than once", field, kind)
		}
		declared[key] = struct{}{}
	}
	return nil
}

// validateIdentifiers checks a model or provider list. The rule depends on the
// list: a model list uses the model identifier grammar, a provider list the provider
// key grammar.
func validateIdentifiers(field string, values []string) error {
	if len(values) != len(uniqueStrings(values)) {
		return fmt.Errorf("routing.policy.%s declares the same entry more than once", field)
	}
	for index, value := range values {
		if value == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("routing.policy.%s[%d] must be a non-empty identifier without surrounding whitespace", field, index)
		}
		if strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("routing.policy.%s[%d] must not contain NUL", field, index)
		}
		if strings.HasSuffix(field, "_providers") {
			if err := models.ValidateProviderKey(value); err != nil {
				return fmt.Errorf("routing.policy.%s[%d]: %w", field, index, err)
			}
			continue
		}
		if err := models.ValidateModelID(value); err != nil {
			return fmt.Errorf("routing.policy.%s[%d]: %w", field, index, err)
		}
	}
	return nil
}

// validatePairs checks a "<provider>/<model>" list. The split reuses the shared
// helper, so a model ID containing a slash stays addressable and the policy engine
// and the sync allowlist agree on the syntax.
func validatePairs(field string, values []string) error {
	if len(values) != len(uniqueStrings(values)) {
		return fmt.Errorf("routing.policy.%s declares the same entry more than once", field)
	}
	for index, value := range values {
		providerKey, modelID, err := models.SplitIncludeEntry(value)
		if err != nil {
			return fmt.Errorf("routing.policy.%s[%d] must be provider/model", field, index)
		}
		if providerKey != strings.TrimSpace(providerKey) || modelID != strings.TrimSpace(modelID) {
			return fmt.Errorf("routing.policy.%s[%d] must not be padded with whitespace", field, index)
		}
	}
	return nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

// PolicyEngine compiles the policy section. It exists so the composition root has
// exactly one way to turn configuration into an engine, and so a configuration that
// passes Validate but cannot be compiled is reported as a startup error rather than
// failing on the first request.
func (c Config) PolicyEngine() (*policy.Engine, error) {
	engine, err := policy.New(c.Routing.Policy.Domain())
	if err != nil {
		return nil, fmt.Errorf("compile routing policy: %w", err)
	}
	return engine, nil
}
