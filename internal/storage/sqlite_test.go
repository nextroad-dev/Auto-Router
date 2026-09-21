package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(t.Context(), Options{
		Path:        filepath.Join(t.TempDir(), "router.db"),
		BusyTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSQLitePersistenceAndEscapedPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "folder #100%", "auto router.db")
	opts := Options{Path: path, BusyTimeout: 50 * time.Millisecond}
	db, err := Open(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE example (id INTEGER PRIMARY KEY, value TEXT NOT NULL); INSERT INTO example VALUES (1, 'persisted')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database was not created at the literal configured path: %v", err)
	}
	reopened, err := Open(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var value string
	if err := reopened.QueryRowContext(t.Context(), "SELECT value FROM example WHERE id = 1").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "persisted" {
		t.Fatalf("unexpected persisted value: %q", value)
	}
}

func TestSQLitePragmasSurviveConnectionReplacement(t *testing.T) {
	db := testDB(t)
	for attempt := 0; attempt < 2; attempt++ {
		var mode string
		if err := db.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
			t.Fatalf("journal mode=%q, err=%v", mode, err)
		}
		var foreignKeys, busyTimeout int
		if err := db.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
			t.Fatalf("foreign_keys=%d, err=%v", foreignKeys, err)
		}
		if err := db.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil || busyTimeout != 50 {
			t.Fatalf("busy_timeout=%d, err=%v", busyTimeout, err)
		}
		// Close the idle physical connection, then allow a replacement.
		db.SetMaxIdleConns(0)
		db.SetMaxIdleConns(1)
	}
	if db.Stats().MaxOpenConnections != 1 {
		t.Fatal("expected a single-connection pool")
	}
	if _, err := db.ExecContext(t.Context(), `
		CREATE TABLE parent (id INTEGER PRIMARY KEY);
		CREATE TABLE child (parent_id INTEGER REFERENCES parent(id));
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO child VALUES (99)"); err == nil {
		t.Fatal("foreign key violation should fail")
	}
}

func TestMemoryDatabasesAreIsolated(t *testing.T) {
	opts := Options{Path: ":memory:", BusyTimeout: time.Millisecond}
	first, err := Open(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := first.ExecContext(t.Context(), "CREATE TABLE only_first (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := second.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name = 'only_first'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("in-memory databases must not share data: count=%d err=%v", count, err)
	}
}

func TestOpenRejectsInvalidOptions(t *testing.T) {
	cases := []Options{
		{Path: "", BusyTimeout: time.Second},
		{Path: "\x00", BusyTimeout: time.Second},
		{Path: "file:shared?mode=memory", BusyTimeout: time.Second},
		{Path: ":memory:", BusyTimeout: 0},
		{Path: ":memory:", BusyTimeout: time.Microsecond},
		{Path: ":memory:", BusyTimeout: 1500 * time.Microsecond},
		{Path: ":memory:", BusyTimeout: (1 << 31) * time.Millisecond},
	}
	for _, opts := range cases {
		db, err := Open(t.Context(), opts)
		if err == nil {
			db.Close()
			t.Fatalf("expected error for options %+v", opts)
		}
	}
}

func TestOpenCanceledHasNoFilesystemSideEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	path := filepath.Join(t.TempDir(), "not-created", "router.db")
	_, err := Open(ctx, Options{Path: path, BusyTimeout: time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled open should not create directories: %v", err)
	}
}

func TestOpenRejectsCorruptFileAndInvalidDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-database")
	if err := os.WriteFile(path, []byte("this is not SQLite"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{path, filepath.Join(path, "router.db")} {
		db, err := Open(t.Context(), Options{Path: invalid, BusyTimeout: time.Millisecond})
		if err == nil {
			db.Close()
			t.Fatalf("expected error opening %q", invalid)
		}
	}
}

func TestSQLiteWriterLockIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.db")
	opts := Options{Path: path, BusyTimeout: 25 * time.Millisecond}
	first, err := Open(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := first.ExecContext(t.Context(), "CREATE TABLE example (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	tx, err := first.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), "INSERT INTO example VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := second.ExecContext(ctx, "INSERT INTO example VALUES (2)"); err == nil {
		t.Fatal("second writer should fail while the lock is held")
	}
	if ctx.Err() != nil {
		t.Fatal("busy timeout should expire before the request deadline")
	}
}

// The registry stores provider API keys in plaintext, so POSIX platforms must
// keep the database and its WAL sidecars owner-only. Windows has no equivalent
// mode bits and is excluded rather than falsely asserted.
func TestDatabaseFilesAreOwnerOnlyOnPOSIX(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows has no POSIX mode bits")
	}
	path := filepath.Join(t.TempDir(), "router.db")
	// A pre-existing file with loose permissions must be tightened, not trusted.
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := Open(t.Context(), Options{Path: path, BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	// Mirror the real startup order: migrations are the first write, then the
	// sidecars they created are tightened.
	if err := HardenDatabaseFiles(path); err != nil {
		t.Fatal(err)
	}
	// Force the sidecars to exist: on SQLite versions that already created them
	// during the migrations this changes nothing.
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE example (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s should exist after a write transaction", filepath.Base(candidate))
		}
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			t.Fatalf("%s mode is %04o, want no group/other bits", filepath.Base(candidate), mode)
		}
	}
	// The sidecars survive a close only until the last connection releases them;
	// after reopening they must still be tightened.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), Options{Path: path, BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := Migrate(t.Context(), reopened); err != nil {
		t.Fatal(err)
	}
	if err := HardenDatabaseFiles(path); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ExecContext(t.Context(), "INSERT INTO example VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Fatalf("reopened -wal mode is %04o, want no group/other bits", mode)
	}
}
