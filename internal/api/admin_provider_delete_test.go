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

func TestDeleteAdminProviderEndpoint(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Options{Path: ":memory:", BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateProvider(ctx, db, storage.NewProvider{Key: "disposable", DisplayName: "Disposable"}); err != nil {
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
	request := httptest.NewRequest(http.MethodDelete, "/admin/v1/providers/disposable", nil)
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Key     string `json:"key"`
		Deleted bool   `json:"deleted"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Key != "disposable" || !payload.Deleted {
		t.Fatalf("response = %+v, expected deleted disposable", payload)
	}
	if !catalogReloaded || !groupsReloaded {
		t.Fatalf("reload callbacks called catalog=%t groups=%t, want both", catalogReloaded, groupsReloaded)
	}
	if _, found, err := storage.GetProvider(ctx, db, "disposable"); err != nil || found {
		t.Fatalf("provider found=%t, error=%v after delete", found, err)
	}
}
