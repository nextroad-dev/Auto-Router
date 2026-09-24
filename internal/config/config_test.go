package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadUsesCodeDefaultsInsteadOfFilesOrEnvironment(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "legacy-config.json")
	if err := os.WriteFile(configPath, []byte(`{"routing":{"default_preference":"cost"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(RoutingPreferenceEnv, "latency")

	got, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	want := Defaults()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load result differs from code defaults: got preference %q and database %q, want %q and %q",
			got.Routing.DefaultPreference, got.Database.Path, want.Routing.DefaultPreference, want.Database.Path)
	}
}

func TestLoadWithSourcesRetainsCompatibilityButReportsDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "legacy-config.json")
	if err := os.WriteFile(configPath, []byte(`not-json`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUTO_ROUTER_ROUTING_DEFAULT_PREFERENCE", "quality")

	got, sources, err := LoadWithSources(configPath)
	if err != nil {
		t.Fatalf("LoadWithSources returned error: %v", err)
	}
	if !reflect.DeepEqual(got, Defaults()) {
		t.Fatal("LoadWithSources did not return code defaults")
	}
	if sources.Source("routing.default_preference") != SourceDefault || sources.EnvVariable("routing.default_preference") != "" || sources.File() != "" {
		t.Fatalf("unexpected legacy provenance: source=%q env=%q file=%q", sources.Source("routing.default_preference"), sources.EnvVariable("routing.default_preference"), sources.File())
	}
}
