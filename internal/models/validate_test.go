package models

import (
	"strings"
	"testing"
)

func TestValidateProviderKey(t *testing.T) {
	valid := []string{"a", "openai", "my-provider", "a.b_c-d", strings.Repeat("a", 64), "0abc"}
	for _, key := range valid {
		if err := ValidateProviderKey(key); err != nil {
			t.Errorf("key %q should be valid: %v", key, err)
		}
	}
	invalid := []string{"", "OpenAI", "-leading", ".dot", "_underscore", "with space", "with/slash", strings.Repeat("a", 65), "é", "a\x00"}
	for _, key := range invalid {
		if err := ValidateProviderKey(key); err == nil {
			t.Errorf("key %q should be rejected", key)
		}
	}
}

func TestValidateModelID(t *testing.T) {
	valid := []string{
		"gpt-4o",
		"meta-llama/Llama-3.3-70B",
		"claude-sonnet-4@20250514",
		"model~latest",
		"a.b:c+d_e-f",
		strings.Repeat("a", 128),
	}
	for _, id := range valid {
		if err := ValidateModelID(id); err != nil {
			t.Errorf("model id %q should be valid: %v", id, err)
		}
	}
	invalid := []string{"", "-leading", "/slash", "@at", "with space", strings.Repeat("a", 129), "quote\"", "brace}"}
	for _, id := range invalid {
		if err := ValidateModelID(id); err == nil {
			t.Errorf("model id %q should be rejected", id)
		}
	}
}

func TestValidateBaseURL(t *testing.T) {
	valid := []string{
		"http://127.0.0.1:11434/v1",
		"https://api.openai.com/v1",
		"https://gateway.internal/v1/openai/",
		"https://[::1]:8443/v1",
	}
	for _, raw := range valid {
		if err := ValidateBaseURL(raw); err != nil {
			t.Errorf("base url %q should be valid: %v", raw, err)
		}
	}
	invalid := map[string]string{
		"empty":            "",
		"relative":         "/v1",
		"no host":          "https:///v1",
		"scheme":           "ftp://example.com/v1",
		"userinfo":         "https://user:secret@example.com/v1",
		"query":            "https://example.com/v1?api_key=secret",
		"empty query":      "https://example.com/v1?",
		"fragment":         "https://example.com/v1#frag",
		"opaque":           "https:example.com",
		"control characte": "https://example.com/v1\x00",
	}
	for name, raw := range invalid {
		t.Run(name, func(t *testing.T) {
			if err := ValidateBaseURL(raw); err == nil {
				t.Fatalf("base url %q should be rejected", raw)
			}
		})
	}
}

func TestValidateSourceURL(t *testing.T) {
	valid := []string{
		"https://models.dev/api.json",
		"http://127.0.0.1:4545/api.json",
		"http://localhost:4545/api.json",
		"http://[::1]:4545/api.json",
		"https://models.dev/api.json",
	}
	for _, raw := range valid {
		if err := ValidateSourceURL(raw); err != nil {
			t.Errorf("source url %q should be valid: %v", raw, err)
		}
	}
	invalid := map[string]string{
		"remote http":    "http://models.dev/api.json",
		"remote http ip": "http://10.0.0.5/api.json",
		"relative":       "/api.json",
		"scheme":         "file:///etc/passwd",
		"userinfo":       "https://user:secret@models.dev/api.json",
		"query":          "https://models.dev/api.json?token=secret",
		"fragment":       "https://models.dev/api.json#x",
		"empty":          "",
	}
	for name, raw := range invalid {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSourceURL(raw); err == nil {
				t.Fatalf("source url %q should be rejected", raw)
			}
		})
	}
}

func TestSplitIncludeEntry(t *testing.T) {
	cases := []struct {
		entry        string
		wantProvider string
		wantModel    string
		wantError    bool
	}{
		{entry: "openai/gpt-4o", wantProvider: "openai", wantModel: "gpt-4o"},
		{entry: "openrouter/meta-llama/llama-3.3-70b", wantProvider: "openrouter", wantModel: "meta-llama/llama-3.3-70b"},
		{entry: "groq/groq/compound-mini", wantProvider: "groq", wantModel: "groq/compound-mini"},
		{entry: "google-vertex/claude-sonnet-4@20250514", wantProvider: "google-vertex", wantModel: "claude-sonnet-4@20250514"},
		{entry: "openai/gpt-4o/", wantProvider: "openai", wantModel: "gpt-4o/"},
		{entry: "OpenAI/gpt-4o", wantError: true},
		{entry: "openai/", wantError: true},
		{entry: "/gpt-4o", wantError: true},
		{entry: "openai", wantError: true},
		{entry: "", wantError: true},
		{entry: " openai/gpt-4o", wantError: true},
		{entry: "openai/gpt-4o ", wantError: true},
		{entry: "openai/gpt 4o", wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.entry, func(t *testing.T) {
			providerKey, modelID, err := SplitIncludeEntry(tc.entry)
			if tc.wantError {
				if err == nil {
					t.Fatalf("entry %q should be rejected, got %q/%q", tc.entry, providerKey, modelID)
				}
				return
			}
			if err != nil {
				t.Fatalf("entry %q should be valid: %v", tc.entry, err)
			}
			if providerKey != tc.wantProvider || modelID != tc.wantModel {
				t.Fatalf("got %q/%q, want %q/%q", providerKey, modelID, tc.wantProvider, tc.wantModel)
			}
		})
	}
}

func TestValidateProvider(t *testing.T) {
	base := Provider{Key: "openai", DisplayName: "OpenAI", BaseURL: "https://api.openai.com/v1", Enabled: true, Source: SourceLocal}
	if err := ValidateProvider(base); err != nil {
		t.Fatal(err)
	}
	disabled := base
	disabled.Enabled = false
	disabled.BaseURL = ""
	if err := ValidateProvider(disabled); err != nil {
		t.Fatalf("a disabled provider without a base URL is valid: %v", err)
	}
	cases := map[string]Provider{
		"bad key":                        {Key: "OpenAI", DisplayName: "OpenAI", Source: SourceLocal},
		"empty display name":             {Key: "openai", DisplayName: " ", Source: SourceLocal},
		"bad source":                     {Key: "openai", DisplayName: "OpenAI", Source: "other"},
		"enabled without url":            {Key: "openai", DisplayName: "OpenAI", Enabled: true, Source: SourceLocal},
		"invalid url even when disabled": {Key: "openai", DisplayName: "OpenAI", BaseURL: "not-a-url", Source: SourceLocal},
		"negative priority":              {Key: "openai", DisplayName: "OpenAI", Priority: -1, Source: SourceLocal},
	}
	for name, provider := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateProvider(provider); err == nil {
				t.Fatal("expected provider validation error")
			}
		})
	}
}

func TestValidateModelAndPair(t *testing.T) {
	if err := ValidateModel(Model{ID: "gpt-4o", DisplayName: "GPT-4o", Source: SourceModelsDev}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []Model{
		{ID: "", DisplayName: "x", Source: SourceModelsDev},
		{ID: "gpt-4o", DisplayName: "", Source: SourceModelsDev},
		{ID: "gpt-4o", DisplayName: "x", Source: "bogus"},
		{ID: "gpt-4o", DisplayName: "x", Source: SourceModelsDev, Priority: -2},
	} {
		if err := ValidateModel(invalid); err == nil {
			t.Fatalf("model %+v should be rejected", invalid)
		}
	}

	maxOutput := 100
	valid := Pair{ProviderKey: "openai", ModelID: "gpt-4o", UpstreamModelID: "gpt-4o-2024-11-20", ContextWindow: 128000, MaxOutput: &maxOutput, Enabled: true, Source: SourceLocal}
	if err := ValidatePair(valid); err != nil {
		t.Fatal(err)
	}
	overCapacity := 128001
	cases := map[string]Pair{
		"empty upstream":      {ProviderKey: "openai", ModelID: "gpt-4o", UpstreamModelID: "", ContextWindow: 1, Source: SourceLocal},
		"padded upstream":     {ProviderKey: "openai", ModelID: "gpt-4o", UpstreamModelID: " gpt-4o", ContextWindow: 1, Source: SourceLocal},
		"zero context":        {ProviderKey: "openai", ModelID: "gpt-4o", UpstreamModelID: "gpt-4o", ContextWindow: 0, Source: SourceLocal},
		"zero output":         {ProviderKey: "openai", ModelID: "gpt-4o", UpstreamModelID: "gpt-4o", ContextWindow: 100, MaxOutput: new(int), Source: SourceLocal},
		"output over context": {ProviderKey: "openai", ModelID: "gpt-4o", UpstreamModelID: "gpt-4o", ContextWindow: 128000, MaxOutput: &overCapacity, Source: SourceLocal},
		"bad provider":        {ProviderKey: "OpenAI", ModelID: "gpt-4o", UpstreamModelID: "gpt-4o", ContextWindow: 10, Source: SourceLocal},
		"bad model":           {ProviderKey: "openai", ModelID: "gpt 4o", UpstreamModelID: "gpt-4o", ContextWindow: 10, Source: SourceLocal},
		"bad source":          {ProviderKey: "openai", ModelID: "gpt-4o", UpstreamModelID: "gpt-4o", ContextWindow: 10, Source: "other"},
		"negative priority":   {ProviderKey: "openai", ModelID: "gpt-4o", UpstreamModelID: "gpt-4o", ContextWindow: 10, Source: SourceLocal, Priority: -1},
	}
	for name, pair := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidatePair(pair); err == nil {
				t.Fatal("expected pair validation error")
			}
		})
	}
}
