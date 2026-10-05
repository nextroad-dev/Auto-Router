package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/models"
)

func TestDeleteModelRemovesAllBindingsAndCompactsGroups(t *testing.T) {
	ctx := context.Background()
	db := newAdminPairDeleteTestDB(t)
	addAdminPairForDeleteTest(t, ctx, db, "keep-a", "model-a")
	addAdminPairForDeleteTest(t, ctx, db, "remove-a", "tenant/model")
	addAdminPairForDeleteTest(t, ctx, db, "keep-c", "model-c")
	if err := CreatePair(ctx, db, NewPair{ProviderKey: "keep-c", ModelID: "tenant/model", UpstreamModelID: "other", ContextWindow: 1024, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	before := ModelGroups{
		Simple:  []ModelGroupMember{{"keep-a", "model-a"}, {"remove-a", "tenant/model"}, {"keep-c", "tenant/model"}, {"keep-c", "model-c"}},
		Medium:  []ModelGroupMember{{"keep-c", "tenant/model"}, {"keep-a", "model-a"}},
		Complex: []ModelGroupMember{{"remove-a", "tenant/model"}},
	}
	if err := ReplaceModelGroups(ctx, db, before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO routing_events
		(request_id, started_at, duration_ms, protocol, requested_model, effective_model, provider_key, status,
		 stream, bytes_written, gateway_attempts, failover_used, usage_status)
		VALUES ('historical', '2026-10-05T00:00:00Z', 1, 'chat_completions', 'tenant/model', 'tenant/model', 'remove-a', 200, 0, 0, 1, 0, 'absent');
		INSERT INTO routing_attempts (request_id, attempt_index, provider_key, model_id, started_at, status, usage_status)
		VALUES ('historical', 1, 'remove-a', 'tenant/model', '2026-10-05T00:00:00Z', 200, 'absent')`); err != nil {
		t.Fatal(err)
	}
	if err := DeleteModel(ctx, db, "tenant/model"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := GetModel(ctx, db, "tenant/model"); err != nil || found {
		t.Fatalf("deleted model found=%t, err=%v", found, err)
	}
	var count int
	for query, want := range map[string]int{
		`SELECT count(*) FROM provider_models WHERE model_id='tenant/model'`:         0,
		`SELECT count(*) FROM providers`:                                             3,
		`SELECT count(*) FROM models`:                                                2,
		`SELECT count(*) FROM deleted_models`:                                        1,
		`SELECT count(*) FROM deleted_provider_models WHERE model_id='tenant/model'`: 2,
		`SELECT count(*) FROM routing_events WHERE effective_model='tenant/model'`:   1,
		`SELECT count(*) FROM routing_attempts WHERE model_id='tenant/model'`:        1,
	} {
		if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil || count != want {
			t.Fatalf("%s: count=%d, err=%v, want=%d", query, count, err, want)
		}
	}
	groups, err := LoadModelGroups(ctx, db)
	want := ModelGroups{Simple: []ModelGroupMember{{"keep-a", "model-a"}, {"keep-c", "model-c"}}, Medium: []ModelGroupMember{{"keep-a", "model-a"}}, Complex: []ModelGroupMember{}}
	if err != nil || !reflect.DeepEqual(groups, want) {
		t.Fatalf("groups=%+v, err=%v, want=%+v", groups, err, want)
	}
	var position int
	if err := db.QueryRowContext(ctx, `SELECT position FROM routing_group_members WHERE group_name='simple' AND model_id='model-c'`).Scan(&position); err != nil || position != 1 {
		t.Fatalf("compacted position=%d, err=%v", position, err)
	}
	attempts, err := NewRoutingLog(db).SelectAttemptsForRequest(ctx, "historical")
	if err != nil || len(attempts) != 1 || attempts[0].Attempt.ModelID != "tenant/model" {
		t.Fatalf("historical attempts=%+v, err=%v", attempts, err)
	}
}

func deletedModelImport() Import {
	return Import{
		Providers: []models.Provider{{Key: "first", DisplayName: "First", BaseURL: "https://first.example/v1", Enabled: true}, {Key: "second", DisplayName: "Second", BaseURL: "https://second.example/v1", Enabled: true}},
		Models:    []models.Model{{ID: "tenant/model", DisplayName: "Model", Enabled: true}},
		Pairs: []models.Pair{
			{ProviderKey: "first", ModelID: "tenant/model", UpstreamModelID: "model", ContextWindow: 1024, Enabled: true},
			{ProviderKey: "second", ModelID: "tenant/model", UpstreamModelID: "model", ContextWindow: 1024, Enabled: true},
		},
	}
}

func TestDeletedModelExclusionSurvivesRestartAndBothImportSources(t *testing.T) {
	for _, source := range []models.Source{models.SourceLocal, models.SourceModelsDev} {
		t.Run(string(source), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "registry.db")
			open := func() *sql.DB {
				t.Helper()
				db, err := Open(ctx, Options{Path: path, BusyTimeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				if err := Migrate(ctx, db); err != nil {
					db.Close()
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				return db
			}
			db := open()
			input := deletedModelImport()
			if _, err := ApplyRegistryImport(ctx, db, source, input); err != nil {
				t.Fatal(err)
			}
			if err := DeleteModel(ctx, db, "tenant/model"); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db = open()
			defer db.Close()
			// Even a newly discovered provider must not resurrect this model.
			input.Providers = append(input.Providers, models.Provider{Key: "new", DisplayName: "New", BaseURL: "https://new.example/v1", Enabled: true})
			input.Pairs = append(input.Pairs, models.Pair{ProviderKey: "new", ModelID: "tenant/model", UpstreamModelID: "model", ContextWindow: 1024, Enabled: true})
			if source == models.SourceModelsDev {
				input.Sync = &SyncState{URL: "https://models.dev/api.json", FetchedAt: time.Now().UTC(), ImportedPairs: 3, SkippedPairs: 2}
			}
			summary, err := ApplyRegistryImport(ctx, db, source, input)
			if err != nil || summary.ModelsExcluded != 1 || summary.PairsExcluded != 3 {
				t.Fatalf("repeat import=%+v, err=%v", summary, err)
			}
			if _, found, err := GetModel(ctx, db, "tenant/model"); err != nil || found {
				t.Fatalf("model resurrected=%t, err=%v", found, err)
			}
			if source == models.SourceModelsDev {
				state, found, err := LoadSyncState(ctx, db, source)
				if err != nil || !found || state.ImportedPairs != 0 || state.SkippedPairs != 5 {
					t.Fatalf("state=%+v, found=%t, err=%v", state, found, err)
				}
			}
			if _, err := SelectProviderModel(ctx, db, "first", "tenant/model", nil); err != nil {
				t.Fatal(err)
			}
			// Rebinding restores only the selected old binding, not the second.
			if _, found, err := GetPair(ctx, db, "first", "tenant/model"); err != nil || !found {
				t.Fatalf("selected binding found=%t, err=%v", found, err)
			}
			if _, err := ApplyRegistryImport(ctx, db, source, input); err != nil {
				t.Fatal(err)
			}
			if _, found, err := GetPair(ctx, db, "second", "tenant/model"); err != nil || found {
				t.Fatalf("unselected old binding resurrected=%t, err=%v", found, err)
			}
		})
	}
}

func TestCreateModelExplicitlyRestoresDeletedModelWithoutBindings(t *testing.T) {
	ctx := context.Background()
	db := newAdminPairDeleteTestDB(t)
	addAdminPairForDeleteTest(t, ctx, db, "provider", "model")
	if err := DeleteModel(ctx, db, "model"); err != nil {
		t.Fatal(err)
	}
	// Failed creation must not clear the exclusion.
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_create BEFORE INSERT ON models BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := CreateModel(ctx, db, NewModel{ID: "model", Enabled: true}); err == nil {
		t.Fatal("expected creation failure")
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM deleted_models`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("exclusions=%d, err=%v", count, err)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER reject_create`); err != nil {
		t.Fatal(err)
	}
	if err := CreateModel(ctx, db, NewModel{ID: "model", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]int{`SELECT count(*) FROM deleted_models`: 0, `SELECT count(*) FROM provider_models`: 0, `SELECT count(*) FROM deleted_provider_models`: 1} {
		if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil || count != want {
			t.Fatalf("%s: count=%d, err=%v", query, count, err)
		}
	}
	if err := CreatePair(ctx, db, NewPair{ProviderKey: "provider", ModelID: "model", UpstreamModelID: "model", ContextWindow: 1024, Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteModelMissingEmptyAndRollback(t *testing.T) {
	ctx := context.Background()
	db := newAdminPairDeleteTestDB(t)
	if err := DeleteModel(ctx, db, "missing"); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("missing error=%v", err)
	}
	if err := CreateModel(ctx, db, NewModel{ID: "empty"}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteModel(ctx, db, "empty"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteModel(ctx, db, "empty"); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("repeat error=%v", err)
	}
	addAdminPairForDeleteTest(t, ctx, db, "provider", "rollback")
	before := ModelGroups{Simple: []ModelGroupMember{{"provider", "rollback"}}}
	if err := ReplaceModelGroups(ctx, db, before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_delete BEFORE DELETE ON models BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := DeleteModel(ctx, db, "rollback"); err == nil {
		t.Fatal("expected delete failure")
	}
	groups, err := LoadModelGroups(ctx, db)
	if err != nil || !reflect.DeepEqual(groups.Simple, before.Simple) {
		t.Fatalf("groups changed on failure: %+v, err=%v", groups, err)
	}
	for _, query := range []string{`SELECT count(*) FROM models WHERE id='rollback'`, `SELECT count(*) FROM provider_models WHERE model_id='rollback'`} {
		var count int
		if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil || count != 1 {
			t.Fatalf("rollback %s: count=%d, err=%v", query, count, err)
		}
	}
	for _, query := range []string{`SELECT count(*) FROM deleted_models WHERE id IN ('missing', 'rollback')`, `SELECT count(*) FROM deleted_provider_models WHERE model_id='rollback'`} {
		var count int
		if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rollback %s: count=%d, err=%v", query, count, err)
		}
	}
}

func TestModelExclusionMigrationUpgradesExistingSchema(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Options{Path: ":memory:", BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := applyMigrations(ctx, db, businessMigrations[:13]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO models(id, display_name, enabled, source, updated_at) VALUES('old', 'Old', 1, 'local', 'now')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, found, err := GetModel(ctx, db, "old"); err != nil || !found {
		t.Fatalf("upgrade lost old model, found=%t, err=%v", found, err)
	}
	if err := DeleteModel(ctx, db, "old"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
}
