package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nextroad-dev/Auto-Router/internal/logging"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
	"github.com/nextroad-dev/Auto-Router/internal/router/auto"
)

type refusingAutoRouter struct{ err error }

func (r refusingAutoRouter) Route(context.Context, auto.Request) (auto.Target, error) {
	return auto.Target{}, r.err
}
func (refusingAutoRouter) Failover(context.Context, auto.Target, error) (auto.Target, bool) {
	return auto.Target{}, false
}

type eventRecorder struct{ events []logging.Event }

func (r *eventRecorder) Record(event logging.Event) { r.events = append(r.events, event) }

type unusedExecutor struct{}

func (unusedExecutor) Do(context.Context, *providers.Request) (*providers.Response, error) {
	return nil, nil
}

func TestAutomaticRoutingRefusalIsRecorded(t *testing.T) {
	recorder := &eventRecorder{}
	handler := New(Options{
		ProviderConfigured: true,
		Executor:           unusedExecutor{},
		AutoRouter: refusingAutoRouter{err: &auto.RefusalError{
			Status: 422, Code: auto.CodeNoEligibleCandidate, Message: "no eligible model is available",
		}},
		Recorder: recorder,
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("recorded %d events, want exactly one", len(recorder.events))
	}
	event := recorder.events[0]
	if event.ErrorCode != auto.CodeNoEligibleCandidate || event.Status != http.StatusUnprocessableEntity {
		t.Fatalf("recorded event error = %q / %d, want %q / %d", event.ErrorCode, event.Status, auto.CodeNoEligibleCandidate, http.StatusUnprocessableEntity)
	}
	if event.RequestedModel != "auto" || event.RoutingMode != "auto" {
		t.Fatalf("recorded routing identity = %q / %q, want auto / auto", event.RequestedModel, event.RoutingMode)
	}
	if err := event.Valid(); err != nil {
		t.Fatalf("recorded refusal is not storable: %v", err)
	}
}
