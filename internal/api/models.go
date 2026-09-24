package api

import (
	"encoding/json"
	"net/http"

	"github.com/nextroad-dev/Auto-Router/internal/proxy"
)

// modelObject is one entry of the OpenAI /v1/models listing. created is
// deliberately absent: the registry has no creation timestamps and inventing
// one would be a fabricated fact that clients would cache. The optional
// capability fields describe the virtual auto model; ordinary logical models
// keep the standard OpenAI-compatible fields only.
type modelObject struct {
	ID                string `json:"id"`
	Object            string `json:"object"`
	OwnedBy           string `json:"owned_by"`
	ContextWindow     int    `json:"context_window,omitempty"`
	SupportsTools     bool   `json:"supports_tools,omitempty"`
	SupportsVision    bool   `json:"supports_vision,omitempty"`
	SupportsReasoning bool   `json:"supports_reasoning,omitempty"`
}

// modelList is the OpenAI list envelope. data is always an array, never null,
// including when the registry has no configured logical models.
type modelList struct {
	Object string        `json:"object"`
	Data   []modelObject `json:"data"`
}

// handleModels serves GET/HEAD /v1/models from the current registry snapshot.
// The virtual auto model is always listed first with its advertised capability
// envelope; logical models follow only when they have a routable pair, and their
// owned_by names the provider a request would currently select.
func (s *server) handleModels(w http.ResponseWriter, r *http.Request) {
	// The catalogue shares the correlation contract with the forwarding paths.
	w.Header().Set(proxy.RequestIDHeader, proxy.RequestIDFor(r))
	entries := proxy.ListTargets(s.catalog.Load())
	listing := modelList{Object: "list", Data: make([]modelObject, 0, len(entries))}
	for _, entry := range entries {
		listing.Data = append(listing.Data, modelObject{
			ID:                entry.ID,
			Object:            "model",
			OwnedBy:           entry.OwnedBy,
			ContextWindow:     entry.ContextWindow,
			SupportsTools:     entry.SupportsTools,
			SupportsVision:    entry.SupportsVision,
			SupportsReasoning: entry.SupportsReasoning,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(listing)
}
