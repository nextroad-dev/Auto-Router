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
