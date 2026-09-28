package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// scriptBlock captures a <script> element's attributes and its inner text. Blocks whose
// attributes carry `src=` are external and need no hash; RE2 has no negative lookahead, so
// the two cases are separated below instead of in the pattern.
var scriptBlock = regexp.MustCompile(`(?s)<script([^>]*)>(.*?)</script>`)

// TestInlineScriptsAreAllowedByCSP pins the CSP hash for every inline script the shell carries.
//
// The theme bootstrap has to run before first paint, so it cannot move to a same-origin file,
// and `script-src 'self'` alone silently cancels it: the page still works, it just flashes the
// light palette on every dark-mode reload. That failure is invisible to the build and to a
// screenshot taken after load, which is why the hash is derived here instead of trusted.
func TestInlineScriptsAreAllowedByCSP(t *testing.T) {
	content, err := fs.ReadFile(dashboardAssets, "dashboard/static/web/index.html")
	if err != nil {
		t.Fatalf("read embedded shell: %v", err)
	}

	var inline [][]byte
	for _, match := range scriptBlock.FindAllSubmatch(content, -1) {
		if bytes.Contains(match[1], []byte("src=")) {
			continue
		}
		inline = append(inline, match[2])
	}
	if len(inline) == 0 {
		t.Fatal("the shell carries no inline script, so the theme is applied after first paint and dark mode flashes")
	}

	if strings.Contains(dashboardContentSecurityPolicy, "unsafe-inline") {
		t.Fatal("script-src or style-src enables unsafe-inline; only hashed authorings are allowed")
	}

	for index, body := range inline {
		digest := sha256.Sum256(body)
		hash := "sha256-" + base64.StdEncoding.EncodeToString(digest[:])
		if !strings.Contains(dashboardContentSecurityPolicy, "'"+hash+"'") {
			t.Errorf("inline script %d is blocked by CSP: add '%s=' to dashboardContentSecurityPolicy (recompute it from the source HTML, byte for byte)",
				index+1, hash)
		}
		t.Logf("inline script %d allowed via %s", index+1, hash)
	}
}
