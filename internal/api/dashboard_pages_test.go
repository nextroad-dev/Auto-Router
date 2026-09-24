package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// adminPageShellRoutes reads the top-level route paths out of webui/src/router.ts.
//
// The dashboard is a single-page application, so the Go side must serve a page shell for every
// route the client can render. That list lives in adminPageShells and used to be duplicated in
// the unauthenticated-request allowlist; both copies can silently omit a page, which makes the
// page unreachable by URL even though the sidebar still navigates to it client-side. This test
// keeps the two sides in step.
func adminPageShellRoutes(t *testing.T) map[string]struct{} {
	t.Helper()
	path := filepath.Join("..", "..", "webui", "src", "router.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// Normalize line endings: .gitattributes pins these files to LF, but a Windows working tree
	// can still materialize them as CRLF.
	source := strings.ReplaceAll(string(raw), "\r\n", "\n")

	// Only the top-level `path: '/...'` entries are page routes; nested children and the
	// catch-all redirect are not pages an operator can address.
	matches := regexp.MustCompile(`(?m)^\s{6}path:\s*'([^']+)',`).FindAllStringSubmatch(source, -1)
	if len(matches) == 0 {
		t.Fatal("found no top-level routes in webui/src/router.ts; the pattern needs updating")
	}
	routes := map[string]struct{}{}
	for _, m := range matches {
		path := m[1]
		if strings.Contains(path, ":pathMatch") {
			continue
		}
		routes["/admin"+path] = struct{}{}
	}
	if len(routes) == 0 {
		t.Fatal("webui/src/router.ts has no addressable page routes")
	}
	return routes
}

// TestAdminPageShellsMatchTheRouter fails when the server shell list and the client router
// disagree. A route that exists only in the router is a page that 404s on refresh; a shell that
// exists only in Go is a dead entry.
func TestAdminPageShellsMatchTheRouter(t *testing.T) {
	routes := adminPageShellRoutes(t)

	var onlyInRouter, onlyInGo []string
	for route := range routes {
		if _, ok := adminPageShells[route]; !ok {
			onlyInRouter = append(onlyInRouter, route)
		}
	}
	for shell := range adminPageShells {
		if _, ok := routes[shell]; !ok {
			onlyInGo = append(onlyInGo, shell)
		}
	}
	sort.Strings(onlyInRouter)
	sort.Strings(onlyInGo)

	if len(onlyInRouter) > 0 {
		t.Errorf("webui/src/router.ts defines pages the server serves no shell for (they 404 on refresh): %s",
			strings.Join(onlyInRouter, ", "))
	}
	if len(onlyInGo) > 0 {
		t.Errorf("adminPageShells lists shells with no matching client route: %s", strings.Join(onlyInGo, ", "))
	}
}

// TestEveryAdminPageShellIsServedByTheMux drives the real route registration and the real
// middleware together, which is the only way to catch a page that is in adminPageShells but was
// never mounted: the handler would answer 200 if asked directly, yet the mux answers 404.
//
// /admin/models and /admin/logs were unreachable this way — the sidebar reached them through
// client-side routing, so the defect only showed up on a refresh, a bookmark or a shared link.
func TestEveryAdminPageShellIsServedByTheMux(t *testing.T) {
	mux := http.NewServeMux()
	registerDashboardRoutes(mux, &adminHandler{})

	for route := range adminPageShellRoutes(t) {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
		if response.Code != http.StatusOK {
			t.Errorf("GET %s: status = %d, want %d (the page 404s on refresh)", route, response.Code, http.StatusOK)
			continue
		}
		if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
			t.Errorf("GET %s: Content-Type = %q, want text/html", route, contentType)
		}
	}

	// The bare root keeps its permanent redirect rather than rendering a shell directly.
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if response.Code != http.StatusMovedPermanently {
		t.Errorf("GET /admin: status = %d, want %d", response.Code, http.StatusMovedPermanently)
	}

	// An unregistered path below the prefix stays a 404; the mux is not an SPA wildcard.
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/nope", nil))
	if response.Code != http.StatusNotFound {
		t.Errorf("GET /admin/nope: status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

// TestEveryAdminPageShellIsReachableAndPublic pins the two properties an operator depends on:
// the page handler answers with the HTML shell, and the authentication middleware lets an
// anonymous browser fetch it. A shell that the middleware refuses is a page that renders a 401
// JSON body instead of the application.
//
// The assertions iterate the router's routes rather than adminPageShells, so removing an entry
// from the Go map fails here too instead of only shrinking the set under test.
func TestEveryAdminPageShellIsReachableAndPublic(t *testing.T) {
	routes := adminPageShellRoutes(t)

	for route := range routes {
		request := httptest.NewRequest(http.MethodGet, route, nil)
		if !unauthenticatedRequest(request) {
			t.Errorf("%s is a page route but the authentication middleware refuses it without a credential", route)
		}
	}

	// The page handler must serve the shell for every route the client can render. A nil database
	// is fine: rendering the shell never touches it.
	handler := &adminHandler{}
	for route := range routes {
		response := httptest.NewRecorder()
		handler.handleDashboardPage(response, httptest.NewRequest(http.MethodGet, route, nil))
		if response.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want %d (the page would 404 on refresh)", route, response.Code, http.StatusOK)
			continue
		}
		if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
			t.Errorf("%s: Content-Type = %q, want text/html", route, contentType)
		}
	}

	// A path that is neither a page nor a known asset stays a 404: the shell handler is an
	// exact lookup, not an SPA wildcard fallback.
	for _, path := range []string{"/admin/nope", "/admin/models/extra", "/admin/v1/settings"} {
		response := httptest.NewRecorder()
		handler.handleDashboardPage(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want %d", path, response.Code, http.StatusNotFound)
		}
	}
}
