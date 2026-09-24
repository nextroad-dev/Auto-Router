package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file is the persistence half of the runtime settings overlay: the single
// row of the settings table and the optimistic concurrency rule that guards it.
//
// The stored JSON is the overlay only, never the effective configuration. The
// effective configuration is derived by merging the overlay onto the file and
// default values, which is the settings package's job; keeping the overlay here
// means "reset to the file" is a single DELETE and can never be confused with
// "write the file's current value as an override".

// SettingsRecord is one row of the settings table.
type SettingsRecord struct {
	// Version is the optimistic concurrency token. Every successful write
	// increments it, so two concurrent writers cannot both succeed.
	Version int64
	// JSON is the stored overlay document.
	JSON string
	// UpdatedAt is when the overlay was last written.
	UpdatedAt time.Time
}

// ErrSettingsConflict means the stored version differs from the one the caller
// based its change on. It is a distinct error rather than a generic failure
// because the client's remedy is specific: re-read, re-apply, retry.
var ErrSettingsConflict = errors.New("the settings overlay changed since it was read")

// LoadSettings reads the overlay. A database with no row reports ok = false,
// which is the documented "no runtime override" state, not an error.
func LoadSettings(ctx context.Context, db *sql.DB) (SettingsRecord, bool, error) {
	var record SettingsRecord
	var updatedAt string
	err := db.QueryRowContext(ctx, `SELECT version, json, updated_at FROM settings WHERE id = 1`).Scan(&record.Version, &record.JSON, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SettingsRecord{}, false, nil
	}
	if err != nil {
		return SettingsRecord{}, false, fmt.Errorf("read settings: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return SettingsRecord{}, false, fmt.Errorf("read settings: invalid updated_at")
	}
	record.UpdatedAt = parsed
	return record, true, nil
}

// WriteSettings stores the overlay at expectedVersion + 1, refusing the write when
// the stored version is not expectedVersion.
//
// The check and the write are one statement, so no window exists between them at
// any isolation level: "UPDATE ... WHERE id = 1 AND version = ?" either matches
// the version the caller read or affects no row. An empty document is stored as
// an empty overlay rather than as a reset; ResetSettings is the only way to
// return to the file's values.
func WriteSettings(ctx context.Context, db *sql.DB, document string, expectedVersion int64) (int64, error) {
	if expectedVersion < 0 {
		return 0, errors.New("the settings version must not be negative")
	}
	if expectedVersion == 0 {
		// The first write has no row to update, so it inserts. A concurrent first
		// write loses the race through the primary key, which is the same conflict
		// the version comparison produces for every later write.
		result, err := db.ExecContext(ctx, `
			INSERT INTO settings (id, version, json, updated_at) VALUES (1, 1, ?, `+nowExpression+`)
			ON CONFLICT(id) DO NOTHING
		`, document)
		if err != nil {
			return 0, wrapWrite("write settings", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("count settings writes: %w", err)
		}
		if affected == 0 {
			return 0, ErrSettingsConflict
		}
		return 1, nil
	}
	result, err := db.ExecContext(ctx, `
		UPDATE settings SET version = version + 1, json = ?, updated_at = `+nowExpression+`
		WHERE id = 1 AND version = ?
	`, document, expectedVersion)
	if err != nil {
		return 0, wrapWrite("write settings", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count settings writes: %w", err)
	}
	if affected == 0 {
		return 0, ErrSettingsConflict
	}
	return expectedVersion + 1, nil
}

// ResetSettings removes the overlay. The next write starts again at version 1,
// which is deliberate: a reset is a new baseline, and reusing version numbers
// after a delete would make a stale client's retry succeed against a different
// document than the one it read.
func ResetSettings(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM settings WHERE id = 1`); err != nil {
		return wrapWrite("reset settings", err)
	}
	return nil
}
