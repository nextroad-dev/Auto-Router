package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

func writeRuntimeConfig(t *testing.T, cfg config.Config) string {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type startupLog struct {
	address chan string
}

func (w startupLog) Write(p []byte) (int, error) {
	var event struct {
		Message string `json:"msg"`
		Address string `json:"address"`
	}
	if err := json.Unmarshal(p, &event); err != nil {
		return 0, err
	}
	if event.Message == "service started" {
		w.address <- event.Address
	}
	return len(p), nil
}

func TestRunFullLifecycle(t *testing.T) {
	cfg := config.Defaults()
	cfg.HTTP.Address = "127.0.0.1:0"
	cfg.Database.Path = filepath.Join(t.TempDir(), "data", "router.db")
	path := writeRuntimeConfig(t, cfg)
	ctx, cancel := context.WithCancel(t.Context())
	output := startupLog{address: make(chan string, 1)}
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = run(ctx, []string{"-config", path}, output)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		waitSignal(t, done)
	})
	var address string
	select {
	case address = <-output.address:
	case <-done:
		t.Fatalf("startup failed: %v", runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("service did not start")
	}
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	for _, endpoint := range []string{"/healthz", "/readyz"} {
		response, err := client.Get("http://" + address + endpoint)
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("%s: status=%d read error=%v", endpoint, response.StatusCode, readErr)
		}
	}
	cancel()
	waitSignal(t, done)
	if runErr != nil {
		t.Fatalf("normal cancellation should shut down cleanly: %v", runErr)
	}
	// Reopen the exact persisted database after shutdown, not a mocked store.
	db, err := storage.Open(t.Context(), storage.Options{Path: cfg.Database.Path, BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(t.Context(), db); err != nil {
		t.Fatalf("startup did not leave a valid schema: %v", err)
	}
}

func TestRunFailsBeforeListeningOnNewerSchema(t *testing.T) {
	cfg := config.Defaults()
	cfg.HTTP.Address = "127.0.0.1:0"
	cfg.Database.Path = filepath.Join(t.TempDir(), "router.db")
	db, err := storage.Open(t.Context(), storage.Options{Path: cfg.Database.Path, BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO schema_migrations (version, name, checksum) VALUES (1, 'future', 'future')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err = run(ctx, []string{"-config", writeRuntimeConfig(t, cfg)}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("startup should reject a newer database before listening, got %v", err)
	}
}

func TestRunReportsStorageAndBindErrors(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cfg := config.Defaults()
	cfg.HTTP.Address = listener.Addr().String()
	cfg.Database.Path = filepath.Join(t.TempDir(), "router.db")
	if err := run(t.Context(), []string{"-config", writeRuntimeConfig(t, cfg)}, io.Discard); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("expected address-in-use error, got %v", err)
	}
	// A regular file cannot host a database directory, which fails during
	// storage setup and must be reported before any listener is bound.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Database.Path = filepath.Join(blocker, "data", "router.db")
	if err := run(t.Context(), []string{"-config", writeRuntimeConfig(t, cfg)}, io.Discard); err == nil || strings.Contains(err.Error(), "listen") {
		t.Fatalf("expected storage failure before listen, got %v", err)
	}
}
