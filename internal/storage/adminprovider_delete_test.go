package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDeleteAdminProviderRemovesBindingsAndCompactsGroups(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Options{Path: ":memory:", BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"remove", "keep-a", "keep-b"} {
		if err := CreateProvider(ctx, db, NewProvider{Key: key, DisplayName: key, Priority: 0}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"model-a", "model-b", "model-c"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO models(id, display_name, enabled, priority, source, updated_at, admin_owned) VALUES(?, ?, 1, 0, 'local', 'now', 1)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range []struct{ provider, model string }{
		{"keep-a", "model-a"}, {"remove", "model-b"}, {"keep-b", "model-c"},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO provider_models(provider_key, model_id, upstream_model_id, context_window, supports_tools, supports_vision, supports_reasoning, enabled, priority, source, updated_at, admin_owned) VALUES(?, ?, ?, 1000, 0, 0, 0, 1, 0, 'local', 'now', 1)`, pair.provider, pair.model, pair.model); err != nil {
			t.Fatal(err)
		}
	}
	for _, member := range []struct {
		group    string
		position int
		provider string
		model    string
	}{
		{"simple", 0, "keep-a", "model-a"}, {"simple", 1, "remove", "model-b"}, {"simple", 2, "keep-b", "model-c"},
		{"medium", 0, "remove", "model-b"},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO routing_group_members(group_name, position, provider_key, model_id) VALUES(?, ?, ?, ?)`, member.group, member.position, member.provider, member.model); err != nil {
			t.Fatal(err)
		}
	}

	if err := DeleteAdminProvider(ctx, db, "remove"); err != nil {
		t.Fatalf("DeleteAdminProvider() error = %v", err)
	}
	if _, found, err := GetProvider(ctx, db, "remove"); err != nil || found {
		t.Fatalf("deleted provider found=%t, error=%v", found, err)
	}
	groups, err := LoadModelGroups(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	want := []ModelGroupMember{{Provider: "keep-a", Model: "model-a"}, {Provider: "keep-b", Model: "model-c"}}
	if len(groups.Simple) != len(want) || groups.Simple[0] != want[0] || groups.Simple[1] != want[1] {
		t.Fatalf("simple group = %#v, want %#v", groups.Simple, want)
	}
	if len(groups.Medium) != 0 {
		t.Fatalf("medium group = %#v, want empty", groups.Medium)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM provider_models WHERE provider_key = 'remove'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("removed provider bindings = %d, error = %v", count, err)
	}
}

func TestDeleteAdminProviderRejectsMissingAndUnownedRows(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Options{Path: ":memory:", BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := CreateProvider(ctx, db, NewProvider{Key: "admin", DisplayName: "admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO providers(key, display_name, base_url, api_key, enabled, priority, source, updated_at, admin_owned) VALUES('config', 'config', '', '', 0, 0, 'local', 'now', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := DeleteAdminProvider(ctx, db, "missing"); !errors.Is(err, ErrProviderNotFound) {
		t.Fatalf("missing provider error = %v, want ErrProviderNotFound", err)
	}
	if err := DeleteAdminProvider(ctx, db, "config"); !errors.Is(err, ErrProviderNotAdminOwned) {
		t.Fatalf("unowned provider error = %v, want ErrProviderNotAdminOwned", err)
	}
}
