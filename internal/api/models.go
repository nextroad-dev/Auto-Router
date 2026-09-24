package api

import (
	"encoding/json"
	"net/http"

	"github.com/nextroad-dev/Auto-Router/internal/proxy"
)

// modelObject is one entry of the OpenAI /v1/models listing. created is
// deliberately absent: the registry has no creation timestamps and inventing
// one would be a fabricated fact that clients would cache.
type modelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

// modelList is the OpenAI list envelope. data is always an array, never null,
// so a client with an empty registry sees an empty catalogue instead of a
// decode error.
type modelList struct {
	Object string        `json:"object"`
	Data   []modelObject `json:"data"`
}

// handleModels serves GET/HEAD /v1/models from the current registry snapshot.
//
// Only models with at least one routable pair are advertised, and owned_by names
// the provider a request for that model would actually select, so the listing
// never promises a target the router would refuse.
func (s *server) handleModels(w http.ResponseWriter, r *http.Request) {
	// The catalogue shares the correlation contract with the forwarding paths.
	w.Header().Set(proxy.RequestIDHeader, proxy.RequestIDFor(r))
	entries := proxy.ListTargets(s.catalog.Load())
	listing := modelList{Object: "list", Data: make([]modelObject, 0, len(entries))}
	for _, entry := range entries {
		listing.Data = append(listing.Data, modelObject{
			ID:      entry.ID,
			Object:  "model",
			OwnedBy: entry.OwnedBy,
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
