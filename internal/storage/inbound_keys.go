package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrCredentialNotFound    = errors.New("inbound credential was not found or is inactive")
	ErrCredentialNameExists  = errors.New("inbound credential audit name already exists")
	ErrCredentialNameInvalid = errors.New("inbound credential audit name is invalid")
	ErrCredentialLimit       = errors.New("active inbound credential limit has been reached")
)

// InboundCredential is safe to return to the runtime authentication layer: it
// contains only the digest, audit name and scopes, never the original key.
type InboundCredential struct {
	Name      string
	Hash      string
	Scopes    []string
	Active    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

func RefuseLegacyDatabase(ctx context.Context, db *sql.DB, requiredVersion int) error {
	var present int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&present); err != nil {
		return fmt.Errorf("inspect database schema: %w", err)
	}
	var userTables int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations'`).Scan(&userTables); err != nil {
		return fmt.Errorf("inspect database tables: %w", err)
	}
	if present == 0 {
		if userTables != 0 {
			return errors.New("database contains an unrecognized schema without migration history; stop the service and move the old database aside before first installation")
		}
		return nil
	}
	var version sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("inspect database migration history: %w", err)
	}
	if !version.Valid && userTables != 0 {
		return errors.New("database contains application tables but no migration history; stop the service and move the old database aside before first installation")
	}
	if version.Valid && version.Int64 < int64(requiredVersion) {
		return fmt.Errorf("database schema version %d is obsolete; stop the service and move the old database aside before first installation (required version %d)", version.Int64, requiredVersion)
	}
	return nil
}

func LoadInboundCredentials(ctx context.Context, db *sql.DB) ([]InboundCredential, error) {
	rows, err := db.QueryContext(ctx, `SELECT name,key_hash,scopes_json,active,created_at,updated_at FROM inbound_keys WHERE active=1 ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("read inbound credentials: %w", err)
	}
	defer rows.Close()
	keys := []InboundCredential{}
	for rows.Next() {
		var key InboundCredential
		var scopes string
		var active int
		var created, updated string
		if err := rows.Scan(&key.Name, &key.Hash, &scopes, &active, &created, &updated); err != nil {
			return nil, fmt.Errorf("read inbound credential: %w", err)
		}
		if err := json.Unmarshal([]byte(scopes), &key.Scopes); err != nil {
			return nil, errors.New("stored inbound credential scopes are corrupt")
		}
		key.Active = active == 1
		key.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, errors.New("stored inbound credential timestamp is corrupt")
		}
		key.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, errors.New("stored inbound credential timestamp is corrupt")
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read inbound credentials: %w", err)
	}
	return keys, nil
}

func ListInboundCredentials(ctx context.Context, db *sql.DB) ([]InboundCredential, error) {
	rows, err := db.QueryContext(ctx, `SELECT name,key_hash,scopes_json,active,created_at,updated_at FROM inbound_keys ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list inbound credentials: %w", err)
	}
	defer rows.Close()
	keys := []InboundCredential{}
	for rows.Next() {
		var key InboundCredential
		var scopes string
		var active int
		var created, updated string
		if err := rows.Scan(&key.Name, &key.Hash, &scopes, &active, &created, &updated); err != nil {
			return nil, fmt.Errorf("read inbound credential: %w", err)
		}
		if err := json.Unmarshal([]byte(scopes), &key.Scopes); err != nil {
			return nil, errors.New("stored inbound credential scopes are corrupt")
		}
		if !isInferenceCredential(key.Scopes) {
			continue
		}
		key.Active = active == 1
		key.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, errors.New("stored inbound credential timestamp is corrupt")
		}
		key.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, errors.New("stored inbound credential timestamp is corrupt")
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list inbound credentials: %w", err)
	}
	return keys, nil
}

func CreateInboundCredential(ctx context.Context, db *sql.DB, key InboundCredential) error {
	if err := validateInboundCredential(key.Name, key.Hash, key.Scopes); err != nil {
		return err
	}
	if len(key.Scopes) != 1 || key.Scopes[0] != "inference" {
		return errors.New("inbound credentials must have only the inference scope")
	}
	if err := insertCredential(ctx, db, key); err != nil {
		return err
	}
	return nil
}

func RotateInboundCredential(ctx context.Context, db *sql.DB, name, digest string) error {
	if err := validateHash(digest); err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE inbound_keys SET key_hash=?,updated_at=`+nowExpression+` WHERE name=? AND active=1`, digest, name)
	if err != nil {
		return fmt.Errorf("rotate inbound credential: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

func DisableInboundCredential(ctx context.Context, db *sql.DB, name string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin credential deactivation: %w", err)
	}
	defer tx.Rollback()
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM inbound_keys WHERE name=? AND active=1`, name).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCredentialNotFound
	}
	if err != nil {
		return fmt.Errorf("read credential: %w", err)
	}
	if exists == 0 {
		return ErrCredentialNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE inbound_keys SET active=0,updated_at=`+nowExpression+` WHERE name=? AND active=1`, name); err != nil {
		return fmt.Errorf("deactivate inbound credential: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit credential deactivation: %w", err)
	}
	return nil
}

const maxActiveInboundCredentials = 64

func insertCredential(ctx context.Context, db *sql.DB, key InboundCredential) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin inbound credential write: %w", err)
	}
	defer tx.Rollback()
	var activeKeys int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM inbound_keys WHERE active=1 AND scopes_json='["inference"]'`).Scan(&activeKeys); err != nil {
		return fmt.Errorf("check active inbound credentials: %w", err)
	}
	if activeKeys >= maxActiveInboundCredentials {
		return ErrCredentialLimit
	}
	if err := insertInboundCredential(ctx, tx, key); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit inbound credential write: %w", err)
	}
	return nil
}

func insertInboundCredential(ctx context.Context, tx *sql.Tx, key InboundCredential) error {
	scopes, err := json.Marshal(key.Scopes)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO inbound_keys(name,key_hash,scopes_json,active,created_at,updated_at) VALUES(?,?,?,1,`+nowExpression+`,`+nowExpression+`)`, key.Name, key.Hash, string(scopes))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ErrCredentialNameExists
		}
		return fmt.Errorf("store inbound credential: %w", err)
	}
	return nil
}

func validateCredentialName(name string) error {
	if len(name) == 0 || len(name) > 64 || name != strings.TrimSpace(name) {
		return ErrCredentialNameInvalid
	}
	for i, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || i > 0 && (r == '.' || r == '_' || r == '-')) {
			return ErrCredentialNameInvalid
		}
	}
	return nil
}

func validateInboundCredential(name, hash string, scopes []string) error {
	if err := validateCredentialName(name); err != nil {
		return err
	}
	if len(scopes) == 0 {
		return errors.New("credential must have at least one scope")
	}
	seen := map[string]bool{}
	for _, scope := range scopes {
		if scope != "inference" {
			return errors.New("credential scope is invalid")
		}
		if seen[scope] {
			return errors.New("credential scope is duplicated")
		}
		seen[scope] = true
	}
	return validateHash(hash)
}

func isInferenceCredential(scopes []string) bool {
	return len(scopes) == 1 && scopes[0] == "inference"
}

func validateHash(value string) error {
	if len(value) != 64 {
		return errors.New("credential digest is invalid")
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return errors.New("credential digest is invalid")
		}
	}
	return nil
}
