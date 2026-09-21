package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

type migration struct {
	Version int
	Name    string
	SQL     string
}

// businessMigrations are append-only. Never edit or reorder an applied
// migration: the journal records a checksum and startup refuses to continue
// when the SQL differs from history.
var businessMigrations = []migration{
	{
		Version: 1,
		Name:    "create_registry_tables",
		SQL: `
			CREATE TABLE providers (
				key TEXT PRIMARY KEY,
				display_name TEXT NOT NULL,
				base_url TEXT NOT NULL DEFAULT '',
				api_key TEXT NOT NULL DEFAULT '',
				enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
				priority INTEGER NOT NULL,
				source TEXT NOT NULL CHECK (source IN ('modelsdev', 'local')),
				updated_at TEXT NOT NULL
			) STRICT;
			CREATE TABLE models (
				id TEXT PRIMARY KEY,
				display_name TEXT NOT NULL,
				enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
				priority INTEGER NOT NULL,
				source TEXT NOT NULL CHECK (source IN ('modelsdev', 'local')),
				updated_at TEXT NOT NULL
			) STRICT;
			CREATE TABLE provider_models (
				provider_key TEXT NOT NULL REFERENCES providers(key) ON DELETE RESTRICT,
				model_id TEXT NOT NULL REFERENCES models(id) ON DELETE RESTRICT,
				upstream_model_id TEXT NOT NULL,
				context_window INTEGER NOT NULL CHECK (context_window > 0),
				max_output INTEGER CHECK (max_output IS NULL OR (max_output > 0 AND max_output <= context_window)),
				supports_tools INTEGER NOT NULL CHECK (supports_tools IN (0, 1)),
				supports_vision INTEGER NOT NULL CHECK (supports_vision IN (0, 1)),
				supports_reasoning INTEGER NOT NULL CHECK (supports_reasoning IN (0, 1)),
				enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
				priority INTEGER NOT NULL,
				source TEXT NOT NULL CHECK (source IN ('modelsdev', 'local')),
				updated_at TEXT NOT NULL,
				PRIMARY KEY (provider_key, model_id)
			) STRICT;
			CREATE INDEX provider_models_model_id_idx ON provider_models (model_id);
			CREATE TABLE registry_sync_state (
				source TEXT PRIMARY KEY CHECK (source IN ('modelsdev')),
				url TEXT NOT NULL,
				fetched_at TEXT NOT NULL,
				allowlist_digest TEXT NOT NULL,
				imported_pairs INTEGER NOT NULL,
				skipped_pairs INTEGER NOT NULL,
				warnings TEXT NOT NULL,
				updated_at TEXT NOT NULL
			) STRICT;
		`,
	},
}

// Migrate initializes the migration journal and applies this binary's schema.
// History is verified before anything is written, so a database created by a
// newer binary or tampered with is rejected instead of partially upgraded.
func Migrate(ctx context.Context, db *sql.DB) error {
	return applyMigrations(ctx, db, businessMigrations)
}

func applyMigrations(ctx context.Context, db *sql.DB, migrations []migration) error {
	for i, m := range migrations {
		if m.Version != i+1 || strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.SQL) == "" {
			return fmt.Errorf("invalid migration at position %d: versions must be consecutive from 1, with a name and SQL", i+1)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema migration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY CHECK (version > 0),
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		) STRICT
	`); err != nil {
		return fmt.Errorf("initialize migration journal: %w", err)
	}
	applied, err := checkHistory(ctx, tx, migrations)
	if err != nil {
		return err
	}
	for _, m := range migrations[applied:] {
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			return fmt.Errorf("apply migration %d (%s): %w", m.Version, m.Name, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, name, checksum) VALUES (?, ?, ?)",
			m.Version, m.Name, checksum(m.SQL),
		); err != nil {
			return fmt.Errorf("record migration %d: %w", m.Version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema migrations: %w", err)
	}
	return nil
}

func checkHistory(ctx context.Context, tx *sql.Tx, migrations []migration) (int, error) {
	rows, err := tx.QueryContext(ctx, "SELECT version, name, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return 0, fmt.Errorf("read migration journal: %w", err)
	}
	defer rows.Close()
	applied := 0
	for rows.Next() {
		var version int
		var name, sum string
		if err := rows.Scan(&version, &name, &sum); err != nil {
			return 0, fmt.Errorf("read migration entry: %w", err)
		}
		if version != applied+1 {
			return 0, fmt.Errorf("migration history is not consecutive at version %d", version)
		}
		if version > len(migrations) {
			return 0, fmt.Errorf("database schema version %d is newer than this binary supports (%d)", version, len(migrations))
		}
		m := migrations[version-1]
		if name != m.Name || sum != checksum(m.SQL) {
			return 0, fmt.Errorf("migration %d differs from its recorded history", version)
		}
		applied++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read migration journal: %w", err)
	}
	return applied, nil
}

func checksum(query string) string {
	sum := sha256.Sum256([]byte(query))
	return hex.EncodeToString(sum[:])
}
