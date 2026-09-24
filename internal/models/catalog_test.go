package models

import "testing"

func TestGatewayKeyRemainsCompatibleWithLegacyMetadata(t *testing.T) {
	legacy := "openai"
	if got := (Provider{Key: "openai-compatible", GatewayProvider: &legacy}).GatewayKey(); got != legacy {
		t.Fatalf("GatewayKey() = %q, want legacy mapping %q", got, legacy)
	}
	if got := (Provider{Key: "native-provider"}).GatewayKey(); got != "native-provider" {
		t.Fatalf("GatewayKey() fallback = %q, want provider key", got)
	}
}
