package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nextroad-dev/Auto-Router/internal/models"
)

var ErrProviderNotFound = errors.New("provider not found")

// SelectedProviderModelMetadata is capability data resolved from models.dev for
// the provider-native ID selected in the Admin UI.
type SelectedProviderModelMetadata struct {
	DisplayName        string
	ContextWindow      int
	MaxOutput          *int
	SupportsTools      bool
	SupportsVision     bool
	SupportsAudioInput bool
	SupportsReasoning  bool
}

// SelectProviderModel enables one discovered model for a provider. When metadata
// is available it imports the models.dev capabilities; an unknown model receives
// conservative defaults that remain editable in model management. A repeated
// selection refreshes generated/default metadata but never overwrites a deliberate
// administrator edit.
func SelectProviderModel(ctx context.Context, db *sql.DB, providerKey, modelID string, metadata *SelectedProviderModelMetadata) (bool, error) {
	if err := models.ValidateModelID(modelID); err != nil {
		return false, err
	}
	if err := models.ValidateProviderKey(providerKey); err != nil {
		return false, err
	}
	if metadata != nil {
		candidate := models.Pair{
			ProviderKey: providerKey, ModelID: modelID, UpstreamModelID: modelID,
			ContextWindow: metadata.ContextWindow, MaxOutput: metadata.MaxOutput,
			SupportsTools: metadata.SupportsTools, SupportsVision: metadata.SupportsVision,
			SupportsAudioInput: metadata.SupportsAudioInput, SupportsReasoning: metadata.SupportsReasoning,
			Enabled: true, Source: models.SourceModelsDev,
		}
		if err := models.ValidatePair(candidate); err != nil {
			return false, fmt.Errorf("invalid models.dev model metadata: %w", err)
		}
		if metadata.DisplayName != "" {
			if err := models.ValidateModel(models.Model{ID: modelID, DisplayName: metadata.DisplayName, Source: models.SourceModelsDev}); err != nil {
				return false, fmt.Errorf("invalid models.dev display name: %w", err)
			}
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin provider model selection: %w", err)
	}
	defer tx.Rollback()
	var found int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM providers WHERE key=?`, providerKey).Scan(&found); errors.Is(err, sql.ErrNoRows) {
		return false, ErrProviderNotFound
	} else if err != nil {
		return false, fmt.Errorf("read provider: %w", err)
	}

	displayName := modelID
	if metadata != nil && metadata.DisplayName != "" {
		displayName = metadata.DisplayName
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO models (id, display_name, enabled, priority, source, admin_owned, updated_at)
		VALUES (?, ?, 1, 0, 'local', 1, `+nowExpression+`)
		ON CONFLICT(id) DO UPDATE SET
			display_name = CASE WHEN models.display_name = models.id THEN excluded.display_name ELSE models.display_name END,
			enabled=1, admin_owned=1, updated_at=`+nowExpression+`
	`, modelID, displayName); err != nil {
		return false, fmt.Errorf("select logical model: %w", err)
	}

	var existingAdminOwned, existingContext, existingTools, existingVision, existingAudio, existingReasoning int
	var existingSource string
	var existingMaxOutput sql.NullInt64
	existingErr := tx.QueryRowContext(ctx, `
		SELECT admin_owned, source, context_window, max_output,
			supports_tools, supports_vision, supports_audio_input, supports_reasoning
		FROM provider_models WHERE provider_key=? AND model_id=?
	`, providerKey, modelID).Scan(&existingAdminOwned, &existingSource, &existingContext, &existingMaxOutput,
		&existingTools, &existingVision, &existingAudio, &existingReasoning)
	if existingErr != nil && !errors.Is(existingErr, sql.ErrNoRows) {
		return false, fmt.Errorf("read selected provider model: %w", existingErr)
	}
	exists := existingErr == nil
	metadataApplied := metadata != nil
	if metadata != nil && exists && existingAdminOwned != 0 && !isGeneratedDefault(existingSource, existingContext, existingMaxOutput, existingTools, existingVision, existingAudio, existingReasoning) {
		// An administrator has edited this binding. Re-selecting it may re-enable
		// it, but must not replace the locally managed capability record.
		metadataApplied = false
	}

	if metadataApplied {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO provider_models (provider_key, model_id, upstream_model_id, context_window, max_output,
				supports_tools, supports_vision, supports_audio_input, supports_reasoning, enabled, priority, source, admin_owned, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 0, 'modelsdev', 0, `+nowExpression+`)
			ON CONFLICT(provider_key, model_id) DO UPDATE SET
				upstream_model_id=excluded.upstream_model_id, context_window=excluded.context_window,
				max_output=excluded.max_output, supports_tools=excluded.supports_tools,
				supports_vision=excluded.supports_vision, supports_audio_input=excluded.supports_audio_input,
				supports_reasoning=excluded.supports_reasoning, enabled=1, source='modelsdev', admin_owned=0,
				updated_at=`+nowExpression+`
		`, providerKey, modelID, modelID, metadata.ContextWindow, nullableInt(metadata.MaxOutput),
			boolToInt(metadata.SupportsTools), boolToInt(metadata.SupportsVision), boolToInt(metadata.SupportsAudioInput), boolToInt(metadata.SupportsReasoning))
	} else if exists {
		_, err = tx.ExecContext(ctx, `UPDATE provider_models SET enabled=1, updated_at=`+nowExpression+` WHERE provider_key=? AND model_id=?`, providerKey, modelID)
	} else {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO provider_models (provider_key, model_id, upstream_model_id, context_window, max_output,
				supports_tools, supports_vision, supports_audio_input, supports_reasoning, enabled, priority, source, admin_owned, updated_at)
			VALUES (?, ?, ?, 32768, NULL, 0, 0, 0, 0, 1, 0, 'local', 1, `+nowExpression+`)
		`, providerKey, modelID, modelID)
	}
	if err != nil {
		return false, fmt.Errorf("select provider model: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit provider model selection: %w", err)
	}
	return metadataApplied, nil
}

func isGeneratedDefault(source string, contextWindow int, maxOutput sql.NullInt64, tools, vision, audio, reasoning int) bool {
	return source == string(models.SourceLocal) && contextWindow == 32768 && !maxOutput.Valid &&
		tools == 0 && vision == 0 && audio == 0 && reasoning == 0
}
