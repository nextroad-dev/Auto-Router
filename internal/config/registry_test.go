package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func intPointer(value int) *int { return &value }

func TestRegistryDefaultsAreNormalized(t *testing.T) {
	for name, contents := range map[string]string{
		"file without registry":      `{}`,
		"empty registry object":      `{"registry":{}}`,
		"empty sync object":          `{"registry":{"sync":{}}}`,
		"explicit empty list values": `{"registry":{"sync":{"include":[]},"providers":[],"models":[]}}`,
		"sync url and timeout only":  `{"registry":{"sync":{"url":"https://models.dev/api.json","timeout":"30s"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := load(configFile(t, contents), noEnv)
			if err != nil {
				t.Fatal(err)
			}
			if !equalRegistry(cfg.Registry, Defaults().Registry) {
				t.Fatalf("registry section differs from defaults: %+v", cfg.Registry)
			}
		})
	}
	if got := Defaults().Registry.Sync.URL; got != "https://models.dev/api.json" {
		t.Fatalf("default sync URL = %q", got)
	}
	if got := time.Duration(Defaults().Registry.Sync.Timeout); got != 30*time.Second {
		t.Fatalf("default sync timeout = %v", got)
	}
}

func TestRegistryParsingAndDomainConversion(t *testing.T) {
	contents := `{
		"registry": {
			"sync": {"url": "https://mirror.example/api.json", "timeout": "5s", "include": ["openai/gpt-4o", "openrouter/meta-llama/llama-3.3-70b"]},
			"providers": [
				{"key": "openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-plain", "enabled": true, "priority": 5},
				{"key": "groq", "display_name": "Groq", "base_url": "https://api.groq.com/openai/v1", "enabled": false, "priority": 9}
			],
			"models": [
				{"provider": "openai", "model": "gpt-4o", "upstream_model_id": "gpt-4o-2024-11-20", "context_window": 128000, "max_output": 16384, "supports_tools": true, "supports_vision": true, "enabled": true, "priority": 3}
			]
		}
	}`
	cfg, err := load(configFile(t, contents), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Registry.Sync.URL != "https://mirror.example/api.json" || time.Duration(cfg.Registry.Sync.Timeout) != 5*time.Second {
		t.Fatalf("sync settings not parsed: %+v", cfg.Registry.Sync)
	}
	if len(cfg.Registry.Sync.Include) != 2 {
		t.Fatalf("include list not parsed: %+v", cfg.Registry.Sync.Include)
	}
	providers := cfg.Registry.LocalProviders()
	if len(providers) != 2 {
		t.Fatalf("providers = %+v", providers)
	}
	if providers[0].Key != "openai" || providers[0].DisplayName != "openai" || providers[0].APIKey != "sk-plain" || !providers[0].Enabled || providers[0].Priority != 5 {
		t.Fatalf("provider conversion incorrect: %+v", providers[0])
	}
	if providers[1].DisplayName != "Groq" {
		t.Fatalf("explicit display name lost: %+v", providers[1])
	}
	catalogModels, pairs := cfg.Registry.LocalCatalog()
	if len(catalogModels) != 1 || len(pairs) != 1 {
		t.Fatalf("models=%+v pairs=%+v", catalogModels, pairs)
	}
	model, pair := catalogModels[0], pairs[0]
	if model.ID != "gpt-4o" || model.DisplayName != "gpt-4o" || !model.Enabled || model.Priority != 3 {
		t.Fatalf("model conversion incorrect: %+v", model)
	}
	if pair.UpstreamModelID != "gpt-4o-2024-11-20" || pair.ContextWindow != 128000 || pair.MaxOutput == nil || *pair.MaxOutput != 16384 {
		t.Fatalf("pair conversion incorrect: %+v", pair)
	}
	if !pair.SupportsTools || !pair.SupportsVision || pair.SupportsReasoning {
		t.Fatalf("capabilities converted incorrectly: %+v", pair)
	}
}

func TestRegistryModelDefaults(t *testing.T) {
	contents := `{"registry": {
		"providers": [{"key": "local", "base_url": "http://127.0.0.1:11434/v1", "enabled": true, "priority": 0}],
		"models": [{"provider": "local", "model": "llama3.3", "context_window": 8192, "enabled": true}]
	}}`
	cfg, err := load(configFile(t, contents), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	model, pair := cfg.Registry.LocalCatalog()
	if model[0].DisplayName != "llama3.3" {
		t.Fatalf("display name should default to the model ID: %+v", model[0])
	}
	if pair[0].UpstreamModelID != "llama3.3" {
		t.Fatalf("upstream model ID should default to the model ID: %+v", pair[0])
	}
}

func TestRegistryValidationRejectsInvalidValues(t *testing.T) {
	validProvider := `{"key":"openai","base_url":"https://api.openai.com/v1","enabled":true,"priority":1}`
	cases := map[string]string{
		"uppercase provider key":        `{"registry":{"providers":[{"key":"OpenAI","base_url":"https://x.example/v1","enabled":true}]}}`,
		"missing base url when enabled": `{"registry":{"providers":[{"key":"openai","enabled":true}]}}`,
		"base url with query":           `{"registry":{"providers":[{"key":"openai","base_url":"https://x.example/v1?key=secret","enabled":true}]}}`,
		"base url with userinfo":        `{"registry":{"providers":[{"key":"openai","base_url":"https://user:pass@x.example/v1","enabled":true}]}}`,
		"negative provider priority":    `{"registry":{"providers":[{"key":"openai","base_url":"https://x.example/v1","enabled":false,"priority":-1}]}}`,
		"duplicate provider key":        `{"registry":{"providers":[` + validProvider + `,` + validProvider + `]}}`,
		"sync url over plain http":      `{"registry":{"sync":{"url":"http://models.dev/api.json"}}}`,
		"sync url with query":           `{"registry":{"sync":{"url":"https://models.dev/api.json?token=secret"}}}`,
		"sync timeout zero":             `{"registry":{"sync":{"timeout":"0s"}}}`,
		"sync timeout negative":         `{"registry":{"sync":{"timeout":"-1s"}}}`,
		"empty include entry":           `{"registry":{"sync":{"include":[""]}}}`,
		"include without slash":         `{"registry":{"sync":{"include":["openai"]}}}`,
		"include bad provider":          `{"registry":{"sync":{"include":["OpenAI/gpt-4o"]}}}`,
		"include missing model":         `{"registry":{"sync":{"include":["openai/"]}}}`,
		"include padded":                `{"registry":{"sync":{"include":["openai/gpt-4o "]}}}`,
		"model for undeclared provider": `{"registry":{"providers":[` + validProvider + `],"models":[{"provider":"groq","model":"x","context_window":1,"enabled":true}]}}`,
		"model with zero context":       `{"registry":{"providers":[` + validProvider + `],"models":[{"provider":"openai","model":"x","context_window":0,"enabled":true}]}}`,
		"model output over context":     `{"registry":{"providers":[` + validProvider + `],"models":[{"provider":"openai","model":"x","context_window":10,"max_output":11,"enabled":true}]}}`,
		"model with zero output":        `{"registry":{"providers":[` + validProvider + `],"models":[{"provider":"openai","model":"x","context_window":10,"max_output":0,"enabled":true}]}}`,
		"model bad id":                  `{"registry":{"providers":[` + validProvider + `],"models":[{"provider":"openai","model":"bad model","context_window":10,"enabled":true}]}}`,
		"model negative priority":       `{"registry":{"providers":[` + validProvider + `],"models":[{"provider":"openai","model":"x","context_window":10,"enabled":true,"priority":-1}]}}`,
		"unknown registry field":        `{"registry":{"sync":{"interval":"1m"}}}`,
	}
	for name, contents := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := load(configFile(t, contents), noEnv); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func TestExpandEnv(t *testing.T) {
	lookup := fromEnv(map[string]string{"OPENAI_API_KEY": "sk-value", "EMPTY": ""})
	cases := map[string]string{
		"${OPENAI_API_KEY}":                  "sk-value",
		"prefix-${OPENAI_API_KEY}":           "prefix-sk-value",
		"${EMPTY}":                           "",
		"$${OPENAI_API_KEY}":                 "${OPENAI_API_KEY}",
		"$OPENAI_API_KEY":                    "$OPENAI_API_KEY",
		"literal":                            "literal",
		"$":                                  "$",
		"a$$b":                               "a$b",
		"${OPENAI_API_KEY}${OPENAI_API_KEY}": "sk-valuesk-value",
	}
	for raw, want := range cases {
		got, err := expandEnv(raw, lookup)
		if err != nil {
			t.Fatalf("expandEnv(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("expandEnv(%q) = %q, want %q", raw, got, want)
		}
	}
	for _, raw := range []string{"${MISSING}", "prefix-${MISSING}", "${", "${}", "tail-${"} {
		if _, err := expandEnv(raw, lookup); err == nil {
			t.Fatalf("expandEnv(%q) should fail", raw)
		}
	}
	if _, err := expandEnv("${MISSING}", lookup); err == nil || !strings.Contains(err.Error(), "MISSING") {
		t.Fatalf("error should name the missing variable, got %v", err)
	}
}

func TestRegistryAPIKeyExpansion(t *testing.T) {
	contents := `{"registry":{
		"providers":[{"key":"openai","base_url":"https://api.openai.com/v1","api_key":"${OPENAI_API_KEY}","enabled":true}],
		"models":[]
	}}`
	cfg, err := load(configFile(t, contents), fromEnv(map[string]string{"OPENAI_API_KEY": "sk-from-env"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Registry.Providers[0].APIKey != "sk-from-env" {
		t.Fatalf("api key was not expanded: %q", cfg.Registry.Providers[0].APIKey)
	}
	if _, err := load(configFile(t, contents), noEnv); err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("an unset variable must fail and name itself, got %v", err)
	}
	// The variable value must not appear in the error text.
	_, err = load(configFile(t, contents), fromEnv(map[string]string{"OTHER": "sk-secret"}))
	if err == nil || strings.Contains(err.Error(), "sk-secret") {
		t.Fatalf("error must not leak credential values: %v", err)
	}
}

func TestRegistryWarnings(t *testing.T) {
	contents := `{"registry":{"providers":[
		{"key":"openai","base_url":"https://api.openai.com/v1","enabled":true},
		{"key":"local","base_url":"http://127.0.0.1:11434/v1","enabled":true,"api_key":"tok"},
		{"key":"disabled","base_url":"https://x.example/v1","enabled":false}
	]}}`
	cfg, err := load(configFile(t, contents), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	warnings := cfg.Warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], `"openai"`) {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if strings.Contains(strings.Join(warnings, "\n"), "tok") {
		t.Fatal("warnings must not include credentials")
	}
}

func TestRegistryLocalCatalogFoldsDuplicateModels(t *testing.T) {
	contents := `{"registry":{
		"providers":[{"key":"openai","base_url":"https://api.openai.com/v1","enabled":true}],
		"models":[
			{"provider":"openai","model":"gpt-4o","display_name":"first","context_window":1000,"enabled":true,"priority":1},
			{"provider":"openai","model":"gpt-4o","display_name":"second","context_window":2000,"enabled":true,"priority":2}
		]
	}}`
	cfg, err := load(configFile(t, contents), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	catalogModels, pairs := cfg.Registry.LocalCatalog()
	if len(catalogModels) != 1 || catalogModels[0].DisplayName != "second" {
		t.Fatalf("duplicate logical models should fold last-one-wins: %+v", catalogModels)
	}
	if len(pairs) != 2 {
		t.Fatalf("each configured entry is a pair: %+v", pairs)
	}
}

func TestRegistryExampleFileLoads(t *testing.T) {
	path := filepath.Join("..", "..", "configs", "registry.example.json")
	lookup := fromEnv(map[string]string{"OPENAI_API_KEY": "sk-example", "GROQ_API_KEY": "gsk-example"})
	cfg, err := load(path, lookup)
	if err != nil {
		t.Fatalf("registry example must be loadable: %v", err)
	}
	if len(cfg.Registry.Sync.Include) == 0 || len(cfg.Registry.Providers) == 0 || len(cfg.Registry.Models) == 0 {
		t.Fatalf("registry example should exercise every section: %+v", cfg.Registry)
	}
	if cfg.Registry.Providers[0].APIKey != "sk-example" {
		t.Fatalf("example API key was not expanded: %q", cfg.Registry.Providers[0].APIKey)
	}
	if cfg.HTTP.Address != Defaults().HTTP.Address || cfg.Database.Path != Defaults().Database.Path {
		t.Fatal("a registry-only file must keep the other defaults")
	}
	if _, err := load(path, noEnv); err == nil {
		t.Fatal("the example must fail loudly when its environment variables are missing")
	}
}

func TestRegistryIsNotOverridableByEnvironment(t *testing.T) {
	contents := `{"registry":{"providers":[{"key":"openai","base_url":"https://api.openai.com/v1","enabled":false}]}}`
	cfg, err := load(configFile(t, contents), fromEnv(map[string]string{
		"AUTO_ROUTER_REGISTRY_PROVIDERS": "[]",
		"AUTO_ROUTER_REGISTRY_SYNC_URL":  "https://evil.example/api.json",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Registry.Providers) != 1 || cfg.Registry.Sync.URL != "https://models.dev/api.json" {
		t.Fatalf("registry must be file-only: %+v", cfg.Registry)
	}
}

func equalRegistry(a, b RegistryConfig) bool {
	if a.Sync.URL != b.Sync.URL || a.Sync.Timeout != b.Sync.Timeout {
		return false
	}
	if len(a.Sync.Include) != len(b.Sync.Include) || len(a.Providers) != len(b.Providers) || len(a.Models) != len(b.Models) {
		return false
	}
	for i := range a.Sync.Include {
		if a.Sync.Include[i] != b.Sync.Include[i] {
			return false
		}
	}
	for i := range a.Providers {
		if a.Providers[i] != b.Providers[i] {
			return false
		}
	}
	for i := range a.Models {
		if !equalRegistryModel(a.Models[i], b.Models[i]) {
			return false
		}
	}
	return true
}

func equalRegistryModel(a, b RegistryModel) bool {
	if a.Provider != b.Provider || a.Model != b.Model || a.UpstreamModelID != b.UpstreamModelID || a.DisplayName != b.DisplayName {
		return false
	}
	if a.ContextWindow != b.ContextWindow || a.Enabled != b.Enabled || a.Priority != b.Priority {
		return false
	}
	if a.SupportsTools != b.SupportsTools || a.SupportsVision != b.SupportsVision || a.SupportsReasoning != b.SupportsReasoning {
		return false
	}
	if (a.MaxOutput == nil) != (b.MaxOutput == nil) {
		return false
	}
	return a.MaxOutput == nil || *a.MaxOutput == *b.MaxOutput
}
