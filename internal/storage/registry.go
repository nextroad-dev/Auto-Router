package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/models"
)

// SyncState records the outcome of one models.dev synchronization. It is
// metadata for operators; the catalog itself never depends on it.
type SyncState struct {
	URL             string
	FetchedAt       time.Time
	AllowlistDigest string
	ImportedPairs   int
	SkippedPairs    int
	Warnings        []string
}

// Import is one batch of registry entries from a single source.
//
// A models.dev import refreshes metadata only: it never rewrites local
// overrides (base_url, api_key, enabled, priority) and never touches rows owned
// by the local source. A local import is authoritative for its entries and
// disables local entries that disappeared from configuration.
type Import struct {
	Providers []models.Provider
	Models    []models.Model
	Pairs     []models.Pair
	// Sync is optional models.dev sync metadata, written when non-nil.
	Sync *SyncState
}

// ImportSummary counts what an import added, refreshed and disabled. It exists
// so operators and tests can see that a repeated import changes nothing.
type ImportSummary struct {
	ProvidersAdded    int
	ProvidersUpdated  int
	ProvidersDisabled int
	ModelsAdded       int
	ModelsUpdated     int
	ModelsDisabled    int
	PairsAdded        int
	PairsUpdated      int
	PairsDisabled     int
	// The four *AdminOwned counts report rows the import left alone because the
	// Admin API owns them. They are counted separately rather than folded into
	// "updated = 0" so an operator reading the startup log can tell "nothing
	// matched" from "everything matched but the administrator owns it".
	ProvidersAdminOwned int
	ModelsAdminOwned    int
	PairsAdminOwned     int
}

// rowState is what an import needs to know about one existing row: who owns its
// metadata, whether it is enabled, and whether the Admin API modified it. An
// admin-owned row is never rewritten and never disabled by an import, which is
// what makes a management change survive a restart.
type rowState struct {
	source     models.Source
	enabled    bool
	adminOwned bool
}

type pairKey struct {
	providerKey string
	modelID     string
}

// ApplyRegistryImport writes one source's registry entries in a single
// transaction. Validation and every cross-reference check happen before the
// first write, so a rejected batch leaves the database untouched. Removed pairs
// (models.dev) and removed local entries are disabled, never deleted, so
// historical references and credentials do not silently vanish.
func ApplyRegistryImport(ctx context.Context, db *sql.DB, source models.Source, in Import) (ImportSummary, error) {
	var summary ImportSummary
	if !source.Valid() {
		return summary, fmt.Errorf("registry import source %q is not supported", source)
	}
	if source == models.SourceLocal && in.Sync != nil {
		return summary, errors.New("local registry imports must not carry models.dev sync state")
	}
	providers := dedupeProviders(in.Providers, source)
	catalogModels := dedupeModels(in.Models, source)
	pairs := dedupePairs(in.Pairs, source)
	for _, provider := range providers {
		if err := models.ValidateProvider(provider); err != nil {
			return summary, fmt.Errorf("provider %q: %w", provider.Key, err)
		}
	}
	for _, model := range catalogModels {
		if err := models.ValidateModel(model); err != nil {
			return summary, fmt.Errorf("model %q: %w", model.ID, err)
		}
	}
	for _, pair := range pairs {
		if err := models.ValidatePair(pair); err != nil {
			return summary, fmt.Errorf("pair %s/%s: %w", pair.ProviderKey, pair.ModelID, err)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return summary, fmt.Errorf("begin registry import: %w", err)
	}
	defer tx.Rollback()

	existingProviders, err := readProviderStates(ctx, tx)
	if err != nil {
		return summary, err
	}
	existingModels, err := readModelStates(ctx, tx)
	if err != nil {
		return summary, err
	}
	existingPairs, err := readPairStates(ctx, tx)
	if err != nil {
		return summary, err
	}

	knownProviders := make(map[string]struct{}, len(existingProviders)+len(providers))
	for key := range existingProviders {
		knownProviders[key] = struct{}{}
	}
	importedProviders := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		importedProviders[provider.Key] = struct{}{}
		knownProviders[provider.Key] = struct{}{}
	}
	knownModels := make(map[string]struct{}, len(existingModels)+len(catalogModels))
	for id := range existingModels {
		knownModels[id] = struct{}{}
	}
	importedModels := make(map[string]struct{}, len(catalogModels))
	for _, model := range catalogModels {
		importedModels[model.ID] = struct{}{}
		knownModels[model.ID] = struct{}{}
	}
	importedPairs := make(map[pairKey]struct{}, len(pairs))
	for _, pair := range pairs {
		if _, ok := knownProviders[pair.ProviderKey]; !ok {
			return summary, fmt.Errorf("pair %s/%s references a provider that is neither stored nor part of this import", pair.ProviderKey, pair.ModelID)
		}
		if _, ok := knownModels[pair.ModelID]; !ok {
			return summary, fmt.Errorf("pair %s/%s references a model that is neither stored nor part of this import", pair.ProviderKey, pair.ModelID)
		}
		importedPairs[pairKey{pair.ProviderKey, pair.ModelID}] = struct{}{}
	}

	for _, provider := range providers {
		existing, ok := existingProviders[provider.Key]
		switch {
		case !ok:
			if err := insertProvider(ctx, tx, source, provider); err != nil {
				return summary, err
			}
			summary.ProvidersAdded++
		case existing.adminOwned:
			summary.ProvidersAdminOwned++
		case source == models.SourceModelsDev && existing.source == models.SourceLocal:
			// Local overrides win; a sync must not rewrite them.
		default:
			if err := updateProvider(ctx, tx, source, provider); err != nil {
				return summary, err
			}
			summary.ProvidersUpdated++
		}
	}
	for _, model := range catalogModels {
		existing, ok := existingModels[model.ID]
		switch {
		case !ok:
			if err := insertModel(ctx, tx, source, model); err != nil {
				return summary, err
			}
			summary.ModelsAdded++
		case existing.adminOwned:
			summary.ModelsAdminOwned++
		case source == models.SourceModelsDev && existing.source == models.SourceLocal:
		default:
			if err := updateModel(ctx, tx, source, model); err != nil {
				return summary, err
			}
			summary.ModelsUpdated++
		}
	}
	for _, pair := range pairs {
		existing, ok := existingPairs[pairKey{pair.ProviderKey, pair.ModelID}]
		switch {
		case !ok:
			if err := insertPair(ctx, tx, source, pair); err != nil {
				return summary, err
			}
			summary.PairsAdded++
		case existing.adminOwned:
			summary.PairsAdminOwned++
		case source == models.SourceModelsDev && existing.source == models.SourceLocal:
		default:
			if err := updatePair(ctx, tx, source, pair); err != nil {
				return summary, err
			}
			summary.PairsUpdated++
		}
	}

	// Entries that disappeared from this source are disabled, never deleted.
	for key, existing := range existingProviders {
		if existing.source != source || !existing.enabled {
			continue
		}
		if _, imported := importedProviders[key]; imported {
			continue
		}
		if existing.adminOwned {
			// The row would have been disabled because it disappeared from this
			// source, but the administrator owns it now. Skipping it is the
			// documented behavior, and counting it is what tells an operator why
			// their configuration file no longer has an effect.
			summary.ProvidersAdminOwned++
			continue
		}
		if err := disableProvider(ctx, tx, key); err != nil {
			return summary, err
		}
		summary.ProvidersDisabled++
	}
	for id, existing := range existingModels {
		if existing.source != source || !existing.enabled {
			continue
		}
		if _, imported := importedModels[id]; imported {
			continue
		}
		if existing.adminOwned {
			summary.ModelsAdminOwned++
			continue
		}
		if err := disableModel(ctx, tx, id); err != nil {
			return summary, err
		}
		summary.ModelsDisabled++
	}
	for key, existing := range existingPairs {
		if existing.source != source || !existing.enabled {
			continue
		}
		if _, imported := importedPairs[key]; imported {
			continue
		}
		if existing.adminOwned {
			summary.PairsAdminOwned++
			continue
		}
		if err := disablePair(ctx, tx, key); err != nil {
			return summary, err
		}
		summary.PairsDisabled++
	}

	if in.Sync != nil {
		if err := writeSyncState(ctx, tx, in.Sync); err != nil {
			return summary, err
		}
	}
	if err := tx.Commit(); err != nil {
		return summary, fmt.Errorf("commit registry import: %w", err)
	}
	return summary, nil
}

const nowExpression = `strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`

func insertProvider(ctx context.Context, tx *sql.Tx, source models.Source, provider models.Provider) error {
	if source == models.SourceModelsDev {
		// Synchronized providers start disabled and without a base URL: without
		// local credentials there is nothing safe to forward to. The historical
		// gateway_provider column is a local compatibility field, so it is always NULL here.
		_, err := tx.ExecContext(ctx, `
			INSERT INTO providers (key, display_name, base_url, api_key, enabled, priority, source, gateway_provider, kind, updated_at)
			VALUES (?, ?, '', '', 0, 0, 'modelsdev', NULL, 'openai_compatible', `+nowExpression+`)
		`, provider.Key, provider.DisplayName)
		return wrapWrite("insert provider", err)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO providers (key, display_name, base_url, api_key, enabled, priority, source, gateway_provider, kind, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'local', ?, ?, `+nowExpression+`)
	`, provider.Key, provider.DisplayName, provider.BaseURL, provider.APIKey, boolToInt(provider.Enabled), provider.Priority, nullableGatewayProvider(provider.GatewayProvider), providerKindOrDefault(provider.Kind))
	return wrapWrite("insert provider", err)
}

func updateProvider(ctx context.Context, tx *sql.Tx, source models.Source, provider models.Provider) error {
	if source == models.SourceModelsDev {
		// The historical gateway mapping is a deployment attribute owned by local
		// configuration, so a sync never rewrites it; direct execution ignores it.
		_, err := tx.ExecContext(ctx, `
			UPDATE providers SET display_name = ?, updated_at = `+nowExpression+` WHERE key = ?
		`, provider.DisplayName, provider.Key)
		return wrapWrite("update provider", err)
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE providers
		SET display_name = ?, base_url = ?, api_key = ?, enabled = ?, priority = ?, source = 'local', gateway_provider = ?, kind = ?, updated_at = `+nowExpression+`
		WHERE key = ?
	`, provider.DisplayName, provider.BaseURL, provider.APIKey, boolToInt(provider.Enabled), provider.Priority, nullableGatewayProvider(provider.GatewayProvider), providerKindOrDefault(provider.Kind), provider.Key)
	return wrapWrite("update provider", err)
}

func disableProvider(ctx context.Context, tx *sql.Tx, key string) error {
	_, err := tx.ExecContext(ctx, `UPDATE providers SET enabled = 0, updated_at = `+nowExpression+` WHERE key = ?`, key)
	return wrapWrite("disable provider", err)
}

func insertModel(ctx context.Context, tx *sql.Tx, source models.Source, model models.Model) error {
	if source == models.SourceModelsDev {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO models (id, display_name, enabled, priority, source, updated_at)
			VALUES (?, ?, 1, 0, 'modelsdev', `+nowExpression+`)
		`, model.ID, model.DisplayName)
		return wrapWrite("insert model", err)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO models (id, display_name, enabled, priority, source, updated_at)
		VALUES (?, ?, ?, ?, 'local', `+nowExpression+`)
	`, model.ID, model.DisplayName, boolToInt(model.Enabled), model.Priority)
	return wrapWrite("insert model", err)
}

func updateModel(ctx context.Context, tx *sql.Tx, source models.Source, model models.Model) error {
	if source == models.SourceModelsDev {
		_, err := tx.ExecContext(ctx, `UPDATE models SET display_name = ?, updated_at = `+nowExpression+` WHERE id = ?`, model.DisplayName, model.ID)
		return wrapWrite("update model", err)
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE models SET display_name = ?, enabled = ?, priority = ?, source = 'local', updated_at = `+nowExpression+`
		WHERE id = ?
	`, model.DisplayName, boolToInt(model.Enabled), model.Priority, model.ID)
	return wrapWrite("update model", err)
}

func disableModel(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `UPDATE models SET enabled = 0, updated_at = `+nowExpression+` WHERE id = ?`, id)
	return wrapWrite("disable model", err)
}

func insertPair(ctx context.Context, tx *sql.Tx, source models.Source, pair models.Pair) error {
	if source == models.SourceModelsDev {
		// A synchronized pair is enabled so that configuring the provider (base
		// URL, key, enabled) is enough to make it routable; while the provider
		// itself is disabled the pair cannot be selected.
		_, err := tx.ExecContext(ctx, `
			INSERT INTO provider_models (provider_key, model_id, upstream_model_id, context_window, max_output,
				supports_tools, supports_vision, supports_audio_input, supports_reasoning, enabled, priority, source, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 0, 'modelsdev', `+nowExpression+`)
		`, pair.ProviderKey, pair.ModelID, pair.UpstreamModelID, pair.ContextWindow, nullableInt(pair.MaxOutput),
			boolToInt(pair.SupportsTools), boolToInt(pair.SupportsVision), boolToInt(pair.SupportsAudioInput), boolToInt(pair.SupportsReasoning))
		return wrapWrite("insert pair", err)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO provider_models (provider_key, model_id, upstream_model_id, context_window, max_output,
			supports_tools, supports_vision, supports_audio_input, supports_reasoning, enabled, priority, source, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'local', `+nowExpression+`)
	`, pair.ProviderKey, pair.ModelID, pair.UpstreamModelID, pair.ContextWindow, nullableInt(pair.MaxOutput),
		boolToInt(pair.SupportsTools), boolToInt(pair.SupportsVision), boolToInt(pair.SupportsAudioInput), boolToInt(pair.SupportsReasoning),
		boolToInt(pair.Enabled), pair.Priority)
	return wrapWrite("insert pair", err)
}

func updatePair(ctx context.Context, tx *sql.Tx, source models.Source, pair models.Pair) error {
	if source == models.SourceModelsDev {
		// Capabilities and the upstream model ID are synchronized; enabled and
		// priority stay under local control.
		_, err := tx.ExecContext(ctx, `
			UPDATE provider_models
			SET upstream_model_id = ?, context_window = ?, max_output = ?,
				supports_tools = ?, supports_vision = ?, supports_audio_input = ?, supports_reasoning = ?, updated_at = `+nowExpression+`
			WHERE provider_key = ? AND model_id = ?
		`, pair.UpstreamModelID, pair.ContextWindow, nullableInt(pair.MaxOutput),
			boolToInt(pair.SupportsTools), boolToInt(pair.SupportsVision), boolToInt(pair.SupportsAudioInput), boolToInt(pair.SupportsReasoning),
			pair.ProviderKey, pair.ModelID)
		return wrapWrite("update pair", err)
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE provider_models
		SET upstream_model_id = ?, context_window = ?, max_output = ?,
			supports_tools = ?, supports_vision = ?, supports_audio_input = ?, supports_reasoning = ?,
			enabled = ?, priority = ?, source = 'local', updated_at = `+nowExpression+`
		WHERE provider_key = ? AND model_id = ?
	`, pair.UpstreamModelID, pair.ContextWindow, nullableInt(pair.MaxOutput),
		boolToInt(pair.SupportsTools), boolToInt(pair.SupportsVision), boolToInt(pair.SupportsAudioInput), boolToInt(pair.SupportsReasoning),
		boolToInt(pair.Enabled), pair.Priority, pair.ProviderKey, pair.ModelID)
	return wrapWrite("update pair", err)
}

func disablePair(ctx context.Context, tx *sql.Tx, key pairKey) error {
	_, err := tx.ExecContext(ctx, `UPDATE provider_models SET enabled = 0, updated_at = `+nowExpression+` WHERE provider_key = ? AND model_id = ?`, key.providerKey, key.modelID)
	return wrapWrite("disable pair", err)
}

func readProviderStates(ctx context.Context, tx *sql.Tx) (map[string]rowState, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key, source, enabled, admin_owned FROM providers`)
	if err != nil {
		return nil, fmt.Errorf("read providers: %w", err)
	}
	defer rows.Close()
	states := map[string]rowState{}
	for rows.Next() {
		var key, source string
		var enabled, adminOwned int
		if err := rows.Scan(&key, &source, &enabled, &adminOwned); err != nil {
			return nil, fmt.Errorf("read provider: %w", err)
		}
		states[key] = rowState{source: models.Source(source), enabled: enabled != 0, adminOwned: adminOwned != 0}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read providers: %w", err)
	}
	return states, nil
}

func readModelStates(ctx context.Context, tx *sql.Tx) (map[string]rowState, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, source, enabled, admin_owned FROM models`)
	if err != nil {
		return nil, fmt.Errorf("read models: %w", err)
	}
	defer rows.Close()
	states := map[string]rowState{}
	for rows.Next() {
		var id, source string
		var enabled, adminOwned int
		if err := rows.Scan(&id, &source, &enabled, &adminOwned); err != nil {
			return nil, fmt.Errorf("read model: %w", err)
		}
		states[id] = rowState{source: models.Source(source), enabled: enabled != 0, adminOwned: adminOwned != 0}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read models: %w", err)
	}
	return states, nil
}

func readPairStates(ctx context.Context, tx *sql.Tx) (map[pairKey]rowState, error) {
	rows, err := tx.QueryContext(ctx, `SELECT provider_key, model_id, source, enabled, admin_owned FROM provider_models`)
	if err != nil {
		return nil, fmt.Errorf("read pairs: %w", err)
	}
	defer rows.Close()
	states := map[pairKey]rowState{}
	for rows.Next() {
		var key pairKey
		var source string
		var enabled, adminOwned int
		if err := rows.Scan(&key.providerKey, &key.modelID, &source, &enabled, &adminOwned); err != nil {
			return nil, fmt.Errorf("read pair: %w", err)
		}
		states[key] = rowState{source: models.Source(source), enabled: enabled != 0, adminOwned: adminOwned != 0}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read pairs: %w", err)
	}
	return states, nil
}

func writeSyncState(ctx context.Context, tx *sql.Tx, state *SyncState) error {
	warnings, err := json.Marshal(boundedWarnings(state.Warnings))
	if err != nil {
		return fmt.Errorf("encode sync warnings: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO registry_sync_state (source, url, fetched_at, allowlist_digest, imported_pairs, skipped_pairs, warnings, updated_at)
		VALUES ('modelsdev', ?, ?, ?, ?, ?, ?, `+nowExpression+`)
		ON CONFLICT(source) DO UPDATE SET
			url = excluded.url,
			fetched_at = excluded.fetched_at,
			allowlist_digest = excluded.allowlist_digest,
			imported_pairs = excluded.imported_pairs,
			skipped_pairs = excluded.skipped_pairs,
			warnings = excluded.warnings,
			updated_at = `+nowExpression+`
	`, state.URL, formatTimestamp(state.FetchedAt), state.AllowlistDigest, state.ImportedPairs, state.SkippedPairs, string(warnings))
	return wrapWrite("record sync state", err)
}

const maxStoredWarnings = 100

func boundedWarnings(warnings []string) []string {
	if warnings == nil {
		return []string{}
	}
	if len(warnings) <= maxStoredWarnings {
		return warnings
	}
	bounded := append([]string(nil), warnings[:maxStoredWarnings]...)
	return append(bounded, fmt.Sprintf("%d more warnings suppressed", len(warnings)-maxStoredWarnings))
}

// LoadSyncState returns the recorded synchronization metadata for a source.
func LoadSyncState(ctx context.Context, db *sql.DB, source models.Source) (SyncState, bool, error) {
	var state SyncState
	var fetchedAt, warnings string
	err := db.QueryRowContext(ctx, `
		SELECT url, fetched_at, allowlist_digest, imported_pairs, skipped_pairs, warnings
		FROM registry_sync_state WHERE source = ?
	`, string(source)).Scan(&state.URL, &fetchedAt, &state.AllowlistDigest, &state.ImportedPairs, &state.SkippedPairs, &warnings)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncState{}, false, nil
	}
	if err != nil {
		return SyncState{}, false, fmt.Errorf("read sync state: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, fetchedAt)
	if err != nil {
		return SyncState{}, false, fmt.Errorf("read sync state: invalid fetched_at %q", fetchedAt)
	}
	state.FetchedAt = parsed
	if err := json.Unmarshal([]byte(warnings), &state.Warnings); err != nil {
		return SyncState{}, false, fmt.Errorf("read sync state: invalid warnings: %w", err)
	}
	return state, true, nil
}

// LoadCatalog reads the whole registry into a validated, sorted snapshot. It is
// the only way routing code obtains providers, models and pairs.
func LoadCatalog(ctx context.Context, db *sql.DB) (*models.Catalog, error) {
	providers, err := queryProviders(ctx, db)
	if err != nil {
		return nil, err
	}
	catalogModels, err := queryModels(ctx, db)
	if err != nil {
		return nil, err
	}
	pairs, err := queryPairs(ctx, db)
	if err != nil {
		return nil, err
	}
	catalog, err := models.NewCatalog(0, providers, catalogModels, pairs)
	if err != nil {
		return nil, fmt.Errorf("load model registry: %w", err)
	}
	return catalog, nil
}

func queryProviders(ctx context.Context, db *sql.DB) ([]models.Provider, error) {
	rows, err := db.QueryContext(ctx, `SELECT key, display_name, base_url, api_key, enabled, priority, source, gateway_provider, kind FROM providers`)
	if err != nil {
		return nil, fmt.Errorf("read providers: %w", err)
	}
	defer rows.Close()
	var providers []models.Provider
	for rows.Next() {
		var provider models.Provider
		var enabled int
		var source, kind string
		var gatewayProvider sql.NullString
		if err := rows.Scan(&provider.Key, &provider.DisplayName, &provider.BaseURL, &provider.APIKey, &enabled, &provider.Priority, &source, &gatewayProvider, &kind); err != nil {
			return nil, fmt.Errorf("read provider: %w", err)
		}
		provider.Enabled = enabled != 0
		provider.Source = models.Source(source)
		provider.Kind = models.ProviderKind(kind)
		if gatewayProvider.Valid {
			mapped := gatewayProvider.String
			provider.GatewayProvider = &mapped
		}
		providers = append(providers, provider)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read providers: %w", err)
	}
	return providers, nil
}

func queryModels(ctx context.Context, db *sql.DB) ([]models.Model, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, display_name, enabled, priority, source FROM models`)
	if err != nil {
		return nil, fmt.Errorf("read models: %w", err)
	}
	defer rows.Close()
	var catalogModels []models.Model
	for rows.Next() {
		var model models.Model
		var enabled int
		var source string
		if err := rows.Scan(&model.ID, &model.DisplayName, &enabled, &model.Priority, &source); err != nil {
			return nil, fmt.Errorf("read model: %w", err)
		}
		model.Enabled = enabled != 0
		model.Source = models.Source(source)
		catalogModels = append(catalogModels, model)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read models: %w", err)
	}
	return catalogModels, nil
}

func queryPairs(ctx context.Context, db *sql.DB) ([]models.Pair, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT provider_key, model_id, upstream_model_id, context_window, max_output,
			supports_tools, supports_vision, supports_audio_input, supports_reasoning, enabled, priority, source
		FROM provider_models
	`)
	if err != nil {
		return nil, fmt.Errorf("read pairs: %w", err)
	}
	defer rows.Close()
	var pairs []models.Pair
	for rows.Next() {
		var pair models.Pair
		var maxOutput sql.NullInt64
		var supportsTools, supportsVision, supportsAudioInput, supportsReasoning, enabled int
		var source string
		if err := rows.Scan(&pair.ProviderKey, &pair.ModelID, &pair.UpstreamModelID, &pair.ContextWindow, &maxOutput,
			&supportsTools, &supportsVision, &supportsAudioInput, &supportsReasoning, &enabled, &pair.Priority, &source); err != nil {
			return nil, fmt.Errorf("read pair: %w", err)
		}
		if maxOutput.Valid {
			value := int(maxOutput.Int64)
			pair.MaxOutput = &value
		}
		pair.SupportsTools = supportsTools != 0
		pair.SupportsVision = supportsVision != 0
		pair.SupportsAudioInput = supportsAudioInput != 0
		pair.SupportsReasoning = supportsReasoning != 0
		pair.Enabled = enabled != 0
		pair.Source = models.Source(source)
		pairs = append(pairs, pair)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read pairs: %w", err)
	}
	return pairs, nil
}

func dedupeProviders(providers []models.Provider, source models.Source) []models.Provider {
	index := make(map[string]int, len(providers))
	deduped := make([]models.Provider, 0, len(providers))
	for _, provider := range providers {
		provider.Source = source
		if position, ok := index[provider.Key]; ok {
			deduped[position] = provider
			continue
		}
		index[provider.Key] = len(deduped)
		deduped = append(deduped, provider)
	}
	return deduped
}

func dedupeModels(catalogModels []models.Model, source models.Source) []models.Model {
	index := make(map[string]int, len(catalogModels))
	deduped := make([]models.Model, 0, len(catalogModels))
	for _, model := range catalogModels {
		model.Source = source
		if position, ok := index[model.ID]; ok {
			deduped[position] = model
			continue
		}
		index[model.ID] = len(deduped)
		deduped = append(deduped, model)
	}
	return deduped
}

func dedupePairs(pairs []models.Pair, source models.Source) []models.Pair {
	index := make(map[pairKey]int, len(pairs))
	deduped := make([]models.Pair, 0, len(pairs))
	for _, pair := range pairs {
		pair.Source = source
		key := pairKey{pair.ProviderKey, pair.ModelID}
		if position, ok := index[key]; ok {
			deduped[position] = pair
			continue
		}
		index[key] = len(deduped)
		deduped = append(deduped, pair)
	}
	return deduped
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

// nullableGatewayProvider stores an absent mapping as SQL NULL. A pointer to an
// empty string would violate the domain invariant enforced by
// models.ValidateProvider, so it is normalized to NULL instead of written.
func nullableGatewayProvider(value *string) any {
	if value == nil || *value == "" {
		return nil
	}
	return *value
}

func formatTimestamp(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

// wrapWrite keeps SQL errors free of bound values, which include API keys.
func wrapWrite(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", action, err)
}
