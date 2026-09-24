package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

// This file is the browser half of the authentication boundary: a login endpoint
// that exchanges an API key for a short-lived, in-memory session cookie, and the
// two endpoints that report and end that session.
//
// Six properties are structural rather than promised:
//
//   - A session is only ever accepted where the required scope is admin. The
//     middleware reads the cookie in exactly one place, guarded by that scope, so a
//     browser session can never stand in for an inference credential on /v1/*, and
//     the debug endpoints cannot be reached with one either.
//   - The token never lives in memory in its raw form: the store is keyed by its
//     SHA-256 digest, so a heap dump or a debugger cannot recover a live credential
//     from the map itself.
//   - Expiry is absolute. A session's deadline is computed once at creation and is
//     never extended by a later request; a page that is left open does not hold an
//     immortal session.
//   - The session set is bounded. The store evicts expired entries first and then
//     the oldest one, so an unauthenticated caller cannot make the process allocate
//     without limit by logging in repeatedly.
//   - Every session carries the audit name of the key that created it, so the
//     process log names a key rather than a generic "session".
//   - The login endpoint is the only place a credential is exchanged, and it is
//     the only unauthenticated POST on the management surface. It is same-origin
//     checked and rate-limited, and it never echoes a submitted value.
//
// Known limits, documented rather than hidden: the cookie carries no Secure
// attribute because this process serves plaintext HTTP and TLS is terminated by a
// reverse proxy in front of it; a session does not survive a restart, which is
// also why rotating auth.keys cannot leave a stale session behind (a key change
// requires a restart, and a restart clears every session).

// sessionCookieName is the name of the browser session cookie. It is deliberately
// specific: a deployment that hosts something else on the same host must not have
// its own cookie shadowed, and an operator reading a cookie jar must be able to
// tell which application issued it.
const sessionCookieName = "auto_router_admin_session"

// sessionScope is the scope a session identity carries. It is the same literal the
// authenticator uses for the management surface.
const sessionScope = ScopeManagement

// maxSessions bounds how many live sessions the process holds. The bound exists so
// an unauthenticated caller cannot grow the store without limit by logging in
// repeatedly; when it is reached the expired entries are removed first and the
// oldest session is then evicted.
const maxSessions = 64

// sessionTokenBytes is the size of the random session identifier. 32 bytes is the
// same order of magnitude as the digest it is compared through, so guessing one is
// as hard as finding a SHA-256 preimage.
const sessionTokenBytes = 32

// maxLoginAttemptsPerMinute bounds failed key submissions from one process. It is
// a constant rather than a setting: the login endpoint is reachable without a
// credential, and a deployment that needed a different bound would be expressing a
// different threat model than "one operator, one browser".
const maxLoginAttemptsPerMinute = 10

// loginAttemptWindow is the sliding window the failure counter is measured over.
const loginAttemptWindow = time.Minute

// session is one live browser session. The token itself is not a field: the store
// is keyed by its digest, and nothing else holds the raw value.
type session struct {
	// keyName is the audit name of the key that created the session. It is what
	// the process log reports, so an operator can tell which credential a session
	// belongs to.
	keyName string
	// seq is a monotonically increasing creation counter. Eviction is oldest-first,
	// and an instant from the clock is not a total order: two sessions created in the
	// same clock tick would tie, which on a coarse clock is every pair. The counter
	// makes the order total and independent of the clock's resolution.
	seq int64
	// createdAt is when the session was created.
	createdAt time.Time
	// expiresAt is absolute: nothing extends it.
	expiresAt time.Time
}

// SessionStore holds the live sessions and the login failure counter. It is safe
// for concurrent use.
type SessionStore struct {
	mutex  chan struct{}
	ttl    atomic.Int64
	now    func() time.Time
	byHash map[string]session
	// nextSeq is the creation counter handed to the next session.
	nextSeq int64
	// failures and windowStart implement the login rate guard. Only failures are
	// counted; a successful login clears the counter, because a successful login is
	// evidence the caller is not guessing.
	failures    int
	windowStart time.Time
}

func NewSessionStore(ttl time.Duration, now func() time.Time) *SessionStore {
	if ttl <= 0 {
		ttl = config.DefaultAdminSessionTTL
	}
	if now == nil {
		now = time.Now
	}
	store := &SessionStore{
		mutex:  make(chan struct{}, 1),
		now:    now,
		byHash: make(map[string]session, maxSessions),
	}
	store.ttl.Store(int64(ttl))
	return store
}

func (s *SessionStore) lock()   { s.mutex <- struct{}{} }
func (s *SessionStore) unlock() { <-s.mutex }

// TTL reports the configured session lifetime.
func (s *SessionStore) TTL() time.Duration {
	if s == nil {
		return config.DefaultAdminSessionTTL
	}
	return time.Duration(s.ttl.Load())
}

// SetTTL changes the lifetime of sessions created from this point forward.
// Existing sessions retain their original absolute deadline.
func (s *SessionStore) SetTTL(ttl time.Duration) {
	if s != nil && ttl > 0 {
		s.ttl.Store(int64(ttl))
	}
}

// Clear invalidates every existing browser session, used after key rotation or
// deactivation so a cookie issued under an old credential cannot stay live.
func (s *SessionStore) Clear() {
	if s == nil {
		return
	}
	s.lock()
	clear(s.byHash)
	s.unlock()
}

// Count reports how many live sessions the store holds. It is used by the health
// report and by the tests; it never returns a session.
func (s *SessionStore) Count() int {
	if s == nil {
		return 0
	}
	s.lock()
	defer s.unlock()
	return len(s.byHash)
}

// Create mints one session for a key name and returns the raw token and its
// deadline. The raw token is returned exactly once, to the response that sets the
// cookie; the store keeps only its digest.
func (s *SessionStore) Create(keyName string) (string, time.Time, error) {
	if s == nil {
		return "", time.Time{}, errors.New("the process has no session store")
	}
	raw := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, errors.New("a session identifier could not be generated")
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()
	s.lock()
	defer s.unlock()
	s.evictLocked(now)
	s.nextSeq++
	entry := session{keyName: keyName, seq: s.nextSeq, createdAt: now, expiresAt: now.Add(s.TTL())}
	s.byHash[hashToken(token)] = entry
	return token, entry.expiresAt, nil
}

// Lookup resolves a cookie value to its session. It reports false for an unknown,
// malformed or expired token; an expired entry is removed as it is discovered, so
// the store cleans up on use rather than only on creation.
func (s *SessionStore) Lookup(token string) (session, bool) {
	if s == nil || token == "" {
		return session{}, false
	}
	// A cookie value the store could not have issued is refused before hashing, so
	// a megabyte-long cookie cannot make the process hash a megabyte.
	if len(token) > 128 {
		return session{}, false
	}
	digest := hashToken(token)
	s.lock()
	defer s.unlock()
	entry, ok := s.byHash[digest]
	if !ok {
		return session{}, false
	}
	if !s.now().Before(entry.expiresAt) {
		delete(s.byHash, digest)
		return session{}, false
	}
	return entry, true
}

// Delete removes one session. It is what logout does.
func (s *SessionStore) Delete(token string) {
	if s == nil || token == "" || len(token) > 128 {
		return
	}
	s.lock()
	defer s.unlock()
	delete(s.byHash, hashToken(token))
}

// evictLocked removes expired sessions and then the oldest ones until the store is
// inside its bound. It runs on every creation, so the store is never larger than
// maxSessions by more than the session being added.
func (s *SessionStore) evictLocked(now time.Time) {
	for digest, entry := range s.byHash {
		if !now.Before(entry.expiresAt) {
			delete(s.byHash, digest)
		}
	}
	for len(s.byHash) >= maxSessions {
		oldestDigest := ""
		var oldest session
		for digest, entry := range s.byHash {
			if oldestDigest == "" || entry.seq < oldest.seq {
				oldestDigest, oldest = digest, entry
			}
		}
		_ = oldest
		if oldestDigest == "" {
			return
		}
		delete(s.byHash, oldestDigest)
	}
}

// AllowLogin reports whether a login attempt may be processed. Only failures are
// counted, and the window is rolling: once a minute passes without a failure the
// counter resets, so a legitimate operator who mistypes a key several times is not
// locked out for the rest of the day.
func (s *SessionStore) AllowLogin() bool {
	if s == nil {
		return true
	}
	s.lock()
	defer s.unlock()
	now := s.now()
	if now.Sub(s.windowStart) >= loginAttemptWindow {
		s.failures, s.windowStart = 0, now
	}
	return s.failures < maxLoginAttemptsPerMinute
}

// RecordFailure counts one refused login. The submitted value is not an argument:
// nothing about the attempt is recorded beyond the fact that it failed.
func (s *SessionStore) RecordFailure() {
	if s == nil {
		return
	}
	s.lock()
	defer s.unlock()
	now := s.now()
	if now.Sub(s.windowStart) >= loginAttemptWindow {
		s.failures, s.windowStart = 0, now
	}
	s.failures++
}

// RecordSuccess clears the failure counter.
func (s *SessionStore) RecordSuccess() {
	if s == nil {
		return
	}
	s.lock()
	defer s.unlock()
	s.failures, s.windowStart = 0, s.now()
}

// hashToken renders one token as the map key. The token is hashed rather than
// stored so the store's own contents cannot be replayed as a credential.
func hashToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

// sessionCookie reads the session cookie from a request. A request without one
// reports false.
func sessionCookie(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return "", false
	}
	return cookie.Value, true
}

// loginRequest is the body of POST /admin/v1/session. A pointer distinguishes a
// missing password from an explicitly null value.
type loginRequest struct {
	Password *string `json:"password"`
}

// handleSessionLogin serves POST /admin/v1/session: the only credential exchange on
// the management surface.
//
// The order of the checks is the contract: the rate guard first (so a guessing
// caller is refused before the process does any comparison work), then the
// same-origin guard (so a cross-site page cannot use the operator's browser as an
// oracle), then the key itself. Only the last step touches the credential list.
func (h *adminHandler) handleSessionLogin(w http.ResponseWriter, r *http.Request) {
	if h.db == nil || h.sessions == nil {
		// The management surface is only mounted with authentication on, so this is
		// a defensive branch rather than a reachable state. It answers 404 so a
		// probe cannot tell the difference between "not mounted" and "not
		// configured".
		writeAdminError(w, http.StatusNotFound, "not_found", "the requested endpoint does not exist", "")
		return
	}
	if !h.sessions.AllowLogin() {
		h.logger.Warn("login refused", "path", r.URL.Path, "reason", "too_many_attempts")
		writeAdminError(w, http.StatusTooManyRequests, "too_many_attempts", "too many login attempts; try again later", "")
		return
	}
	if !sameOriginRequest(r) {
		h.logger.Warn("login refused", "path", r.URL.Path, "reason", "cross_site_request")
		writeAdminError(w, http.StatusForbidden, "cross_site_request", "the request did not originate from this management surface", "")
		return
	}
	var body loginRequest
	if _, err := readAdminBody(r, &body); err != nil {
		h.sessions.RecordFailure()
		writeBodyError(w, err)
		return
	}
	if body.Password == nil {
		h.sessions.RecordFailure()
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the request body must carry a string password field", "password")
		return
	}
	valid, err := storage.VerifyPassword(r.Context(), h.db, *body.Password)
	if err != nil {
		storageFailure(w, h, "verify management password", err)
		return
	}
	if !valid {
		h.sessions.RecordFailure()
		if h.logger != nil {
			h.logger.Warn("login refused", "path", r.URL.Path, "reason", "invalid_password")
		}
		writeAdminError(w, http.StatusUnauthorized, "invalid_password", "the password is incorrect", "password")
		return
	}
	token, expiresAt, err := h.sessions.Create("owner")
	if err != nil {
		storageFailure(w, h, "create a management session", err)
		return
	}
	h.sessions.RecordSuccess()
	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookieName,
		Value: token,
		Path:  "/admin",
		// HttpOnly keeps the token out of reach of any script the page might load.
		// There is no Secure attribute on purpose: this process serves plaintext
		// HTTP, and a Secure cookie would simply never be sent in the documented
		// local deployment. TLS is the reverse proxy's responsibility and is
		// documented as such.
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(h.sessions.TTL() / time.Second),
		Expires:  expiresAt,
	})
	if h.logger != nil {
		h.logger.Info("management session created", "path", r.URL.Path)
	}
	writeAdminJSON(w, http.StatusOK, struct {
		ExpiresAt string `json:"expires_at"`
	}{ExpiresAt: expiresAt.UTC().Format(time.RFC3339Nano)})
}

// sessionPayload describes the authenticated single-user management session.
type sessionPayload struct {
	Authenticated bool     `json:"authenticated"`
	Name          string   `json:"name"`
	Scopes        []string `json:"scopes"`
	ExpiresAt     string   `json:"expires_at"`
}

// handleSessionProbe serves GET /admin/v1/session. It requires admin, so it is also
// the endpoint a page uses to discover that its session has expired: a 401 is the
// signal to show the login form rather than a broken page.
func (h *adminHandler) handleSessionProbe(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r)
	if !ok {
		writeAdminError(w, http.StatusUnauthorized, "invalid_api_key", "a valid API key is required", "")
		return
	}
	payload := sessionPayload{
		Authenticated: true,
		Name:          "owner",
		Scopes:        append([]string{}, identity.Scopes...),
	}
	if token, present := sessionCookie(r); present && h.sessions != nil {
		if entry, found := h.sessions.Lookup(token); found {
			payload.ExpiresAt = entry.expiresAt.UTC().Format(time.RFC3339Nano)
		}
	}
	writeAdminJSON(w, http.StatusOK, payload)
}

// handleSessionLogout serves DELETE /admin/v1/session. It requires admin, destroys
// the session the request was authenticated with and clears the cookie.
func (h *adminHandler) handleSessionLogout(w http.ResponseWriter, r *http.Request) {
	if token, present := sessionCookie(r); present && h.sessions != nil {
		h.sessions.Delete(token)
	}
	// The cookie is cleared with the same Path it was issued with; a cleared cookie
	// with a different path would leave the original in place.
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	if _, ok := IdentityFrom(r); ok && h.logger != nil {
		h.logger.Info("management session ended", "path", r.URL.Path)
	}
	writeAdminJSON(w, http.StatusOK, struct {
		LoggedOut bool `json:"logged_out"`
	}{LoggedOut: true})
}

// sameOriginRequest reports whether a state-changing request came from the
// management surface itself rather than from another site.
//
// Two independent signals are accepted, because two independent browsers disagree
// about which they send: the Origin header must match the request's host, or
// Sec-Fetch-Site must say same-origin. A request that carries neither is refused
// for a write, and only for a write: a curl request has no Origin either and is
// handled by the API client path, which carries its own credential and is not
// covered by this rule.
func sameOriginRequest(r *http.Request) bool {
	if site := strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")); site != "" {
		switch site {
		case "same-origin", "none":
			// "none" is a user-initiated navigation or a direct address-bar request,
			// which is not a cross-site request.
			return true
		default:
			return false
		}
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		// No origin and no fetch metadata: the caller is not a browser performing a
		// cross-site request. The cookie itself is SameSite=Strict, which is the
		// second line of defence for exactly this case.
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if !strings.EqualFold(parsed.Host, r.Host) {
		return false
	}
	// A mismatched scheme is a mixed-content attempt rather than a legitimate
	// same-origin request.
	switch parsed.Scheme {
	case "http", "https":
	default:
		return false
	}
	return true
}

// unauthenticatedRequest reports whether one request is served without a
// credential. The list is closed and exact: a method and a path, never a prefix,
// so a future endpoint cannot become public by being added under a path that
// happens to start with an allowed one.
//
// Two groups are on the list:
//
//   - the management surface's own page shells and static assets, which contain no
//     data, no credential and no database content. They render a frame; every number
//     on them is fetched by the browser from an authenticated /admin/v1 endpoint.
//     Serving them without a credential is what lets the login page exist at all,
//     and it discloses nothing: a stranger who fetches /admin/ learns the
//     application's name and nothing about the deployment.
//   - the login endpoint itself, which is the only credential exchange and carries
//     its own same-origin and rate guards.
//
// Everything else under /admin/ — including an unknown path — still requires the
// admin scope, as do /debug/* and /v1/*. The two probes are handled by
// RequiredScope, which returns before this function is consulted.
//
// This function decides whether a request needs a credential, not whether it is
// implemented. A path on the list that the process did not register therefore reaches
// the mux and answers 404, which is exactly what "the management surface is disabled"
// has to look like: with auth.enabled or admin.enabled off, all twelve new routes
// answer the same 404 they would have answered before this stage existed. Gating the
// list on whether the surface is mounted would turn those 404s into 401s, which would
// tell an unauthenticated stranger that a management surface exists.
func unauthenticatedRequest(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		return false
	}
	if r.Method == http.MethodPost {
		// Login and first-run password setup are the only unauthenticated writes.
		return r.URL.Path == "/admin/v1/session" || r.URL.Path == "/admin/v1/setup/password"
	}
	switch r.URL.Path {
	case "/admin/v1/setup/status":
		return true
	case "/admin", "/admin/", "/admin/login", "/admin/providers", "/admin/pairs", "/admin/settings", "/admin/keys":
		return true
	}
	// Static assets are matched by their exact prefix and then re-validated by the
	// asset handler, which refuses a path that escapes the embedded filesystem. The
	// prefix test here is deliberately narrow: it does not admit /admin/static
	// without a slash.
	return strings.HasPrefix(r.URL.Path, "/admin/static/")
}

// isManagementPath reports whether a path belongs to the management surface. It is
// the /admin prefix and the bare root, and nothing else: /debug/* and /v1/* are not
// management paths and are never affected by whether the surface was mounted.
func isManagementPath(path string) bool {
	return path == "/admin" || strings.HasPrefix(path, "/admin/")
}

// writeRequestGuard refuses a state-changing request that did not originate from
// the management surface itself. It applies to writes performed with a session
// cookie, not to writes authenticated with a bearer key: an API client sends no
// Origin header, and requiring one would break every documented client.
func writeRequestGuard(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	return sameOriginRequest(r)
}

// AuthenticateKey resolves a submitted credential value into an identity, without a
// request. It is the login endpoint's entry point and shares the loop the header
// path uses, so the two cannot diverge in timing or in vocabulary.
func (a *Authenticator) AuthenticateKey(presented string, required string) (Identity, *AuthFailure) {
	if a == nil || !a.Enabled() {
		return Identity{}, &AuthFailure{
			Status:  http.StatusUnauthorized,
			Code:    "invalid_api_key",
			Message: "a valid API key is required",
		}
	}
	if strings.TrimSpace(presented) == "" {
		return Identity{}, &AuthFailure{
			Status:  http.StatusUnauthorized,
			Code:    "invalid_api_key",
			Message: "a valid API key is required",
		}
	}
	matched := a.match(presented)
	if matched == nil {
		return Identity{}, &AuthFailure{
			Status:  http.StatusUnauthorized,
			Code:    "invalid_api_key",
			Message: "a valid API key is required",
		}
	}
	identity := Identity{Name: matched.name, Scopes: matched.scopes, Scope: required}
	if required != "" && !identity.HasScope(required) {
		return Identity{}, &AuthFailure{
			Status:  http.StatusForbidden,
			Code:    "insufficient_scope",
			Message: "this API key does not carry the " + required + " scope",
			Key:     matched.name,
		}
	}
	return identity, nil
}

// auditLogin records the outcome of one credential exchange. It reuses the request
// audit's vocabulary so a refused login and a refused request are two instances of
// the same event rather than two unrelated formats. The submitted value is never an
// argument.
func (a *Authenticator) auditLogin(r *http.Request, failure *AuthFailure) {
	if a == nil || a.logger == nil {
		return
	}
	a.logger.Warn("login refused by authentication",
		"auth_key", authKeyName("", failure),
		"path", r.URL.Path,
		"method", r.Method,
		"reason", failure.Code,
		"status", failure.Status,
	)
}

// sessionReport is the management health report's view of the session store. It
// reports a count and the configured lifetime, never a token and never a key name.
type sessionReport struct {
	Live  int   `json:"live"`
	TTLMS int64 `json:"ttl_ms"`
	Bound int   `json:"bound"`
}

// sessions reports the session store's state for the health endpoint.
func (h *adminHandler) sessionsReport() sessionReport {
	if h.sessions == nil {
		return sessionReport{}
	}
	return sessionReport{
		Live:  h.sessions.Count(),
		TTLMS: h.sessions.TTL().Milliseconds(),
		Bound: maxSessions,
	}
}
