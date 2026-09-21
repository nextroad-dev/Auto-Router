// Package api owns the HTTP boundary. Stage 1 only registers health probes;
// model-facing and admin endpoints are added in their respective stages.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// ReadinessChecker is intentionally independent of a particular SQL driver.
// The composition root only supplies a database after migrations succeed.
type ReadinessChecker interface {
	PingContext(context.Context) error
}

func NewHandler(checker ReadinessChecker, readinessTimeout time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeHealth(w, r, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()
		if checker == nil || checker.PingContext(ctx) != nil {
			// Do not expose database paths, driver errors or configuration.
			writeHealth(w, r, http.StatusServiceUnavailable, "not_ready")
			return
		}
		writeHealth(w, r, http.StatusOK, "ready")
	})
	return mux
}

func writeHealth(w http.ResponseWriter, r *http.Request, code int, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(struct {
			Status string `json:"status"`
		}{Status: status})
	}
}
