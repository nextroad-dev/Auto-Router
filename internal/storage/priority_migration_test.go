package storage

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestRemoveModelAndBindingPrioritiesPreservesRegistryData(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Options{Path: ":memory:", BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Stop just before version 11, the migration under test.
	if err := applyMigrations(ctx, db, businessMigrations[:10]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO providers
		(key, display_name, base_url, api_key, enabled, priority, source, gateway_provider, kind, admin_owned, updated_at)
		VALUES ('provider-a', 'Provider A', 'https://a.example/v1', '', 1, 7, 'local', NULL, 'openai_compatible', 1, 'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO models
		(id, display_name, enabled, priority, source, admin_owned, updated_at)
		VALUES ('model-a', 'Model A', 1, 3, 'local', 1, 'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO provider_models
		(provider_key, model_id, upstream_model_id, context_window, max_output, supports_tools,
		 supports_vision, supports_reasoning, supports_audio_input, enabled, priority, source, admin_owned, updated_at)
		VALUES ('provider-a', 'model-a', 'upstream-a', 8192, 2048, 1, 1, 0, 1, 1, 4, 'local', 1, 'now')`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate(): %v", err)
	}

	provider, found, err := GetProvider(ctx, db, "provider-a")
	if err != nil || !found {
		t.Fatalf("GetProvider() found=%t, error=%v", found, err)
	}
	if provider.Provider.Priority != 7 {
		t.Fatalf("provider priority = %d, want 7", provider.Provider.Priority)
	}
	model, found, err := GetModel(ctx, db, "model-a")
	if err != nil || !found || model.Model.DisplayName != "Model A" || !model.Model.Enabled {
		t.Fatalf("GetModel() = %+v, found=%t, error=%v", model, found, err)
	}
	pair, found, err := GetPair(ctx, db, "provider-a", "model-a")
	if err != nil || !found || pair.Pair.UpstreamModelID != "upstream-a" || pair.Pair.ContextWindow != 8192 || pair.Pair.MaxOutput == nil || *pair.Pair.MaxOutput != 2048 || !pair.Pair.SupportsAudioInput {
		t.Fatalf("GetPair() = %+v, found=%t, error=%v", pair, found, err)
	}

	for _, table := range []string{"models", "provider_models"} {
		if hasColumn(t, ctx, db, table, "priority") {
			t.Errorf("%s still has a priority column after migration", table)
		}
	}
}

func hasColumn(t *testing.T, ctx context.Context, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		t.Fatalf("inspect %s columns: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan %s column: %v", table, err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s columns: %v", table, err)
	}
	return false
}
