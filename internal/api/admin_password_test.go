package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

func TestInitialPasswordSetupAllowsNonLoopbackConnection(t *testing.T) {
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
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/setup/password", strings.NewReader(`{"password":"correct-horse-battery"}`))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "203.0.113.20:43210"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("remote password setup status = %d, body = %s; want %d", response.Code, response.Body.String(), http.StatusCreated)
	}
	set, err := storage.PasswordSet(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if !set {
		t.Fatal("initial password was not persisted")
	}
}
