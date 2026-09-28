package auto

import (
	"reflect"
	"testing"

	"github.com/nextroad-dev/Auto-Router/internal/models"
)

func TestCandidatesOrderByProviderPriorityThenProviderAndModelID(t *testing.T) {
	providers := []models.Provider{
		{Key: "provider-z", DisplayName: "Provider Z", BaseURL: "https://z.example/v1", Enabled: true, Priority: 1, Source: models.SourceLocal},
		{Key: "provider-b", DisplayName: "Provider B", BaseURL: "https://b.example/v1", Enabled: true, Priority: 0, Source: models.SourceLocal},
		{Key: "provider-a", DisplayName: "Provider A", BaseURL: "https://a.example/v1", Enabled: true, Priority: 0, Source: models.SourceLocal},
	}
	catalogModels := []models.Model{
		{ID: "model-z", DisplayName: "Model Z", Enabled: true, Source: models.SourceLocal},
		{ID: "model-a", DisplayName: "Model A", Enabled: true, Source: models.SourceLocal},
		{ID: "model-x", DisplayName: "Model X", Enabled: true, Source: models.SourceLocal},
	}
	pairs := []models.Pair{
		{ProviderKey: "provider-z", ModelID: "model-z", UpstreamModelID: "model-z", ContextWindow: 100, Enabled: true, Source: models.SourceLocal},
		{ProviderKey: "provider-b", ModelID: "model-x", UpstreamModelID: "model-x", ContextWindow: 100, Enabled: true, Source: models.SourceLocal},
		{ProviderKey: "provider-a", ModelID: "model-z", UpstreamModelID: "model-z", ContextWindow: 100, Enabled: true, Source: models.SourceLocal},
		{ProviderKey: "provider-z", ModelID: "model-a", UpstreamModelID: "model-a", ContextWindow: 100, Enabled: true, Source: models.SourceLocal},
		{ProviderKey: "provider-a", ModelID: "model-a", UpstreamModelID: "model-a", ContextWindow: 100, Enabled: true, Source: models.SourceLocal},
	}
	catalog, err := models.NewCatalog(1, providers, catalogModels, pairs)
	if err != nil {
		t.Fatal(err)
	}

	candidates := Candidates(catalog)
	got := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		got = append(got, candidate.ProviderKey+"/"+candidate.ModelID)
	}
	want := []string{
		"provider-a/model-a", "provider-a/model-z", "provider-b/model-x",
		"provider-z/model-a", "provider-z/model-z",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidate order = %v, want %v", got, want)
	}
}
