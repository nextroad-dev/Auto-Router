package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/modelsdev"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

func TestSelectingProviderModelUsesResolvedModelsDevMetadata(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Options{Path: ":memory:", BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateProvider(ctx, db, storage.NewProvider{Key: "custom", DisplayName: "Custom"}); err != nil {
		t.Fatal(err)
	}
	output := 8192
	handler := NewAdmin(AdminOptions{
		DB: db,
		ResolveModelMetadata: func(_ context.Context, provider, model string) (modelsdev.MetadataMatch, bool, error) {
			if provider != "custom" || model != "tenant/deployment-gpt-6-sol" {
				t.Fatalf("metadata lookup arguments = %q / %q", provider, model)
			}
			return modelsdev.MetadataMatch{
				ProviderID: "openai", ModelID: "gpt-6-sol", DisplayName: "GPT 6 Sol", MatchBy: "keyword",
				Pair: models.Pair{ContextWindow: 128000, MaxOutput: &output, SupportsTools: true, SupportsVision: true},
			}, true, nil
		},
	})
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/providers/custom/models", strings.NewReader(`{"model":"tenant/deployment-gpt-6-sol"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		MetadataSource  string `json:"metadata_source"`
		MetadataApplied bool   `json:"metadata_applied"`
		MetadataMatch   string `json:"metadata_match"`
		MetadataModel   string `json:"metadata_model"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.MetadataSource != "models.dev" || !payload.MetadataApplied || payload.MetadataMatch != "keyword" || payload.MetadataModel != "openai/gpt-6-sol" {
		t.Fatalf("selection metadata response = %+v", payload)
	}
	view, found, err := storage.GetPair(ctx, db, "custom", "tenant/deployment-gpt-6-sol")
	if err != nil || !found {
		t.Fatalf("GetPair found=%t error=%v", found, err)
	}
	if view.Pair.ContextWindow != 128000 || view.Pair.MaxOutput == nil || *view.Pair.MaxOutput != output || !view.Pair.SupportsTools || !view.Pair.SupportsVision || view.Pair.Source != models.SourceModelsDev {
		t.Fatalf("stored metadata = %+v", view.Pair)
	}
}
