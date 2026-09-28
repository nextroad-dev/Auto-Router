package api

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

// The browser application is built ahead of time from webui/ and committed in
// dashboard/static/web. The embedded index is a data-free page shell; authenticated
// content is fetched from /admin/v1 by the same-origin browser client.
//
//go:embed all:dashboard/static
var dashboardAssets embed.FS

// adminPageShells is the one list of management pages the dashboard bundle serves a shell for.
//
// Every entry must correspond to a top-level route in webui/src/router.ts, and both the page
// handler and the unauthenticated-shell test read this map, so the two cannot drift apart again.
// A page missing here is a page an operator cannot reach by URL: the sidebar still navigates to
// it client-side, but a refresh, a bookmark or a shared link answers 404.
var adminPageShells = map[string]struct{}{
	"/admin/":          {},
	"/admin/login":     {},
	"/admin/providers": {},
	"/admin/models":    {},
	"/admin/pairs":     {},
	"/admin/settings":  {},
	"/admin/keys":      {},
	"/admin/logs":      {},
}

// The shell carries exactly one inline authoring, pinned by hash instead of by relaxing
// the directive: the theme bootstrap, which has to run before first paint or a dark-mode
// reload flashes the light palette. It cannot move to a same-origin file, and a blocked
// copy of it fails quietly — the page still works, it just flashes — so
// dashboard_theme_script_test.go recomputes the hash from the embedded HTML on every run.
//
// Styles need no exception at all: the bundle links its stylesheet, and the few dynamic
// styles the console applies go through the CSSOM rather than style attributes.
const dashboardContentSecurityPolicy = "default-src 'none'; script-src 'self' 'sha256-0rJ6++JwMTUckjv3GExgWxc2/Hv4tuA4QBgQnh13TE8='; " +
	"style-src 'self'; " +
	"font-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'self'; " +
	"frame-ancestors 'none'"

func (h *adminHandler) handleAdminRootRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
}

// handleDashboardPage is intentionally an exact route lookup. Unknown /admin paths
// remain 404s; this is not an SPA wildcard fallback.
func (h *adminHandler) handleDashboardPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminPageShells[r.URL.Path]; !ok {
		http.NotFound(w, r)
		return
	}
	content, err := fs.ReadFile(dashboardAssets, "dashboard/static/web/index.html")
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "dashboard_missing", "the page could not be rendered", "")
		return
	}
	writeShellHeaders(w.Header())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconvItoa(len(content)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(content)
	}
}

func writeShellHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Content-Security-Policy", dashboardContentSecurityPolicy)
}

// dashboardAssetTypes is the allowlist of static file types the dashboard serves, keyed by
// extension. It is an allowlist rather than a passthrough because the embedded tree is served
// from the same origin as the authenticated API.
//
// The font entries are load-bearing: the stylesheet references webfonts with `url(...)`, and a
// type missing here makes every font request a 404. That failure is quiet — the browser drops to
// the next family in the CSS font stack, so the page still renders with system fonts.
var dashboardAssetTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".woff2": "font/woff2",
}

// Vite emits content-hashed names for production assets; those URLs are immutable across
// releases, so browsers and proxies can cache them without revalidation. Keep unversioned
// assets revalidatable if the build pipeline ever starts emitting any.
var contentHashedAssetName = regexp.MustCompile(`-[A-Za-z0-9_-]{8}\.[A-Za-z0-9]+$`)

func (h *adminHandler) handleDashboardAsset(w http.ResponseWriter, r *http.Request) {
	serveEmbeddedAsset(w, r, r.PathValue("path"))
}

// serveEmbeddedAsset serves a single clean, known-type file from the embedded
// dashboard filesystem. The caller is responsible for choosing which static
// subtree is reachable; the path itself is always checked before opening a file.
func serveEmbeddedAsset(w http.ResponseWriter, r *http.Request, requested string) {
	if requested == "" || strings.HasPrefix(requested, "/") || strings.Contains(requested, "..") {
		http.NotFound(w, r)
		return
	}
	cleaned := path.Clean(requested)
	if cleaned != requested || cleaned == "." {
		http.NotFound(w, r)
		return
	}
	contentType, allowed := dashboardAssetTypes[path.Ext(cleaned)]
	if !allowed {
		http.NotFound(w, r)
		return
	}
	content, err := fs.ReadFile(dashboardAssets, "dashboard/static/"+cleaned)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeShellHeaders(w.Header())
	w.Header().Set("Content-Type", contentType)
	if contentHashedAssetName.MatchString(path.Base(cleaned)) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("ETag", assetETag(content))
	http.ServeContent(w, r, cleaned, dashboardAssetModTime, bytes.NewReader(content))
}

func assetETag(content []byte) string {
	digest := sha256.Sum256(content)
	return `"` + hex.EncodeToString(digest[:16]) + `"`
}

var dashboardAssetModTime = time.Time{}

func dashboardAssetPaths() ([]string, error) {
	var paths []string
	err := fs.WalkDir(dashboardAssets, "dashboard/static", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		paths = append(paths, strings.TrimPrefix(name, "dashboard/static/"))
		return nil
	})
	return paths, err
}

// strconvItoa avoids importing strconv in the static-serving hot path solely for
// the shell's deterministic Content-Length.
func strconvItoa(value int) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}
