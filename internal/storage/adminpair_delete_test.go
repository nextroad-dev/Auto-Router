package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/models"
)

func newAdminPairDeleteTestDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, Options{Path: ":memory:", BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return db
}

func addAdminPairForDeleteTest(t *testing.T, ctx context.Context, db *sql.DB, provider, model string) {
	t.Helper()
	if err := CreateProvider(ctx, db, NewProvider{Key: provider, DisplayName: provider, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO models(id, display_name, enabled, source, updated_at, admin_owned)
		VALUES(?, ?, 1, 'local', 'now', 1)
	`, model, model); err != nil {
		t.Fatal(err)
	}
	if err := CreatePair(ctx, db, NewPair{
		ProviderKey: provider, ModelID: model, UpstreamModelID: model, ContextWindow: 32768, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeletePairRemovesGroupReferencesAndCompactsPositions(t *testing.T) {
	ctx := context.Background()
	db := newAdminPairDeleteTestDB(t)
	for _, pair := range [][2]string{{"keep-a", "model-a"}, {"remove", "model-b"}, {"keep-c", "model-c"}} {
		addAdminPairForDeleteTest(t, ctx, db, pair[0], pair[1])
	}
	if err := ReplaceModelGroups(ctx, db, ModelGroups{
		Simple: []ModelGroupMember{{Provider: "keep-a", Model: "model-a"}, {Provider: "remove", Model: "model-b"}, {Provider: "keep-c", Model: "model-c"}},
		Medium: []ModelGroupMember{{Provider: "remove", Model: "model-b"}},
	}); err != nil {
		t.Fatal(err)
	}

	if err := DeletePair(ctx, db, "remove", "model-b"); err != nil {
		t.Fatalf("DeletePair() error = %v", err)
	}
	if _, found, err := GetPair(ctx, db, "remove", "model-b"); err != nil || found {
		t.Fatalf("deleted pair found=%t, error=%v", found, err)
	}
	groups, err := LoadModelGroups(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	want := []ModelGroupMember{{Provider: "keep-a", Model: "model-a"}, {Provider: "keep-c", Model: "model-c"}}
	if len(groups.Simple) != len(want) || groups.Simple[0] != want[0] || groups.Simple[1] != want[1] {
		t.Fatalf("simple group = %#v, want %#v", groups.Simple, want)
	}
	if len(groups.Medium) != 0 {
		t.Fatalf("medium group = %#v, want empty", groups.Medium)
	}
	var exclusions int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM deleted_provider_models WHERE provider_key = 'remove' AND model_id = 'model-b'`).Scan(&exclusions); err != nil || exclusions != 1 {
		t.Fatalf("deletion exclusions = %d, error = %v; want one", exclusions, err)
	}
	if err := DeletePair(ctx, db, "remove", "model-b"); !errors.Is(err, ErrPairNotFound) {
		t.Fatalf("repeated DeletePair() error = %v, want ErrPairNotFound", err)
	}
}

func TestDeletedPairStaysExcludedFromImportsUntilExplicitlySelected(t *testing.T) {
	ctx := context.Background()
	db := newAdminPairDeleteTestDB(t)
	input := Import{
		Providers: []models.Provider{{Key: "sync", DisplayName: "Sync"}},
		Models:    []models.Model{{ID: "vendor/model", DisplayName: "Model"}},
		Pairs: []models.Pair{{
			ProviderKey: "sync", ModelID: "vendor/model", UpstreamModelID: "vendor/model",
			ContextWindow: 128000, Enabled: true,
		}},
		Sync: &SyncState{URL: "https://models.dev/api.json", FetchedAt: time.Now().UTC(), ImportedPairs: 1, SkippedPairs: 2},
	}
	if _, err := ApplyRegistryImport(ctx, db, models.SourceModelsDev, input); err != nil {
		t.Fatalf("initial import: %v", err)
	}
	if err := DeletePair(ctx, db, "sync", "vendor/model"); err != nil {
		t.Fatalf("DeletePair(): %v", err)
	}

	summary, err := ApplyRegistryImport(ctx, db, models.SourceModelsDev, input)
	if err != nil {
		t.Fatalf("repeat import: %v", err)
	}
	if summary.PairsExcluded != 1 {
		t.Fatalf("excluded pairs = %d, want 1", summary.PairsExcluded)
	}
	state, found, err := LoadSyncState(ctx, db, models.SourceModelsDev)
	if err != nil || !found || state.ImportedPairs != 0 || state.SkippedPairs != 3 {
		t.Fatalf("sync state = %+v, found=%t, error=%v; want imported=0 skipped=3", state, found, err)
	}
	if _, found, err := GetPair(ctx, db, "sync", "vendor/model"); err != nil || found {
		t.Fatalf("excluded pair found=%t, error=%v after import", found, err)
	}

	if _, err := SelectProviderModel(ctx, db, "sync", "vendor/model", nil); err != nil {
		t.Fatalf("explicit rebind: %v", err)
	}
	if _, err := ApplyRegistryImport(ctx, db, models.SourceModelsDev, input); err != nil {
		t.Fatalf("import after explicit rebind: %v", err)
	}
	if _, found, err := GetPair(ctx, db, "sync", "vendor/model"); err != nil || !found {
		t.Fatalf("explicitly rebound pair found=%t, error=%v", found, err)
	}
}

func TestDeletePairMissingDoesNotCreateExclusion(t *testing.T) {
	ctx := context.Background()
	db := newAdminPairDeleteTestDB(t)
	if err := DeletePair(ctx, db, "missing", "missing-model"); !errors.Is(err, ErrPairNotFound) {
		t.Fatalf("DeletePair() error = %v, want ErrPairNotFound", err)
	}
	var exclusions int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM deleted_provider_models`).Scan(&exclusions); err != nil || exclusions != 0 {
		t.Fatalf("deletion exclusions = %d, error = %v; want zero", exclusions, err)
	}
}
