package modelsdev

import (
	"testing"

	"github.com/nextroad-dev/Auto-Router/internal/models"
)

func TestFindMetadataMatchesCustomUpstreamPrefixByKeywords(t *testing.T) {
	contextWindow := 128000
	output := 16000
	document := Document{
		"openai": {
			ID: "openai", Name: "OpenAI", Models: map[string]Model{
				"gpt-6-sol": {ID: "gpt-6-sol", Name: "GPT 6 Sol", ToolCall: true, Reasoning: true,
					Modalities: &Modalities{Input: []string{"text", "image"}}, Limit: &Limit{Context: contextWindow, Output: &output}},
			},
		},
	}

	match, found := FindMetadata(document, "custom-openai", "tenant-prod/gpt-6-sol-v2")
	if !found {
		t.Fatal("expected custom-prefixed upstream identifier to match models.dev metadata")
	}
	if match.ProviderID != "openai" || match.ModelID != "gpt-6-sol" || match.MatchBy != "prefix" {
		t.Fatalf("match = %+v, want openai/gpt-6-sol by prefix", match)
	}
	if match.Pair.ContextWindow != contextWindow || match.Pair.MaxOutput == nil || *match.Pair.MaxOutput != output ||
		!match.Pair.SupportsTools || !match.Pair.SupportsVision || !match.Pair.SupportsReasoning {
		t.Fatalf("metadata = %+v, capabilities not copied", match.Pair)
	}
}

func TestFindMetadataRejectsAmbiguousCapabilityMatches(t *testing.T) {
	document := Document{
		"provider-a": {Name: "Provider A", Models: map[string]Model{
			"vendor-a/claude-sonnet": {ID: "claude-sonnet", Limit: &Limit{Context: 64000}, ToolCall: true},
		}},
		"provider-b": {Name: "Provider B", Models: map[string]Model{
			"vendor-b/claude-sonnet": {ID: "claude-sonnet", Limit: &Limit{Context: 32000}, ToolCall: false},
		}},
	}
	if _, found := FindMetadata(document, "", "custom/claude-sonnet"); found {
		t.Fatal("ambiguous capability candidates should not be guessed")
	}
	match, found := FindMetadata(document, "provider-a", "custom/claude-sonnet")
	if !found || match.ProviderID != "provider-a" || !match.Pair.SupportsTools {
		t.Fatalf("provider hint did not disambiguate: match=%+v found=%t", match, found)
	}
}

func TestLookupModelMapsCustomPrefixAndPreservesProviderID(t *testing.T) {
	contextWindow := 64000
	document := Document{"anthropic": {Models: map[string]Model{
		"claude-sonnet-4": {ID: "claude-sonnet-4", Name: "Claude Sonnet 4", ToolCall: true, Limit: &Limit{Context: contextWindow}},
	}}}
	result, err := Map(document, []string{"anthropic/team-a/claude-sonnet-4-deployment"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pairs) != 1 {
		t.Fatalf("pairs = %d, want 1", len(result.Pairs))
	}
	pair := result.Pairs[0]
	if pair.ModelID != "claude-sonnet-4" || pair.UpstreamModelID != "team-a/claude-sonnet-4-deployment" || pair.Source != models.SourceModelsDev {
		t.Fatalf("pair = %+v, expected canonical model with original custom upstream ID", pair)
	}
}

func TestMapAllMapsTheFullCatalogDeterministicallyAndSkipsUnusableModels(t *testing.T) {
	document := Document{
		"z-provider": {ID: "z-provider", Name: "Z Provider", Models: map[string]Model{
			"z-model": {ID: "z-model", Name: "Z Model", Limit: &Limit{Context: 64000}},
		}},
		"a-provider": {ID: "a-provider", Name: "A Provider", Models: map[string]Model{
			"a-model":    {ID: "a-model", Name: "A Model", Limit: &Limit{Context: 32000}},
			"no-context": {ID: "no-context", Name: "No Context", Limit: &Limit{Context: 0}},
		}},
	}

	result, scope, err := MapAll(document)
	if err != nil {
		t.Fatalf("MapAll failed: %v", err)
	}
	wantScope := []string{"a-provider/a-model", "a-provider/no-context", "z-provider/z-model"}
	if len(scope) != len(wantScope) {
		t.Fatalf("scope=%v, want %v", scope, wantScope)
	}
	for i := range wantScope {
		if scope[i] != wantScope[i] {
			t.Fatalf("scope=%v, want %v", scope, wantScope)
		}
	}
	if len(result.Pairs) != 2 || result.Skipped != 1 || len(result.Warnings) != 1 {
		t.Fatalf("mapped pairs=%d skipped=%d warnings=%v; want 2, 1 and one warning", len(result.Pairs), result.Skipped, result.Warnings)
	}
	if result.Pairs[0].ProviderKey != "a-provider" || result.Pairs[0].ModelID != "a-model" || result.Pairs[1].ProviderKey != "z-provider" {
		t.Fatalf("catalog mapping order is not deterministic: %+v", result.Pairs)
	}

	again, secondScope, err := MapAll(document)
	if err != nil {
		t.Fatal(err)
	}
	if IncludeDigest(scope) != IncludeDigest(secondScope) || len(again.Pairs) != len(result.Pairs) {
		t.Fatalf("repeated catalog mapping changed: scope=%v pairs=%d", secondScope, len(again.Pairs))
	}
}
