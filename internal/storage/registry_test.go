package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/models"
)

func localProvider(key string, enabled bool, priority int) models.Provider {
	return models.Provider{
		Key:         key,
		DisplayName: key + " (local)",
		BaseURL:     "https://" + key + ".example/v1",
		APIKey:      "sk-secret-" + key + "-0123456789abcdef",
		Enabled:     enabled,
		Priority:    priority,
	}
}

func syncedProvider(key, displayName string) models.Provider {
	return models.Provider{Key: key, DisplayName: displayName}
}

func logicalModel(id string) models.Model {
	return models.Model{ID: id, DisplayName: id, Enabled: true, Priority: 5}
}

func syncedPair(providerKey, modelID string, contextWindow, maxOutput int) models.Pair {
	output := maxOutput
	return models.Pair{
		ProviderKey:       providerKey,
		ModelID:           modelID,
		UpstreamModelID:   modelID,
		ContextWindow:     contextWindow,
		MaxOutput:         &output,
		SupportsTools:     true,
		SupportsVision:    true,
		SupportsReasoning: false,
	}
}

func localPair(providerKey, modelID string, enabled bool, priority int) models.Pair {
	return models.Pair{
		ProviderKey:     providerKey,
		ModelID:         modelID,
		UpstreamModelID: modelID + "-pinned",
		ContextWindow:   32000,
		SupportsTools:   true,
		Enabled:         enabled,
		Priority:        priority,
	}
}

func registryDB(t *testing.T) *sql.DB {
	t.Helper()
	db := testDB(t)
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestApplyLocalImportUpsertsAndDisablesWithoutDeleting(t *testing.T) {
	db := registryDB(t)
	imported := Import{
		Providers: []models.Provider{localProvider("openai", true, 10)},
		Models:    []models.Model{logicalModel("gpt-4o")},
		Pairs:     []models.Pair{localPair("openai", "gpt-4o", true, 10)},
	}
	summary, err := ApplyRegistryImport(t.Context(), db, models.SourceLocal, imported)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ProvidersAdded != 1 || summary.ModelsAdded != 1 || summary.PairsAdded != 1 {
		t.Fatalf("unexpected first import summary: %+v", summary)
	}
	// Re-applying the same configuration is idempotent and reports updates.
	summary, err = ApplyRegistryImport(t.Context(), db, models.SourceLocal, imported)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ProvidersAdded != 0 || summary.ProvidersUpdated != 1 || summary.ModelsAdded != 0 || summary.ModelsUpdated != 1 || summary.PairsAdded != 0 || summary.PairsUpdated != 1 {
		t.Fatalf("unexpected repeat import summary: %+v", summary)
	}
	if countRows(t, db, "providers") != 1 || countRows(t, db, "models") != 1 || countRows(t, db, "provider_models") != 1 {
		t.Fatal("repeated import must not duplicate rows")
	}

	// Local configuration is authoritative for the fields it owns.
	updated := imported
	updated.Providers = []models.Provider{localProvider("openai", false, 3)}
	updated.Providers[0].DisplayName = "OpenAI Custom"
	updated.Providers[0].BaseURL = "https://gateway.internal/openai"
	updated.Pairs = []models.Pair{localPair("openai", "gpt-4o", true, 1)}
	if _, err := ApplyRegistryImport(t.Context(), db, models.SourceLocal, updated); err != nil {
		t.Fatal(err)
	}
	var displayName, baseURL, apiKey string
	var enabled, priority int
	if err := db.QueryRowContext(t.Context(), `SELECT display_name, base_url, api_key, enabled, priority FROM providers WHERE key = 'openai'`).
		Scan(&displayName, &baseURL, &apiKey, &enabled, &priority); err != nil {
		t.Fatal(err)
	}
	if displayName != "OpenAI Custom" || baseURL != "https://gateway.internal/openai" || enabled != 0 || priority != 3 {
		t.Fatalf("provider update not applied: %q %q %d %d", displayName, baseURL, enabled, priority)
	}
	if apiKey == "" {
		t.Fatal("api key should be stored (plaintext, documented risk)")
	}

	// Removing a local model and pair disables them; credentials remain but are
	// unusable because the provider is disabled and the pair is gone.
	removed := Import{Providers: []models.Provider{localProvider("openai", false, 3)}}
	summary, err = ApplyRegistryImport(t.Context(), db, models.SourceLocal, removed)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ModelsDisabled != 1 || summary.PairsDisabled != 1 || summary.ProvidersDisabled != 0 {
		t.Fatalf("unexpected removal summary: %+v", summary)
	}
	if countRows(t, db, "models") != 1 || countRows(t, db, "provider_models") != 1 {
		t.Fatal("removed entries must be disabled, not deleted")
	}
	var modelEnabled, pairEnabled int
	if err := db.QueryRowContext(t.Context(), `SELECT enabled FROM models WHERE id = 'gpt-4o'`).Scan(&modelEnabled); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT enabled FROM provider_models WHERE provider_key = 'openai' AND model_id = 'gpt-4o'`).Scan(&pairEnabled); err != nil {
		t.Fatal(err)
	}
	if modelEnabled != 0 || pairEnabled != 0 {
		t.Fatalf("removed local entries must be disabled: model=%d pair=%d", modelEnabled, pairEnabled)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT api_key FROM providers WHERE key = 'openai'`).Scan(&apiKey); err != nil {
		t.Fatal(err)
	}
	if apiKey == "" {
		t.Fatal("disabling must not delete stored credentials")
	}

	// Removing the provider as well disables the row exactly once: it is still
	// enabled at this point, so an empty local import must disable it.
	if _, err := ApplyRegistryImport(t.Context(), db, models.SourceLocal, Import{
		Providers: []models.Provider{localProvider("openai", true, 3)},
	}); err != nil {
		t.Fatal(err)
	}
	summary, err = ApplyRegistryImport(t.Context(), db, models.SourceLocal, Import{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.ProvidersDisabled != 1 {
		t.Fatalf("the removed provider should be disabled once: %+v", summary)
	}
	summary, err = ApplyRegistryImport(t.Context(), db, models.SourceLocal, Import{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.ProvidersDisabled != 0 {
		t.Fatalf("an already disabled provider must not be counted again: %+v", summary)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT enabled FROM providers WHERE key = 'openai'`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 {
		t.Fatalf("provider should stay disabled: %d", enabled)
	}
}

func TestSyncImportPreservesLocalOverrides(t *testing.T) {
	db := registryDB(t)
	if _, err := ApplyRegistryImport(t.Context(), db, models.SourceLocal, Import{
		Providers: []models.Provider{localProvider("openai", true, 7)},
		Models:    []models.Model{{ID: "gpt-4o", DisplayName: "GPT-4o (local)", Enabled: true, Priority: 4}},
		Pairs:     []models.Pair{localPair("openai", "gpt-4o", true, 3)},
	}); err != nil {
		t.Fatal(err)
	}

	summary, err := ApplyRegistryImport(t.Context(), db, models.SourceModelsDev, Import{
		Providers: []models.Provider{syncedProvider("openai", "OpenAI")},
		Models:    []models.Model{{ID: "gpt-4o", DisplayName: "GPT-4o"}},
		Pairs:     []models.Pair{syncedPair("openai", "gpt-4o", 128000, 16384)},
		Sync:      &SyncState{URL: "https://models.dev/api.json", FetchedAt: time.Now(), AllowlistDigest: "digest", ImportedPairs: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.ProvidersAdded != 0 || summary.ProvidersUpdated != 0 {
		t.Fatalf("sync must not rewrite a locally owned provider: %+v", summary)
	}
	if summary.ModelsAdded != 0 || summary.ModelsUpdated != 0 || summary.PairsAdded != 0 || summary.PairsUpdated != 0 {
		t.Fatalf("sync must not rewrite locally owned entries: %+v", summary)
	}

	var displayName, baseURL string
	var enabled, priority int
	if err := db.QueryRowContext(t.Context(), `SELECT display_name, base_url, enabled, priority FROM providers WHERE key = 'openai'`).Scan(&displayName, &baseURL, &enabled, &priority); err != nil {
		t.Fatal(err)
	}
	if displayName != "openai (local)" || baseURL != "https://openai.example/v1" || enabled != 1 || priority != 7 {
		t.Fatalf("local provider fields were changed: %q %q %d %d", displayName, baseURL, enabled, priority)
	}
	var contextWindow int
	var pairEnabled, pairPriority int
	if err := db.QueryRowContext(t.Context(), `SELECT context_window, enabled, priority FROM provider_models WHERE provider_key = 'openai' AND model_id = 'gpt-4o'`).
		Scan(&contextWindow, &pairEnabled, &pairPriority); err != nil {
		t.Fatal(err)
	}
	if contextWindow != 32000 || pairEnabled != 1 || pairPriority != 3 {
		t.Fatalf("local pair was changed: context=%d enabled=%d priority=%d", contextWindow, pairEnabled, pairPriority)
	}
	var modelDisplay string
	if err := db.QueryRowContext(t.Context(), `SELECT display_name FROM models WHERE id = 'gpt-4o'`).Scan(&modelDisplay); err != nil {
		t.Fatal(err)
	}
	if modelDisplay != "GPT-4o (local)" {
		t.Fatalf("local model display name was changed: %q", modelDisplay)
	}
}

func TestSyncImportStartsProvidersDisabledAndDisablesRemovedPairs(t *testing.T) {
	db := registryDB(t)
	full := Import{
		Providers: []models.Provider{syncedProvider("openai", "OpenAI"), syncedProvider("groq", "Groq")},
		Models:    []models.Model{{ID: "gpt-4o", DisplayName: "GPT-4o"}, {ID: "llama-3.3-70b", DisplayName: "Llama 3.3 70B"}},
		Pairs: []models.Pair{
			syncedPair("openai", "gpt-4o", 128000, 16384),
			syncedPair("groq", "llama-3.3-70b", 131072, 32768),
		},
		Sync: &SyncState{URL: "https://models.dev/api.json", FetchedAt: time.Now(), AllowlistDigest: "full", ImportedPairs: 2},
	}
	summary, err := ApplyRegistryImport(t.Context(), db, models.SourceModelsDev, full)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ProvidersAdded != 2 || summary.ModelsAdded != 2 || summary.PairsAdded != 2 {
		t.Fatalf("unexpected sync summary: %+v", summary)
	}
	var enabled int
	var baseURL, apiKey string
	if err := db.QueryRowContext(t.Context(), `SELECT enabled, base_url, api_key FROM providers WHERE key = 'openai'`).Scan(&enabled, &baseURL, &apiKey); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 || baseURL != "" || apiKey != "" {
		t.Fatalf("freshly synced providers must be disabled without credentials: enabled=%d base_url=%q", enabled, baseURL)
	}
	catalog, err := LoadCatalog(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if got := catalog.PairsForModel("gpt-4o"); len(got) != 0 {
		t.Fatalf("a disabled provider must not expose pairs: %+v", got)
	}

	// The whitelist shrinks: removed pairs are disabled, providers and logical
	// models stay (they may still serve other sources).
	smaller := Import{
		Providers: []models.Provider{syncedProvider("openai", "OpenAI Renamed"), syncedProvider("groq", "Groq")},
		Models:    []models.Model{{ID: "gpt-4o", DisplayName: "GPT-4o"}},
		Pairs:     []models.Pair{syncedPair("openai", "gpt-4o", 128000, 16384)},
		Sync:      &SyncState{URL: "https://models.dev/api.json", FetchedAt: time.Now(), AllowlistDigest: "smaller", ImportedPairs: 1},
	}
	summary, err = ApplyRegistryImport(t.Context(), db, models.SourceModelsDev, smaller)
	if err != nil {
		t.Fatal(err)
	}
	if summary.PairsDisabled != 1 {
		t.Fatalf("the removed pair should be disabled: %+v", summary)
	}
	var pairEnabled int
	if err := db.QueryRowContext(t.Context(), `SELECT enabled FROM provider_models WHERE provider_key = 'groq' AND model_id = 'llama-3.3-70b'`).Scan(&pairEnabled); err != nil {
		t.Fatal(err)
	}
	if pairEnabled != 0 {
		t.Fatal("removed pair must be disabled, not deleted")
	}
	if countRows(t, db, "provider_models") != 2 || countRows(t, db, "models") != 2 || countRows(t, db, "providers") != 2 {
		t.Fatal("sync must never delete rows")
	}
	var syncedDisplayName string
	if err := db.QueryRowContext(t.Context(), `SELECT display_name FROM providers WHERE key = 'openai'`).Scan(&syncedDisplayName); err != nil {
		t.Fatal(err)
	}
	if syncedDisplayName != "OpenAI Renamed" {
		t.Fatalf("sync should refresh metadata of non-local rows: %q", syncedDisplayName)
	}
}

func TestSyncStateRoundTripAndBounding(t *testing.T) {
	db := registryDB(t)
	if _, ok, err := LoadSyncState(t.Context(), db, models.SourceModelsDev); err != nil || ok {
		t.Fatalf("missing sync state must be reported as absent: ok=%v err=%v", ok, err)
	}
	warnings := make([]string, maxStoredWarnings+5)
	for i := range warnings {
		warnings[i] = "warning"
	}
	fetched := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	if _, err := ApplyRegistryImport(t.Context(), db, models.SourceModelsDev, Import{
		Sync: &SyncState{
			URL:             "https://models.dev/api.json",
			FetchedAt:       fetched,
			AllowlistDigest: "digest-1",
			SkippedPairs:    2,
			Warnings:        warnings,
		},
	}); err != nil {
		t.Fatal(err)
	}
	state, ok, err := LoadSyncState(t.Context(), db, models.SourceModelsDev)
	if err != nil || !ok {
		t.Fatalf("sync state should be recorded: ok=%v err=%v", ok, err)
	}
	if !state.FetchedAt.Equal(fetched) || state.AllowlistDigest != "digest-1" || state.SkippedPairs != 2 {
		t.Fatalf("unexpected sync state: %+v", state)
	}
	if len(state.Warnings) != maxStoredWarnings+1 || !strings.Contains(state.Warnings[len(state.Warnings)-1], "more warnings suppressed") {
		t.Fatalf("warnings should be bounded with a marker, got %d", len(state.Warnings))
	}
	// Re-synchronizing replaces the single row instead of accumulating.
	if _, err := ApplyRegistryImport(t.Context(), db, models.SourceModelsDev, Import{
		Sync: &SyncState{URL: "https://mirror.example/api.json", FetchedAt: fetched.Add(time.Hour), AllowlistDigest: "digest-2"},
	}); err != nil {
		t.Fatal(err)
	}
	if countRows(t, db, "registry_sync_state") != 1 {
		t.Fatal("sync state must be upserted per source")
	}
	state, _, err = LoadSyncState(t.Context(), db, models.SourceModelsDev)
	if err != nil {
		t.Fatal(err)
	}
	if state.AllowlistDigest != "digest-2" || state.URL != "https://mirror.example/api.json" || len(state.Warnings) != 0 {
		t.Fatalf("sync state was not replaced: %+v", state)
	}
}

func TestApplyRegistryImportRejectsInvalidBatchesWithoutWriting(t *testing.T) {
	db := registryDB(t)
	cases := map[string]Import{
		"dangling pair": {
			Providers: []models.Provider{localProvider("openai", true, 0)},
			Models:    []models.Model{logicalModel("gpt-4o")},
			Pairs:     []models.Pair{localPair("missing", "gpt-4o", true, 0)},
		},
		"dangling model": {
			Providers: []models.Provider{localProvider("openai", true, 0)},
			Models:    []models.Model{logicalModel("gpt-4o")},
			Pairs:     []models.Pair{localPair("openai", "unknown", true, 0)},
		},
		"invalid provider": {
			Providers: []models.Provider{{Key: "OpenAI", DisplayName: "OpenAI"}},
		},
		"enabled without url": {
			Providers: []models.Provider{{Key: "openai", DisplayName: "OpenAI", Enabled: true}},
		},
		"capacity": {
			Providers: []models.Provider{localProvider("openai", true, 0)},
			Models:    []models.Model{logicalModel("gpt-4o")},
			Pairs: []models.Pair{{
				ProviderKey: "openai", ModelID: "gpt-4o", UpstreamModelID: "gpt-4o", ContextWindow: 100, MaxOutput: intPointer(101),
			}},
		},
		"local with sync state": {
			Sync: &SyncState{URL: "https://models.dev/api.json"},
		},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ApplyRegistryImport(t.Context(), db, models.SourceLocal, in); err == nil {
				t.Fatal("expected import rejection")
			}
			if countRows(t, db, "providers") != 0 || countRows(t, db, "models") != 0 || countRows(t, db, "provider_models") != 0 {
				t.Fatal("a rejected batch must leave the registry untouched")
			}
		})
	}
	if _, err := ApplyRegistryImport(t.Context(), db, "bogus", Import{}); err == nil {
		t.Fatal("unknown source should be rejected")
	}
}

func TestApplyRegistryImportRollsBackOnWriteFailure(t *testing.T) {
	db := registryDB(t)
	if _, err := ApplyRegistryImport(t.Context(), db, models.SourceLocal, Import{
		Providers: []models.Provider{localProvider("openai", true, 1)},
		Models:    []models.Model{logicalModel("gpt-4o")},
		Pairs:     []models.Pair{localPair("openai", "gpt-4o", true, 1)},
	}); err != nil {
		t.Fatal(err)
	}
	// A second import that cannot complete (dangling reference) must not
	// disable or modify anything from the first.
	before, err := LoadCatalog(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyRegistryImport(t.Context(), db, models.SourceLocal, Import{
		Providers: []models.Provider{localProvider("openai", false, 9)},
		Models:    []models.Model{logicalModel("gpt-4o")},
		Pairs:     []models.Pair{localPair("openai", "gpt-4o", true, 9), localPair("missing", "gpt-4o", true, 9)},
	}); err == nil {
		t.Fatal("dangling pair should fail the whole batch")
	}
	after, err := LoadCatalog(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if after.Providers[0].Enabled != before.Providers[0].Enabled || after.Providers[0].Priority != before.Providers[0].Priority {
		t.Fatalf("failed import leaked a partial update: %+v", after.Providers[0])
	}
	if after.Pairs[0].Priority != before.Pairs[0].Priority {
		t.Fatal("failed import modified the stored pair")
	}
}

func TestSchemaConstraintsRejectInvalidRegistryRows(t *testing.T) {
	db := registryDB(t)
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO providers (key, display_name, source, enabled, priority, updated_at) VALUES ('openai', 'OpenAI', 'local', 0, 0, 'now');
		INSERT INTO models (id, display_name, source, enabled, priority, updated_at) VALUES ('gpt-4o', 'GPT-4o', 'modelsdev', 1, 0, 'now');
	`); err != nil {
		t.Fatal(err)
	}
	insertPair := func(providerKey string, contextWindow int, maxOutput any) error {
		_, err := db.ExecContext(t.Context(), `
			INSERT INTO provider_models (provider_key, model_id, upstream_model_id, context_window, max_output,
				supports_tools, supports_vision, supports_reasoning, enabled, priority, source, updated_at)
			VALUES (?, 'gpt-4o', 'gpt-4o', ?, ?, 0, 0, 0, 1, 0, 'modelsdev', 'now')
		`, providerKey, contextWindow, maxOutput)
		return err
	}
	if err := insertPair("openai", 1000, nil); err != nil {
		t.Fatalf("baseline pair insert should succeed: %v", err)
	}
	if err := insertPair("unknown-provider", 1000, nil); err == nil {
		t.Fatal("a dangling provider reference must be rejected by the foreign key")
	}
	invalid := map[string]string{
		"zero context window": `INSERT INTO provider_models (provider_key, model_id, upstream_model_id, context_window, enabled, priority, source, updated_at)
			VALUES ('openai', 'gpt-4o-2', 'x', 0, 1, 0, 'modelsdev', 'now')`,
		"max_output above context": `INSERT INTO provider_models (provider_key, model_id, upstream_model_id, context_window, max_output, enabled, priority, source, updated_at)
			VALUES ('openai', 'gpt-4o-2', 'x', 100, 101, 1, 0, 'modelsdev', 'now')`,
		"boolean outside 0/1":  `INSERT INTO providers (key, display_name, enabled, priority, source, updated_at) VALUES ('bad', 'Bad', 2, 0, 'local', 'now')`,
		"provider source enum": `INSERT INTO providers (key, display_name, enabled, priority, source, updated_at) VALUES ('bad', 'Bad', 0, 0, 'bogus', 'now')`,
		"model source enum":    `INSERT INTO models (id, display_name, enabled, priority, source, updated_at) VALUES ('bad', 'Bad', 0, 0, 'bogus', 'now')`,
		"sync state enum": `INSERT INTO registry_sync_state (source, url, fetched_at, allowlist_digest, imported_pairs, skipped_pairs, warnings, updated_at)
			VALUES ('bogus', 'u', 'now', 'd', 0, 0, '[]', 'now')`,
	}
	for name, statement := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := db.ExecContext(t.Context(), statement); err == nil {
				t.Fatalf("expected constraint violation for %s", statement)
			}
		})
	}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM models WHERE id = 'gpt-4o'`); err == nil {
		t.Fatal("deleting a model still referenced by a pair must be rejected")
	}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM providers WHERE key = 'openai'`); err == nil {
		t.Fatal("deleting a provider still referenced by a pair must be rejected")
	}
}

func TestRegistrySurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.db")
	opts := Options{Path: path, BusyTimeout: time.Second}
	db, err := Open(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyRegistryImport(t.Context(), db, models.SourceLocal, Import{
		Providers: []models.Provider{localProvider("openai", true, 4)},
		Models:    []models.Model{logicalModel("gpt-4o")},
		Pairs:     []models.Pair{localPair("openai", "gpt-4o", true, 4)},
	}); err != nil {
		t.Fatal(err)
	}
	before, err := LoadCatalog(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := Migrate(t.Context(), reopened); err != nil {
		t.Fatal(err)
	}
	after, err := LoadCatalog(t.Context(), reopened)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Providers) != 1 || len(after.Models) != 1 || len(after.Pairs) != 1 {
		t.Fatalf("registry did not survive reopen: %+v", after)
	}
	if after.Providers[0].APIKey != before.Providers[0].APIKey || !after.Pairs[0].Enabled {
		t.Fatal("reopened registry lost data")
	}
	if pairs := after.PairsForModel("gpt-4o"); len(pairs) != 1 {
		t.Fatalf("routable pairs after reopen = %d, want 1", len(pairs))
	}
}

func TestLoadCatalogRejectsCorruptRows(t *testing.T) {
	db := registryDB(t)
	// The schema allows arbitrary base_url text on a disabled provider, so a
	// foreign writer can store a value the domain rejects. Loading must fail
	// rather than publish an unusable catalog.
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO providers (key, display_name, base_url, enabled, priority, source, updated_at)
		VALUES ('openai', 'OpenAI', 'not-a-url', 0, 0, 'local', 'now')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCatalog(t.Context(), db); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("expected a base_url validation error, got %v", err)
	}
}

func TestApplyRegistryImportPropagatesCancellation(t *testing.T) {
	db := registryDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ApplyRegistryImport(ctx, db, models.SourceLocal, Import{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if _, err := LoadCatalog(ctx, db); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation from LoadCatalog, got %v", err)
	}
}

func intPointer(value int) *int { return &value }
