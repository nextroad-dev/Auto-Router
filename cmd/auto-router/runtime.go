package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/api"
	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/settings"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

func toAPICredentials(credentials []storage.InboundCredential) []api.StoredCredential {
	out := make([]api.StoredCredential, 0, len(credentials))
	for _, key := range credentials {
		if key.Active {
			out = append(out, api.StoredCredential{Name: key.Name, Hash: key.Hash, Scopes: append([]string(nil), key.Scopes...)})
		}
	}
	return out
}

func loadRegistryFromDB(ctx context.Context, db *sql.DB, logger *slog.Logger) (*models.Store, error) {
	catalog, err := storage.LoadCatalog(ctx, db)
	if err != nil {
		return nil, err
	}
	store := models.NewStore()
	snapshot := store.Swap(catalog)
	logCatalogSnapshot(logger, snapshot, true)
	return store, nil
}

func gateDebugHandler(next http.Handler, store *settings.Store, analyzerEndpoint bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if store == nil || !requestIsLoopback(r.RemoteAddr) {
			http.NotFound(w, r)
			return
		}
		cfg := store.Config()
		enabled := cfg.Routing.PolicyDebugEndpoint
		if analyzerEndpoint {
			enabled = cfg.Routing.AnalyzerDebugEndpoint
		}
		if !enabled {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestIsLoopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func loggerFor(cfg config.Config, output io.Writer) *slog.Logger {
	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.Log.Level))
	return slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level}))
}

// loadStoredSettingsIfPresent makes offline analysis commands use the effective
// SQLite overlay without creating a missing database or importing any JSON file.
func loadStoredSettingsIfPresent(ctx context.Context, cfg config.Config) (config.Config, error) {
	info, err := os.Stat(cfg.Database.Path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return config.Config{}, fmt.Errorf("inspect database: %w", err)
	}
	if info.IsDir() {
		return config.Config{}, errors.New("database path is a directory")
	}
	db, err := storage.Open(ctx, storage.Options{Path: cfg.Database.Path, BusyTimeout: time.Duration(cfg.Database.BusyTimeout)})
	if err != nil {
		return config.Config{}, err
	}
	defer db.Close()
	if err := storage.RefuseLegacyDatabase(ctx, db, 8); err != nil {
		return config.Config{}, err
	}
	var hasSettings int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='settings'`).Scan(&hasSettings); err != nil {
		return config.Config{}, err
	}
	if hasSettings == 0 {
		return cfg, nil
	}
	store, err := settings.New(ctx, settings.Options{Base: cfg, DB: db})
	if err != nil {
		return config.Config{}, err
	}
	return store.Config(), nil
}

func runAdminRecovery(ctx context.Context, cfg config.Config, output io.Writer) error { // Holding the normal service port proves that no running instance can continue
	// accepting requests while the offline credential transaction is committed.
	listener, err := net.Listen("tcp", cfg.HTTP.Address)
	if err != nil {
		return fmt.Errorf("cannot acquire fixed listener %s; stop Auto Router before recovery: %w", cfg.HTTP.Address, err)
	}
	defer listener.Close()
	if _, err := os.Stat(cfg.Database.Path); err != nil {
		return fmt.Errorf("administrator recovery requires an existing database: %w", err)
	}
	db, err := storage.Open(ctx, storage.Options{Path: cfg.Database.Path, BusyTimeout: time.Duration(cfg.Database.BusyTimeout)})
	if err != nil {
		return err
	}
	defer db.Close()
	if err := storage.RefuseLegacyDatabase(ctx, db, 8); err != nil {
		return err
	}
	password := os.Getenv("AUTO_ROUTER_RECOVERY_PASSWORD")
	if password == "" {
		return errors.New("set AUTO_ROUTER_RECOVERY_PASSWORD to a new password before running -recover-admin")
	}
	if err := storage.ResetPassword(ctx, db, password); err != nil {
		return fmt.Errorf("administrator password recovery failed: %w", err)
	}
	_, _ = fmt.Fprintln(output, "Administrator password updated.")
	return nil
}
