package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var ErrModelNotFound = errors.New("logical model not found")

// DeleteModel removes a logical model, all its bindings and group references in
// one transaction. Exclusions survive imports/restarts; historical log snapshots
// deliberately outlive the registry rows they describe.
func DeleteModel(ctx context.Context, db *sql.DB, id string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin model deletion: %w", err)
	}
	defer tx.Rollback()

	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM models WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrModelNotFound
	}
	if err != nil {
		return fmt.Errorf("read model before deletion: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO deleted_models (id, created_at) VALUES (?, `+nowExpression+`)
		ON CONFLICT(id) DO NOTHING
	`, id); err != nil {
		return fmt.Errorf("record deleted model exclusion: %w", err)
	}
	// Keep existing bindings excluded even after an explicit model recreation.
	// Selecting a provider clears only that provider/model exclusion.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO deleted_provider_models (provider_key, model_id, created_at)
		SELECT provider_key, model_id, `+nowExpression+` FROM provider_models WHERE model_id = ?
		ON CONFLICT(provider_key, model_id) DO NOTHING
	`, id); err != nil {
		return fmt.Errorf("record model binding exclusions: %w", err)
	}
	if err := removeRoutingGroupMembers(ctx, tx, func(member pairGroupMember) bool { return member.model == id }); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM provider_models WHERE model_id = ?`, id); err != nil {
		return fmt.Errorf("delete model bindings: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM models WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete model: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit model deletion: %w", err)
	}
	return nil
}

func removeModelExclusion(ctx context.Context, tx *sql.Tx, id string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM deleted_models WHERE id = ?`, id); err != nil {
		return fmt.Errorf("clear deleted model exclusion: %w", err)
	}
	return nil
}

func readModelExclusions(ctx context.Context, tx *sql.Tx) (map[string]struct{}, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM deleted_models`)
	if err != nil {
		return nil, fmt.Errorf("read deleted model exclusions: %w", err)
	}
	defer rows.Close()
	excluded := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan deleted model exclusion: %w", err)
		}
		excluded[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read deleted model exclusions: %w", err)
	}
	return excluded, nil
}
