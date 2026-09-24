// Package proxy implements the OpenAI-compatible forwarding path: it resolves
// the client-supplied model to exactly one registry target, rewrites only the
// top-level model field of the request body, and relays the selected provider's
// response including server-sent events without buffering or reshaping it.
//
// The package deliberately contains no OpenAI semantics. Protocol conversion,
// prompt/tool/usage handling stays outside this boundary; automatic failover is
// delegated to the routing orchestrator.
package proxy

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/nextroad-dev/Auto-Router/internal/models"
)

// Target is a fully resolved forwarding destination.
type Target struct {
	// ProviderKey is the registry provider key, used for logs and /v1/models
	// owned_by so operators see the name they configured.
	ProviderKey string
	// Provider is the selected provider row.
	Provider models.Provider
	// Pair is the selected provider/model pair.
	Pair models.Pair
	// UpstreamModel is the provider-native model identifier sent to the selected
	// provider endpoint.
	UpstreamModel string
	// Override reports whether the target came from the explicit
	// "provider:<provider>/<model>" request syntax rather than a logical ID.
	Override bool
}

// TargetError is a resolution failure with its client-facing status and code.
type TargetError struct {
	Status  int
	Code    string
	Message string
}

func (e *TargetError) Error() string { return e.Message }

// ResolveTarget maps a requested model string to one forwarding target.
//
// Resolution order is fixed and has no heuristics:
//  1. "auto" is reserved for stage 7 automatic routing and is refused with 501.
//  2. A "provider:" prefix selects the explicit override (permission switch).
//  3. Anything else is a logical model ID resolved through the registry.
//
// A logical ID resolves to the first routable pair in the registry's
// deterministic ordering contract. Before stage 7 there is no Policy engine, so
// "first" means pair priority, then provider priority, then provider key, then
// model ID ascending; this is a documented temporary rule.
func ResolveTarget(catalog *models.Catalog, requested string, allowOverride bool) (Target, error) {
	if requested == "" {
		return Target{}, &TargetError{
			Status:  http.StatusBadRequest,
			Code:    "invalid_request_error",
			Message: "the request body must contain a non-empty string model field",
		}
	}
	if requested == models.AutoModelID {
		return Target{}, &TargetError{
			Status:  http.StatusNotImplemented,
			Code:    "auto_routing_unavailable",
			Message: "automatic routing (model=\"auto\") is not implemented in this release",
		}
	}
	if strings.HasPrefix(requested, models.ProviderOverridePrefix()) {
		return resolveOverride(catalog, requested, allowOverride)
	}
	return resolveLogical(catalog, requested)
}

// resolveLogical selects the first routable pair for a logical model.
func resolveLogical(catalog *models.Catalog, requested string) (Target, error) {
	if catalog == nil {
		return Target{}, modelNotFound(requested)
	}
	if _, ok := catalog.Lookup(requested); !ok {
		return Target{}, modelNotFound(requested)
	}
	// PairsForModel already enforces that the pair, its provider and the
	// logical model are all enabled, and returns them in contract order.
	pairs := catalog.PairsForModel(requested)
	if len(pairs) == 0 {
		return Target{}, modelNotFound(requested)
	}
	pair := pairs[0]
	provider, ok := catalog.Provider(pair.ProviderKey)
	if !ok {
		return Target{}, modelNotFound(requested)
	}
	return Target{
		ProviderKey:   provider.Key,
		Provider:      provider,
		Pair:          pair,
		UpstreamModel: upstreamModel(provider, pair.UpstreamModelID),
	}, nil
}

// resolveOverride handles "provider:<provider>/<upstream_model_id>". The
// upstream model must already exist as an enabled pair for the named provider;
// the syntax selects a target, it does not create one.
func resolveOverride(catalog *models.Catalog, requested string, allowOverride bool) (Target, error) {
	if !allowOverride {
		return Target{}, &TargetError{
			Status:  http.StatusForbidden,
			Code:    "provider_override_disabled",
			Message: "the provider:<provider>/<model> override syntax is disabled by configuration",
		}
	}
	remainder := strings.TrimPrefix(requested, models.ProviderOverridePrefix())
	index := strings.Index(remainder, "/")
	if index <= 0 || index == len(remainder)-1 {
		return Target{}, &TargetError{
			Status:  http.StatusBadRequest,
			Code:    "invalid_model_identifier",
			Message: "an override model must be provider:<provider>/<model>",
		}
	}
	providerKey := remainder[:index]
	upstreamModelID := remainder[index+1:]
	if err := models.ValidateProviderKey(providerKey); err != nil {
		return Target{}, &TargetError{
			Status:  http.StatusBadRequest,
			Code:    "invalid_model_identifier",
			Message: fmt.Sprintf("override provider %q is not a valid provider key", providerKey),
		}
	}
	if catalog == nil {
		return Target{}, providerNotFound(providerKey)
	}
	provider, ok := catalog.Provider(providerKey)
	if !ok || !provider.Enabled {
		// A disabled provider is not a usable override target; reporting it as
		// unregistered is more actionable than claiming it has no models.
		return Target{}, providerNotFound(providerKey)
	}
	for _, pair := range catalog.PairsForProvider(providerKey) {
		if pair.UpstreamModelID != upstreamModelID {
			continue
		}
		return Target{
			ProviderKey:   provider.Key,
			Provider:      provider,
			Pair:          pair,
			UpstreamModel: upstreamModel(provider, pair.UpstreamModelID),
			Override:      true,
		}, nil
	}
	return Target{}, &TargetError{
		Status:  http.StatusNotFound,
		Code:    "model_not_found",
		Message: fmt.Sprintf("provider %q has no enabled model %q", providerKey, upstreamModelID),
	}
}

// upstreamModel returns the provider-native identifier. GatewayProvider is a
// retained compatibility field and is intentionally not part of the direct path.
func upstreamModel(provider models.Provider, upstreamModelID string) string {
	return upstreamModelID
}

func modelNotFound(requested string) *TargetError {
	return &TargetError{
		Status:  http.StatusNotFound,
		Code:    "model_not_found",
		Message: fmt.Sprintf("model %q is not available", requested),
	}
}

func providerNotFound(providerKey string) *TargetError {
	return &TargetError{
		Status:  http.StatusNotFound,
		Code:    "provider_not_found",
		Message: fmt.Sprintf("provider %q is not registered", providerKey),
	}
}

// ModelEntry is one advertised model for GET /v1/models. Capability fields are
// populated for the virtual automatic-routing model; logical models retain the
// OpenAI-compatible base fields only.
type ModelEntry struct {
	ID                string
	OwnedBy           string
	ContextWindow     int
	SupportsTools     bool
	SupportsVision    bool
	SupportsReasoning bool
}

// ListTargets returns the models a client may request. The virtual auto model
// is always first; routable logical models follow in registry order (model
// priority ascending, then model ID ascending).
//
// Auto is a virtual routing target, so it is advertised even when no provider
// is currently eligible. A logical model is advertised only when it has at
// least one routable pair, because advertising one without a binding would send
// clients into a guaranteed 404. Models are listed by logical ID: the override
// syntax is a privileged escape hatch, not part of the catalogue surface.
func ListTargets(catalog *models.Catalog) []ModelEntry {
	entries := []ModelEntry{{
		ID:                models.AutoModelID,
		OwnedBy:           "auto-router",
		ContextWindow:     1_000_000,
		SupportsTools:     true,
		SupportsVision:    true,
		SupportsReasoning: true,
	}}
	if catalog == nil {
		return entries
	}
	for _, model := range catalog.Models {
		if !model.Enabled {
			continue
		}
		pairs := catalog.PairsForModel(model.ID)
		if len(pairs) == 0 {
			continue
		}
		entries = append(entries, ModelEntry{ID: model.ID, OwnedBy: pairs[0].ProviderKey})
	}
	return entries
}
