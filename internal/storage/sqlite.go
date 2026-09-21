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
	if opts.Path != ":memory:" {
		absolute, err := filepath.Abs(opts.Path)
		if err != nil {
			return nil, fmt.Errorf("resolve database path: %w", err)
		}
		if err := prepareFile(absolute); err != nil {
			return nil, err
		}
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
	if opts.Path != ":memory:" {
		var mode string
		if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
			db.Close()
			return nil, fmt.Errorf("check SQLite journal mode: %w", err)
		}
		if !strings.EqualFold(mode, "wal") {
			db.Close()
			return nil, errors.New("SQLite did not enable WAL mode")
		}
	}
	return db, nil
}

func prepareFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil // Do not change permissions on pre-existing user files.
	}
	if err != nil {
		return fmt.Errorf("create database file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close new database file: %w", err)
	}
	return nil
}
