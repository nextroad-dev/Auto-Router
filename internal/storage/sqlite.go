// Package storage owns SQLite setup and schema migrations, not routing policy.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Options struct {
	Path        string
	BusyTimeout time.Duration
}

// Open creates an isolated database/sql pool. Pragmas are applied by the driver
// for every physical connection, including connections reopened by the pool.
// Schema initialization is explicit through Migrate.
func Open(ctx context.Context, opts Options) (*sql.DB, error) {
	if strings.TrimSpace(opts.Path) == "" || strings.ContainsRune(opts.Path, '\x00') {
		return nil, errors.New("database path is required")
	}
	if strings.HasPrefix(strings.ToLower(opts.Path), "file:") {
		return nil, errors.New("database path must not be a SQLite URI")
	}
	if opts.BusyTimeout < time.Millisecond || opts.BusyTimeout%time.Millisecond != 0 || opts.BusyTimeout > (1<<31-1)*time.Millisecond {
		return nil, errors.New("database busy timeout must be between 1 and 2147483647 whole milliseconds")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	name := ":memory:"
	databasePath := ""
	if opts.Path != ":memory:" {
		absolute, err := filepath.Abs(opts.Path)
		if err != nil {
			return nil, fmt.Errorf("resolve database path: %w", err)
		}
		if err := prepareFile(absolute); err != nil {
			return nil, err
		}
		databasePath = absolute
		// A file URI prevents #, %, spaces or query-like path components from
		// being mistaken for SQLite connection options. Prefix Windows drives
		// with / to obtain file:///C:/... rather than a URI authority.
		uriPath := filepath.ToSlash(absolute)
		if !strings.HasPrefix(uriPath, "/") {
			uriPath = "/" + uriPath
		}
		name = (&url.URL{Scheme: "file", Path: uriPath}).String()
	}
	params := url.Values{}
	params.Add("_pragma", "busy_timeout("+strconv.FormatInt(opts.BusyTimeout.Milliseconds(), 10)+")")
	params.Add("_pragma", "foreign_keys(1)")
	params.Add("_pragma", "journal_mode(WAL)")
	db, err := sql.Open("sqlite", name+"?"+params.Encode())
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	// A single connection is deliberate for the V1 single-process deployment.
	// It also keeps a :memory: database alive for the lifetime of this pool.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect SQLite: %w", err)
	}
	if databasePath != "" {
		var mode string
		if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
			db.Close()
			return nil, fmt.Errorf("check SQLite journal mode: %w", err)
		}
		if !strings.EqualFold(mode, "wal") {
			db.Close()
			return nil, errors.New("SQLite did not enable WAL mode")
		}
		// The database stores provider API keys in plaintext, so the file and
		// its WAL sidecars must not be group/world readable. Sidecars do not
		// necessarily exist yet: SQLite creates them on the first write
		// transaction, so callers run HardenDatabaseFiles again after
		// migrations. Windows has no equivalent mode bits; there the default
		// ACL applies (a documented residual risk).
		if err := HardenDatabaseFiles(databasePath); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

// HardenDatabaseFiles narrows the database file and its -wal/-shm sidecars to
// owner-only access on platforms with POSIX mode bits. Missing sidecars are
// fine: SQLite creates them lazily, so callers invoke this again after the first
// write transaction (migrations) to tighten files that did not exist earlier.
func HardenDatabaseFiles(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(candidate, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("restrict permissions on %s: %w", filepath.Base(candidate), err)
		}
	}
	return nil
}

func prepareFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil // Open narrows pre-existing files to 0600 after connecting.
	}
	if err != nil {
		return fmt.Errorf("create database file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close new database file: %w", err)
	}
	return nil
}
