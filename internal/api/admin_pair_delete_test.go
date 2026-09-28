package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

func TestDeleteAdminPairEndpointRemovesGroupsAndReloadsSnapshots(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Options{Path: ":memory:", BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateProvider(ctx, db, storage.NewProvider{Key: "provider", DisplayName: "Provider", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO models(id, display_name, enabled, source, updated_at, admin_owned)
		VALUES('tenant/model', 'Model', 1, 'local', 'now', 1)
	`); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreatePair(ctx, db, storage.NewPair{
		ProviderKey: "provider", ModelID: "tenant/model", UpstreamModelID: "upstream/model", ContextWindow: 32768, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := storage.ReplaceModelGroups(ctx, db, storage.ModelGroups{
		Simple: []storage.ModelGroupMember{{Provider: "provider", Model: "tenant/model"}},
	}); err != nil {
		t.Fatal(err)
	}

	catalogReloaded, groupsReloaded := false, false
	handler := NewAdmin(AdminOptions{
		DB:            db,
		ReloadCatalog: func(context.Context) error { catalogReloaded = true; return nil },
		ReloadGroups: func(_ context.Context, groups storage.ModelGroups) error {
			groupsReloaded = true
			if len(groups.Simple)+len(groups.Medium)+len(groups.Complex) != 0 {
				t.Errorf("reloaded groups unexpectedly contain entries: %#v", groups)
			}
			return nil
		},
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/admin/v1/pairs/provider/tenant%2Fmodel", nil)
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Deleted  bool   `json:"deleted"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Provider != "provider" || payload.Model != "tenant/model" || !payload.Deleted {
		t.Fatalf("response = %+v, expected deleted pair coordinates", payload)
	}
	if !catalogReloaded || !groupsReloaded {
		t.Fatalf("reload callbacks called catalog=%t groups=%t, want both", catalogReloaded, groupsReloaded)
	}
	if _, found, err := storage.GetPair(ctx, db, "provider", "tenant/model"); err != nil || found {
		t.Fatalf("pair found=%t, error=%v after delete", found, err)
	}
}

func TestDeleteAdminPairEndpointReportsUnknownPair(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Options{Path: ":memory:", BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	handler := NewAdmin(AdminOptions{DB: db})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/admin/v1/pairs/missing/model", nil)
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s; want 404", response.Code, response.Body.String())
	}
}
