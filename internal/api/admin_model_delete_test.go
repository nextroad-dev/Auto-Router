package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

func modelDeleteTestDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Options{Path: ":memory:", BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateProvider(ctx, db, storage.NewProvider{Key: "provider", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateModel(ctx, db, storage.NewModel{ID: "tenant/model", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreatePair(ctx, db, storage.NewPair{ProviderKey: "provider", ModelID: "tenant/model", UpstreamModelID: "upstream/model", ContextWindow: 1024, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := storage.ReplaceModelGroups(ctx, db, storage.ModelGroups{Simple: []storage.ModelGroupMember{{Provider: "provider", Model: "tenant/model"}}}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestDeleteAdminModelEndpointRemovesGroupsAndReloadsSnapshots(t *testing.T) {
	db := modelDeleteTestDB(t)
	var reloads []string
	handler := NewAdmin(AdminOptions{
		DB: db,
		ReloadGroups: func(_ context.Context, groups storage.ModelGroups) error {
			reloads = append(reloads, "groups")
			if len(groups.Simple)+len(groups.Medium)+len(groups.Complex) != 0 {
				t.Errorf("groups still reference deleted model: %+v", groups)
			}
			return nil
		},
		ReloadCatalog: func(context.Context) error { reloads = append(reloads, "catalog"); return nil },
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/admin/v1/models/tenant%2Fmodel", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d, body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		ID      string `json:"id"`
		Deleted bool   `json:"deleted"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ID != "tenant/model" || !payload.Deleted {
		t.Fatalf("payload=%+v", payload)
	}
	if strings.Join(reloads, ",") != "groups,catalog" {
		t.Fatalf("reload order=%v", reloads)
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Request-Id") == "" {
		t.Fatalf("missing management response headers: %v", response.Header())
	}
	if _, found, err := storage.GetModel(context.Background(), db, "tenant/model"); err != nil || found {
		t.Fatalf("model found=%t, err=%v", found, err)
	}
	if _, found, err := storage.GetProvider(context.Background(), db, "provider"); err != nil || !found {
		t.Fatalf("provider removed: found=%t, err=%v", found, err)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/admin/v1/models/tenant%2Fmodel", nil))
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":"unknown_model"`) {
		t.Fatalf("repeated deletion status=%d, body=%s", response.Code, response.Body.String())
	}
}

func TestDeleteAdminModelReportsSnapshotFailureAfterCommittedDeletion(t *testing.T) {
	for _, failing := range []string{"groups", "catalog"} {
		t.Run(failing, func(t *testing.T) {
			db := modelDeleteTestDB(t)
			handler := NewAdmin(AdminOptions{
				DB: db,
				ReloadGroups: func(context.Context, storage.ModelGroups) error {
					if failing == "groups" {
						return errors.New("test failure")
					}
					return nil
				},
				ReloadCatalog: func(context.Context) error {
					if failing == "catalog" {
						return errors.New("test failure")
					}
					return nil
				},
			})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/admin/v1/models/tenant/model", nil))
			if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"snapshot_publish_failed"`) {
				t.Fatalf("status=%d, body=%s", response.Code, response.Body.String())
			}
			if _, found, err := storage.GetModel(context.Background(), db, "tenant/model"); err != nil || found {
				t.Fatalf("deletion not committed: found=%t, err=%v", found, err)
			}
		})
	}
}

func TestDeleteAdminModelUsesManagementGuardAndDatabaseProtection(t *testing.T) {
	db := modelDeleteTestDB(t)
	guarded := WithManagementSurface(NewAdmin(AdminOptions{DB: db}), nil, nil, true)
	response := httptest.NewRecorder()
	guarded.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/admin/v1/models/tenant/model", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d, body=%s", response.Code, response.Body.String())
	}
	if _, found, err := storage.GetModel(context.Background(), db, "tenant/model"); err != nil || !found {
		t.Fatalf("unauthorized deletion changed model, found=%t, err=%v", found, err)
	}

	response = httptest.NewRecorder()
	NewAdmin(AdminOptions{}).ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/admin/v1/models/tenant/model", nil))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"storage_error"`) {
		t.Fatalf("missing db status=%d, body=%s", response.Code, response.Body.String())
	}
}
