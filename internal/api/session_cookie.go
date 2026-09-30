package api

import (
	"net/http"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/config"
)

// secureCookie decides the Secure attribute of the session cookie. This process
// serves plaintext HTTP, so in the documented local deployment a Secure cookie
// would never be sent back; behind an HTTPS reverse proxy it must be Secure. The
// "auto" mode therefore follows the browser-facing scheme, which is only taken
// from X-Forwarded-Proto when a trusted proxy sent it.
func (h *adminHandler) secureCookie(r *http.Request) bool {
	switch h.secureCookies {
	case config.SecureCookiesAlways:
		return true
	case config.SecureCookiesNever:
		return false
	default:
		return h.trustedProxies.IsHTTPS(r)
	}
}

// newSessionCookie builds the session cookie. HttpOnly keeps the token out of
// reach of any script the page might load; SameSite=Strict keeps it off every
// cross-site request.
func (h *adminHandler) newSessionCookie(r *http.Request, value string, maxAge int, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/admin",
		Secure:   h.secureCookie(r),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
		Expires:  expires,
	}
}

// clearSessionCookie expires the session cookie with the attributes it was
// issued with.
func (h *adminHandler) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, h.newSessionCookie(r, "", -1, time.Time{}))
}
