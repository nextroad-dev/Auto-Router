package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var ErrPairNotFound = errors.New("provider/model binding not found")

type pairGroupMember struct {
	group    string
	provider string
	model    string
}

// DeletePair permanently removes one binding, prevents registry imports from
// recreating it, and removes its routing-group references atomically. Historical
// routing attempts are snapshots and are intentionally retained.
func DeletePair(ctx context.Context, db *sql.DB, providerKey, modelID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin pair deletion: %w", err)
	}
	defer tx.Rollback()

	var one int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM provider_models WHERE provider_key = ? AND model_id = ?`, providerKey, modelID,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPairNotFound
	}
	if err != nil {
		return fmt.Errorf("read binding before deletion: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO deleted_provider_models (provider_key, model_id, created_at)
		VALUES (?, ?, `+nowExpression+`)
		ON CONFLICT(provider_key, model_id) DO NOTHING
	`, providerKey, modelID); err != nil {
		return fmt.Errorf("record deleted binding exclusion: %w", err)
	}
	if err := removePairFromRoutingGroups(ctx, tx, providerKey, modelID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx,
		`DELETE FROM provider_models WHERE provider_key = ? AND model_id = ?`, providerKey, modelID,
	)
	if err != nil {
		return fmt.Errorf("delete binding: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count deleted bindings: %w", err)
	}
	if deleted != 1 {
		return ErrPairNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit binding deletion: %w", err)
	}
	return nil
}

func removePairFromRoutingGroups(ctx context.Context, tx *sql.Tx, providerKey, modelID string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT group_name, provider_key, model_id
		FROM routing_group_members
		ORDER BY group_name, position
	`)
	if err != nil {
		return fmt.Errorf("read routing groups for binding deletion: %w", err)
	}
	members := make([]pairGroupMember, 0)
	for rows.Next() {
		var member pairGroupMember
		if err := rows.Scan(&member.group, &member.provider, &member.model); err != nil {
			rows.Close()
			return fmt.Errorf("scan routing groups for binding deletion: %w", err)
		}
		if member.provider != providerKey || member.model != modelID {
			members = append(members, member)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read routing groups for binding deletion: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close routing groups for binding deletion: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM routing_group_members`); err != nil {
		return fmt.Errorf("clear routing groups for binding deletion: %w", err)
	}
	positions := make(map[string]int)
	for _, member := range members {
		position := positions[member.group]
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO routing_group_members(group_name, position, provider_key, model_id)
			VALUES (?, ?, ?, ?)
		`, member.group, position, member.provider, member.model); err != nil {
			return fmt.Errorf("restore routing groups after binding deletion: %w", err)
		}
		positions[member.group] = position + 1
	}
	return nil
}

func removePairExclusion(ctx context.Context, tx *sql.Tx, providerKey, modelID string) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM deleted_provider_models WHERE provider_key = ? AND model_id = ?`, providerKey, modelID,
	); err != nil {
		return fmt.Errorf("clear deleted binding exclusion: %w", err)
	}
	return nil
}

func readPairExclusions(ctx context.Context, tx *sql.Tx) (map[pairKey]struct{}, error) {
	rows, err := tx.QueryContext(ctx, `SELECT provider_key, model_id FROM deleted_provider_models`)
	if err != nil {
		return nil, fmt.Errorf("read deleted binding exclusions: %w", err)
	}
	defer rows.Close()
	excluded := make(map[pairKey]struct{})
	for rows.Next() {
		var key pairKey
		if err := rows.Scan(&key.providerKey, &key.modelID); err != nil {
			return nil, fmt.Errorf("scan deleted binding exclusion: %w", err)
		}
		excluded[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read deleted binding exclusions: %w", err)
	}
	return excluded, nil
}
