package config

import (
	"errors"
	"fmt"
	"math"

	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/router/policy"
)

// RoutingPolicyConfig is the `routing.policy` section: the settings the policy
// engine is compiled from. It is a separate type from the engine's own Config
// because the configuration and API use JSON spellings and validation messages,
// while the engine uses domain types directly; the conversion is one function
// (Domain). Tiers use the same validated runtime overlay as scalar values.
type RoutingPolicyConfig struct {
	// Version is the policy schema version. It must equal policy.Version; an
	// unsupported schema is refused rather than partially applied.
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
	}
}

func domainTiers(tiers []RoutingTier) []policy.Tier {
	converted := make([]policy.Tier, 0, len(tiers))
	for _, tier := range tiers {
		converted = append(converted, policy.Tier{Model: tier.Model, Provider: tier.Provider, Tier: tier.Tier})
	}
	return converted
}

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
