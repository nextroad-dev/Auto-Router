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

// Migrate initializes the migration journal and applies this binary's schema.
// Stage 1 has no business tables; subsequent stages add append-only migrations
// here without changing callers or pretending a registry already exists.
func Migrate(ctx context.Context, db *sql.DB) error {
	return applyMigrations(ctx, db, nil)
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
