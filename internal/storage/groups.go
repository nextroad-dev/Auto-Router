package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ModelGroupMember identifies one enabled provider/model pair. Its position in a
// group is the retry order; no priority or map iteration is involved.
type ModelGroupMember struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type ModelGroups struct {
	Simple  []ModelGroupMember `json:"simple"`
	Medium  []ModelGroupMember `json:"medium"`
	Complex []ModelGroupMember `json:"complex"`
}

var ErrInvalidModelGroups = errors.New("invalid model groups")

func (g ModelGroups) members(name string) []ModelGroupMember {
	switch name {
	case "simple":
		return g.Simple
	case "medium":
		return g.Medium
	default:
		return g.Complex
	}
}

func LoadModelGroups(ctx context.Context, db *sql.DB) (ModelGroups, error) {
	groups := ModelGroups{Simple: []ModelGroupMember{}, Medium: []ModelGroupMember{}, Complex: []ModelGroupMember{}}
	rows, err := db.QueryContext(ctx, `SELECT group_name, provider_key, model_id FROM routing_group_members ORDER BY group_name, position`)
	if err != nil {
		return ModelGroups{}, fmt.Errorf("load model groups: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var item ModelGroupMember
		if err := rows.Scan(&name, &item.Provider, &item.Model); err != nil {
			return ModelGroups{}, fmt.Errorf("scan model group member: %w", err)
		}
		switch name {
		case "simple":
			groups.Simple = append(groups.Simple, item)
		case "medium":
			groups.Medium = append(groups.Medium, item)
		case "complex":
			groups.Complex = append(groups.Complex, item)
		default:
			return ModelGroups{}, fmt.Errorf("stored model group %q: %w", name, ErrInvalidModelGroups)
		}
	}
	if err := rows.Err(); err != nil {
		return ModelGroups{}, fmt.Errorf("load model groups: %w", err)
	}
	return groups, nil
}

// ReplaceModelGroups validates every reference and replaces all three ordered
// lists in one transaction. Empty groups are allowed during initial setup.
func ReplaceModelGroups(ctx context.Context, db *sql.DB, groups ModelGroups) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin model group update: %w", err)
	}
	defer tx.Rollback()
	for _, name := range []string{"simple", "medium", "complex"} {
		members := groups.members(name)
		if len(members) > 8 {
			return fmt.Errorf("%s exceeds eight models: %w", name, ErrInvalidModelGroups)
		}
		seen := map[ModelGroupMember]bool{}
		for _, item := range members {
			if item.Provider == "" || item.Model == "" || seen[item] {
				return fmt.Errorf("%s has an empty or duplicate member: %w", name, ErrInvalidModelGroups)
			}
			seen[item] = true
			var enabled int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM provider_models pm JOIN providers p ON p.key=pm.provider_key JOIN models m ON m.id=pm.model_id WHERE pm.provider_key=? AND pm.model_id=? AND pm.enabled=1 AND p.enabled=1 AND m.enabled=1`, item.Provider, item.Model).Scan(&enabled)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%s references an unavailable model: %w", name, ErrInvalidModelGroups)
			}
			if err != nil {
				return fmt.Errorf("check model group member: %w", err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM routing_group_members`); err != nil {
		return fmt.Errorf("replace model groups: %w", err)
	}
	for _, name := range []string{"simple", "medium", "complex"} {
		for position, item := range groups.members(name) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO routing_group_members(group_name, position, provider_key, model_id) VALUES(?, ?, ?, ?)`, name, position, item.Provider, item.Model); err != nil {
				return fmt.Errorf("save model group member: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit model groups: %w", err)
	}
	return nil
}
