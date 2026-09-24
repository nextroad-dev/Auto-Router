package api

import (
	"net/http"
	"strings"
)

// This file is the management surface's page-shell registration. The shells
// themselves are built in dashboard.go; this function exists so the route table is
// one list a reader can check against the middleware's unauthenticated list.
//
// The registration is separated from the handler construction because the two have
// different failure modes: a missing registration makes a documented page answer
// 404, while a missing middleware entry makes a page answer 401. A test asserts that
// every route registered here is also on the unauthenticated list.

// registerDashboardRoutes mounts the page shells and the static asset directory on
// the management mux. Both are served without a credential; the middleware's
// unauthenticatedRequest is the authority on that, and this function only decides
// which paths exist.
//
// Every shell is mounted from adminPageShells rather than from its own literal list, so a page
// cannot be present in the handler's lookup and absent from the route table. That split is
// exactly how /admin/models and /admin/logs came to answer 404 for a direct visit while the
// sidebar still reached them through client-side routing.
//
// The bare root path is registered without a method pattern. A method-less pattern
// matches every method, and it wins over the mux's own "add a trailing slash" rule,
// so the bare path answers this handler's permanent redirect rather than the mux's
// temporary one. That matters for more than the status code: a permanent redirect is
// what a browser caches, and the canonical path never changes.
func registerDashboardRoutes(mux *http.ServeMux, handler *adminHandler) {
	if handler == nil {
		return
	}
	mux.HandleFunc("/admin", handler.handleAdminRootRedirect)
	for shell := range adminPageShells {
		// Go's ServeMux spells "this exact path" as `{$}`. The map keeps the operator-facing
		// path, so the trailing-slash root has to be translated before it can be registered.
		pattern := shell
		if strings.HasSuffix(shell, "/") {
			pattern = shell + "{$}"
		}
		mux.HandleFunc("GET "+pattern, handler.handleDashboardPage)
	}
	mux.HandleFunc("GET /admin/static/{path...}", handler.handleDashboardAsset)
}
