// Package api owns the HTTP boundary: the health probes, the model catalogue
// endpoint and the mounts for the forwarding handler. Protocol handling and
// upstream transport live in internal/proxy and internal/providers.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/models"
)

// ReadinessChecker is intentionally independent of a particular SQL driver.
// The composition root only supplies a database after migrations succeed.
type ReadinessChecker interface {
	PingContext(context.Context) error
}

// Options wires the HTTP boundary.
//
// Catalog may be nil, in which case the catalogue endpoint reports an empty
// list rather than panicking. Proxy may be nil, in which case the model paths
// are not registered and unknown paths keep returning 404 the way stage 1 and 2
// behaved. Debug may be nil, in which case POST /debug/analyze stays
// unregistered and answers 404: the endpoint is an opt-in development tool.
// RouteDebug is the same idea for the policy engine: nil keeps POST /debug/route
// unregistered.
//
// Admin is the same pattern with one extra rule: it is only registered when Auth
// is enabled. The composition root decides that, and it is also checked here, so a
// caller cannot mount a management surface that can rewrite the routing policy onto
// a process that authenticates nobody.
type Options struct {
	Readiness        ReadinessChecker
	ReadinessTimeout time.Duration
	Catalog          *models.Store
	Proxy            http.Handler
	Debug            http.Handler
	RouteDebug       http.Handler
	Admin            http.Handler
	// Sessions is the browser session store the management surface populates. It is
	// optional and only consulted when Auth is enabled: nil means the cookie path is
	// not installed, so a caller that never asked for it cannot have a cookie
	// accepted.
	Sessions *SessionStore
	// Auth is the inbound authenticator. Nil, or a disabled one, means the process
	// requires no credential: the handler returned by NewHandler is then exactly the
	// handler the previous stage returned.
	Auth *Authenticator
}

type server struct {
	checker          ReadinessChecker
	readinessTimeout time.Duration
	catalog          *models.Store
}

// NewHandler builds the HTTP handler. It registers exactly the endpoints the
// current stage implements: health probes, the read-only model catalogue, the two
// model protocols plus the optional debug endpoints, and — when a management
// handler and an enabled authenticator are both supplied — the Admin API.
//
// The authentication wrapper is applied outside the mux rather than inside each
// handler, so a route cannot be added without a reader of this function noticing
// which scope it needs, and so the two probes stay unauthenticated by an explicit
// rule rather than by an omission.
func NewHandler(options Options) http.Handler {
	readinessTimeout := options.ReadinessTimeout
	if readinessTimeout <= 0 {
		readinessTimeout = 2 * time.Second
	}
	catalog := options.Catalog
	if catalog == nil {
		catalog = models.NewStore()
	}
	s := &server{
		checker:          options.Readiness,
		readinessTimeout: readinessTimeout,
		catalog:          catalog,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeHealth(w, r, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), s.readinessTimeout)
		defer cancel()
		if s.checker == nil || s.checker.PingContext(ctx) != nil {
			// Do not expose database paths, driver errors or configuration.
			writeHealth(w, r, http.StatusServiceUnavailable, "not_ready")
			return
		}
		writeHealth(w, r, http.StatusOK, "ready")
	})
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("HEAD /v1/models", s.handleModels)
	if options.Proxy != nil {
		mux.Handle("POST /v1/chat/completions", options.Proxy)
		mux.Handle("POST /v1/responses", options.Proxy)
		if native, ok := options.Proxy.(interface {
			ServeAnthropicMessages(http.ResponseWriter, *http.Request)
			ServeGemini(http.ResponseWriter, *http.Request)
		}); ok {
			mux.HandleFunc("POST /v1/messages", native.ServeAnthropicMessages)
			// The colon operation is part of Google's API path and is parsed in
			// the proxy method because ServeMux wildcards cover whole segments.
			mux.HandleFunc("POST /v1beta/models/", native.ServeGemini)
		}
	}
	if options.Debug != nil {
		// The method is part of the pattern, so a GET is a 405 from the mux
		// rather than an accidental analysis.
		mux.Handle("POST /debug/analyze", options.Debug)
	}
	if options.RouteDebug != nil {
		mux.Handle("POST /debug/route", options.RouteDebug)
	}
	// The management surface is registered last and only behind an enabled
	// authenticator: a process that authenticates nobody must not expose an
	// unauthenticated way to rewrite its routing policy and registry. Its page shells
	// and the login endpoint are the only paths the middleware serves without a
	// credential, and they carry no data of their own.
	adminMounted := options.Admin != nil && options.Auth.Enabled()
	if adminMounted {
		// Both the bare path and the prefix are registered. The prefix pattern routes
		// the pages; the bare path keeps the mux from answering its own temporary
		// redirect for /admin, so the surface's own permanent redirect is the one a
		// browser sees and caches.
		mux.Handle("/admin", options.Admin)
		mux.Handle("/admin/", options.Admin)
	}
	return WithManagementSurface(mux, options.Auth, options.Sessions, adminMounted)
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
