package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyStartupOptionsOnlyWarnAndDoNotRunOldOperations(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "legacy-config.json")
	if err := os.WriteFile(configPath, []byte(`not-json`), 0o600); err != nil {
		t.Fatal(err)
	}
	const secret = "do-not-print-this-value"
	t.Setenv("AUTO_ROUTER_LEGACY_TEST_SECRET", secret)

	var output bytes.Buffer
	err := run(context.Background(), []string{"-config", configPath, "-sync-models"}, &output)
	if err != nil {
		t.Fatalf("legacy flags should exit after migration warnings, got: %v", err)
	}
	text := output.String()
	for _, expected := range []string{
		"legacy AUTO_ROUTER_* environment variables are ignored",
		"-config is deprecated and ignored",
		"-sync-models is deprecated and ignored",
		"Admin → Models → Sync",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("warning output %q does not contain %q", text, expected)
		}
	}
	for _, forbidden := range []string{configPath, "AUTO_ROUTER_LEGACY_TEST_SECRET", secret, "not-json"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("warning output disclosed %q: %s", forbidden, text)
		}
	}
}

func TestHasLegacyAutoRouterEnvironmentUsesNamesOnly(t *testing.T) {
	if !hasLegacyAutoRouterEnvironment([]string{"AUTO_ROUTER_ROUTING_DEFAULT_PREFERENCE=cost"}) {
		t.Fatal("expected legacy variable to be detected")
	}
	if !hasLegacyAutoRouterEnvironment([]string{"auto_router_legacy=secret"}) {
		t.Fatal("expected case-insensitive legacy prefix to be detected")
	}
	for _, environment := range [][]string{
		{"HTTP_PROXY=http://example.invalid"},
		{"AUTO_ROUTER_=empty-suffix"},
		{"AUTO_ROUTERISH_OPTION=value"},
	} {
		if hasLegacyAutoRouterEnvironment(environment) {
			t.Errorf("unexpected legacy environment detection for %q", environment)
		}
	}
}
