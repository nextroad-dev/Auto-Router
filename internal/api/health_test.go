package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

type readinessFunc func(context.Context) error

func (f readinessFunc) PingContext(ctx context.Context) error { return f(ctx) }

func TestHealthRoutes(t *testing.T) {
	cases := []struct {
		name, method, path string
		dbError            error
		status             int
		body               string
		calls              int
	}{
		{"liveness", "GET", "/healthz", errors.New("database offline"), 200, "{\"status\":\"ok\"}\n", 0},
		{"readiness", "GET", "/readyz", nil, 200, "{\"status\":\"ready\"}\n", 1},
		{"unavailable", "GET", "/readyz", errors.New("secret path and credentials"), 503, "{\"status\":\"not_ready\"}\n", 1},
		{"head liveness", "HEAD", "/healthz", nil, 200, "", 0},
		{"head readiness", "HEAD", "/readyz", nil, 200, "", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			handler := NewHandler(readinessFunc(func(context.Context) error {
				calls++
				return tc.dbError
			}), time.Second)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
			if response.Code != tc.status || response.Body.String() != tc.body || calls != tc.calls {
				t.Fatalf("status=%d body=%q calls=%d", response.Code, response.Body.String(), calls)
			}
			if response.Header().Get("Content-Type") != "application/json" || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("unexpected headers: %v", response.Header())
			}
		})
	}
}

func TestOnlyImplementedEndpointsAreRegistered(t *testing.T) {
	handler := NewHandler(nil, time.Second)
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"POST", "/healthz", http.StatusMethodNotAllowed},
		{"POST", "/readyz", http.StatusMethodNotAllowed},
		{"GET", "/", http.StatusNotFound},
		{"POST", "/v1/chat/completions", http.StatusNotFound},
		{"POST", "/v1/responses", http.StatusNotFound},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
		if response.Code != tc.status {
			t.Fatalf("%s %s: got %d, want %d", tc.method, tc.path, response.Code, tc.status)
		}
	}
}

func TestReadinessHasDeadlineAndHonorsCancellation(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		if cancelRequest {
			cancel()
		}
		var observed error
		handler := NewHandler(readinessFunc(func(ctx context.Context) error {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("readiness check has no deadline")
			}
			<-ctx.Done()
			observed = ctx.Err()
			return observed
		}), 10*time.Millisecond)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "/readyz", nil).WithContext(ctx))
		cancel()
		want := context.DeadlineExceeded
		if cancelRequest {
			want = context.Canceled
		}
		if !errors.Is(observed, want) || response.Code != http.StatusServiceUnavailable {
			t.Fatalf("readiness did not propagate %v: status=%d error=%v", want, response.Code, observed)
		}
	}
}

func TestNoCheckerIsNotReady(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler(nil, time.Second).ServeHTTP(response, httptest.NewRequest("GET", "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing database should not be ready: %d", response.Code)
	}
}

func TestReadinessWithRealSQLite(t *testing.T) {
	db, err := storage.Open(t.Context(), storage.Options{
		Path: filepath.Join(t.TempDir(), "router.db"), BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(db, time.Second)
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest("GET", "/readyz", nil))
	if ready.Code != http.StatusOK {
		t.Fatal("migrated database should be ready")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for path, status := range map[string]int{"/healthz": 200, "/readyz": 503} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != status || strings.Contains(response.Body.String(), "closed") {
			t.Fatalf("%s after closing database: status=%d body=%q", path, response.Code, response.Body.String())
		}
	}
}
