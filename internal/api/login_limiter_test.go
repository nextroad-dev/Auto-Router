package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/netx"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func TestLoginThrottleIsPerSource(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	store := NewSessionStore(time.Hour, clock.Now)
	for i := 0; i < maxLoginAttemptsPerMinute; i++ {
		store.RecordFailure("203.0.113.1")
	}
	if store.AllowLogin("203.0.113.1") {
		t.Fatal("the guessing source was not throttled")
	}
	if !store.AllowLogin("198.51.100.2") {
		t.Fatal("one source's failures locked out another source")
	}
	clock.now = clock.now.Add(loginAttemptWindow)
	if !store.AllowLogin("203.0.113.1") {
		t.Fatal("the window did not expire")
	}
}

func TestLoginThrottleKeepsAGlobalCap(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	store := NewSessionStore(time.Hour, clock.Now)
	for i := 0; i < maxGlobalLoginFailuresPerMinute; i++ {
		store.RecordFailure(fmt.Sprintf("source-%d", i))
	}
	if store.AllowLogin("fresh-source") {
		t.Fatal("the global emergency cap did not apply")
	}
	clock.now = clock.now.Add(loginAttemptWindow)
	if !store.AllowLogin("fresh-source") {
		t.Fatal("the global window did not expire")
	}
}

func TestLoginThrottleSourceTableIsBounded(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	store := NewSessionStore(time.Hour, clock.Now)
	for i := 0; i < maxLoginSources+100; i++ {
		// Spread failures across windows so the global cap never triggers.
		if i%(maxGlobalLoginFailuresPerMinute-1) == 0 {
			clock.now = clock.now.Add(loginAttemptWindow)
		}
		store.RecordFailure(fmt.Sprintf("source-%d", i))
	}
	if size := len(store.limiter.sources); size > maxLoginSources {
		t.Fatalf("source table grew to %d; bound is %d", size, maxLoginSources)
	}
}

func TestSessionCookieSecureAttribute(t *testing.T) {
	trusted, _ := netx.ParseTrustedProxies([]string{"10.0.0.0/8"})
	for _, tc := range []struct {
		mode   string
		remote string
		proto  string
		want   bool
	}{
		{config.SecureCookiesAuto, "127.0.0.1:1", "", false},
		{config.SecureCookiesAuto, "10.0.0.1:1", "https", true},
		{config.SecureCookiesAuto, "203.0.113.1:1", "https", false},
		{config.SecureCookiesAlways, "127.0.0.1:1", "", true},
		{config.SecureCookiesNever, "10.0.0.1:1", "https", false},
	} {
		handler := &adminHandler{trustedProxies: trusted, secureCookies: tc.mode}
		request := httptest.NewRequest(http.MethodPost, "/admin/v1/session", nil)
		request.RemoteAddr = tc.remote
		if tc.proto != "" {
			request.Header.Set("X-Forwarded-Proto", tc.proto)
		}
		for _, cookie := range []*http.Cookie{
			handler.newSessionCookie(request, "token", 60, time.Now().Add(time.Minute)),
			func() *http.Cookie {
				response := httptest.NewRecorder()
				handler.clearSessionCookie(response, request)
				cookies := response.Result().Cookies()
				if len(cookies) != 1 {
					t.Fatal("logout did not clear exactly one cookie")
				}
				return cookies[0]
			}(),
		} {
			if cookie.Secure != tc.want || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/admin" {
				t.Fatalf("mode %s from %s proto %q: cookie = %+v; want Secure=%v HttpOnly SameSite=Strict Path=/admin", tc.mode, tc.remote, tc.proto, cookie, tc.want)
			}
		}
		if header := handler.newSessionCookie(request, "t", 1, time.Time{}).String(); tc.want != strings.Contains(header, "Secure") {
			t.Fatalf("serialized cookie %q", header)
		}
	}
}
