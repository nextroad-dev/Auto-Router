package storage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/models"
)

func openProviderModelTestDB(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, Options{Path: ":memory:", BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := CreateProvider(ctx, db, NewProvider{Key: "custom", DisplayName: "Custom"}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, ctx
}

func TestSelectProviderModelUsesModelsDevMetadataAndPreservesAdminEdits(t *testing.T) {
	db, ctx := openProviderModelTestDB(t)
	output := 8192
	metadata := &SelectedProviderModelMetadata{
		DisplayName: "Catalog Model", ContextWindow: 128000, MaxOutput: &output,
		SupportsTools: true, SupportsVision: true, SupportsAudioInput: true, SupportsReasoning: true,
	}
	applied, err := SelectProviderModel(ctx, db, "custom", "tenant/model-x", metadata)
	if err != nil || !applied {
		t.Fatalf("SelectProviderModel applied=%t, error=%v; want metadata applied", applied, err)
	}
	view, found, err := GetPair(ctx, db, "custom", "tenant/model-x")
	if err != nil || !found {
		t.Fatalf("GetPair found=%t, error=%v", found, err)
	}
	if view.Pair.Source != models.SourceModelsDev || view.AdminOwned || view.Pair.ContextWindow != 128000 || !view.Pair.SupportsTools || !view.Pair.SupportsVision || !view.Pair.SupportsAudioInput {
		t.Fatalf("pair metadata = %+v admin_owned=%t", view.Pair, view.AdminOwned)
	}
	model, found, err := GetModel(ctx, db, "tenant/model-x")
	if err != nil || !found || model.Model.DisplayName != "Catalog Model" {
		t.Fatalf("model = %+v found=%t error=%v", model.Model, found, err)
	}

	newOutput := 4096
	newOutputPtr := &newOutput
	if _, err := UpdatePair(ctx, db, "custom", "tenant/model-x", PairPatch{ContextWindow: intPtr(64000), MaxOutput: &newOutputPtr}); err != nil {
		t.Fatalf("manually edit pair: %v", err)
	}
	changed := &SelectedProviderModelMetadata{ContextWindow: 200000, SupportsTools: true}
	applied, err = SelectProviderModel(ctx, db, "custom", "tenant/model-x", changed)
	if err != nil || applied {
		t.Fatalf("repeat selection applied=%t, error=%v; admin edit should be preserved", applied, err)
	}
	view, _, err = GetPair(ctx, db, "custom", "tenant/model-x")
	if err != nil || view.Pair.ContextWindow != 64000 || view.Pair.Source != models.SourceModelsDev || !view.AdminOwned {
		t.Fatalf("administrator-edited metadata was overwritten: pair=%+v admin_owned=%t error=%v", view.Pair, view.AdminOwned, err)
	}
}

func TestSelectProviderModelUpgradesLegacyDefaultMetadata(t *testing.T) {
	db, ctx := openProviderModelTestDB(t)
	applied, err := SelectProviderModel(ctx, db, "custom", "model-y", nil)
	if err != nil || applied {
		t.Fatalf("initial default selection applied=%t, error=%v", applied, err)
	}
	metadata := &SelectedProviderModelMetadata{ContextWindow: 100000, SupportsTools: true}
	applied, err = SelectProviderModel(ctx, db, "custom", "model-y", metadata)
	if err != nil || !applied {
		t.Fatalf("metadata refresh applied=%t, error=%v; want generated defaults upgraded", applied, err)
	}
	view, found, err := GetPair(ctx, db, "custom", "model-y")
	if err != nil || !found || view.Pair.Source != models.SourceModelsDev || view.AdminOwned || view.Pair.ContextWindow != 100000 || !view.Pair.SupportsTools {
		t.Fatalf("refreshed pair = %+v admin_owned=%t found=%t error=%v", view.Pair, view.AdminOwned, found, err)
	}
}

func intPtr(value int) *int { return &value }
