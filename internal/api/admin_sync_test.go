package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestRegistrySyncFailuresAreNeverReportedAsSuccess(t *testing.T) {
	for _, test := range []struct {
		name        string
		cause       error
		status      int
		code        string
		invocations int
	}{
		{name: "empty allowlist", cause: ErrSyncAllowlistEmpty, status: http.StatusUnprocessableEntity, code: "empty_allowlist", invocations: 1},
		{name: "upstream failure", cause: context.DeadlineExceeded, status: http.StatusBadGateway, code: "sync_failed", invocations: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			handler := NewAdmin(AdminOptions{SyncRegistry: func(context.Context) (any, error) {
				calls++
				return map[string]any{"synchronized": true}, test.cause
			}})
			response := settingsRequest(handler, http.MethodPost, "/admin/v1/sync", "")
			if response.Code != test.status {
				t.Fatalf("sync status=%d body=%s; want %d", response.Code, response.Body.String(), test.status)
			}
			if !strings.Contains(response.Body.String(), test.code) || strings.Contains(response.Body.String(), `"synchronized":true`) {
				t.Fatalf("failure response was not an error-only result: %s", response.Body.String())
			}
			if calls != test.invocations {
				t.Fatalf("sync callback count=%d, want %d", calls, test.invocations)
			}
		})
	}
}
