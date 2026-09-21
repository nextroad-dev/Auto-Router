package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

func modelsDevFixture(t *testing.T) []byte {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("..", "..", "internal", "modelsdev", "testdata", "api.json"))
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func fixtureSource(t *testing.T) *httptest.Server {
	t.Helper()
	payload := modelsDevFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)
	return server
}

func syncConfig(t *testing.T, source *httptest.Server, include []string) config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.HTTP.Address = "127.0.0.1:0"
	cfg.Database.Path = filepath.Join(t.TempDir(), "router.db")
	cfg.Registry.Sync.URL = source.URL
	cfg.Registry.Sync.Timeout = config.Duration(5 * time.Second)
	cfg.Registry.Sync.Include = include
	return cfg
}

func TestRunSyncModelsImportsAllowlistWithoutListening(t *testing.T) {
	source := fixtureSource(t)
	// gpt-4.1-nano is served by two providers in the fixture and must fold into
	// one logical model with two pairs.
	cfg := syncConfig(t, source, []string{"openai/gpt-4.1-nano", "helicone/gpt-4.1-nano", "openai/gpt-4o-mini"})
	// Occupy the configured HTTP address: a sync that accidentally started the
	// server would fail to bind and report an error.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	cfg.HTTP.Address = blocker.Addr().String()

	var output bytes.Buffer
	if err := run(t.Context(), []string{"-config", writeRuntimeConfig(t, cfg), "-sync-models"}, &output); err != nil {
		t.Fatalf("sync failed: %v\n%s", err, output.String())
	}
	logs := output.String()
	for _, want := range []string{"model registry sync starting", "model registry synchronized", "model registry loaded"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs should contain %q: %s", want, logs)
		}
	}

	db, err := storage.Open(t.Context(), storage.Options{Path: cfg.Database.Path, BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	catalog, err := storage.LoadCatalog(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Providers) != 2 || len(catalog.Models) != 2 || len(catalog.Pairs) != 3 {
		t.Fatalf("unexpected catalog after sync: %+v", catalog)
	}
	var sharedModels, sharedPairs int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM models WHERE id = 'gpt-4.1-nano'`).Scan(&sharedModels); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM provider_models WHERE model_id = 'gpt-4.1-nano'`).Scan(&sharedPairs); err != nil {
		t.Fatal(err)
	}
	if sharedModels != 1 || sharedPairs != 2 {
		t.Fatalf("one logical model must map to two providers: models=%d pairs=%d", sharedModels, sharedPairs)
	}
	for _, provider := range catalog.Providers {
		if provider.Enabled || provider.BaseURL != "" || provider.APIKey != "" || provider.Source != models.SourceModelsDev {
			t.Fatalf("synced provider should be inert and metadata-only: %+v", provider)
		}
	}
	if pairs := catalog.PairsForModel("gpt-4o-mini"); len(pairs) != 0 {
		t.Fatalf("disabled providers must not be routable: %+v", pairs)
	}
	state, ok, err := storage.LoadSyncState(t.Context(), db, models.SourceModelsDev)
	if err != nil || !ok {
		t.Fatalf("sync state missing: ok=%v err=%v", ok, err)
	}
	if state.ImportedPairs != 3 || state.SkippedPairs != 0 || state.URL != source.URL {
		t.Fatalf("unexpected sync state: %+v", state)
	}
	if state.AllowlistDigest == "" {
		t.Fatal("sync state should fingerprint the allowlist")
	}

	// Adding a model is a configuration change plus a re-run, with no code
	// change: the new whitelist entry appears in the catalog afterwards.
	cfg.Registry.Sync.Include = append(cfg.Registry.Sync.Include, "groq/llama-3.3-70b-versatile")
	if err := run(t.Context(), []string{"-config", writeRuntimeConfig(t, cfg), "-sync-models"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	extended, err := storage.LoadCatalog(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := extended.Lookup("llama-3.3-70b-versatile"); !ok {
		t.Fatalf("a newly allowlisted model should appear after a re-sync: %+v", extended.Models)
	}
	if len(extended.Providers) != 3 || len(extended.Models) != 3 || len(extended.Pairs) != 4 {
		t.Fatalf("unexpected catalog after the second sync: %+v", extended)
	}
	if _, ok := extended.Provider("groq"); !ok {
		t.Fatal("the newly allowlisted provider should be stored")
	}
}

func TestRunSyncModelsFailsLoudlyAndKeepsTheRegistry(t *testing.T) {
	source := fixtureSource(t)
	cfg := syncConfig(t, source, []string{"openai/gpt-4o-mini", "openai/not-a-real-model"})
	var output bytes.Buffer
	err := run(t.Context(), []string{"-config", writeRuntimeConfig(t, cfg), "-sync-models"}, &output)
	if err == nil || !strings.Contains(err.Error(), "not-a-real-model") {
		t.Fatalf("a missing allowlist entry must fail the sync, got %v", err)
	}
	db, openErr := storage.Open(t.Context(), storage.Options{Path: cfg.Database.Path, BusyTimeout: time.Second})
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer db.Close()
	catalog, loadErr := storage.LoadCatalog(t.Context(), db)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if !catalog.Empty() {
		t.Fatalf("a failed sync must not change the registry: %+v", catalog)
	}
	if _, ok, stateErr := storage.LoadSyncState(t.Context(), db, models.SourceModelsDev); stateErr != nil || ok {
		t.Fatalf("a failed sync must not record state: ok=%v err=%v", ok, stateErr)
	}
}

func TestRunSyncModelsRequiresAnAllowlist(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.Path = filepath.Join(t.TempDir(), "router.db")
	err := run(t.Context(), []string{"-config", writeRuntimeConfig(t, cfg), "-sync-models"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "include is empty") {
		t.Fatalf("expected an empty-allowlist error, got %v", err)
	}
}

func TestRunSyncThenServeRoutesTheSyncedModel(t *testing.T) {
	source := fixtureSource(t)
	cfg := syncConfig(t, source, []string{"openai/gpt-4o-mini"})
	if err := run(t.Context(), []string{"-config", writeRuntimeConfig(t, cfg), "-sync-models"}, io.Discard); err != nil {
		t.Fatal(err)
	}

	// Configuring the provider locally is the only step needed to make the
	// synchronized pair routable; no code change and no model-specific config.
	cfg.Registry.Providers = []config.RegistryProvider{{
		Key:         "openai",
		DisplayName: "OpenAI",
		BaseURL:     "https://api.openai.com/v1",
		APIKey:      "sk-super-secret-key-0123456789",
		Enabled:     true,
		Priority:    1,
	}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	cfg.HTTP.Address = address
	logs := &recordingLog{address: make(chan string, 1)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = run(ctx, []string{"-config", writeRuntimeConfig(t, cfg)}, logs)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		waitSignal(t, done)
	})
	waitForService(t, logs, done)
	requireReady(t, address)
	cancel()
	waitSignal(t, done)
	if runErr != nil {
		t.Fatalf("serve run failed: %v\n%s", runErr, logs.String())
	}
	if rendered := logs.String(); strings.Contains(rendered, "sk-super-secret-key-0123456789") {
		t.Fatalf("logs leaked the provider API key:\n%s", rendered)
	}
	db, err := storage.Open(t.Context(), storage.Options{Path: cfg.Database.Path, BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	catalog, err := storage.LoadCatalog(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	pairs := catalog.PairsForModel("gpt-4o-mini")
	if len(pairs) != 1 || pairs[0].ProviderKey != "openai" || pairs[0].UpstreamModelID != "gpt-4o-mini" {
		t.Fatalf("the synced pair should be routable after local provider configuration: %+v", pairs)
	}
	provider, _ := catalog.Provider("openai")
	if provider.APIKey == "" || provider.Source != models.SourceLocal {
		t.Fatalf("local provider configuration should own the row: %+v", provider)
	}
}

func TestRunEmptyRegistryWarnsButServes(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.Path = filepath.Join(t.TempDir(), "router.db")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	cfg.HTTP.Address = address
	logs := &recordingLog{address: make(chan string, 1)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = run(ctx, []string{"-config", writeRuntimeConfig(t, cfg)}, logs)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		waitSignal(t, done)
	})
	waitForService(t, logs, done)
	requireReady(t, address)
	cancel()
	waitSignal(t, done)
	if runErr != nil {
		t.Fatalf("an empty registry must not block startup: %v", runErr)
	}
	if !strings.Contains(logs.String(), "model registry is empty") {
		t.Fatalf("startup should warn about the empty registry:\n%s", logs.String())
	}
}

func TestRunAppliesLocalRegistryAndWarnsOnDisabledProvider(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.Path = filepath.Join(t.TempDir(), "router.db")
	cfg.Registry.Providers = []config.RegistryProvider{}
	cfg.Registry.Models = []config.RegistryModel{}
	// A pair declared for a declared-but-disabled provider is valid and warns.
	cfg.Registry.Providers = append(cfg.Registry.Providers, config.RegistryProvider{
		Key: "local-gateway", BaseURL: "http://127.0.0.1:11434/v1", Enabled: false,
	})
	cfg.Registry.Models = append(cfg.Registry.Models, config.RegistryModel{
		Provider: "local-gateway", Model: "llama3.3", ContextWindow: 8192, MaxOutput: intPointer(4096), Enabled: true,
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	cfg.HTTP.Address = address
	logs := &recordingLog{address: make(chan string, 1)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = run(ctx, []string{"-config", writeRuntimeConfig(t, cfg)}, logs)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		waitSignal(t, done)
	})
	waitForService(t, logs, done)
	cancel()
	waitSignal(t, done)
	if runErr != nil {
		t.Fatal(runErr)
	}
	rendered := logs.String()
	if !strings.Contains(rendered, "model registry warning") || !strings.Contains(rendered, "local-gateway") {
		t.Fatalf("expected a disabled-provider warning:\n%s", rendered)
	}
	if !strings.Contains(rendered, "local registry overrides applied") {
		t.Fatalf("expected the local import summary:\n%s", rendered)
	}
}

func TestRunSyncModelsRejectsMalformedSource(t *testing.T) {
	cases := map[string]struct {
		handler func(http.ResponseWriter, *http.Request)
		want    string
	}{
		"malformed json": {handler: func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{not json")) }, want: "decode"},
		"server error":   {handler: func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusBadGateway) }, want: "status 502"},
		"oversized":      {handler: func(w http.ResponseWriter, r *http.Request) {}, want: ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tc.handler))
			defer server.Close()
			cfg := syncConfig(t, server, []string{"openai/gpt-4o-mini"})
			err := run(t.Context(), []string{"-config", writeRuntimeConfig(t, cfg), "-sync-models"}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
			db, openErr := storage.Open(t.Context(), storage.Options{Path: cfg.Database.Path, BusyTimeout: time.Second})
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer db.Close()
			catalog, loadErr := storage.LoadCatalog(t.Context(), db)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if !catalog.Empty() {
				t.Fatalf("a failed sync must leave the registry empty: %+v", catalog)
			}
		})
	}
}

// recordingLog captures every JSON log line and signals the first "service
// started" event with its address.
type recordingLog struct {
	mutex   sync.Mutex
	lines   []string
	address chan string
}

func (w *recordingLog) Write(p []byte) (int, error) {
	var event struct {
		Message string `json:"msg"`
		Address string `json:"address"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(p), &event); err != nil {
		w.mutex.Lock()
		w.lines = append(w.lines, string(p))
		w.mutex.Unlock()
		return len(p), nil
	}
	w.mutex.Lock()
	w.lines = append(w.lines, string(p))
	w.mutex.Unlock()
	if event.Message == "service started" {
		select {
		case w.address <- event.Address:
		default:
		}
	}
	return len(p), nil
}

func (w *recordingLog) String() string {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return strings.Join(w.lines, "")
}

func waitForService(t *testing.T, logs *recordingLog, done <-chan struct{}) {
	t.Helper()
	select {
	case <-logs.address:
	case <-done:
		t.Fatalf("service failed to start:\n%s", logs.String())
	case <-time.After(10 * time.Second):
		t.Fatalf("service did not start:\n%s", logs.String())
	}
}

func requireReady(t *testing.T, address string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(10 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		response, err := client.Get("http://" + address + "/readyz")
		if err == nil {
			_, readErr := io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK {
				return
			}
			last = readErr
		} else {
			last = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service never became ready: %v", last)
}

func intPointer(value int) *int { return &value }
