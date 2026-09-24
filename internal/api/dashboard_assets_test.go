package api

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEveryReferencedAssetTypeIsServable closes the gap that let the webfonts 404 in production.
//
// serveEmbeddedAsset refuses any extension outside dashboardAssetTypes, but the stylesheet
// references fonts with `url(...)` just like it references nothing else. When the font files were
// added to the bundle the allowlist was not extended, so every font request answered 404 and the
// browser silently fell back to the next family in the CSS font stack: the pages kept rendering,
// which is exactly why nobody noticed.
//
// scripts/check-embedded-assets.mjs verifies that a referenced file exists in the embedded tree,
// but it never asks whether the server will serve that file's type. This test asks that question
// from the serving side, so a new asset type cannot be added without extending the allowlist.
func TestEveryReferencedAssetTypeIsServable(t *testing.T) {
	// Collect every distinct extension the embedded CSS and JS actually reference.
	referenced := map[string][]string{}
	err := fs.WalkDir(dashboardAssets, "dashboard/static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch path.Ext(p) {
		case ".css", ".js":
		default:
			return nil
		}
		content, readErr := fs.ReadFile(dashboardAssets, p)
		if readErr != nil {
			return readErr
		}
		for _, m := range regexp.MustCompile(`["'(]([^"'()\s]+?\.(?:js|css|woff2?|ttf|otf|svg|png|webp))["')]`).FindAllStringSubmatch(string(content), -1) {
			ext := path.Ext(m[1])
			referenced[ext] = append(referenced[ext], path.Base(m[1]))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded assets: %v", err)
	}
	if len(referenced) == 0 {
		t.Fatal("found no asset references in the embedded bundle; the pattern needs updating")
	}

	var missing []string
	for ext, files := range referenced {
		if _, ok := dashboardAssetTypes[ext]; !ok {
			sort.Strings(files)
			missing = append(missing, ext+" (referenced by e.g. "+files[0]+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the embedded bundle references asset types the server refuses to serve:\n  %s",
			strings.Join(missing, "\n  "))
	}
}

// TestFontAssetsAreServedWithTheRightType pins the WOFF2 type and rejects legacy WOFF
// fallbacks, which nearly double the embedded font payload without helping supported browsers.
func TestFontAssetsAreServedWithTheRightType(t *testing.T) {
	if got, ok := dashboardAssetTypes[".woff2"]; !ok {
		t.Error(".woff2 is not in dashboardAssetTypes; webfont requests would 404")
	} else if got != "font/woff2" {
		t.Errorf(".woff2: Content-Type = %q, want %q", got, "font/woff2")
	}
	if _, ok := dashboardAssetTypes[".woff"]; ok {
		t.Error(".woff is unexpectedly allowed; dashboard fonts should be WOFF2-only")
	}
}

// TestAssetHandlerServesFonts drives the real handler for one font per declared type, so the
// claim is verified end to end rather than by inspecting the map.
func TestAssetHandlerServesFonts(t *testing.T) {
	handler := &adminHandler{}
	served := 0
	err := fs.WalkDir(dashboardAssets, "dashboard/static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ext := path.Ext(p)
		wantType, ok := dashboardAssetTypes[ext]
		if !ok || !strings.HasPrefix(wantType, "font/") {
			return nil
		}
		rel := strings.TrimPrefix(p, "dashboard/static/")
		request := httptest.NewRequest(http.MethodGet, "/admin/static/"+rel, nil)
		request.SetPathValue("path", rel)
		response := httptest.NewRecorder()
		handler.handleDashboardAsset(response, request)
		if response.Code != http.StatusOK {
			t.Errorf("GET /admin/static/%s: status = %d, want %d", rel, response.Code, http.StatusOK)
			return nil
		}
		if got := response.Header().Get("Content-Type"); got != wantType {
			t.Errorf("GET /admin/static/%s: Content-Type = %q, want %q", rel, got, wantType)
		}
		if response.Body.Len() == 0 {
			t.Errorf("GET /admin/static/%s: served an empty body", rel)
		}
		served++
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded assets: %v", err)
	}
	if served == 0 {
		t.Error("the embedded bundle contains no font assets; the font imports are missing")
	}
}

func TestNotoFontFacesRetainUnicodeRangesAndUseWOFF2Only(t *testing.T) {
	var css strings.Builder
	err := fs.WalkDir(dashboardAssets, "dashboard/static/web", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".css" {
			return err
		}
		content, readErr := fs.ReadFile(dashboardAssets, p)
		if readErr != nil {
			return readErr
		}
		css.Write(content)
		css.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded CSS: %v", err)
	}

	facePattern := regexp.MustCompile(`@font-face\s*\{[^}]*\}`)
	woffPattern := regexp.MustCompile(`url\([^)]*\.woff(?:["')?]|$)`)
	counts := map[string]int{"400": 0, "500": 0}
	for _, face := range facePattern.FindAllString(css.String(), -1) {
		if !strings.Contains(face, "Noto Sans SC") {
			continue
		}
		if !strings.Contains(face, "unicode-range:") {
			t.Errorf("Noto Sans SC face lost unicode-range slicing: %.160s", face)
		}
		if !strings.Contains(face, ".woff2") || woffPattern.MatchString(face) {
			t.Errorf("Noto Sans SC face must reference WOFF2 only: %.160s", face)
		}
		weight := regexp.MustCompile(`font-weight:\s*(400|500)`).FindStringSubmatch(face)
		if len(weight) == 2 {
			counts[weight[1]]++
		}
	}
	for _, weight := range []string{"400", "500"} {
		if counts[weight] < 90 {
			t.Errorf("found %d unicode-range Noto faces at weight %s; expected at least 90", counts[weight], weight)
		}
	}
}

func TestDashboardHTMLShellIsNotCached(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	response := httptest.NewRecorder()
	(&adminHandler{}).handleDashboardPage(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /admin/login: status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("dashboard HTML Cache-Control = %q, want no-store", got)
	}
}

func TestContentHashedAssetsUseImmutableCache(t *testing.T) {
	const wantCache = "public, max-age=31536000, immutable"
	handler := &adminHandler{}
	checked := map[string]bool{}
	err := fs.WalkDir(dashboardAssets, "dashboard/static/web/assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !contentHashedAssetName.MatchString(path.Base(p)) {
			return err
		}
		ext := path.Ext(p)
		if _, ok := dashboardAssetTypes[ext]; !ok || checked[ext] {
			return nil
		}
		rel := strings.TrimPrefix(p, "dashboard/static/")
		request := httptest.NewRequest(http.MethodGet, "/admin/static/"+rel, nil)
		request.SetPathValue("path", rel)
		response := httptest.NewRecorder()
		handler.handleDashboardAsset(response, request)
		if response.Code != http.StatusOK {
			t.Errorf("GET /admin/static/%s: status = %d, want %d", rel, response.Code, http.StatusOK)
		} else if got := response.Header().Get("Cache-Control"); got != wantCache {
			t.Errorf("GET /admin/static/%s: Cache-Control = %q, want %q", rel, got, wantCache)
		}
		checked[ext] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded assets: %v", err)
	}
	for _, ext := range []string{".css", ".js", ".woff2"} {
		if !checked[ext] {
			t.Errorf("did not verify immutable cache policy for %s assets", ext)
		}
	}
}
