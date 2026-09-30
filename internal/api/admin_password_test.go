package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/netx"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Options{Path: ":memory:", BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return db
}

func setupRequest(remote, body string, header map[string]string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/setup/password", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = remote
	for name, value := range header {
		request.Header.Set(name, value)
	}
	return request
}

func passwordIsSet(t *testing.T, db *sql.DB) bool {
	t.Helper()
	set, err := storage.PasswordSet(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestInitialPasswordSetupRemoteRequiresBootstrapToken(t *testing.T) {
	db := setupTestDB(t)
	token, value, err := NewBootstrapToken()
	if err != nil {
		t.Fatal(err)
	}
	handler := NewAdmin(AdminOptions{DB: db, Bootstrap: token})

	// A non-browser client: no Origin and no Fetch Metadata.
	for _, body := range []string{
		`{"password":"correct-horse-battery"}`,
		`{"password":"correct-horse-battery","bootstrap_token":"wrong"}`,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, setupRequest("203.0.113.20:43210", body, nil))
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "bootstrap_token_required") {
			t.Fatalf("remote setup without a valid token: status = %d, body = %s; want 403 bootstrap_token_required", response.Code, response.Body.String())
		}
	}
	if passwordIsSet(t, db) {
		t.Fatal("a remote client without the bootstrap token set the owner password")
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, setupRequest("203.0.113.20:43210", `{"password":"correct-horse-battery","bootstrap_token":"`+value+`"}`, nil))
	if response.Code != http.StatusCreated {
		t.Fatalf("remote setup with the token: status = %d, body = %s; want 201", response.Code, response.Body.String())
	}
	if !passwordIsSet(t, db) {
		t.Fatal("initial password was not persisted")
	}
	if token.Verify(value) {
		t.Fatal("the bootstrap token is still valid after a successful setup")
	}
}

func TestInitialPasswordSetupWithoutIssuedTokenRefusesRemote(t *testing.T) {
	db := setupTestDB(t)
	handler := NewAdmin(AdminOptions{DB: db})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, setupRequest("203.0.113.20:43210", `{"password":"correct-horse-battery","bootstrap_token":""}`, nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want 403", response.Code)
	}
}

func TestInitialPasswordSetupAllowsLocalClientWithoutToken(t *testing.T) {
	db := setupTestDB(t)
	handler := NewAdmin(AdminOptions{DB: db})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, setupRequest("127.0.0.1:43210", `{"password":"correct-horse-battery"}`, map[string]string{"Sec-Fetch-Site": "same-origin"}))
	if response.Code != http.StatusCreated {
		t.Fatalf("local setup status = %d, body = %s; want 201", response.Code, response.Body.String())
	}
}

func TestInitialPasswordSetupTreatsUntrustedLocalProxyAsRemote(t *testing.T) {
	db := setupTestDB(t)
	handler := NewAdmin(AdminOptions{DB: db})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, setupRequest("127.0.0.1:43210", `{"password":"correct-horse-battery"}`, map[string]string{"X-Forwarded-For": "198.51.100.7"}))
	if response.Code != http.StatusForbidden {
		t.Fatalf("setup relayed by an untrusted local proxy: status = %d; want 403", response.Code)
	}

	trusted, err := netx.ParseTrustedProxies([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	handler = NewAdmin(AdminOptions{DB: db, TrustedProxies: trusted})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, setupRequest("127.0.0.1:43210", `{"password":"correct-horse-battery"}`, map[string]string{"X-Forwarded-For": "198.51.100.7"}))
	if response.Code != http.StatusForbidden {
		t.Fatalf("remote client behind a trusted proxy: status = %d; want 403", response.Code)
	}
}

func TestSetupStatusReportsBootstrapRequirement(t *testing.T) {
	db := setupTestDB(t)
	handler := NewAdmin(AdminOptions{DB: db})
	for _, tc := range []struct {
		remote string
		want   bool
	}{
		{"127.0.0.1:1", false},
		{"203.0.113.20:1", true},
	} {
		request := httptest.NewRequest(http.MethodGet, "/admin/v1/setup/status", nil)
		request.RemoteAddr = tc.remote
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var status struct {
			PasswordSet            bool `json:"password_set"`
			BootstrapTokenRequired bool `json:"bootstrap_token_required"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if status.BootstrapTokenRequired != tc.want {
			t.Fatalf("%s: bootstrap_token_required = %v; want %v", tc.remote, status.BootstrapTokenRequired, tc.want)
		}
	}
}
