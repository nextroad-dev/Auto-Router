package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/proxy"
)

// This file is the inbound authentication boundary: it turns the configured key
// list into one decision per request and reports the decision to the client
// without ever revealing a credential.
//
// Four properties are deliberately structural rather than promised:
//
//   - The comparison is constant time over every configured key and never returns
//     early. A caller therefore cannot learn "which half of the key was right" from
//     the time a rejection took, and the loop cannot be optimized into an early
//     exit by a future reader.
//   - A credential value never reaches a log line, an error message or a response
//     body. The identity that travels through the request context carries the key's
//     audit name and its scopes; the comparison happens against precomputed
//     digests, so the plaintext exists only inside the request's own header value.
//   - The two probes are the only endpoints without a scope, and that is a closed
//     decision written here: they answer "ok" or "not_ready" and carry no data.
//   - The decision is a pure function of the request and the configuration. It is
//     applied by one wrapper around the mux, so a route cannot be added to the
//     service without a reader of this file noticing which scope it needs.

// The scopes an endpoint may require. They are the configuration's own literals,
// aliased here so the boundary and the validator cannot drift apart.
const (
	// ScopeInference is the only scope carried by inference API keys.
	ScopeInference = config.ScopeInference
	// ScopeManagement marks password-authenticated browser sessions and is never
	// accepted as an API-key scope.
	ScopeManagement = "management"
	// Deprecated compatibility literals. Neither is accepted for API keys.
	ScopeAdmin    = config.ScopeAdmin
	ScopeOverride = config.ScopeOverride
)

// Identity is the authenticated caller as the request path may refer to it: an
// audit name and a set of scopes. There is deliberately no field for the
// credential.
type Identity struct {
	// Name is the configured key's audit name.
	Name string
	// Scopes are the permissions the key carries.
	Scopes []string
	// Scope is the scope this particular request required. It is what the audit log
	// records, so one key used for two purposes is visible as two facts.
	Scope string
}

// HasScope reports whether the identity carries one scope.
func (i Identity) HasScope(scope string) bool {
	for _, candidate := range i.Scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

// authIdentityKey is the request-context key holding the Identity. It is an
// unexported type so no other package can plant or read an identity by using a
// colliding string key.
type authIdentityContext struct{}

// IdentityFrom returns the authenticated identity of a request. The second result
// is false when the request was not authenticated, which is the state of every
// request to a process with authentication disabled.
func IdentityFrom(r *http.Request) (Identity, bool) {
	identity, ok := r.Context().Value(authIdentityContext{}).(Identity)
	return identity, ok
}

// Authenticator decides who a request is. It is immutable after construction and
// safe for concurrent use.
type Authenticator struct {
	enabled bool
	header  string
	scheme  string
	keys    atomic.Pointer[credentialSnapshot]
	logger  *slog.Logger
}

type credentialSnapshot struct{ keys []configuredKey }

// StoredCredential is an inbound credential as loaded from SQLite. Hash is the
// hexadecimal SHA-256 digest; plaintext keys are never persisted.
type StoredCredential struct {
	Name   string
	Scopes []string
	Hash   string
}

// configuredKey is one accepted credential in its precomputed form. The digest is
// what the comparison runs over, so the plaintext does not have to be held twice.
type configuredKey struct {
	name   string
	scopes []string
	digest [sha256.Size]byte
}

// NewAuthenticator builds the authenticator from the auth section. It returns a
// disabled authenticator for a disabled section rather than nil, so the wrapper
// has one code path and no nil check.
//
// The configuration has already been validated, so a key that is too short or a
// scope that is unknown cannot reach this function through the normal startup
// path; a malformed key list is refused rather than partially installed.
func NewAuthenticator(cfg config.AuthConfig, logger *slog.Logger) (*Authenticator, error) {
	authenticator := &Authenticator{
		enabled: cfg.Enabled,
		header:  cfg.Header,
		scheme:  cfg.Scheme,
		logger:  logger,
	}
	if authenticator.header == "" {
		authenticator.header = config.DefaultAuthHeader
	}
	if !cfg.Enabled {
		// A disabled section installs nothing. Validating the keys anyway would
		// refuse to start over credentials that are never consulted, which is worse
		// than useless: an operator could not keep a rotated-out key in the file.
		return authenticator, nil
	}
	keys := make([]configuredKey, 0, len(cfg.Keys))
	for _, key := range cfg.Keys {
		if err := validateKeyForAuthentication(key); err != nil {
			return nil, err
		}
		keys = append(keys, configuredKey{
			name:   key.Name,
			scopes: append([]string(nil), key.Scopes...),
			digest: sha256.Sum256([]byte(key.Key)),
		})
	}
	authenticator.keys.Store(&credentialSnapshot{keys: keys})
	return authenticator, nil
}

// NewDatabaseAuthenticator builds the fixed Authorization: Bearer authenticator
// from SQLite digests without ever loading plaintext credentials.
func NewDatabaseAuthenticator(credentials []StoredCredential, logger *slog.Logger) (*Authenticator, error) {
	a := &Authenticator{enabled: true, header: config.DefaultAuthHeader, scheme: config.DefaultAuthScheme, logger: logger}
	if err := a.ReplaceDatabaseCredentials(credentials); err != nil {
		return nil, err
	}
	return a, nil
}

var storedNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ReplaceDatabaseCredentials atomically publishes the digest snapshot after its
// SQLite transaction has committed.
func (a *Authenticator) ReplaceDatabaseCredentials(credentials []StoredCredential) error {
	if a == nil {
		return errors.New("authenticator is nil")
	}
	if len(credentials) > 64 {
		return errors.New("too many stored inbound credentials")
	}
	keys := make([]configuredKey, 0, len(credentials))
	seenNames := make(map[string]struct{}, len(credentials))
	seenDigests := make(map[string]struct{}, len(credentials))
	for _, credential := range credentials {
		if !storedNamePattern.MatchString(credential.Name) || len(credential.Scopes) == 0 {
			return errors.New("stored inbound credential metadata is invalid")
		}
		if _, exists := seenNames[credential.Name]; exists {
			return errors.New("stored inbound credential names are duplicated")
		}
		seenNames[credential.Name] = struct{}{}
		digest, err := hex.DecodeString(credential.Hash)
		if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != credential.Hash {
			return errors.New("stored inbound credential digest is invalid")
		}
		if _, exists := seenDigests[credential.Hash]; exists {
			return errors.New("stored inbound credential digests are duplicated")
		}
		seenDigests[credential.Hash] = struct{}{}
		scopes := append([]string(nil), credential.Scopes...)
		seenScopes := make(map[string]struct{}, len(scopes))
		for _, scope := range scopes {
			if !config.ValidScope(scope) {
				return errors.New("stored inbound credential scope is invalid")
			}
			if _, exists := seenScopes[scope]; exists {
				return errors.New("stored inbound credential scopes are duplicated")
			}
			seenScopes[scope] = struct{}{}
		}
		// Keep legacy rows readable for bootstrap compatibility, but never install
		// admin, override or mixed-scope keys into the active authenticator.
		if len(scopes) != 1 || scopes[0] != ScopeInference {
			continue
		}
		var sum [sha256.Size]byte
		copy(sum[:], digest)
		keys = append(keys, configuredKey{name: credential.Name, scopes: scopes, digest: sum})
	}
	a.keys.Store(&credentialSnapshot{keys: keys})
	return nil
}

// Enabled reports whether authentication is on. When it is off the wrapper does
// nothing at all: the request reaches the mux exactly as it did before this file.
func (a *Authenticator) Enabled() bool {
	return a != nil && a.enabled
}

// RequiredScope reports which scope one request needs. The second result is false
// for a path that requires no scope at all.
//
// The mapping is by path rather than by route, which is deliberate: a request to a
// path the service does not implement is still refused without a credential when it
// is inside a protected prefix, so the authentication boundary does not double as a
// route enumeration oracle.
func RequiredScope(path string) (string, bool) {
	switch {
	case path == "/healthz" || path == "/readyz":
		// The two probes are permanently unauthenticated. They answer with one word
		// and carry no data: a probe that needs a credential is a probe a load
		// balancer cannot use.
		return "", false
	case strings.HasPrefix(path, "/admin/"):
		return ScopeManagement, true
	case strings.HasPrefix(path, "/debug/"):
		return ScopeManagement, true
	case strings.HasPrefix(path, "/v1/"), strings.HasPrefix(path, "/v1beta/models/"):
		return ScopeInference, true
	default:
		return "", false
	}
}

// Authenticate resolves one request into an identity. It returns the identity, and
// on failure the status and code to answer with.
//
// The failure vocabulary is the two facts a client can act on: "this credential is
// not accepted" (401, so the client retries with a different one) and "this
// credential is accepted but may not do this" (403, so the client stops trying).
func (a *Authenticator) Authenticate(r *http.Request, required string) (Identity, *AuthFailure) {
	if !a.Enabled() {
		// An unauthenticated process has no identity to report; the empty identity
		// carries no scopes and is only visible to code that checked Enabled first.
		return Identity{Scope: required}, nil
	}
	presented, ok := a.presentedKey(r)
	if !ok {
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
			// The key is known, so the audit line names it: an operator has to be
			// able to tell "a colleague's key was refused" from "an unknown key was
			// presented".
			Key: matched.name,
		}
	}
	return identity, nil
}

// presentedKey extracts the credential from the configured header. The value is
// either "<scheme> <key>" or, when no scheme is configured, the key itself.
//
// The scheme comparison is case-insensitive because HTTP authentication schemes
// are: an operator writing "bearer" in a client must not be refused while the
// configured value is "Bearer".
func (a *Authenticator) presentedKey(r *http.Request) (string, bool) {
	values := r.Header.Values(a.header)
	if len(values) == 0 {
		return "", false
	}
	// More than one value is ambiguous: presenting two credentials is not a state
	// this service accepts, and guessing which one the client meant is how a
	// confused-deputy bug starts.
	if len(values) > 1 {
		return "", false
	}
	value := strings.TrimSpace(values[0])
	if value == "" {
		return "", false
	}
	if a.scheme == "" {
		return value, true
	}
	rest, ok := cutScheme(value, a.scheme)
	if !ok {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", false
	}
	return rest, true
}

// cutScheme splits "<scheme> <rest>" when the leading token equals scheme,
// case-insensitively. A value that is only a scheme, or that starts with another
// scheme, is rejected rather than reinterpreted.
func cutScheme(value, scheme string) (string, bool) {
	index := strings.IndexByte(value, ' ')
	if index <= 0 {
		return "", false
	}
	if !strings.EqualFold(value[:index], scheme) {
		return "", false
	}
	return value[index+1:], true
}

// match finds the configured key whose digest equals the presented one.
//
// Every configured key is compared, in configuration order, and the result is
// folded with a constant-time choice rather than returned from inside the loop.
// Returning early would make the rejection time depend on the position of the
// matching key, which is exactly the leak this construction prevents: the loop
// runs len(keys) comparisons and one subtraction per key whether the request is
// accepted or refused.
func (a *Authenticator) match(presented string) *configuredKey {
	digest := sha256.Sum256([]byte(presented))
	var found *configuredKey
	snapshot := a.keys.Load()
	if snapshot == nil {
		return nil
	}
	for i := range snapshot.keys {
		if subtle.ConstantTimeCompare(digest[:], snapshot.keys[i].digest[:]) == 1 {
			if found == nil {
				found = &snapshot.keys[i]
			}
		}
	}
	return found
}

// AuthFailure is one refused request. It carries the client-facing facts and no
// credential.
type AuthFailure struct {
	Status  int
	Code    string
	Message string
	// Key is the audit name of the key when it was recognized. It is empty for an
	// unknown or missing credential.
	Key string
}

// Write answers a refused request. The response shape depends on the surface:
// the model paths keep the OpenAI envelope every client already parses, the
// management surface uses its own. Both carry the correlation identifier and
// never carry a credential.
func (f *AuthFailure) Write(w http.ResponseWriter, r *http.Request, a *Authenticator) {
	// The challenge is written before the error body, because a header set after
	// WriteHeader has no effect.
	if f.Status == http.StatusUnauthorized && a != nil && a.scheme != "" {
		// A challenge is only meaningful with a scheme: RFC 7235 requires one, and
		// inventing a scheme the service does not accept would be a lie.
		w.Header().Set("WWW-Authenticate", a.scheme)
	}
	if strings.HasPrefix(r.URL.Path, "/admin/") {
		writeAdminError(w, f.Status, f.Code, f.Message, "")
	} else {
		proxy.WriteError(w, f.Status, proxy.ErrorTypeForStatus(f.Status), f.Code, f.Message)
	}
}

// audit records one authentication decision in the process log. The credential is
// never part of the record: only the key's audit name (or "unknown"), the scope the
// endpoint required and the outcome.
func (a *Authenticator) audit(r *http.Request, identity Identity, failure *AuthFailure) {
	if a == nil || a.logger == nil {
		return
	}
	attributes := []any{
		"auth_key", authKeyName(identity.Name, failure),
		"path", r.URL.Path,
		"method", r.Method,
	}
	if failure != nil {
		attributes = append(attributes, "reason", failure.Code, "status", failure.Status)
	} else if identity.Scope != "" {
		attributes = append(attributes, "scope", identity.Scope)
	}
	if failure != nil {
		a.logger.Warn("request rejected by authentication", attributes...)
		return
	}
	a.logger.Info("request authenticated", attributes...)
}

// authKeyName reports the audit name of a decision: the matched key's name, or
// "unknown" when no key matched. It never reports a submitted value.
func authKeyName(identityName string, failure *AuthFailure) string {
	if failure == nil {
		if identityName == "" {
			return "none"
		}
		return identityName
	}
	if failure.Key == "" {
		return "unknown"
	}
	return failure.Key
}

// WithAuthentication wraps the service's mux with the authentication boundary.
//
// The wrapper reads the required scope from the path, authenticates, and either
// hands the request to the mux with the identity in its context or answers the
// refusal. A disabled authenticator returns the mux unchanged: not a wrapper that
// forwards, but the handler itself, so a disabled process behaves exactly as it did
// before this file existed.
//
// Stage 10 adds exactly two rules to this function, and neither of them widens a
// protected prefix:
//
//   - a closed list of management page shells, static assets and the single login
//     endpoint is served without a credential. The list is a set of exact
//     method-and-path pairs, never a prefix, so an unknown page under /admin/ is
//     still refused rather than rendered.
//   - a browser session cookie is consulted only when the required scope is admin.
//     A cookie is therefore never accepted on /v1/* or /debug/*, which is what keeps
//     a browser session from becoming an inference credential.
//
// A state-changing request authenticated by a session must also be same-origin:
// SameSite=Strict already stops a cross-site cookie from being sent, and this check
// is the second, independent guard for a browser that ignores it. A request
// authenticated by a bearer key is not covered by that rule, because an API client
// sends no Origin header.
func WithAuthentication(next http.Handler, a *Authenticator) http.Handler {
	// No session store and no management routes: the header boundary alone, exactly as
	// stage 9 installed it. Every /admin/* path passes through to the mux, which
	// answers 404 because nothing registered it.
	return WithManagementSurface(next, a, nil, false)
}

// WithManagementSurface is WithAuthentication with the two pieces the management
// surface adds: the session store the login endpoint populates, and the fact that the
// /admin/* routes were mounted below this middleware.
//
// mounted is not derivable from sessions and is therefore passed explicitly. A process
// may authenticate inbound requests while serving no management surface at all
// (admin.enabled is false): such a process must answer 404 for every /admin/* path,
// because the surface does not exist and a 401 would tell an unauthenticated stranger
// that it does. A caller that registered the routes passes true.
func WithManagementSurface(next http.Handler, a *Authenticator, sessions *SessionStore, mounted bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		required, guarded := RequiredScope(r.URL.Path)
		if !guarded {
			// The probes, and any path outside the service's three prefixes, pass
			// through untouched: the mux answers 404 exactly as it always did.
			next.ServeHTTP(w, r)
			return
		}
		if isManagementPath(r.URL.Path) && !mounted {
			// No management route exists below this middleware, so there is nothing to
			// authenticate against. The request passes through and the mux answers 404,
			// which is what the process answered before this stage existed.
			next.ServeHTTP(w, r)
			return
		}
		if unauthenticatedRequest(r) {
			// The management page shells, the static assets and the login endpoint are
			// served as-is. They carry no data of their own: every number on a page is
			// fetched by the browser from an authenticated endpoint.
			next.ServeHTTP(w, r)
			return
		}
		if required == ScopeManagement && strings.HasPrefix(r.URL.Path, "/admin/") {
			hasKey := false
			if a != nil {
				_, hasKey = a.presentedKey(r)
			}
			if hasKey {
				if a.Enabled() {
					identity, failure := a.Authenticate(r, required)
					if failure != nil {
						a.audit(r, Identity{}, failure)
						failure.Write(w, r, a)
						return
					}
					a.audit(r, identity, nil)
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authIdentityContext{}, identity)))
					return
				}
				(&AuthFailure{Status: http.StatusUnauthorized, Code: "invalid_session", Message: "a valid management session is required"}).Write(w, r, a)
				return
			}
			// The cookie is read here and nowhere else: only for the management scope
			// AND only under /admin/. The path test is what excludes /debug/*, which
			// also requires the admin scope but is a development tool reached with a
			// header credential, never from a page. A browser session therefore cannot
			// reach /v1/*, /debug/* or anything outside the management surface.
			if token, ok := sessionCookie(r); ok && sessions != nil {
				if entry, found := sessions.Lookup(token); found {
					identity := Identity{Name: entry.keyName, Scopes: []string{sessionScope}, Scope: required}
					if !writeRequestGuard(r) {
						if a != nil {
							a.audit(r, Identity{}, &AuthFailure{
								Status:  http.StatusForbidden,
								Code:    "cross_site_request",
								Message: "the request did not originate from this management surface",
								Key:     entry.keyName,
							})
						}
						writeAdminError(w, http.StatusForbidden, "cross_site_request",
							"the request did not originate from this management surface", "")
						return
					}
					if a != nil {
						a.audit(r, identity, nil)
					}
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authIdentityContext{}, identity)))
					return
				}
			}
			if a != nil {
				a.audit(r, Identity{}, &AuthFailure{Status: http.StatusUnauthorized, Code: "invalid_session", Message: "a valid management session is required"})
			}
			(&AuthFailure{Status: http.StatusUnauthorized, Code: "invalid_session", Message: "a valid management session is required"}).Write(w, r, a)
			return
		}
		if !a.Enabled() {
			// Inference authentication remains controlled by auth.enabled. Management
			// paths have already required a password session above.
			next.ServeHTTP(w, r)
			return
		}
		identity, failure := a.Authenticate(r, required)
		if failure != nil {
			a.audit(r, Identity{}, failure)
			failure.Write(w, r, a)
			return
		}
		// A bearer-authenticated write is not subject to the origin rule: an API
		// client legitimately sends no Origin header, and requiring one would break
		// every documented client.
		a.audit(r, identity, nil)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authIdentityContext{}, identity)))
	})
}

// OverrideAuthorizer is retained for source compatibility. Provider override
// policy is controlled by routing configuration, never by an API-key scope.
func OverrideAuthorizer(_ *Authenticator) func(r *http.Request) proxy.ProviderOverrideDecision {
	return nil
}

// validateKeyForAuthentication refuses a key list that would install a credential
// this boundary cannot compare safely. It mirrors the configuration validator
// deliberately: this is the last line before a credential becomes live, and a
// caller that built the configuration programmatically never ran the first one.
func validateKeyForAuthentication(key config.AuthKey) error {
	if key.Name == "" || strings.TrimSpace(key.Key) == "" {
		return errors.New("an authentication key must have a name and a value")
	}
	if len(key.Scopes) != 1 || key.Scopes[0] != ScopeInference {
		return errors.New("an authentication key must carry only the inference scope")
	}
	return nil
}
