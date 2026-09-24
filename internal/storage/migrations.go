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

// This v8 baseline is fresh-database-only: startup refuses earlier schema
// versions before Migrate runs. The journal still checks every applied
// checksum, so subsequent schema changes must be append-only.
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
	{
		Version: 2,
		Name:    "add_gateway_provider",
		// Nullable on purpose: NULL means "no explicit mapping, use the provider
		// key", while a non-NULL value is the provider identity inside the
		// execution. Empty-string input is normalized to NULL in Go, never stored.
		SQL: `
			ALTER TABLE providers ADD COLUMN gateway_provider TEXT NULL;
		`,
	},
	{
		Version: 3,
		Name:    "add_audio_input_capability",
		// Audio input is a capability of its own, not a variant of vision, so it
		// needs its own column. Existing rows default to 0, which is exactly the
		// fail-closed reading of "capability unknown": a request that carries
		// audio is never routed to a pair whose audio support was never observed.
		// An administrator fills the column by running the WebUI models.dev sync;
		// only requests that actually carry audio are affected.
		SQL: `
			ALTER TABLE provider_models ADD COLUMN supports_audio_input INTEGER NOT NULL DEFAULT 0 CHECK (supports_audio_input IN (0, 1));
		`,
	},
	{
		Version: 4,
		Name:    "create_routing_log",
		// The routing log is append-only telemetry with its own deadline, so it
		// lives in its own tables rather than beside the registry. Three decisions
		// are baked into the schema:
		//
		//   - every closed set is a CHECK, so a bug cannot store a status this
		//     binary does not know how to read back (with two deliberate exceptions:
		//     the "failure:<reason>" and "not_eligible:<code>" forms are matched by
		//     GLOB because their suffix is a closed set that Go validates);
		//   - every token count is nullable and has no default. NULL means "the
		//     upstream did not report this", which is a different fact from zero,
		//     and the schema refuses to conflate them;
		//   - the indexes are exactly the ones the documented queries use: the
		//     retention sweep walks (started_at, id), request correlation walks
		//     request_id, and the operator-facing provider filter walks provider_key.
		SQL: `
			CREATE TABLE routing_events (
				id INTEGER PRIMARY KEY,
				request_id TEXT NOT NULL,
				started_at TEXT NOT NULL,
				duration_ms INTEGER NOT NULL CHECK (duration_ms >= 0),
				protocol TEXT NOT NULL CHECK (protocol IN ('chat_completions', 'responses', 'native')),
				routing_mode TEXT CHECK (routing_mode IS NULL OR routing_mode IN ('explicit', 'auto')),
				selection_mode TEXT CHECK (selection_mode IS NULL OR selection_mode IN ('explicit', 'jev', 'blend', 'default_model', 'first_eligible')),
				requested_model TEXT,
				provider_key TEXT,
				upstream_model TEXT,
				status INTEGER NOT NULL CHECK (status BETWEEN 100 AND 599),
				upstream_status INTEGER CHECK (upstream_status IS NULL OR upstream_status BETWEEN 100 AND 599),
				error_code TEXT,
				stream INTEGER NOT NULL CHECK (stream IN (0, 1)),
				bytes_written INTEGER NOT NULL CHECK (bytes_written >= 0),
				client_ip TEXT,
				routing_preference TEXT,
				preference_source TEXT,
				jev_status TEXT CHECK (jev_status IS NULL OR jev_status IN ('disabled', 'skipped_single_model', 'skipped_insufficient_evidence', 'skipped_too_many_models', 'ok') OR jev_status GLOB 'failure:*'),
				confidence REAL,
				confidence_band TEXT CHECK (confidence_band IS NULL OR confidence_band IN ('high', 'medium', 'low')),
				fallback_reason TEXT CHECK (fallback_reason IS NULL OR fallback_reason IN ('none', 'not_requested', 'confidence_low', 'selected_model_ineligible', 'jev_timeout', 'jev_unavailable', 'jev_rejected', 'jev_invalid_result', 'jev_canceled', 'truncated_evidence', 'no_candidates') OR fallback_reason GLOB 'not_eligible:*'),
				evidence_hash TEXT,
				gateway_attempts INTEGER NOT NULL CHECK (gateway_attempts >= 0),
				failover_used INTEGER NOT NULL CHECK (failover_used IN (0, 1)),
				routing_latency_ms INTEGER CHECK (routing_latency_ms IS NULL OR routing_latency_ms >= 0),
				jev_latency_ms INTEGER CHECK (jev_latency_ms IS NULL OR jev_latency_ms >= 0),
				input_tokens INTEGER CHECK (input_tokens IS NULL OR input_tokens >= 0),
				output_tokens INTEGER CHECK (output_tokens IS NULL OR output_tokens >= 0),
				total_tokens INTEGER CHECK (total_tokens IS NULL OR total_tokens >= 0),
				usage_status TEXT NOT NULL CHECK (usage_status IN ('observed', 'absent', 'malformed', 'oversized', 'interrupted')),
				usage_source TEXT CHECK (usage_source IS NULL OR usage_source IN ('chat_completions', 'responses', 'stream_chat_completions', 'stream_responses'))
			) STRICT;
			CREATE INDEX routing_events_started_at_idx ON routing_events (started_at, id);
			CREATE INDEX routing_events_request_id_idx ON routing_events (request_id);
			CREATE INDEX routing_events_provider_key_idx ON routing_events (provider_key);
			CREATE TABLE routing_jev_calls (
				id INTEGER PRIMARY KEY,
				request_id TEXT NOT NULL,
				started_at TEXT NOT NULL,
				status TEXT NOT NULL CHECK (status IN ('disabled', 'skipped_single_model', 'skipped_insufficient_evidence', 'skipped_too_many_models', 'ok') OR status GLOB 'failure:*'),
				failure_reason TEXT CHECK (failure_reason IS NULL OR failure_reason IN ('jev_timeout', 'jev_unavailable', 'jev_rejected', 'jev_invalid_result', 'jev_canceled')),
				input_mode TEXT NOT NULL CHECK (input_mode IN ('content', 'redacted', 'features_only')),
				latency_ms INTEGER CHECK (latency_ms IS NULL OR latency_ms >= 0),
				candidate_count INTEGER NOT NULL CHECK (candidate_count >= 0),
				candidates_json TEXT NOT NULL,
				model_count INTEGER NOT NULL CHECK (model_count >= 0),
				selected_model TEXT,
				confidence REAL,
				confidence_band TEXT CHECK (confidence_band IS NULL OR confidence_band IN ('high', 'medium', 'low')),
				fallback_reason TEXT CHECK (fallback_reason IS NULL OR fallback_reason IN ('none', 'not_requested', 'confidence_low', 'selected_model_ineligible', 'jev_timeout', 'jev_unavailable', 'jev_rejected', 'jev_invalid_result', 'jev_canceled', 'truncated_evidence', 'no_candidates') OR fallback_reason GLOB 'not_eligible:*'),
				distribution_json TEXT NOT NULL,
				evidence_hash TEXT
			) STRICT;
			CREATE INDEX routing_jev_calls_started_at_idx ON routing_jev_calls (started_at, id);
			CREATE INDEX routing_jev_calls_request_id_idx ON routing_jev_calls (request_id);
		`,
	},
	{
		Version: 5,
		Name:    "add_admin_ownership_and_settings",
		// Two ownership facts are added here, and neither of them touches a
		// routing-log column or CHECK.
		//
		//   - admin_owned marks a registry row that the Admin API modified. Such a
		//     row is never updated or disabled by -sync-models or by a local
		//     configuration import, so an operator's management change survives a
		//     restart. The column is appended rather than folded into source: source
		//     answers "where did this metadata come from", which stays true after an
		//     administrator edits a synchronized row, and recreating source's CHECK
		//     would force provider_models' ON DELETE RESTRICT foreign key to be
		//     disabled outside the transaction.
		//   - settings is the single-row runtime overlay. The table exists whether or
		//     not the Admin API is mounted, for the same reason the routing log's
		//     tables do: the schema does not move with a runtime switch.
		SQL: `
			ALTER TABLE providers ADD COLUMN admin_owned INTEGER NOT NULL DEFAULT 0 CHECK (admin_owned IN (0, 1));
			ALTER TABLE models ADD COLUMN admin_owned INTEGER NOT NULL DEFAULT 0 CHECK (admin_owned IN (0, 1));
			ALTER TABLE provider_models ADD COLUMN admin_owned INTEGER NOT NULL DEFAULT 0 CHECK (admin_owned IN (0, 1));
			CREATE TABLE settings (
				id INTEGER PRIMARY KEY CHECK (id = 1),
				version INTEGER NOT NULL CHECK (version > 0),
				json TEXT NOT NULL,
				updated_at TEXT NOT NULL
			) STRICT;
		`,
	},
	{
		Version: 6,
		Name:    "add_effective_and_jev_top_model",
		// Two logical model identifiers are added to the routing log so the
		// management surface can report what was actually chosen instead of what was
		// asked for. Neither column is called routing_model on purpose: stage 8
		// decided that routing_events carries no routing_model column because on the
		// automatic path it is identically equal to requested_model, and that name
		// stays reserved for the process log's own field.
		//
		//   - effective_model is the logical model that actually took effect for the
		//     request: the resolved logical model on the explicit path (the selected
		//     pair's model_id for a provider: override) and the decision's model on
		//     the automatic path. It is NULL for every request that failed before a
		//     destination was resolved, because no model took effect and inventing
		//     one would misreport the failure as a destination choice.
		//   - jev_top_model is the top-1 entry of the normalized Jev distribution of
		//     that request, NULL unless jev_status is ok. It records what Jev
		//     recommended, not what the policy selected, which is what makes an
		//     adoption measurement possible at all. It is independent of
		//     routing.log.jev_trace.enabled: the trace switch decides whether the
		//     diagnostic distribution row is stored, not what the call returned.
		//
		// Both columns are nullable and have no default. Rows written before this
		// migration keep NULL for both, which is the honest value: the fact was
		// never recorded, so a window that spans the upgrade reports "no data" for
		// the adoption metrics rather than a fabricated number. No index is added;
		// the window queries walk routing_events_started_at_idx exactly as the
		// stage 9 aggregate query does.
		SQL: `
			ALTER TABLE routing_events ADD COLUMN effective_model TEXT;
			ALTER TABLE routing_events ADD COLUMN jev_top_model TEXT;
		`,
	},
	{
		Version: 7,
		Name:    "add_inbound_credentials",
		SQL: `
			CREATE TABLE inbound_keys (
				id INTEGER PRIMARY KEY,
				name TEXT NOT NULL UNIQUE,
				key_hash TEXT NOT NULL UNIQUE,
				scopes_json TEXT NOT NULL,
				active INTEGER NOT NULL CHECK (active IN (0, 1)),
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			) STRICT;
			CREATE INDEX inbound_keys_active_idx ON inbound_keys (active, name);
		`,
	},
	{
		Version: 8,
		Name:    "single_operator_groups_and_attempts",
		SQL: `
			CREATE TABLE admin_account (
				id INTEGER PRIMARY KEY CHECK (id = 1),
				password_hash TEXT NOT NULL,
				updated_at TEXT NOT NULL
			) STRICT;
			ALTER TABLE providers ADD COLUMN kind TEXT NOT NULL DEFAULT 'openai_compatible'
				CHECK (kind IN ('openai', 'openai_compatible', 'anthropic', 'gemini'));
			CREATE TABLE routing_group_members (
				group_name TEXT NOT NULL CHECK (group_name IN ('simple', 'medium', 'complex')),
				position INTEGER NOT NULL CHECK (position BETWEEN 0 AND 7),
				provider_key TEXT NOT NULL,
				model_id TEXT NOT NULL,
				PRIMARY KEY (group_name, position),
				UNIQUE (group_name, provider_key, model_id),
				FOREIGN KEY (provider_key, model_id) REFERENCES provider_models(provider_key, model_id) ON DELETE RESTRICT
			) STRICT;
			CREATE TABLE routing_attempts (
				id INTEGER PRIMARY KEY,
				request_id TEXT NOT NULL,
				attempt_index INTEGER NOT NULL CHECK (attempt_index > 0),
				group_name TEXT CHECK (group_name IS NULL OR group_name IN ('simple', 'medium', 'complex')),
				provider_key TEXT,
				model_id TEXT,
				started_at TEXT NOT NULL,
				completed_at TEXT,
				status INTEGER CHECK (status IS NULL OR status BETWEEN 100 AND 599),
				error_code TEXT,
				input_tokens INTEGER CHECK (input_tokens IS NULL OR input_tokens >= 0),
				output_tokens INTEGER CHECK (output_tokens IS NULL OR output_tokens >= 0),
				total_tokens INTEGER CHECK (total_tokens IS NULL OR total_tokens >= 0),
				usage_status TEXT NOT NULL CHECK (usage_status IN ('observed', 'absent', 'malformed', 'oversized', 'interrupted')),
				UNIQUE (request_id, attempt_index)
			) STRICT;
			CREATE INDEX routing_attempts_completed_at_idx ON routing_attempts(completed_at, id);
			CREATE INDEX routing_attempts_started_at_idx ON routing_attempts(started_at, id);
			CREATE INDEX routing_attempts_request_id_idx ON routing_attempts(request_id, attempt_index);
			CREATE INDEX routing_attempts_provider_model_idx ON routing_attempts(provider_key, model_id, completed_at);
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
