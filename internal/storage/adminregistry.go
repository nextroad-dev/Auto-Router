package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/nextroad-dev/Auto-Router/internal/models"
)

// The ownership values the Admin API exposes. They are derived from
// admin_owned plus source and are three fixed literals:
//
//   - "admin": the row was modified through the Admin API and is therefore never
//     rewritten or disabled by -sync-models or by a configuration import;
//   - "config": the row came from the configuration file (source = 'local');
//   - "modelsdev": the row came from a models.dev synchronization.
//
// "admin" wins over the other two, because it answers the question an operator
// actually asks: will my next restart undo this edit?
const (
	OwnerAdmin     = "admin"
	OwnerConfig    = "config"
	OwnerModelsDev = "modelsdev"
)

// registryOwner derives the exposed ownership from the two stored facts.
func registryOwner(adminOwned bool, source models.Source) string {
	if adminOwned {
		return OwnerAdmin
	}
	if source == models.SourceLocal {
		return OwnerConfig
	}
	return OwnerModelsDev
}

// ProviderView is one provider row as the Admin API reads it. APIKey is the
// stored credential and is never returned to a client; API responses expose only
// whether a key is set.
type ProviderView struct {
	Provider   models.Provider
	AdminOwned bool
	// PairCount is how many provider/model pairs reference this provider. It is
	// reported so an operator can see what disabling a provider would affect.
	PairCount int
}

// Owner returns the exposed ownership label.
func (p ProviderView) Owner() string { return registryOwner(p.AdminOwned, p.Provider.Source) }

// ModelView is one logical model row as the Admin API reads it.
type ModelView struct {
	Model      models.Model
	AdminOwned bool
	// PairCount is how many provider/model pairs reference this model.
	PairCount int
}

// Owner returns the exposed ownership label.
func (m ModelView) Owner() string { return registryOwner(m.AdminOwned, m.Model.Source) }

// PairView is one provider/model pair row as the Admin API reads it.
type PairView struct {
	Pair       models.Pair
	AdminOwned bool
}

// Owner returns the exposed ownership label.
func (p PairView) Owner() string { return registryOwner(p.AdminOwned, p.Pair.Source) }

// capUnsetReportedPairs bounds how many pairs one *View carries when a caller
// wants the full set. Pair counts are reported as numbers; the pairs themselves
// are only materialized where the API returns them.
const maxReportedPairs = 4096

// The Admin API's registry reads. Every statement is bounded by a caller-supplied
// limit and every ordering is a prefix of the documented contract order, so a
// page boundary is a stable position rather than an arbitrary one.

// ProviderFilter narrows a provider listing. Empty fields leave the listing
// unfiltered; Search is a literal substring, not a SQL pattern.
type ProviderFilter struct {
	Search  string
	Enabled *bool
}

// ListProviders preserves the unfiltered storage contract used by non-API callers.
func ListProviders(ctx context.Context, db *sql.DB, cursor ProviderCursor, limit int) ([]ProviderView, error) {
	return ListProvidersFiltered(ctx, db, ProviderFilter{}, cursor, limit)
}

// ListProvidersFiltered returns providers in contract order (priority ascending,
// then key ascending), starting strictly after the position cursor encodes.
func ListProvidersFiltered(ctx context.Context, db *sql.DB, filter ProviderFilter, cursor ProviderCursor, limit int) ([]ProviderView, error) {
	if err := checkQuery(limit); err != nil {
		return nil, err
	}
	where := []string{"(p.priority > ? OR (p.priority = ? AND p.key > ?))"}
	arguments := []any{cursor.Priority, cursor.Priority, cursor.Key}
	if filter.Search != "" {
		where = append(where, `(instr(lower(p.key), lower(?)) > 0 OR instr(lower(p.display_name), lower(?)) > 0)`)
		arguments = append(arguments, filter.Search, filter.Search)
	}
	if filter.Enabled != nil {
		where = append(where, "p.enabled = ?")
		arguments = append(arguments, boolToInt(*filter.Enabled))
	}
	arguments = append(arguments, limit+1)
	rows, err := db.QueryContext(ctx, `
		SELECT p.key, p.display_name, p.base_url, p.api_key, p.enabled, p.priority, p.source, p.gateway_provider, p.admin_owned, p.kind,
			(SELECT count(*) FROM provider_models m WHERE m.provider_key = p.key)
		FROM providers p
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY p.priority, p.key
		LIMIT ?
	`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("read providers: %w", err)
	}
	defer rows.Close()
	views := make([]ProviderView, 0, limit+1)
	for rows.Next() {
		view, err := scanProviderView(rows)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read providers: %w", err)
	}
	return views, nil
}

// ProviderCursor is the position of a provider listing: it is exactly the
// contract order prefix the ORDER BY uses (priority, then key).
type ProviderCursor struct {
	Priority int
	Key      string
}

// CountProviders returns how many providers the registry holds.
func CountProviders(ctx context.Context, db *sql.DB) (int, error) {
	return countStoredRows(ctx, db, "providers")
}

// GetProvider returns one provider by key.
func GetProvider(ctx context.Context, db *sql.DB, key string) (ProviderView, bool, error) {
	var view ProviderView
	var enabled, adminOwned int
	var source, kind string
	var gatewayProvider sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT p.key, p.display_name, p.base_url, p.api_key, p.enabled, p.priority, p.source, p.gateway_provider, p.admin_owned, p.kind,
			(SELECT count(*) FROM provider_models m WHERE m.provider_key = p.key)
		FROM providers p WHERE p.key = ?
	`, key).Scan(&view.Provider.Key, &view.Provider.DisplayName, &view.Provider.BaseURL, &view.Provider.APIKey,
		&enabled, &view.Provider.Priority, &source, &gatewayProvider, &adminOwned, &kind, &view.PairCount)
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderView{}, false, nil
	}
	if err != nil {
		return ProviderView{}, false, fmt.Errorf("read provider: %w", err)
	}
	view.Provider.Enabled = enabled != 0
	view.Provider.Source = models.Source(source)
	view.Provider.Kind = models.ProviderKind(kind)
	view.AdminOwned = adminOwned != 0
	if gatewayProvider.Valid {
		mapped := gatewayProvider.String
		view.Provider.GatewayProvider = &mapped
	}
	return view, true, nil
}

// ProviderPatch is one administrator edit. A nil field is left unchanged, which
// is why every field is a pointer: "set priority to 0" and "do not touch the
// priority" are different requests.
type ProviderPatch struct {
	Kind        *models.ProviderKind
	DisplayName *string
	Enabled     *bool
	Priority    *int
	BaseURL     *string
	APIKey      *string
	// GatewayProvider is retained for compatibility with migrated registry data;
	// the direct executor does not use it for routing.
	GatewayProvider *string
}

// UpdateProvider applies a patch and marks the row as administrator-owned in the
// same statement, so a row can never be edited without also being protected from
// the next import.
func UpdateProvider(ctx context.Context, db *sql.DB, key string, patch ProviderPatch) (bool, error) {
	if patch.Kind == nil && patch.DisplayName == nil && patch.Enabled == nil && patch.Priority == nil && patch.BaseURL == nil && patch.APIKey == nil && patch.GatewayProvider == nil {
		// Nothing to do. It is reported as "not applied" rather than as a write
		// that marked the row as administrator-owned for no reason.
		return false, nil
	}
	assignments := make([]string, 0, 5)
	arguments := make([]any, 0, 6)
	if patch.Kind != nil {
		assignments = append(assignments, "kind = ?")
		arguments = append(arguments, string(*patch.Kind))
	}
	if patch.DisplayName != nil {
		assignments = append(assignments, "display_name = ?")
		arguments = append(arguments, *patch.DisplayName)
	}
	if patch.Enabled != nil {
		assignments = append(assignments, "enabled = ?")
		arguments = append(arguments, boolToInt(*patch.Enabled))
	}
	if patch.Priority != nil {
		assignments = append(assignments, "priority = ?")
		arguments = append(arguments, *patch.Priority)
	}
	if patch.BaseURL != nil {
		assignments = append(assignments, "base_url = ?")
		arguments = append(arguments, *patch.BaseURL)
	}
	if patch.APIKey != nil {
		assignments = append(assignments, "api_key = ?")
		arguments = append(arguments, *patch.APIKey)
	}
	if patch.GatewayProvider != nil {
		assignments = append(assignments, "gateway_provider = ?")
		arguments = append(arguments, nullableGatewayProvider(patch.GatewayProvider))
	}
	assignments = append(assignments, "admin_owned = 1", "updated_at = "+nowExpression)
	arguments = append(arguments, key)
	result, err := db.ExecContext(ctx, "UPDATE providers SET "+strings.Join(assignments, ", ")+" WHERE key = ?", arguments...)
	if err != nil {
		return false, wrapWrite("update provider", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count updated providers: %w", err)
	}
	return affected > 0, nil
}

// NewProvider is the description of a provider an administrator asks to create.
// APIKey is write-only at the API boundary and is never copied into a response.
type NewProvider struct {
	Kind        models.ProviderKind
	Key         string
	DisplayName string
	BaseURL     string
	APIKey      string
	Enabled     bool
	Priority    int
}

// CreateProvider inserts an administrator-owned local provider row.
func CreateProvider(ctx context.Context, db *sql.DB, provider NewProvider) error {
	displayName := provider.DisplayName
	if displayName == "" {
		displayName = provider.Key
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO providers (key, display_name, base_url, api_key, enabled, priority, source, gateway_provider, admin_owned, kind, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'local', NULL, 1, ?, `+nowExpression+`)
	`, provider.Key, displayName, provider.BaseURL, provider.APIKey, boolToInt(provider.Enabled), provider.Priority, providerKindOrDefault(provider.Kind))
	if err != nil && isUniqueViolation(err) {
		return &ConflictError{Message: "a provider with this key already exists"}
	}
	return wrapWrite("create provider", err)
}

func providerKindOrDefault(kind models.ProviderKind) string {
	if kind == "" {
		return string(models.ProviderOpenAICompatible)
	}
	return string(kind)
}

// NewModel is the description of a logical model an administrator asks to create.
type NewModel struct {
	ID          string
	DisplayName string
	Enabled     bool
	Priority    int
}

// CreateModel inserts an administrator-owned local logical model row.
func CreateModel(ctx context.Context, db *sql.DB, model NewModel) error {
	displayName := model.DisplayName
	if displayName == "" {
		displayName = model.ID
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO models (id, display_name, enabled, priority, source, admin_owned, updated_at)
		VALUES (?, ?, ?, ?, 'local', 1, `+nowExpression+`)
	`, model.ID, displayName, boolToInt(model.Enabled), model.Priority)
	if err != nil && isUniqueViolation(err) {
		return &ConflictError{Message: "a model with this id already exists"}
	}
	return wrapWrite("create model", err)
}

// ModelFilter narrows a model listing. Empty fields leave the listing
// unfiltered; Search is a literal substring, not a SQL pattern.
type ModelFilter struct {
	Search  string
	Enabled *bool
	Source  string
}

// ListModels preserves the unfiltered storage contract used by non-API callers.
func ListModels(ctx context.Context, db *sql.DB, cursor ModelCursor, limit int) ([]ModelView, error) {
	return ListModelsFiltered(ctx, db, ModelFilter{}, cursor, limit)
}

// ListModelsFiltered returns logical models in contract order (priority ascending,
// then ID ascending) after the position cursor encodes.
func ListModelsFiltered(ctx context.Context, db *sql.DB, filter ModelFilter, cursor ModelCursor, limit int) ([]ModelView, error) {
	if err := checkQuery(limit); err != nil {
		return nil, err
	}
	where := []string{"(m.priority > ? OR (m.priority = ? AND m.id > ?))"}
	arguments := []any{cursor.Priority, cursor.Priority, cursor.ID}
	if filter.Search != "" {
		where = append(where, `(instr(lower(m.id), lower(?)) > 0 OR instr(lower(m.display_name), lower(?)) > 0)`)
		arguments = append(arguments, filter.Search, filter.Search)
	}
	if filter.Enabled != nil {
		where = append(where, "m.enabled = ?")
		arguments = append(arguments, boolToInt(*filter.Enabled))
	}
	if filter.Source != "" {
		where = append(where, "m.source = ?")
		arguments = append(arguments, filter.Source)
	}
	arguments = append(arguments, limit+1)
	rows, err := db.QueryContext(ctx, `
		SELECT m.id, m.display_name, m.enabled, m.priority, m.source, m.admin_owned,
			(SELECT count(*) FROM provider_models p WHERE p.model_id = m.id)
		FROM models m
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY m.priority, m.id
		LIMIT ?
	`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("read models: %w", err)
	}
	defer rows.Close()
	views := make([]ModelView, 0, limit+1)
	for rows.Next() {
		view, err := scanModelView(rows)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read models: %w", err)
	}
	return views, nil
}

// ModelCursor is the position of a model listing: priority, then ID.
type ModelCursor struct {
	Priority int
	ID       string
}

// CountModels returns how many logical models the registry holds.
func CountModels(ctx context.Context, db *sql.DB) (int, error) {
	return countStoredRows(ctx, db, "models")
}

// GetModel returns one logical model by ID.
func GetModel(ctx context.Context, db *sql.DB, id string) (ModelView, bool, error) {
	var view ModelView
	var enabled, adminOwned int
	var source string
	err := db.QueryRowContext(ctx, `
		SELECT m.id, m.display_name, m.enabled, m.priority, m.source, m.admin_owned,
			(SELECT count(*) FROM provider_models p WHERE p.model_id = m.id)
		FROM models m WHERE m.id = ?
	`, id).Scan(&view.Model.ID, &view.Model.DisplayName, &enabled, &view.Model.Priority, &source, &adminOwned, &view.PairCount)
	if errors.Is(err, sql.ErrNoRows) {
		return ModelView{}, false, nil
	}
	if err != nil {
		return ModelView{}, false, fmt.Errorf("read model: %w", err)
	}
	view.Model.Enabled = enabled != 0
	view.Model.Source = models.Source(source)
	view.AdminOwned = adminOwned != 0
	return view, true, nil
}

// ModelPatch is one administrator edit of a logical model.
type ModelPatch struct {
	DisplayName *string
	Enabled     *bool
	Priority    *int
}

// UpdateModel applies a patch and marks the row as administrator-owned.
func UpdateModel(ctx context.Context, db *sql.DB, id string, patch ModelPatch) (bool, error) {
	if patch.DisplayName == nil && patch.Enabled == nil && patch.Priority == nil {
		return false, nil
	}
	assignments := make([]string, 0, 4)
	arguments := make([]any, 0, 5)
	if patch.DisplayName != nil {
		assignments = append(assignments, "display_name = ?")
		arguments = append(arguments, *patch.DisplayName)
	}
	if patch.Enabled != nil {
		assignments = append(assignments, "enabled = ?")
		arguments = append(arguments, boolToInt(*patch.Enabled))
	}
	if patch.Priority != nil {
		assignments = append(assignments, "priority = ?")
		arguments = append(arguments, *patch.Priority)
	}
	assignments = append(assignments, "admin_owned = 1", "updated_at = "+nowExpression)
	arguments = append(arguments, id)
	result, err := db.ExecContext(ctx, "UPDATE models SET "+strings.Join(assignments, ", ")+" WHERE id = ?", arguments...)
	if err != nil {
		return false, wrapWrite("update model", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count updated models: %w", err)
	}
	return affected > 0, nil
}

// PairFilter narrows a pair listing. Every field is optional; an empty filter
// lists everything.
type PairFilter struct {
	ProviderKey string
	ModelID     string
	// Enabled filters on the pair's own enabled flag; nil means "either".
	Enabled *bool
	// Missing, when set, selects pairs that do not support one capability. It is
	// the operator's "what could serve a vision request" query.
	Missing string
}

// The capability names PairFilter.Missing accepts. They are a closed set: an
// unknown name would silently match every row.
const (
	CapabilityTools     = "tools"
	CapabilityVision    = "vision"
	CapabilityAudio     = "audio_input"
	CapabilityReasoning = "reasoning"
)

// ValidCapability reports whether name is one of the four filterable capability
// flags.
func ValidCapability(name string) bool {
	switch name {
	case CapabilityTools, CapabilityVision, CapabilityAudio, CapabilityReasoning:
		return true
	default:
		return false
	}
}

// missingColumn maps a capability name onto its column. The name is validated
// before this is called, so the concatenation cannot carry input.
func missingColumn(name string) string {
	switch name {
	case CapabilityTools:
		return "supports_tools"
	case CapabilityVision:
		return "supports_vision"
	case CapabilityAudio:
		return "supports_audio_input"
	default:
		return "supports_reasoning"
	}
}

// PairCursor is the position of a pair listing: it is exactly the contract order
// prefix the ORDER BY uses.
type PairCursor struct {
	Priority         int
	ProviderPriority int
	ProviderKey      string
	ModelID          string
}

// PairRow is one pair with the provider priority the contract order needs.
type PairRow struct {
	View             PairView
	ProviderPriority int
}

// ListPairs returns pairs in contract order (pair priority, provider priority,
// provider key, model ID) after the position cursor encodes. The filter is applied
// in SQL, so a filtered listing is bounded by the same limit as an unfiltered one.
func ListPairs(ctx context.Context, db *sql.DB, filter PairFilter, cursor PairCursor, limit int) ([]PairRow, error) {
	if err := checkQuery(limit); err != nil {
		return nil, err
	}
	where := []string{"1 = 1"}
	arguments := []any{}
	if filter.ProviderKey != "" {
		where = append(where, "m.provider_key = ?")
		arguments = append(arguments, filter.ProviderKey)
	}
	if filter.ModelID != "" {
		where = append(where, "m.model_id = ?")
		arguments = append(arguments, filter.ModelID)
	}
	if filter.Enabled != nil {
		where = append(where, "m.enabled = ?")
		arguments = append(arguments, boolToInt(*filter.Enabled))
	}
	if filter.Missing != "" {
		where = append(where, missingColumn(filter.Missing)+" = 0")
	}
	// The cursor is the lexicographic form of "strictly after this position", in
	// exactly the order the contract sorts by. A zero cursor starts at the
	// beginning for the same reason it does in ListProviders.
	arguments = append(arguments,
		cursor.Priority, cursor.Priority,
		cursor.ProviderPriority, cursor.ProviderPriority,
		cursor.ProviderKey, cursor.ProviderKey,
		cursor.ModelID,
	)
	where = append(where, `(m.priority > ? OR (m.priority = ? AND (p.priority > ? OR (p.priority = ? AND (p.key > ? OR (p.key = ? AND m.model_id > ?))))))`)
	query := `
		SELECT m.provider_key, m.model_id, m.upstream_model_id, m.context_window, m.max_output,
			m.supports_tools, m.supports_vision, m.supports_audio_input, m.supports_reasoning,
			m.enabled, m.priority, m.source, m.admin_owned, p.priority
		FROM provider_models m JOIN providers p ON p.key = m.provider_key
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY m.priority, p.priority, p.key, m.model_id
		LIMIT ?`
	arguments = append(arguments, limit+1)

	rows, err := db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("read pairs: %w", err)
	}
	defer rows.Close()
	views := make([]PairRow, 0, limit+1)
	for rows.Next() {
		row, err := scanPairRow(rows)
		if err != nil {
			return nil, err
		}
		views = append(views, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read pairs: %w", err)
	}
	return views, nil
}

// CountPairs returns how many pairs the registry holds.
func CountPairs(ctx context.Context, db *sql.DB) (int, error) {
	return countStoredRows(ctx, db, "provider_models")
}

// GetPair returns one pair by provider key and model ID.
func GetPair(ctx context.Context, db *sql.DB, providerKey, modelID string) (PairView, bool, error) {
	var view PairView
	var maxOutput sql.NullInt64
	var tools, vision, audio, reasoning, enabled, adminOwned int
	var source string
	err := db.QueryRowContext(ctx, `
		SELECT provider_key, model_id, upstream_model_id, context_window, max_output,
			supports_tools, supports_vision, supports_audio_input, supports_reasoning,
			enabled, priority, source, admin_owned
		FROM provider_models WHERE provider_key = ? AND model_id = ?
	`, providerKey, modelID).Scan(&view.Pair.ProviderKey, &view.Pair.ModelID, &view.Pair.UpstreamModelID,
		&view.Pair.ContextWindow, &maxOutput, &tools, &vision, &audio, &reasoning,
		&enabled, &view.Pair.Priority, &source, &adminOwned)
	if errors.Is(err, sql.ErrNoRows) {
		return PairView{}, false, nil
	}
	if err != nil {
		return PairView{}, false, fmt.Errorf("read pair: %w", err)
	}
	if maxOutput.Valid {
		value := int(maxOutput.Int64)
		view.Pair.MaxOutput = &value
	}
	view.Pair.SupportsTools = tools != 0
	view.Pair.SupportsVision = vision != 0
	view.Pair.SupportsAudioInput = audio != 0
	view.Pair.SupportsReasoning = reasoning != 0
	view.Pair.Enabled = enabled != 0
	view.Pair.Source = models.Source(source)
	view.AdminOwned = adminOwned != 0
	return view, true, nil
}

// PairPatch is one administrator edit of a pair. The nullable capacity fields use
// a two-level pointer because "clear max_output" and "leave max_output alone" are
// different requests, and NULL is the stored representation of "no ceiling".
type PairPatch struct {
	Enabled            *bool
	Priority           *int
	UpstreamModelID    *string
	ContextWindow      *int
	MaxOutput          **int
	SupportsTools      *bool
	SupportsVision     *bool
	SupportsAudioInput *bool
	SupportsReasoning  *bool
}

// UpdatePair applies a patch and marks the row as administrator-owned. The
// context_window/max_output pair is validated against the merged row before the
// write: a patch that raises context_window without lowering max_output is legal,
// and one that would leave max_output above context_window is refused here rather
// than by SQLite's CHECK, so the error names the fields instead of the constraint.
func UpdatePair(ctx context.Context, db *sql.DB, providerKey, modelID string, patch PairPatch) (bool, error) {
	current, ok, err := GetPair(ctx, db, providerKey, modelID)
	if err != nil || !ok {
		return false, err
	}
	merged := current.Pair
	if patch.Enabled != nil {
		merged.Enabled = *patch.Enabled
	}
	if patch.Priority != nil {
		merged.Priority = *patch.Priority
	}
	if patch.UpstreamModelID != nil {
		merged.UpstreamModelID = *patch.UpstreamModelID
	}
	if patch.ContextWindow != nil {
		merged.ContextWindow = *patch.ContextWindow
	}
	if patch.MaxOutput != nil {
		merged.MaxOutput = *patch.MaxOutput
	}
	if patch.SupportsTools != nil {
		merged.SupportsTools = *patch.SupportsTools
	}
	if patch.SupportsVision != nil {
		merged.SupportsVision = *patch.SupportsVision
	}
	if patch.SupportsAudioInput != nil {
		merged.SupportsAudioInput = *patch.SupportsAudioInput
	}
	if patch.SupportsReasoning != nil {
		merged.SupportsReasoning = *patch.SupportsReasoning
	}
	// The validation is the same function the importer uses, so an administrator
	// cannot reach a state a configuration file could not express. A stored ceiling
	// above the new window is not caught by ValidatePair when the patch did not name
	// the ceiling, so the two columns are compared here as well: the pair's own
	// invariant is "the ceiling never exceeds the window", and a patch that breaks it
	// must be refused rather than stored as a value a later reader would have to
	// second-guess.
	if err := models.ValidatePair(merged); err != nil {
		return false, &ValidationError{Field: "pair", Message: err.Error()}
	}
	if merged.MaxOutput != nil && *merged.MaxOutput > merged.ContextWindow {
		return false, &ValidationError{Field: "max_output", Message: "max_output must not exceed context_window"}
	}
	assignments := []string{
		"upstream_model_id = ?", "context_window = ?", "max_output = ?",
		"supports_tools = ?", "supports_vision = ?", "supports_audio_input = ?", "supports_reasoning = ?",
		"enabled = ?", "priority = ?", "admin_owned = 1", "updated_at = " + nowExpression,
	}
	arguments := []any{
		merged.UpstreamModelID, merged.ContextWindow, nullableInt(merged.MaxOutput),
		boolToInt(merged.SupportsTools), boolToInt(merged.SupportsVision), boolToInt(merged.SupportsAudioInput), boolToInt(merged.SupportsReasoning),
		boolToInt(merged.Enabled), merged.Priority,
		providerKey, modelID,
	}
	_, err = db.ExecContext(ctx, "UPDATE provider_models SET "+strings.Join(assignments, ", ")+" WHERE provider_key = ? AND model_id = ?", arguments...)
	if err != nil {
		return false, wrapWrite("update pair", err)
	}
	return true, nil
}

// NewPair is the description of a binding an administrator asks to create. It
// always binds an existing provider to an existing logical model: the Admin API
// does not invent either row.
type NewPair struct {
	ProviderKey        string
	ModelID            string
	UpstreamModelID    string
	ContextWindow      int
	MaxOutput          *int
	SupportsTools      bool
	SupportsVision     bool
	SupportsAudioInput bool
	SupportsReasoning  bool
	Enabled            bool
	Priority           int
}

// PairExists reports whether a pair already exists. It exists so the API can
// answer "this binding already exists" without a failed insert.
func PairExists(ctx context.Context, db *sql.DB, providerKey, modelID string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM provider_models WHERE provider_key = ? AND model_id = ?`, providerKey, modelID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read pair: %w", err)
	}
	return true, nil
}

// ProviderExists reports whether a provider key is registered.
func ProviderExists(ctx context.Context, db *sql.DB, key string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM providers WHERE key = ?`, key).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read provider: %w", err)
	}
	return true, nil
}

// ModelExists reports whether a logical model is registered.
func ModelExists(ctx context.Context, db *sql.DB, id string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM models WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read model: %w", err)
	}
	return true, nil
}

// ValidationError marks an error produced by the registry's own validator rather
// than by the database: an administrator's change was refused because it would
// leave a row in a state a configuration file could not express. The management
// surface turns it into a 400 that names the field, while a database failure stays
// a 500 that carries no driver text.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

// ConflictError marks a write that lost a race with a concurrent insert of the same
// primary key. It is distinct from every other database failure because the client's
// remedy is specific: the row exists, so patch it instead of creating it.
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// ValidateNewPair checks a binding an administrator asked to create. It is exported
// so the API can refuse an obviously unusable binding before it reads the database,
// with the same rules the insert would apply.
func ValidateNewPair(pair NewPair) error {
	candidate := models.Pair{
		ProviderKey:        pair.ProviderKey,
		ModelID:            pair.ModelID,
		UpstreamModelID:    pair.UpstreamModelID,
		ContextWindow:      pair.ContextWindow,
		MaxOutput:          pair.MaxOutput,
		SupportsTools:      pair.SupportsTools,
		SupportsVision:     pair.SupportsVision,
		SupportsAudioInput: pair.SupportsAudioInput,
		SupportsReasoning:  pair.SupportsReasoning,
		Enabled:            pair.Enabled,
		Priority:           pair.Priority,
		Source:             models.SourceLocal,
	}
	if err := models.ValidatePair(candidate); err != nil {
		return &ValidationError{Field: "pair", Message: err.Error()}
	}
	return nil
}

// CreatePair inserts one administrator-owned binding. The row's source is left
// unchanged in spirit — 'local' means "not synchronized metadata" — while
// admin_owned marks it as owned by the management surface, which is what keeps an
// import from touching it.
//
// There is deliberately no delete counterpart: stage 9 disables, never removes.
func CreatePair(ctx context.Context, db *sql.DB, pair NewPair) error {
	candidate := models.Pair{
		ProviderKey:        pair.ProviderKey,
		ModelID:            pair.ModelID,
		UpstreamModelID:    pair.UpstreamModelID,
		ContextWindow:      pair.ContextWindow,
		MaxOutput:          pair.MaxOutput,
		SupportsTools:      pair.SupportsTools,
		SupportsVision:     pair.SupportsVision,
		SupportsAudioInput: pair.SupportsAudioInput,
		SupportsReasoning:  pair.SupportsReasoning,
		Enabled:            pair.Enabled,
		Priority:           pair.Priority,
		Source:             models.SourceLocal,
	}
	if err := models.ValidatePair(candidate); err != nil {
		return &ValidationError{Field: "pair", Message: err.Error()}
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO provider_models (provider_key, model_id, upstream_model_id, context_window, max_output,
			supports_tools, supports_vision, supports_audio_input, supports_reasoning, enabled, priority, source, admin_owned, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'local', 1, `+nowExpression+`)
	`, candidate.ProviderKey, candidate.ModelID, candidate.UpstreamModelID, candidate.ContextWindow, nullableInt(candidate.MaxOutput),
		boolToInt(candidate.SupportsTools), boolToInt(candidate.SupportsVision), boolToInt(candidate.SupportsAudioInput), boolToInt(candidate.SupportsReasoning),
		boolToInt(candidate.Enabled), candidate.Priority)
	if err != nil && isUniqueViolation(err) {
		return &ConflictError{Message: "the binding already exists"}
	}
	return wrapWrite("create pair", err)
}

// isUniqueViolation reports whether a database error is a primary-key or unique
// constraint conflict. The check is on the driver's error code rather than on the
// message text, so it cannot be broken by a wording change.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var sqliteError interface{ Code() int }
	if errors.As(err, &sqliteError) {
		// 1555 is SQLITE_CONSTRAINT_PRIMARYKEY and 2067 SQLITE_CONSTRAINT_UNIQUE.
		switch sqliteError.Code() {
		case 1555, 2067:
			return true
		}
	}
	return false
}

func scanProviderView(row scanner) (ProviderView, error) {
	var view ProviderView
	var enabled, adminOwned int
	var source, kind string
	var gatewayProvider sql.NullString
	if err := row.Scan(&view.Provider.Key, &view.Provider.DisplayName, &view.Provider.BaseURL, &view.Provider.APIKey,
		&enabled, &view.Provider.Priority, &source, &gatewayProvider, &adminOwned, &kind, &view.PairCount); err != nil {
		return ProviderView{}, fmt.Errorf("scan provider: %w", err)
	}
	view.Provider.Enabled = enabled != 0
	view.Provider.Source = models.Source(source)
	view.Provider.Kind = models.ProviderKind(kind)
	view.AdminOwned = adminOwned != 0
	if gatewayProvider.Valid {
		mapped := gatewayProvider.String
		view.Provider.GatewayProvider = &mapped
	}
	return view, nil
}

func scanModelView(row scanner) (ModelView, error) {
	var view ModelView
	var enabled, adminOwned int
	var source string
	if err := row.Scan(&view.Model.ID, &view.Model.DisplayName, &enabled, &view.Model.Priority, &source, &adminOwned, &view.PairCount); err != nil {
		return ModelView{}, fmt.Errorf("scan model: %w", err)
	}
	view.Model.Enabled = enabled != 0
	view.Model.Source = models.Source(source)
	view.AdminOwned = adminOwned != 0
	return view, nil
}

func scanPairRow(row scanner) (PairRow, error) {
	var result PairRow
	var maxOutput sql.NullInt64
	var tools, vision, audio, reasoning, enabled, adminOwned int
	var source string
	if err := row.Scan(&result.View.Pair.ProviderKey, &result.View.Pair.ModelID, &result.View.Pair.UpstreamModelID,
		&result.View.Pair.ContextWindow, &maxOutput, &tools, &vision, &audio, &reasoning,
		&enabled, &result.View.Pair.Priority, &source, &adminOwned, &result.ProviderPriority); err != nil {
		return PairRow{}, fmt.Errorf("scan pair: %w", err)
	}
	if maxOutput.Valid {
		value := int(maxOutput.Int64)
		result.View.Pair.MaxOutput = &value
	}
	result.View.Pair.SupportsTools = tools != 0
	result.View.Pair.SupportsVision = vision != 0
	result.View.Pair.SupportsAudioInput = audio != 0
	result.View.Pair.SupportsReasoning = reasoning != 0
	result.View.Pair.Enabled = enabled != 0
	result.View.Pair.Source = models.Source(source)
	result.View.AdminOwned = adminOwned != 0
	return result, nil
}

// checkQuery bounds one admin query. A limit of zero would read nothing and a
// negative limit would read everything, so both are refused.
func checkQuery(limit int) error {
	if limit <= 0 || limit > maxAdminQueryLimit {
		return fmt.Errorf("the query limit must be between 1 and %d", maxAdminQueryLimit)
	}
	return nil
}

// maxAdminQueryLimit bounds every Admin API read. It is the same ceiling the
// admin.max_page_size setting is validated against, repeated here so a
// programmatic caller cannot ask for an unbounded scan even if the configuration
// allowed a larger page.
const maxAdminQueryLimit = 1000

func countStoredRows(ctx context.Context, db *sql.DB, table string) (int, error) {
	// The table name is one of three compile-time constants of this package; it
	// never comes from input, so the concatenation cannot carry an injection.
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	return count, nil
}
