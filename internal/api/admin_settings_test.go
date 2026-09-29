package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/router/analyzer"
	"github.com/nextroad-dev/Auto-Router/internal/router/decision"
	"github.com/nextroad-dev/Auto-Router/internal/settings"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

func newSettingsAdminTest(t *testing.T) (*sql.DB, *settings.Store, http.Handler) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Options{Path: ":memory:", BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store, err := settings.New(ctx, settings.Options{Base: config.Defaults(), DB: db})
	if err != nil {
		t.Fatal(err)
	}
	return db, store, NewAdmin(AdminOptions{DB: db, Settings: store})
}

func settingsRequest(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestSettingsReportCoversAllRuntimeMutableFields(t *testing.T) {
	_, _, handler := newSettingsAdminTest(t)
	response := settingsRequest(handler, http.MethodGet, "/admin/v1/settings", "")
	if response.Code != http.StatusOK {
		t.Fatalf("settings status = %d, body=%s", response.Code, response.Body.String())
	}
	var report settingsPayload
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(settings.MutablePaths()) != len(report.Settings) {
		t.Fatalf("mutable paths=%d reported fields=%d, want matching catalogue and report", len(settings.MutablePaths()), len(report.Settings))
	}
	for _, field := range report.Settings {
		if !field.Mutable || field.RestartRequired {
			t.Errorf("mutable setting %q has class mutable=%t restart_required=%t", field.Path, field.Mutable, field.RestartRequired)
		}
	}
}

func TestSettingsNoLongerExposesOrAcceptsPolicyLists(t *testing.T) {
	_, store, handler := newSettingsAdminTest(t)
	paths := []string{
		"routing.policy.allow_models",
		"routing.policy.deny_models",
		"routing.policy.allow_providers",
		"routing.policy.deny_providers",
		"routing.policy.allow_pairs",
		"routing.policy.deny_pairs",
	}

	get := settingsRequest(handler, http.MethodGet, "/admin/v1/settings", "")
	if get.Code != http.StatusOK {
		t.Fatalf("settings status=%d body=%s", get.Code, get.Body.String())
	}
	var report settingsPayload
	if err := json.Unmarshal(get.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		for _, field := range report.Settings {
			if field.Path == path {
				t.Errorf("removed policy setting %q is still reported", path)
			}
		}
		if strings.Contains(get.Body.String(), path) {
			t.Errorf("removed policy setting %q is still present in the settings response", path)
		}

		leaf := strings.TrimPrefix(path, "routing.policy.")
		patch := `{"routing":{"policy":{"` + leaf + `":["model-a"]}}}`
		response := settingsRequest(handler, http.MethodPatch, "/admin/v1/settings", patch)
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "unsupported_setting") {
			t.Errorf("PATCH %s status=%d body=%s; want unsupported_setting", path, response.Code, response.Body.String())
		}
	}
	if store.Current().Version != 0 {
		t.Fatalf("rejected policy-list patches changed settings version to %d", store.Current().Version)
	}
}

func TestLegacyPolicyListsAreIgnoredAndRemovedFromTheNextSettingsWrite(t *testing.T) {
	ctx := context.Background()
	db, _, _ := newSettingsAdminTest(t)
	legacy := `{"routing":{"default_preference":"cost","policy":{"low_confidence":0.3,"high_confidence":0.8,"default_model":"model-a","cost_tiers":[{"model":"model-a","tier":0}],"latency_tiers":[],"allow_models":["model-a"],"deny_models":["model-b"],"allow_providers":["provider-a"],"deny_providers":["provider-b"],"allow_pairs":["provider-a/model-a"],"deny_pairs":["provider-b/model-b"]}}}`
	if _, err := storage.WriteSettings(ctx, db, legacy, 0); err != nil {
		t.Fatal(err)
	}
	store, err := settings.New(ctx, settings.Options{Base: config.Defaults(), DB: db})
	if err != nil {
		t.Fatalf("legacy settings overlay prevented startup: %v", err)
	}
	if store.Current().Version != 1 || store.Current().Config.Routing.Policy.LowConfidence != 0.3 {
		t.Fatalf("legacy overlay lost supported settings: version=%d low_confidence=%v", store.Current().Version, store.Current().Config.Routing.Policy.LowConfidence)
	}
	overlayJSON, err := json.Marshal(store.Current().Overlay)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"allow_models", "deny_models", "allow_providers", "deny_providers", "allow_pairs", "deny_pairs", "default_preference", "high_confidence", "default_model", "cost_tiers", "latency_tiers"} {
		if strings.Contains(string(overlayJSON), key) {
			t.Errorf("legacy key %q survived in-memory overlay normalization: %s", key, overlayJSON)
		}
	}

	candidates := []decision.Candidate{
		{ModelID: "model-a", ProviderKey: "provider-a", GatewayModel: "upstream-a", ContextWindow: 128},
		{ModelID: "model-b", ProviderKey: "provider-b", GatewayModel: "upstream-b", ContextWindow: 128},
	}
	eligible, err := store.Current().Engine.Eligible(analyzer.Features{}, candidates)
	if err != nil || len(eligible) != len(candidates) {
		t.Fatalf("legacy policy lists still restricted candidates: eligible=%v err=%v", eligible, err)
	}

	handler := NewAdmin(AdminOptions{DB: db, Settings: store})
	get := settingsRequest(handler, http.MethodGet, "/admin/v1/settings", "")
	for _, path := range []string{"allow_models", "deny_models", "allow_providers", "deny_providers", "allow_pairs", "deny_pairs", "default_preference", "high_confidence", "default_model", "cost_tiers", "latency_tiers"} {
		if strings.Contains(get.Body.String(), path) {
			t.Errorf("legacy setting %q leaked through settings GET: %s", path, get.Body.String())
		}
	}

	patch := settingsRequest(handler, http.MethodPatch, "/admin/v1/settings", `{"routing":{"policy":{"low_confidence":0.4}}}`)
	if patch.Code != http.StatusOK {
		t.Fatalf("normal settings patch after legacy cleanup status=%d body=%s", patch.Code, patch.Body.String())
	}
	record, found, err := storage.LoadSettings(ctx, db)
	if err != nil || !found {
		t.Fatalf("settings row not persisted: found=%t err=%v", found, err)
	}
	for _, key := range []string{"allow_models", "deny_models", "allow_providers", "deny_providers", "allow_pairs", "deny_pairs", "default_preference", "high_confidence", "default_model", "cost_tiers", "latency_tiers"} {
		if strings.Contains(record.JSON, key) {
			t.Errorf("legacy key %q survived next settings write: %s", key, record.JSON)
		}
	}
}

func TestSettingsPatchesPreserveOtherSectionsAndNeverEchoSecrets(t *testing.T) {
	_, store, handler := newSettingsAdminTest(t)
	for _, body := range []string{
		`{"routing":{"auto":{"default_group":"simple"}}}`,
		`{"routing":{"auto":{"failover":{"enabled":false}}}}`,
		`{"jev":{"api_key":"super-secret-value"}}`,
	} {
		response := settingsRequest(handler, http.MethodPatch, "/admin/v1/settings", body)
		if response.Code != http.StatusOK {
			t.Fatalf("patch %s status = %d, body=%s", body, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "super-secret-value") {
			t.Fatal("settings patch response echoed a Jev credential")
		}
	}
	if store.Current().Config.Routing.Auto.DefaultGroup != "simple" || store.Current().Config.Routing.Auto.Failover.Enabled {
		t.Fatalf("unrelated settings were lost: default_group=%q failover=%t", store.Current().Config.Routing.Auto.DefaultGroup, store.Current().Config.Routing.Auto.Failover.Enabled)
	}
	get := settingsRequest(handler, http.MethodGet, "/admin/v1/settings", "")
	if strings.Contains(get.Body.String(), "super-secret-value") {
		t.Fatal("settings GET echoed a Jev credential")
	}
	var report settingsPayload
	if err := json.Unmarshal(get.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	for _, field := range report.Settings {
		if field.Path == "jev.api_key" {
			if field.Set == nil || !*field.Set {
				t.Fatalf("Jev secret state = %v, want configured", field.Set)
			}
			return
		}
	}
	t.Fatal("Jev API key field missing from report")
}

func TestSettingsRejectRemovedPolicySettings(t *testing.T) {
	_, _, handler := newSettingsAdminTest(t)
	for _, body := range []string{
		`{"routing":{"default_preference":"cost"}}`,
		`{"routing":{"policy":{"high_confidence":0.8}}}`,
		`{"routing":{"policy":{"default_model":"model-a"}}}`,
		`{"routing":{"policy":{"cost_tiers":[{"model":"model-a","tier":0}]}}}`,
		`{"routing":{"policy":{"latency_tiers":[]}}}`,
	} {
		response := settingsRequest(handler, http.MethodPatch, "/admin/v1/settings", body)
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "unsupported_setting") {
			t.Fatalf("removed setting patch %s status = %d, body=%s", body, response.Code, response.Body.String())
		}
	}
}

func TestSettingsRejectInvalidUnknownAndReadOnlyPatchesWithoutPublishing(t *testing.T) {
	db, store, _ := newSettingsAdminTest(t)
	var logs strings.Builder
	handler := NewAdmin(AdminOptions{
		DB:       db,
		Settings: store,
		Logger:   slog.New(slog.NewTextHandler(&logs, nil)),
	})

	cases := []struct {
		name       string
		body       string
		status     int
		code       string
		mustNotLog string
	}{
		{
			name:       "unknown field",
			body:       `{"routing":{"private_unknown":"private-unknown-value"}}`,
			status:     http.StatusUnprocessableEntity,
			code:       "unsupported_setting",
			mustNotLog: "private-unknown-value",
		},
		{
			name:       "read-only field",
			body:       `{"http":{"address":"127.0.0.1:9191"}}`,
			status:     http.StatusUnprocessableEntity,
			code:       "unsupported_setting",
			mustNotLog: "9191",
		},
		{
			name:       "invalid value",
			body:       `{"routing":{"auto":{"default_group":"private-invalid-value"}}}`,
			status:     http.StatusBadRequest,
			code:       "invalid_request",
			mustNotLog: "private-invalid-value",
		},
		{
			name:       "invalid Jev settings with secret",
			body:       `{"jev":{"enabled":true,"base_url":"not-a-url","model":"private-model","api_key":"private-jev-secret"}}`,
			status:     http.StatusBadRequest,
			code:       "invalid_request",
			mustNotLog: "private-jev-secret",
		},
	}

	initial := store.Current()
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := settingsRequest(handler, http.MethodPatch, "/admin/v1/settings", test.body)
			if response.Code != test.status {
				t.Fatalf("status = %d, body=%s; want %d", response.Code, response.Body.String(), test.status)
			}
			if !strings.Contains(response.Body.String(), test.code) {
				t.Fatalf("response %q does not contain error code %q", response.Body.String(), test.code)
			}
			if strings.Contains(response.Body.String(), test.mustNotLog) || strings.Contains(logs.String(), test.mustNotLog) {
				t.Fatalf("sensitive patch value was disclosed in response or logs: response=%s logs=%s", response.Body.String(), logs.String())
			}
			if store.Current() != initial {
				t.Fatal("rejected patch published a new runtime snapshot")
			}
			if _, found, err := storage.LoadSettings(context.Background(), db); err != nil || found {
				t.Fatalf("rejected patch reached storage: found=%t err=%v", found, err)
			}
		})
	}
}

func TestSettingsPersistAcrossStoreRestartAndResetOnlySettings(t *testing.T) {
	ctx := context.Background()
	db, _, handler := newSettingsAdminTest(t)
	if err := storage.CreateProvider(ctx, db, storage.NewProvider{Key: "preserved-provider", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateModel(ctx, db, storage.NewModel{ID: "preserved-model", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateInboundCredential(ctx, db, storage.InboundCredential{
		Name: "preserved-key", Hash: strings.Repeat("a", 64), Scopes: []string{"inference"}, Active: true,
	}); err != nil {
		t.Fatal(err)
	}

	const secret = "restart-persisted-jev-secret"
	patch := settingsRequest(handler, http.MethodPatch, "/admin/v1/settings", `{"routing":{"auto":{"default_group":"simple"}},"jev":{"api_key":"`+secret+`"}}`)
	if patch.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", patch.Code, patch.Body.String())
	}
	if strings.Contains(patch.Body.String(), secret) {
		t.Fatal("successful settings response exposed the Jev key")
	}

	restarted, err := settings.New(ctx, settings.Options{Base: config.Defaults(), DB: db})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Current().Config.Routing.Auto.DefaultGroup != "simple" || restarted.Current().Config.Jev.APIKey != secret {
		t.Fatal("runtime settings did not survive store recreation")
	}

	resetHandler := NewAdmin(AdminOptions{DB: db, Settings: restarted})
	reset := settingsRequest(resetHandler, http.MethodDelete, "/admin/v1/settings", "")
	if reset.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", reset.Code, reset.Body.String())
	}
	if strings.Contains(reset.Body.String(), secret) {
		t.Fatal("settings reset response exposed the Jev key")
	}
	if restarted.Current().Config.Jev.APIKey != "" || restarted.HasOverlay() {
		t.Fatal("reset did not clear the runtime overlay and Jev key")
	}
	if _, found, err := storage.LoadSettings(ctx, db); err != nil || found {
		t.Fatalf("runtime settings row survived reset: found=%t err=%v", found, err)
	}
	if _, found, err := storage.GetProvider(ctx, db, "preserved-provider"); err != nil || !found {
		t.Fatalf("reset removed the provider: found=%t err=%v", found, err)
	}
	if _, found, err := storage.GetModel(ctx, db, "preserved-model"); err != nil || !found {
		t.Fatalf("reset removed the model: found=%t err=%v", found, err)
	}
	keys, err := storage.LoadInboundCredentials(ctx, db)
	if err != nil || len(keys) != 1 || keys[0].Name != "preserved-key" {
		t.Fatalf("reset changed inbound credentials: keys=%+v err=%v", keys, err)
	}
}

func TestSettingsPatchConfiguresGroupAndRetryPolicyAtomically(t *testing.T) {
	_, store, handler := newSettingsAdminTest(t)
	response := settingsRequest(handler, http.MethodPatch, "/admin/v1/settings", `{"routing":{"auto":{"default_group":"complex","failover":{"max_attempts":4,"retry_on":{"pre_request_failure":false,"timeout":true,"status_codes":[429,503]}}}}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("valid retry policy patch status=%d body=%s", response.Code, response.Body.String())
	}
	cfg := store.Current().Config.Routing.Auto
	if cfg.DefaultGroup != "complex" || cfg.Failover.MaxAttempts != 4 || cfg.Failover.RetryOn.PreRequestFailure || !cfg.Failover.RetryOn.Timeout || len(cfg.Failover.RetryOn.StatusCodes) != 2 || cfg.Failover.RetryOn.StatusCodes[0] != 429 || cfg.Failover.RetryOn.StatusCodes[1] != 503 {
		t.Fatalf("runtime retry policy = %+v", cfg)
	}
	version := store.Current().Version
	invalid := settingsRequest(handler, http.MethodPatch, "/admin/v1/settings", `{"routing":{"auto":{"failover":{"retry_on":{"status_codes":[401]}}}}}`)
	if invalid.Code == http.StatusOK {
		t.Fatalf("unsafe status code patch was accepted: %s", invalid.Body.String())
	}
	if store.Current().Version != version || store.Current().Config.Routing.Auto.Failover.RetryOn.StatusCodes[0] != 429 {
		t.Fatal("invalid retry-policy patch changed the running configuration")
	}
}

func TestSettingsResetAndConflictResponses(t *testing.T) {
	db, store, handler := newSettingsAdminTest(t)
	if response := settingsRequest(handler, http.MethodPatch, "/admin/v1/settings", `{"routing":{"auto":{"default_group":"simple"}}}`); response.Code != http.StatusOK {
		t.Fatalf("initial patch status = %d, body=%s", response.Code, response.Body.String())
	}

	// Simulate a second process updating the row behind this Store's published
	// snapshot. Its next write must receive the documented conflict.
	if _, err := storage.WriteSettings(context.Background(), db, `{"routing":{"auto":{"default_group":"complex"}}}`, 1); err != nil {
		t.Fatal(err)
	}
	conflict := settingsRequest(handler, http.MethodPatch, "/admin/v1/settings", `{"routing":{"auto":{"default_group":"medium"}}}`)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, body=%s", conflict.Code, conflict.Body.String())
	}

	reset := settingsRequest(handler, http.MethodDelete, "/admin/v1/settings", "")
	if reset.Code != http.StatusOK {
		t.Fatalf("reset status = %d, body=%s", reset.Code, reset.Body.String())
	}
	if store.Current().Config.Routing.Auto.DefaultGroup != config.Defaults().Routing.Auto.DefaultGroup || store.HasOverlay() {
		t.Fatalf("reset did not restore defaults: default_group=%q overlay=%t", store.Current().Config.Routing.Auto.DefaultGroup, store.HasOverlay())
	}
}
