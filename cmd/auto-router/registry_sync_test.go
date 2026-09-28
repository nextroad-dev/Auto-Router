package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/modelsdev"
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

func TestRegistrySyncEmptyIncludeImportsFullCatalogWithoutLocalBindings(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, syncTestCatalog)
	}))
	defer server.Close()

	db := newRegistrySyncTestDB(t)
	if err := syncRegistry(context.Background(), registrySyncConfig(server.URL, nil), db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("full-catalog sync failed without local bindings: %v", err)
	}
	if requests != 1 {
		t.Fatalf("full-catalog sync made %d upstream requests, want 1", requests)
	}
	state, found, err := storage.LoadSyncState(context.Background(), db, models.SourceModelsDev)
	if err != nil || !found {
		t.Fatalf("sync state found=%t err=%v", found, err)
	}
	wantScope := []string{"anthropic/claude-test", "openai/gpt-test"}
	if state.AllowlistDigest != modelsdev.IncludeDigest(wantScope) || state.ImportedPairs != 2 || state.SkippedPairs != 0 {
		t.Fatalf("full-catalog scope was not recorded: %+v", state)
	}
	catalog, err := storage.LoadCatalog(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Providers) != 2 || len(catalog.Models) != 2 || len(catalog.Pairs) != 2 {
		t.Fatalf("full catalog not imported: providers=%d models=%d pairs=%d", len(catalog.Providers), len(catalog.Models), len(catalog.Pairs))
	}
	for _, provider := range []string{"openai", "anthropic"} {
		view, found, err := storage.GetProvider(context.Background(), db, provider)
		if err != nil || !found || view.Provider.Enabled {
			t.Fatalf("new catalog provider %q must remain disabled: view=%+v found=%t err=%v", provider, view, found, err)
		}
	}
}

func createSyncTestBinding(t *testing.T, ctx context.Context, db *sql.DB, provider, model string, enabled bool) {
	t.Helper()
	if err := storage.CreateProvider(ctx, db, storage.NewProvider{Key: provider, Enabled: enabled}); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateModel(ctx, db, storage.NewModel{ID: model, Enabled: enabled}); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreatePair(ctx, db, storage.NewPair{
		ProviderKey: provider, ModelID: model, UpstreamModelID: model,
		ContextWindow: 128000, Enabled: enabled,
	}); err != nil {
		t.Fatal(err)
	}
}

const syncTestCatalog = `{"openai":{"id":"openai","name":"OpenAI","models":{"gpt-test":{"id":"gpt-test","name":"GPT Test","tool_call":true,"modalities":{"input":["text"],"output":["text"]},"limit":{"context":128000,"output":4096}}}},"anthropic":{"id":"anthropic","name":"Anthropic","models":{"claude-test":{"id":"claude-test","name":"Claude Test","modalities":{"input":["text"],"output":["text"]},"limit":{"context":128000,"output":4096}}}}}`

func TestRegistrySyncEmptyIncludeImportsFullCatalogAndPreservesDisabledLocalBinding(t *testing.T) {
	ctx := context.Background()
	db := newRegistrySyncTestDB(t)
	createSyncTestBinding(t, ctx, db, "openai", "gpt-test", false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, syncTestCatalog)
	}))
	defer server.Close()

	if err := syncRegistry(ctx, registrySyncConfig(server.URL, nil), db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("syncRegistry failed: %v", err)
	}
	state, found, err := storage.LoadSyncState(ctx, db, models.SourceModelsDev)
	if err != nil || !found {
		t.Fatalf("sync state found=%t err=%v", found, err)
	}
	wantScope := []string{"anthropic/claude-test", "openai/gpt-test"}
	if state.AllowlistDigest != modelsdev.IncludeDigest(wantScope) || state.ImportedPairs != 2 {
		t.Fatalf("empty include did not select the full catalog: %+v", state)
	}
	pair, found, err := storage.GetPair(ctx, db, "openai", "gpt-test")
	if err != nil || !found || pair.Pair.Enabled {
		t.Fatalf("sync changed the disabled local binding: pair=%+v found=%t err=%v", pair.Pair, found, err)
	}
	if _, found, err := storage.GetPair(ctx, db, "anthropic", "claude-test"); err != nil || !found {
		t.Fatalf("full-catalog sync omitted an unconfigured catalog pair: found=%t err=%v", found, err)
	}
}

func TestRegistrySyncExplicitIncludeOverridesFullCatalogDefault(t *testing.T) {
	ctx := context.Background()
	db := newRegistrySyncTestDB(t)
	createSyncTestBinding(t, ctx, db, "openai", "gpt-test", false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, syncTestCatalog)
	}))
	defer server.Close()

	include := []string{"anthropic/claude-test"}
	if err := syncRegistry(ctx, registrySyncConfig(server.URL, include), db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("syncRegistry failed: %v", err)
	}
	state, found, err := storage.LoadSyncState(ctx, db, models.SourceModelsDev)
	if err != nil || !found {
		t.Fatalf("sync state found=%t err=%v", found, err)
	}
	if state.AllowlistDigest != modelsdev.IncludeDigest(include) || state.ImportedPairs != 1 {
		t.Fatalf("explicit include did not override full-catalog scope: %+v", state)
	}
	if _, found, err := storage.GetPair(ctx, db, "anthropic", "claude-test"); err != nil || !found {
		t.Fatalf("explicitly included pair missing: found=%t err=%v", found, err)
	}
}

func TestRegistrySyncFullCatalogIsIndependentOfLocalCustomBindings(t *testing.T) {
	ctx := context.Background()
	db := newRegistrySyncTestDB(t)
	createSyncTestBinding(t, ctx, db, "openai", "gpt-test", false)
	createSyncTestBinding(t, ctx, db, "private-provider", "private-model", false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, syncTestCatalog)
	}))
	defer server.Close()

	if err := syncRegistry(ctx, registrySyncConfig(server.URL, nil), db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("custom binding should not fail the automatic-scope sync: %v", err)
	}
	state, found, err := storage.LoadSyncState(ctx, db, models.SourceModelsDev)
	if err != nil || !found {
		t.Fatalf("sync state found=%t err=%v", found, err)
	}
	if state.ImportedPairs != 2 || state.SkippedPairs != 0 || len(state.Warnings) != 0 {
		t.Fatalf("full-catalog scope was affected by local custom bindings: %+v", state)
	}
	privatePair, found, err := storage.GetPair(ctx, db, "private-provider", "private-model")
	if err != nil || !found || privatePair.Pair.Source != models.SourceLocal || privatePair.Pair.Enabled {
		t.Fatalf("local custom binding was not preserved: pair=%+v found=%t err=%v", privatePair.Pair, found, err)
	}
}

func TestRegistrySyncRejectsCatalogWithoutMappableModelsAndKeepsPriorCatalog(t *testing.T) {
	ctx := context.Background()
	db := newRegistrySyncTestDB(t)
	payload := syncTestCatalog
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()

	cfg := registrySyncConfig(server.URL, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := syncRegistry(ctx, cfg, db, logger); err != nil {
		t.Fatalf("initial full-catalog sync failed: %v", err)
	}
	before, found, err := storage.LoadSyncState(ctx, db, models.SourceModelsDev)
	if err != nil || !found {
		t.Fatalf("initial sync state found=%t err=%v", found, err)
	}

	payload = `{"openai":{"id":"openai","name":"OpenAI","models":{"broken":{"id":"broken","limit":{"context":0}}}}}`
	err = syncRegistry(ctx, cfg, db, logger)
	if !errors.Is(err, errNoMappableRegistryModels) {
		t.Fatalf("empty mappable full catalog error=%v, want %v", err, errNoMappableRegistryModels)
	}
	after, found, err := storage.LoadSyncState(ctx, db, models.SourceModelsDev)
	if err != nil || !found || after.AllowlistDigest != before.AllowlistDigest || after.ImportedPairs != before.ImportedPairs || !after.FetchedAt.Equal(before.FetchedAt) {
		t.Fatalf("rejected catalog changed prior sync state: before=%+v after=%+v found=%t err=%v", before, after, found, err)
	}
	pair, found, err := storage.GetPair(ctx, db, "anthropic", "claude-test")
	if err != nil || !found || !pair.Pair.Enabled {
		t.Fatalf("rejected catalog disabled a prior pair: pair=%+v found=%t err=%v", pair.Pair, found, err)
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
