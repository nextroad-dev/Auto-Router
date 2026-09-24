package main

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

func newRegistrySyncTestDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Options{Path: ":memory:", BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return db
}

func registrySyncConfig(url string, include []string) config.Config {
	cfg := config.Defaults()
	cfg.Registry.Sync.URL = url
	cfg.Registry.Sync.Timeout = config.Duration(time.Second)
	cfg.Registry.Sync.Include = include
	return cfg
}

func TestRegistrySyncRejectsEmptyAllowlistWithoutNetworkRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	db := newRegistrySyncTestDB(t)
	err := syncRegistry(context.Background(), registrySyncConfig(server.URL, nil), db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("empty allowlist unexpectedly synchronized")
	}
	if requests != 0 {
		t.Fatalf("empty allowlist made %d upstream requests", requests)
	}
}

func TestRegistrySyncFailureLeavesExistingCatalogAndStateUntouched(t *testing.T) {
	ctx := context.Background()
	db := newRegistrySyncTestDB(t)
	if err := storage.CreateProvider(ctx, db, storage.NewProvider{Key: "existing-provider", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateModel(ctx, db, storage.NewModel{ID: "existing-model", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}))
	defer server.Close()

	err := syncRegistry(context.Background(), registrySyncConfig(server.URL, []string{"openai/gpt-test"}), db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("upstream failure unexpectedly reported success")
	}
	if _, found, err := storage.GetProvider(ctx, db, "existing-provider"); err != nil || !found {
		t.Fatalf("failed sync changed existing provider: found=%t err=%v", found, err)
	}
	if _, found, err := storage.GetModel(ctx, db, "existing-model"); err != nil || !found {
		t.Fatalf("failed sync changed existing model: found=%t err=%v", found, err)
	}
	if _, found, err := storage.LoadSyncState(ctx, db, models.SourceModelsDev); err != nil || found {
		t.Fatalf("failed sync changed the success state: found=%t err=%v", found, err)
	}
}

func TestRegistrySyncSuccessImportsAllowlistedRowsAndState(t *testing.T) {
	ctx := context.Background()
	db := newRegistrySyncTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"openai":{"id":"openai","name":"OpenAI","models":{"gpt-test":{"id":"gpt-test","name":"GPT Test","tool_call":true,"modalities":{"input":["text"],"output":["text"]},"limit":{"context":128000,"output":4096}}}}}`)
	}))
	defer server.Close()

	if err := syncRegistry(context.Background(), registrySyncConfig(server.URL, []string{"openai/gpt-test"}), db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("syncRegistry failed: %v", err)
	}
	catalog, err := storage.LoadCatalog(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Providers) != 1 || len(catalog.Models) != 1 || len(catalog.Pairs) != 1 {
		t.Fatalf("unexpected imported catalog: providers=%d models=%d pairs=%d", len(catalog.Providers), len(catalog.Models), len(catalog.Pairs))
	}
	state, found, err := storage.LoadSyncState(ctx, db, models.SourceModelsDev)
	if err != nil || !found {
		t.Fatalf("sync state found=%t err=%v", found, err)
	}
	if state.ImportedPairs != 1 || state.URL != server.URL {
		t.Fatalf("unexpected sync state: %+v", state)
	}
}
