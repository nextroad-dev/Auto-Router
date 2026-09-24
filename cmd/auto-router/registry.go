package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/modelsdev"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

type registrySyncGate struct{ slot chan struct{} }

func newRegistrySyncGate() *registrySyncGate {
	return &registrySyncGate{slot: make(chan struct{}, 1)}
}

// Do serializes a WebUI-triggered registry import and lets a waiting request
// abandon the queue when its HTTP context is canceled.
func (g *registrySyncGate) Do(ctx context.Context, run func(context.Context) (any, error)) (any, error) {
	if g == nil || g.slot == nil {
		return nil, errors.New("registry sync gate is unavailable")
	}
	if run == nil {
		return nil, errors.New("registry sync callback is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case g.slot <- struct{}{}:
		defer func() { <-g.slot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return run(ctx)
}

// syncRegistry fetches the models.dev catalog, imports the allowlisted models
// and records the outcome. It never rewrites local overrides and never deletes
// rows. On any error the transaction is rolled back and the database keeps its
// previous registry.
func syncRegistry(ctx context.Context, cfg config.Config, db *sql.DB, logger *slog.Logger) error {
	if len(cfg.Registry.Sync.Include) == 0 {
		return errors.New("registry.sync.include is empty; refusing to synchronize an empty allowlist")
	}
	client, err := modelsdev.NewClient(cfg.Registry.Sync.URL, time.Duration(cfg.Registry.Sync.Timeout))
	if err != nil {
		return err
	}
	logger.Info("model registry sync starting", "url", client.URL(), "allowlisted_entries", len(cfg.Registry.Sync.Include))
	payload, err := client.Fetch(ctx)
	if err != nil {
		return err
	}
	document, err := modelsdev.Decode(payload)
	if err != nil {
		return err
	}
	result, err := modelsdev.Map(document, cfg.Registry.Sync.Include)
	if err != nil {
		return err
	}
	summary, err := storage.ApplyRegistryImport(ctx, db, models.SourceModelsDev, storage.Import{
		Providers: result.Providers,
		Models:    result.Models,
		Pairs:     result.Pairs,
		Sync: &storage.SyncState{
			URL:             client.URL(),
			FetchedAt:       time.Now().UTC(),
			AllowlistDigest: modelsdev.IncludeDigest(cfg.Registry.Sync.Include),
			ImportedPairs:   len(result.Pairs),
			SkippedPairs:    result.Skipped,
			Warnings:        result.Warnings,
		},
	})
	if err != nil {
		return err
	}
	logImportSummary(logger, "model registry synchronized", summary)
	for _, warning := range result.Warnings {
		logger.Warn("model registry sync warning", "detail", warning)
	}
	catalog, err := storage.LoadCatalog(ctx, db)
	if err != nil {
		return err
	}
	// Catalog warnings are suppressed here: right after a sync every freshly
	// created provider is disabled by design, and those warnings are reported at
	// the next startup where they are actionable.
	logCatalogSnapshot(logger, catalog, false)
	return nil
}

func logImportSummary(logger *slog.Logger, message string, summary storage.ImportSummary) {
	logger.Info(message,
		slog.Group("providers", "added", summary.ProvidersAdded, "updated", summary.ProvidersUpdated, "disabled", summary.ProvidersDisabled, "admin_owned", summary.ProvidersAdminOwned),
		slog.Group("models", "added", summary.ModelsAdded, "updated", summary.ModelsUpdated, "disabled", summary.ModelsDisabled, "admin_owned", summary.ModelsAdminOwned),
		slog.Group("pairs", "added", summary.PairsAdded, "updated", summary.PairsUpdated, "disabled", summary.PairsDisabled, "admin_owned", summary.PairsAdminOwned),
	)
	if summary.ProvidersAdminOwned+summary.ModelsAdminOwned+summary.PairsAdminOwned > 0 {
		// This is not a warning: skipping administrator-owned rows is the documented
		// behavior. Report it at INFO to make the source-of-truth boundary visible.
		logger.Info("registry rows owned by the Admin API were left untouched",
			"providers", summary.ProvidersAdminOwned, "models", summary.ModelsAdminOwned, "pairs", summary.PairsAdminOwned)
	}
}

func logCatalogSnapshot(logger *slog.Logger, catalog *models.Catalog, includeWarnings bool) {
	attributes := []any{
		"providers", len(catalog.Providers),
		"providers_enabled", catalog.EnabledProviders(),
		"models", len(catalog.Models),
		"models_enabled", catalog.EnabledModels(),
		"pairs", len(catalog.Pairs),
		"pairs_enabled", catalog.EnabledPairs(),
	}
	// A snapshot published through the store carries a generation; a plain
	// database read (sync mode) is not versioned yet.
	if catalog.Generation > 0 {
		attributes = append(attributes, "generation", catalog.Generation)
	}
	logger.Info("model registry loaded", attributes...)
	if catalog.Empty() {
		logger.Warn("model registry is empty; configure a Provider and model in the WebUI before routing can select a model")
	}
	if includeWarnings {
		for _, warning := range catalog.Warnings() {
			logger.Warn("model registry warning", "detail", warning)
		}
	}
}
