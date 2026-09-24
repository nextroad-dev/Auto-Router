package api

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// The browser application is built ahead of time from webui/ and committed in
// dashboard/static/web. The embedded index is a data-free page shell; authenticated
// content is fetched from /admin/v1 by the same-origin browser client.
//
//go:embed all:dashboard/static
var dashboardAssets embed.FS

var dashboardPages = map[string]struct{}{
	"/admin/":          {},
	"/admin/login":     {},
	"/admin/providers": {},
	"/admin/pairs":     {},
	"/admin/settings":  {},
	"/admin/keys":      {},
}

// Nuxt UI inserts a deterministic color-token style element. Allow only that
// exact generated block rather than enabling arbitrary inline styles.
const dashboardContentSecurityPolicy = "default-src 'none'; script-src 'self'; " +
	"style-src 'self' 'sha256-qoouoS2GGd58R0p8qGIMuCoWOfTHBmRhvQ0pn7W/tnw='; " +
	"font-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'self'; " +
	"frame-ancestors 'none'"

func (h *adminHandler) handleAdminRootRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
}

// handleDashboardPage is intentionally an exact route lookup. Unknown /admin paths
// remain 404s; this is not an SPA wildcard fallback.
func (h *adminHandler) handleDashboardPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := dashboardPages[r.URL.Path]; !ok {
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

var dashboardAssetTypes = map[string]string{
	".css": "text/css; charset=utf-8",
	".js":  "text/javascript; charset=utf-8",
}

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
	w.Header().Set("Cache-Control", "no-cache")
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
