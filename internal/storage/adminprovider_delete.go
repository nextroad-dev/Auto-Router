package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var ErrProviderNotAdminOwned = errors.New("provider is not administrator-owned")

// DeleteAdminProvider removes an administrator-owned provider and its bindings
// atomically. Routing-group entries referencing its pairs are removed while the
// remaining entries retain their order. Historical routing events are snapshots
// and are intentionally retained.
func DeleteAdminProvider(ctx context.Context, db *sql.DB, key string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin provider deletion: %w", err)
	}
	defer tx.Rollback()

	var adminOwned int
	err = tx.QueryRowContext(ctx, `SELECT admin_owned FROM providers WHERE key = ?`, key).Scan(&adminOwned)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProviderNotFound
	}
	if err != nil {
		return fmt.Errorf("read provider ownership: %w", err)
	}
	if adminOwned == 0 {
		return ErrProviderNotAdminOwned
	}

	type member struct {
		group    string
		provider string
		model    string
	}
	rows, err := tx.QueryContext(ctx, `SELECT group_name, provider_key, model_id FROM routing_group_members ORDER BY group_name, position`)
	if err != nil {
		return fmt.Errorf("read routing groups for provider deletion: %w", err)
	}
	members := make([]member, 0)
	for rows.Next() {
		var item member
		if err := rows.Scan(&item.group, &item.provider, &item.model); err != nil {
			rows.Close()
			return fmt.Errorf("scan routing groups for provider deletion: %w", err)
		}
		if item.provider != key {
			members = append(members, item)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read routing groups for provider deletion: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close routing groups for provider deletion: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM routing_group_members`); err != nil {
		return fmt.Errorf("clear routing groups for provider deletion: %w", err)
	}
	for _, item := range members {
		// Re-number each remaining group contiguously after removing the deleted
		// provider's members. The original SELECT order is group_name, position.
		var position int
		for _, prior := range members {
			if prior.group == item.group {
				if prior.provider == item.provider && prior.model == item.model {
					break
				}
				position++
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO routing_group_members(group_name, position, provider_key, model_id) VALUES (?, ?, ?, ?)`, item.group, position, item.provider, item.model); err != nil {
			return fmt.Errorf("restore routing groups after provider deletion: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM provider_models WHERE provider_key = ?`, key); err != nil {
		return fmt.Errorf("delete provider bindings: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM providers WHERE key = ? AND admin_owned = 1`, key)
	if err != nil {
		return fmt.Errorf("delete provider: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count deleted providers: %w", err)
	}
	if deleted != 1 {
		return ErrProviderNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit provider deletion: %w", err)
	}
	return nil
}
