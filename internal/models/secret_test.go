package models

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestMaskSecret(t *testing.T) {
	cases := map[string]string{
		"":                             "",
		"sk-1234":                      "***",
		"0123456789abcde":              "***", // 15 characters: too short to reveal anything
		"0123456789abcdef":             "012…cdef",
		"sk-proj-abcdefghijklmnopqrst": "sk-…qrst",
	}
	for secret, want := range cases {
		if got := MaskSecret(secret); got != want {
			t.Fatalf("MaskSecret(%q) = %q, want %q", secret, got, want)
		}
	}
	secret := "sk-proj-abcdefghijklmnopqrstuvwxyz"
	masked := MaskSecret(secret)
	if strings.Contains(masked, secret) {
		t.Fatalf("mask %q leaks the secret", masked)
	}
	if len([]rune(masked)) >= len([]rune(secret)) {
		t.Fatalf("mask %q is not shorter than the secret", masked)
	}
}

func TestProviderLogValueMasksAPIKey(t *testing.T) {
	secret := "sk-proj-abcdefghijklmnopqrstuvwxyz"
	providerRecord := Provider{
		Key:         "openai",
		DisplayName: "OpenAI",
		BaseURL:     "https://api.openai.com/v1",
		APIKey:      secret,
		Enabled:     true,
		Priority:    10,
		Source:      SourceLocal,
	}
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, nil))
	logger.Info("provider configured", "provider", providerRecord)
	rendered := buffer.String()
	if strings.Contains(rendered, secret) {
		t.Fatalf("log output leaked the API key: %s", rendered)
	}
	if !strings.Contains(rendered, MaskSecret(secret)) {
		t.Fatalf("log output should carry the masked key: %s", rendered)
	}
	if !strings.Contains(rendered, "https://api.openai.com/v1") {
		t.Fatalf("log output should keep the non-secret base URL: %s", rendered)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	group, ok := decoded["provider"].(map[string]any)
	if !ok || group["api_key"] != MaskSecret(secret) {
		t.Fatalf("unexpected structured log value: %v", decoded["provider"])
	}

	empty := Provider{Key: "local", DisplayName: "Local", Source: SourceLocal}
	buffer.Reset()
	logger.Info("provider", "provider", empty)
	if !strings.Contains(buffer.String(), `"api_key":""`) {
		t.Fatalf("an empty key should render as an empty masked value: %s", buffer.String())
	}
}

func TestProviderLogValueNeverContainsKeyFormats(t *testing.T) {
	secrets := []string{
		"short",
		"exactly-sixteen!",
		"a-very-long-provider-key-0123456789",
	}
	for _, secret := range secrets {
		rendered := MaskSecret(secret)
		if strings.Contains(rendered, secret) {
			t.Fatalf("mask %q leaks %q", rendered, secret)
		}
		if secret != "" && rendered == secret {
			t.Fatalf("mask equals the secret for %q", secret)
		}
	}
}
