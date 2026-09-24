package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nextroad-dev/Auto-Router/internal/models"
)

type listedModel struct {
	ID                string `json:"id"`
	Object            string `json:"object"`
	OwnedBy           string `json:"owned_by"`
	ContextWindow     *int   `json:"context_window"`
	SupportsTools     *bool  `json:"supports_tools"`
	SupportsVision    *bool  `json:"supports_vision"`
	SupportsReasoning *bool  `json:"supports_reasoning"`
}

func TestModelsAlwaysIncludesAutoWithDeclaredCapabilities(t *testing.T) {
	handler := NewHandler(Options{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET /v1/models status = %d, want %d", response.Code, http.StatusOK)
	}

	var listing struct {
		Object string        `json:"object"`
		Data   []listedModel `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decode model list: %v", err)
	}
	if listing.Object != "list" || len(listing.Data) != 1 {
		t.Fatalf("model list = %+v, want a list containing only auto", listing)
	}
	auto := listing.Data[0]
	if auto.ID != models.AutoModelID || auto.Object != "model" || auto.OwnedBy != "auto-router" {
		t.Fatalf("auto model identity = %+v", auto)
	}
	if auto.ContextWindow == nil || *auto.ContextWindow != 1_000_000 {
		t.Fatalf("auto context_window = %v, want 1000000", auto.ContextWindow)
	}
	for name, capability := range map[string]*bool{
		"supports_tools":     auto.SupportsTools,
		"supports_vision":    auto.SupportsVision,
		"supports_reasoning": auto.SupportsReasoning,
	} {
		if capability == nil || !*capability {
			t.Errorf("auto %s = %v, want true", name, capability)
		}
	}

	headResponse := httptest.NewRecorder()
	handler.ServeHTTP(headResponse, httptest.NewRequest(http.MethodHead, "/v1/models", nil))
	if headResponse.Code != http.StatusOK || headResponse.Body.Len() != 0 {
		t.Fatalf("HEAD /v1/models status/body = %d/%q, want 200/empty", headResponse.Code, headResponse.Body.String())
	}
}

func TestModelsListsAutoBeforeRoutableLogicalModels(t *testing.T) {
	catalog, err := models.NewCatalog(0,
		[]models.Provider{{
			Key: "example", Kind: models.ProviderOpenAICompatible, DisplayName: "Example",
			BaseURL: "https://example.com/v1", Enabled: true, Source: models.SourceLocal,
		}},
		[]models.Model{{ID: "example-model", DisplayName: "Example Model", Enabled: true, Source: models.SourceLocal}},
		[]models.Pair{{
			ProviderKey: "example", ModelID: "example-model", UpstreamModelID: "upstream-model",
			ContextWindow: 128_000, Enabled: true, Source: models.SourceLocal,
		}},
	)
	if err != nil {
		t.Fatalf("create catalog: %v", err)
	}
	store := models.NewStore()
	store.Swap(catalog)
	handler := NewHandler(Options{Catalog: store})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	var listing struct {
		Data []listedModel `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decode model list: %v", err)
	}
	if response.Code != http.StatusOK || len(listing.Data) != 2 {
		t.Fatalf("status/data length = %d/%d, want 200/2; body=%s", response.Code, len(listing.Data), response.Body.String())
	}
	if listing.Data[0].ID != models.AutoModelID || listing.Data[1].ID != "example-model" || listing.Data[1].OwnedBy != "example" {
		t.Fatalf("model ordering/ownership = %+v", listing.Data)
	}
	if listing.Data[1].ContextWindow != nil || listing.Data[1].SupportsTools != nil || listing.Data[1].SupportsVision != nil || listing.Data[1].SupportsReasoning != nil {
		t.Fatalf("logical model unexpectedly has auto capability fields: %+v", listing.Data[1])
	}
}
