package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

var testMigrations = []migration{
	{Version: 1, Name: "create_example", SQL: "CREATE TABLE example (id INTEGER PRIMARY KEY, value TEXT NOT NULL) STRICT"},
	{Version: 2, Name: "extend_example", SQL: "ALTER TABLE example ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1"},
}

func tableCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?", table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestMigrateAppliesRegistrySchemaIdempotently(t *testing.T) {
	db := testDB(t)
	for i := 0; i < 2; i++ {
		if err := Migrate(t.Context(), db); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"providers", "models", "provider_models", "registry_sync_state"} {
		if tableCount(t, db, table) != 1 {
			t.Fatalf("registry migration did not create %q", table)
		}
	}
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != len(businessMigrations) {
		t.Fatalf("unexpected migration count: %d, err=%v", count, err)
	}
	var name, sum string
	if err := db.QueryRowContext(t.Context(), "SELECT name, checksum FROM schema_migrations WHERE version = 1").Scan(&name, &sum); err != nil {
		t.Fatal(err)
	}
	if name != businessMigrations[0].Name || sum != checksum(businessMigrations[0].SQL) {
		t.Fatal("recorded migration does not match the compiled schema")
	}
	if tableCount(t, db, "schema_migrations") != 1 {
		t.Fatal("migration journal is missing")
	}
}

func TestMigrationsUpgradeAndAreIdempotent(t *testing.T) {
	db := testDB(t)
	if err := applyMigrations(t.Context(), db, testMigrations[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO example (id, value) VALUES (1, 'preserved')"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := applyMigrations(t.Context(), db, testMigrations); err != nil {
			t.Fatal(err)
		}
	}
	var value string
	var enabled int
	if err := db.QueryRowContext(t.Context(), "SELECT value, enabled FROM example WHERE id = 1").Scan(&value, &enabled); err != nil || value != "preserved" || enabled != 1 {
		t.Fatalf("upgrade lost data: value=%q enabled=%d err=%v", value, enabled, err)
	}
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != len(testMigrations) {
		t.Fatalf("unexpected migration count: %d, err=%v", count, err)
	}
	var name, sum, timestamp string
	if err := db.QueryRowContext(t.Context(), "SELECT name, checksum, applied_at FROM schema_migrations WHERE version = 1").Scan(&name, &sum, &timestamp); err != nil {
		t.Fatal(err)
	}
	if name != testMigrations[0].Name || sum != checksum(testMigrations[0].SQL) {
		t.Fatal("migration metadata does not match the applied SQL")
	}
	if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil {
		t.Fatalf("migration timestamp is not UTC RFC3339: %q", timestamp)
	}
}

func TestMigrationFailureRollsBackEntireBatch(t *testing.T) {
	db := testDB(t)
	migrations := []migration{
		{Version: 1, Name: "first", SQL: "CREATE TABLE first_table (id INTEGER)"},
		{Version: 2, Name: "broken", SQL: "CREATE TABLE partial_table (id INTEGER); INVALID SQL"},
	}
	if err := applyMigrations(t.Context(), db, migrations); err == nil {
		t.Fatal("expected SQL failure")
	}
	if tableCount(t, db, "first_table") != 0 || tableCount(t, db, "partial_table") != 0 {
		t.Fatal("failed batch left a partially applied schema")
	}
	// The journal itself is part of the same transaction, so a batch that fails
	// before any migration is recorded leaves no journal at all.
	if tableCount(t, db, "schema_migrations") != 0 {
		t.Fatal("failed batch left a migration journal")
	}
	migrations[1].SQL = "CREATE TABLE partial_table (id INTEGER)"
	if err := applyMigrations(t.Context(), db, migrations); err != nil {
		t.Fatalf("a corrected, unapplied migration should succeed: %v", err)
	}
}

func TestMigrationHistoryMustMatchBinary(t *testing.T) {
	cases := []struct {
		name       string
		migrations []migration
		tamper     string
		wantError  string
	}{
		{name: "newer version", migrations: testMigrations[:1], wantError: "newer"},
		{name: "changed SQL", migrations: []migration{
			{Version: 1, Name: testMigrations[0].Name, SQL: testMigrations[0].SQL + " "},
			testMigrations[1],
		}, wantError: "differs"},
		{name: "renamed migration", migrations: []migration{
			{Version: 1, Name: "renamed", SQL: testMigrations[0].SQL},
			testMigrations[1],
		}, wantError: "differs"},
		{name: "gap in history", migrations: testMigrations, tamper: "DELETE FROM schema_migrations WHERE version = 1", wantError: "consecutive"},
		{name: "corrupt checksum", migrations: testMigrations, tamper: "UPDATE schema_migrations SET checksum = 'changed' WHERE version = 1", wantError: "differs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			if err := applyMigrations(t.Context(), db, testMigrations); err != nil {
				t.Fatal(err)
			}
			if tc.tamper != "" {
				if _, err := db.ExecContext(t.Context(), tc.tamper); err != nil {
					t.Fatal(err)
				}
			}
			err := applyMigrations(t.Context(), db, tc.migrations)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected error containing %q, got %v", tc.wantError, err)
			}
		})
	}
}

func TestMigrationDefinitionsAreValidatedBeforeWriting(t *testing.T) {
	cases := [][]migration{
		{{Version: 0, Name: "zero", SQL: "SELECT 1"}},
		{{Version: 2, Name: "gap", SQL: "SELECT 1"}},
		{{Version: 1, Name: "", SQL: "SELECT 1"}},
		{{Version: 1, Name: "empty SQL", SQL: " "}},
		{testMigrations[0], testMigrations[0]},
	}
	for _, migrations := range cases {
		db := testDB(t)
		if err := applyMigrations(t.Context(), db, migrations); err == nil {
			t.Fatal("invalid migration definition should fail")
		}
		if tableCount(t, db, "schema_migrations") != 0 {
			t.Fatal("invalid definition should not write a migration journal")
		}
	}
}

func TestMigratePropagatesCancellation(t *testing.T) {
	db := testDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Migrate(ctx, db); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}
