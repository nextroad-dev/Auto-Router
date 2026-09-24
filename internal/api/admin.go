package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/logging"
	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/modelsdev"
	"github.com/nextroad-dev/Auto-Router/internal/proxy"
	"github.com/nextroad-dev/Auto-Router/internal/settings"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

// This file is the management surface's shared plumbing: the mount, the error
// envelope, the pagination contract and the request-scoped helpers every admin
// endpoint uses.
//
// Three decisions are made once here rather than per endpoint:
//
//   - The error envelope is {"error":{"code","message","field"?}}. It is a
//     different shape from the OpenAI envelope the model paths answer with,
//     because an administrator reads it and a client never sees it. Both carry
//     Cache-Control: no-store and the correlation identifier.
//   - Pagination is {"items":[...],"next_cursor":null|"<opaque>"}. The cursor is an
//     opaque position key rather than an offset, so a concurrent write cannot make
//     a row appear twice or disappear; a cursor this service did not issue is
//     refused with invalid_cursor instead of being interpreted.
//   - An admin request never reaches the routing log. It is not routing traffic,
//     and counting a "list the models" call as a routed request would corrupt the
//     log's own meaning. Only the forwarding path records events.

// AdminBodyLimit bounds one management request body. It is a code constant rather
// than a setting: a settings or registry change is a few kilobytes, and a
// deployment that needed more would be expressing a different intent.
const AdminBodyLimit = 1 << 20

// AdminReadiness reports whether the database behind the management surface
// answers. It is the same seam the readiness probe uses.
type AdminReadiness interface {
	PingContext(context.Context) error
}

// AdminOptions wires the management surface.
//
// Every part is optional and every endpoint degrades in a documented way: no
// database means storage_error, no settings store means the settings endpoints
// report read-only_state, and no catalog means the health report counts zero. The
// composition root only mounts this handler when auth.enabled and admin.enabled are
// both set.
type AdminOptions struct {
	// DB is the single connection pool. It is required: every endpoint except the
	// settings ones reads the registry or the routing log from it.
	DB *sql.DB
	// Catalog is the published registry snapshot. It is read for the health report's
	// generation and for nothing else: the row listings come from the database, which
	// is the authority the snapshot was built from.
	Catalog *models.Store
	// Settings is the runtime configuration store.
	Settings *settings.Store
	// Readiness is the database ping used by the health report.
	Readiness AdminReadiness
	// Recorder reports the routing log writer's state.
	Recorder interface {
		Stats() logging.Stats
	}
	// ReloadCatalog republishes the registry snapshot after a successful registry write.
	ReloadCatalog func(ctx context.Context) error
	// ReloadCredentials publishes the active SQLite digest snapshot after a committed key change.
	ReloadCredentials func(ctx context.Context) error
	// ReloadGroups publishes ordered group membership after a successful save.
	ReloadGroups func(ctx context.Context, groups storage.ModelGroups) error
	SyncRegistry func(ctx context.Context) (any, error)
	// ResolveModelMetadata obtains capability metadata for a provider-native model ID.
	ResolveModelMetadata func(ctx context.Context, providerHint, modelID string) (modelsdev.MetadataMatch, bool, error)
	// PageSize and MaxPageSize are admin.page_size and admin.max_page_size.
	PageSize    int
	MaxPageSize int
	// Authenticator is the inbound authenticator the login endpoint validates a
	// submitted key through. It is required for the session endpoints to be
	// registered; nil leaves them unregistered, which is the state of a process
	// that mounted no authentication.
	Authenticator *Authenticator
	// Sessions is the browser session store. It is built by the composition root and
	// handed to both this handler and the HTTP middleware, because a cookie created
	// by the login endpoint is only accepted by the middleware if the two share one
	// store. Nil means the session endpoints are not registered.
	Sessions *SessionStore
	// StartedAt is when the process began serving. It is the health report's
	// uptime origin.
	StartedAt time.Time
	// Logger receives one INFO line per accepted write. Nil disables it.
	Logger *slog.Logger
}

// adminHandler serves /admin/v1.
type adminHandler struct {
	db                   *sql.DB
	catalog              *models.Store
	settings             *settings.Store
	readiness            AdminReadiness
	recorder             interface{ Stats() logging.Stats }
	reloadCatalog        func(ctx context.Context) error
	reloadCredentials    func(ctx context.Context) error
	reloadGroups         func(ctx context.Context, groups storage.ModelGroups) error
	syncRegistry         func(ctx context.Context) (any, error)
	resolveModelMetadata func(ctx context.Context, providerHint, modelID string) (modelsdev.MetadataMatch, bool, error)
	pageSize             int
	maxPageSize          int
	startedAt            time.Time
	logger               *slog.Logger
	// authenticator validates a login submission through the same constant-time
	// loop the header path uses. It is nil when the surface was built without one.
	authenticator *Authenticator
	// sessions holds the live browser sessions. It is nil when no authenticator was
	// supplied, which is also when the session endpoints are not registered.
	sessions *SessionStore
}

// NewAdmin builds the management handler. It registers exactly the documented
// routes; the method is part of every pattern, so a wrong method is a 405 with an
// Allow header from the mux rather than an accidental write.
func NewAdmin(options AdminOptions) http.Handler {
	handler := &adminHandler{
		db:                   options.DB,
		catalog:              options.Catalog,
		settings:             options.Settings,
		readiness:            options.Readiness,
		recorder:             options.Recorder,
		reloadCatalog:        options.ReloadCatalog,
		reloadCredentials:    options.ReloadCredentials,
		reloadGroups:         options.ReloadGroups,
		syncRegistry:         options.SyncRegistry,
		resolveModelMetadata: options.ResolveModelMetadata,
		pageSize:             options.PageSize,
		maxPageSize:          options.MaxPageSize,
		startedAt:            options.StartedAt,
		logger:               options.Logger,
		authenticator:        options.Authenticator,
		sessions:             options.Sessions,
	}
	if handler.authenticator != nil && handler.authenticator.Enabled() && handler.sessions == nil {
		// The management surface accepts a session only when it can validate one. A
		// surface with an authenticator but no store would register the login endpoint
		// and then fail every probe, which is a wiring bug rather than a state the
		// operator chose; the default lifetime keeps the surface usable.
		handler.sessions = NewSessionStore(config.DefaultAdminSessionTTL, nil)
	}
	if handler.catalog == nil {
		handler.catalog = models.NewStore()
	}
	if handler.pageSize <= 0 {
		handler.pageSize = 50
	}
	if handler.maxPageSize <= 0 {
		handler.maxPageSize = 200
	}
	if handler.maxPageSize < handler.pageSize {
		handler.maxPageSize = handler.pageSize
	}
	if handler.startedAt.IsZero() {
		handler.startedAt = time.Now()
	}
	// The logger is created before any endpoint can run, so a handler with no logger
	// still has a usable one rather than a nil dereference on an error path. A test
	// that omits the logger gets silence, which is what it asked for.
	if handler.logger == nil {
		handler.logger = slog.New(slog.DiscardHandler)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/v1/health", handler.handleHealth)
	mux.HandleFunc("GET /admin/v1/models", handler.handleModelList)
	mux.HandleFunc("GET /admin/v1/models/{id...}", handler.handleModelDetail)
	mux.HandleFunc("POST /admin/v1/models", handler.handleModelCreate)
	mux.HandleFunc("PATCH /admin/v1/models/{id...}", handler.handleModelPatch)
	mux.HandleFunc("GET /admin/v1/providers", handler.handleProviderList)
	mux.HandleFunc("GET /admin/v1/providers/{key}", handler.handleProviderDetail)
	mux.HandleFunc("POST /admin/v1/providers", handler.handleProviderCreate)
	mux.HandleFunc("PATCH /admin/v1/providers/{key}", handler.handleProviderPatch)
	mux.HandleFunc("DELETE /admin/v1/providers/{key}", handler.handleProviderDelete)
	mux.HandleFunc("GET /admin/v1/providers/{key}/discover", handler.handleProviderDiscover)
	mux.HandleFunc("POST /admin/v1/providers/{key}/models", handler.handleProviderModelSelect)
	mux.HandleFunc("GET /admin/v1/pairs", handler.handlePairList)
	mux.HandleFunc("POST /admin/v1/pairs", handler.handlePairCreate)
	mux.HandleFunc("GET /admin/v1/pairs/{provider}/{model...}", handler.handlePairDetail)
	mux.HandleFunc("PATCH /admin/v1/pairs/{provider}/{model...}", handler.handlePairPatch)
	mux.HandleFunc("GET /admin/v1/logs", handler.handleLogList)
	mux.HandleFunc("GET /admin/v1/logs/stats", handler.handleLogStats)
	// The summary is the one endpoint that owns the dashboard's metric definitions:
	// every ratio it reports is computed here, from the log's own columns, so a page
	// cannot define a rate differently from the API.
	mux.HandleFunc("GET /admin/v1/logs/summary", handler.handleLogSummary)
	mux.HandleFunc("GET /admin/v1/dashboard", handler.handleDashboardReport)
	mux.HandleFunc("GET /admin/v1/settings", handler.handleSettingsGet)
	mux.HandleFunc("PATCH /admin/v1/settings", handler.handleSettingsPatch)
	mux.HandleFunc("DELETE /admin/v1/settings", handler.handleSettingsReset)
	mux.HandleFunc("GET /admin/v1/sync-state", handler.handleSyncState)
	mux.HandleFunc("POST /admin/v1/sync", handler.handleRegistrySync)
	mux.HandleFunc("GET /admin/v1/keys", handler.handleKeyList)
	mux.HandleFunc("GET /admin/v1/groups", handler.handleGroupList)
	mux.HandleFunc("PUT /admin/v1/groups", handler.handleGroupReplace)
	mux.HandleFunc("POST /admin/v1/keys", handler.handleKeyCreate)
	mux.HandleFunc("POST /admin/v1/keys/{name}/rotate", handler.handleKeyRotate)
	mux.HandleFunc("DELETE /admin/v1/keys/{name}", handler.handleKeyDisable)
	mux.HandleFunc("GET /admin/v1/setup/status", handler.handleSetupStatus)
	mux.HandleFunc("POST /admin/v1/setup/password", handler.handleSetupPassword)
	mux.HandleFunc("PATCH /admin/v1/password", handler.handlePasswordChange)
	// The session endpoints. Login is the management surface's only unauthenticated
	// POST; the probe and the logout are ordinary admin routes and are therefore
	// covered by the same scope check as everything else.
	if handler.sessions != nil {
		mux.HandleFunc("POST /admin/v1/session", handler.handleSessionLogin)
		mux.HandleFunc("GET /admin/v1/session", handler.handleSessionProbe)
		mux.HandleFunc("DELETE /admin/v1/session", handler.handleSessionLogout)
	}
	// The page shells and the static assets live on the same handler as the API.
	// They are served without a credential, which is what makes the login page
	// reachable; the middleware's unauthenticated list is what decides that, and
	// this registration only decides what exists.
	registerDashboardRoutes(mux, handler)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Every admin response carries the correlation identifier and is uncacheable.
		// It is set here rather than in each handler so an error path cannot forget
		// it.
		w.Header().Set(proxy.RequestIDHeader, proxy.RequestIDFor(r))
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

// adminErrorBody is the management error envelope.
type adminErrorBody struct {
	Error adminErrorDetail `json:"error"`
}

type adminErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

// writeAdminError answers one management failure. field is optional and names the
// offending setting or parameter; neither the message nor the field ever contains a
// value the client sent.
func writeAdminError(w http.ResponseWriter, status int, code, message, field string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(adminErrorBody{Error: adminErrorDetail{Code: code, Message: message, Field: field}})
}

// writeAdminJSON answers one successful management response.
func writeAdminJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// adminPage is the pagination envelope. Items is always an array, never null, so a
// client with an empty registry or an empty log reads an empty page instead of a
// decode error.
type adminPage struct {
	Items      []any   `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// pageOf wraps a typed slice in the pagination envelope. next is the cursor for the
// following page, or nil when this is the last one.
func pageOf[T any](items []T, next *string) adminPage {
	converted := make([]any, 0, len(items))
	for i := range items {
		converted = append(converted, items[i])
	}
	return adminPage{Items: converted, NextCursor: next}
}

// adminSearchTerm normalizes and validates optional search text. The byte bound
// keeps query work bounded; Unicode case folding is intentionally not performed
// by SQLite, so non-ASCII matching remains literal and case-sensitive.
func adminSearchTerm(raw string) (string, bool) {
	if !utf8.ValidString(raw) {
		return "", false
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	value := strings.TrimSpace(raw)
	if len(value) > 128 {
		return "", false
	}
	return value, true
}

// The cursor prefixes. They exist so a cursor issued by one endpoint is refused by
// another instead of being reinterpreted as a position in a different ordering.
const (
	cursorProvider = "p"
	cursorModel    = "m"
	cursorPair     = "r"
	cursorLog      = "l"
)

// encodeCursor renders one position key as an opaque string. The prefix and the
// fields are joined with ":" and the whole value is base64url encoded without
// padding, so a cursor is safe in a query string and is obviously not an integer to
// anyone who tries to construct one.
func encodeCursor(prefix string, fields ...string) string {
	value := prefix
	for _, field := range fields {
		value += ":" + field
	}
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

// decodeCursor parses a cursor this service issued. Anything else — a truncated
// value, a different prefix, the wrong number of fields — is refused, so a client
// cannot steer a query by editing the cursor.
func decodeCursor(cursor, prefix string, fields int) ([]string, error) {
	if cursor == "" {
		return make([]string, fields), nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, errors.New("the cursor is not a value this service issued")
	}
	parts := strings.SplitN(string(decoded), ":", fields+1)
	if len(parts) != fields+1 || parts[0] != prefix {
		return nil, errors.New("the cursor does not belong to this endpoint")
	}
	return parts[1:], nil
}

// pageLimit resolves the ?limit= parameter against the configured bounds. An absent
// limit is admin.page_size; a limit above admin.max_page_size is clamped rather than
// refused, because the caller asked for "as much as possible" and clamping answers
// that question truthfully.
func (h *adminHandler) pageLimit(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return h.pageSize, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return 0, errors.New("the limit must be a positive whole number")
	}
	if parsed > h.maxPageSize {
		return h.maxPageSize, nil
	}
	return parsed, nil
}

// readAdminBody reads and strictly decodes one request body into destination and
// reports which keys the body carried. Every failure is a client error with a fixed
// message: the decoder's own error text can quote a JSON fragment from the body,
// which must not reach a response.
//
// The key set is returned because a patch needs to distinguish "field absent" from
// "field explicitly null": for a nullable column those are two different requests
// ("leave it alone" and "clear it"), and Go's JSON decoding collapses both onto a nil
// pointer.
func readAdminBody(r *http.Request, destination any) (map[string]struct{}, error) {
	if r.ContentLength > AdminBodyLimit {
		return nil, errBodyTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, AdminBodyLimit+1))
	if err != nil {
		return nil, errors.New("the request body could not be read")
	}
	if int64(len(body)) > AdminBodyLimit {
		return nil, errBodyTooLarge
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, errors.New("the request body must be a JSON object")
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return nil, errors.New("the request body is not a valid object for this endpoint")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("the request body must contain exactly one JSON object")
	}
	present := map[string]struct{}{}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(body, &keys); err == nil {
		for key := range keys {
			present[key] = struct{}{}
		}
	}
	return present, nil
}

// wasPresent reports whether a key was carried by the request body.
func wasPresent(present map[string]struct{}, key string) bool {
	_, ok := present[key]
	return ok
}

var errBodyTooLarge = errors.New("the request body exceeds the size limit")

// writeBodyError maps a body failure onto the right status: a size failure is 413
// and everything else is 400.
func writeBodyError(w http.ResponseWriter, err error) {
	if errors.Is(err, errBodyTooLarge) {
		writeAdminError(w, http.StatusRequestEntityTooLarge, "request_too_large", err.Error(), "body")
		return
	}
	writeAdminError(w, http.StatusBadRequest, "invalid_request", err.Error(), "body")
}

// requireDatabase reports a missing connection pool. It cannot happen in a mounted
// process, and a 500 with a code is far easier to diagnose than a nil dereference.
func (h *adminHandler) requireDatabase(w http.ResponseWriter) bool {
	if h.db == nil {
		writeAdminError(w, http.StatusInternalServerError, "storage_error", "the management surface has no database", "")
		return false
	}
	return true
}

// storageFailure answers a storage or write failure. The message is authored here
// and never carries the driver's error text, which can contain a path or a bound
// value.
func storageFailure(w http.ResponseWriter, h *adminHandler, action string, err error) {
	if h.logger != nil {
		h.logger.Error("management request failed", "action", action, "error", err.Error())
	}
	writeAdminError(w, http.StatusInternalServerError, "storage_error", "the request could not be completed", "")
}

// reloadAfterWrite republishes the registry snapshot so the next request sees a
// registry change. A reload failure is reported as a 500 with the code
// snapshot_publish_failed and the fact that the change was stored, because the
// operator's remedy is different from "the write failed": the row is already there.
func (h *adminHandler) reloadAfterWrite(w http.ResponseWriter, r *http.Request, payload any, status ...int) {
	responseStatus := http.StatusOK
	if len(status) > 0 {
		responseStatus = status[0]
	}
	if h.reloadCatalog == nil {
		writeAdminJSON(w, responseStatus, payload)
		return
	}
	if err := h.reloadCatalog(r.Context()); err != nil {
		if h.logger != nil {
			h.logger.Error("the registry snapshot could not be republished", "error", err.Error())
		}
		writeAdminError(w, http.StatusInternalServerError, "snapshot_publish_failed",
			"the change was stored, but the running snapshot could not be republished; it will be picked up at the next restart", "")
		return
	}
	writeAdminJSON(w, responseStatus, payload)
}

// parseBoolParam reads one optional boolean query parameter. An unparsable value is
// a client error rather than a silently ignored filter.
func parseBoolParam(raw string) (*bool, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, errors.New("the value must be true or false")
	}
	return &parsed, nil
}

// parseTimeParam reads one optional timestamp parameter. Both RFC3339 and Unix
// seconds are accepted because an operator pasting a timestamp from a shell has one
// and an operator writing an ISO timestamp has the other.
func parseTimeParam(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed, nil
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(seconds, 0).UTC(), nil
	}
	return time.Time{}, errors.New("the value must be an RFC3339 timestamp or a Unix timestamp")
}

// isAutoModel reports whether a client-supplied identifier names the reserved
// automatic target. It is a small helper here so two endpoints cannot disagree.
func isAutoModel(id string) bool { return id == models.AutoModelID }

// unknownPathID reports a path wildcard that cannot name a registry row, without
// echoing the value. A wildcard that does not match the registry's own identifier
// grammar could not name a row anyway, so it is a 404 rather than a 400: the answer
// the client gets does not depend on whether the identifier would have been valid.
func unknownPathID(w http.ResponseWriter, code, what string) {
	writeAdminError(w, http.StatusNotFound, code, "the requested "+what+" is not registered", "")
}

// trimPathWildcard normalizes a path wildcard. Go's mux already unescapes the path,
// so the only normalization left is removing a trailing slash a client may have
// added, which would otherwise produce a lookup for a different identifier.
func trimPathWildcard(value string) string { return strings.TrimSuffix(value, "/") }
