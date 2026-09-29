package config

import (
	"errors"
	"fmt"
	"math"

	"github.com/nextroad-dev/Auto-Router/internal/router/policy"
)

// RoutingPolicyConfig is the `routing.policy` section. It is a separate type
// from the engine's own Config because the configuration and API use JSON
// spellings and validation messages, while the engine uses domain types
// directly; the conversion is one function (Domain).
type RoutingPolicyConfig struct {
	// Version is the policy schema version. It must equal policy.Version; an
	// unsupported schema is refused rather than partially applied.
	Version int `json:"version"`
	// LowConfidence is the Jev group-recommendation threshold: a recommendation
	// below it is discarded and routing.auto.default_group is used instead.
	LowConfidence float64 `json:"low_confidence"`
	// RefuseTruncatedEvidence makes a truncated analysis a hard failure instead of
	// a routing input. It defaults to true: "the analyzer did not see the request"
	// must never be read as "the request does not contain it".
	RefuseTruncatedEvidence bool `json:"refuse_truncated_evidence"`
}

func defaultRoutingPolicyConfig() RoutingPolicyConfig {
	defaults := policy.DefaultConfig()
	return RoutingPolicyConfig{
		Version:                 defaults.Version,
		LowConfidence:           policy.DefaultLowConfidence,
		RefuseTruncatedEvidence: defaults.RefuseTruncatedEvidence,
	}
}

// Domain converts the section into the engine's configuration. It performs no
// validation: Validate has already run by the time anything may be compiled.
func (p RoutingPolicyConfig) Domain() policy.Config {
	return policy.Config{
		Version:                 p.Version,
		RefuseTruncatedEvidence: p.RefuseTruncatedEvidence,
	}
}

// Validate checks the policy section before any listener is bound. Every error
// names the setting and never repeats an offending value.
func (p RoutingPolicyConfig) Validate() error {
	if p.Version != policy.Version {
		return fmt.Errorf("routing.policy.version must be %d", policy.Version)
	}
	if math.IsNaN(p.LowConfidence) || math.IsInf(p.LowConfidence, 0) || p.LowConfidence < 0 || p.LowConfidence > 1 {
		return errors.New("routing.policy.low_confidence must be between 0 and 1")
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
