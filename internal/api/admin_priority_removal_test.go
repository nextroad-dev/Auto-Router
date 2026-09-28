package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

func TestModelAndBindingPriorityAreAbsentFromAdminAPI(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Options{Path: ":memory:", BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateProvider(ctx, db, storage.NewProvider{
		Key: "provider-a", DisplayName: "Provider A", Priority: 7,
	}); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateModel(ctx, db, storage.NewModel{
		ID: "model-a", DisplayName: "Model A", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreatePair(ctx, db, storage.NewPair{
		ProviderKey: "provider-a", ModelID: "model-a", UpstreamModelID: "upstream-a",
		ContextWindow: 8192, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	handler := NewAdmin(AdminOptions{DB: db})

	for _, path := range []string{"/admin/v1/models", "/admin/v1/pairs"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, body = %s", path, response.Code, response.Body.String())
		}
		var page struct {
			Items []map[string]json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode GET %s response: %v", path, err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("GET %s returned %d items, want 1", path, len(page.Items))
		}
		if _, exists := page.Items[0]["priority"]; exists {
			t.Errorf("GET %s still exposes model/binding priority: %s", path, response.Body.String())
		}
	}

	providerResponse := httptest.NewRecorder()
	handler.ServeHTTP(providerResponse, httptest.NewRequest(http.MethodGet, "/admin/v1/providers", nil))
	var providerPage struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(providerResponse.Body.Bytes(), &providerPage); err != nil || len(providerPage.Items) != 1 {
		t.Fatalf("decode provider list = %s, error = %v", providerResponse.Body.String(), err)
	}
	if _, exists := providerPage.Items[0]["priority"]; !exists {
		t.Fatalf("provider priority disappeared from its separate API: %s", providerResponse.Body.String())
	}

	for _, test := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPatch, "/admin/v1/models/model-a", `{"priority":0}`},
		{http.MethodPost, "/admin/v1/models", `{"id":"model-b","display_name":"Model B","enabled":true,"priority":0}`},
		{http.MethodPatch, "/admin/v1/pairs/provider-a/model-a", `{"priority":0}`},
		{http.MethodPost, "/admin/v1/pairs", `{"provider":"provider-a","model":"model-a","upstream_model_id":"upstream-a","context_window":8192,"enabled":true,"priority":0}`},
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(test.method, test.path, bytes.NewBufferString(test.body))
		request.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s %s with removed priority field status = %d, body = %s; want 400",
				test.method, test.path, response.Code, response.Body.String())
		}
	}
}
