package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nextroad-dev/Auto-Router/internal/logging"
	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
	"github.com/nextroad-dev/Auto-Router/internal/router/auto"
	"github.com/nextroad-dev/Auto-Router/internal/router/jev"
)

type refusingAutoRouter struct{ err error }

func (r refusingAutoRouter) Route(context.Context, auto.Request) (auto.Target, error) {
	return auto.Target{}, r.err
}
func (refusingAutoRouter) Failover(context.Context, auto.Target, auto.AttemptFailure) (auto.Target, bool) {
	return auto.Target{}, false
}

type eventRecorder struct{ events []logging.Event }

func (r *eventRecorder) Record(event logging.Event) { r.events = append(r.events, event) }

type unusedExecutor struct{}

func (unusedExecutor) Do(context.Context, *providers.Request) (*providers.Response, error) {
	return nil, nil
}

type responseSequenceExecutor struct {
	responses []*providers.Response
	models    []string
}

func (e *responseSequenceExecutor) Do(_ context.Context, request *providers.Request) (*providers.Response, error) {
	e.models = append(e.models, request.Model)
	response := e.responses[0]
	e.responses = e.responses[1:]
	return response, nil
}

type statusFailoverRouter struct {
	first    auto.Target
	next     auto.Target
	failures []auto.AttemptFailure
}

func (r *statusFailoverRouter) Route(context.Context, auto.Request) (auto.Target, error) {
	return r.first, nil
}
func (r *statusFailoverRouter) Failover(_ context.Context, _ auto.Target, failure auto.AttemptFailure) (auto.Target, bool) {
	r.failures = append(r.failures, failure)
	return r.next, failure.StatusCode == http.StatusServiceUnavailable
}

func TestProxyPreservesGroupLevelJevTraceContract(t *testing.T) {
	confidence := 0.2
	mapped := jevTrace(auto.Target{JevTrace: &auto.JevTrace{
		Status: auto.JevStatusOK, InputMode: string(jev.InputModeContent),
		CandidateGroups: []auto.GroupName{auto.GroupSimple, auto.GroupMedium}, GroupCount: 2,
		CandidateCount: 5, RecommendedGroup: auto.GroupSimple, SelectedGroup: auto.GroupMedium,
		Confidence: &confidence, ConfidenceBand: "low", FallbackReason: "confidence_low",
		GroupProbabilities: []auto.GroupProbability{{Group: auto.GroupSimple, Probability: 0.4}, {Group: auto.GroupMedium, Probability: 0.6}},
	}})
	if mapped == nil {
		t.Fatal("group trace was omitted")
	}
	if err := mapped.Valid(); err != nil {
		t.Fatalf("mapped group trace violates the logging contract: %v (%+v)", err, mapped)
	}
	if mapped.RecommendedGroup != "simple" || mapped.SelectedGroup != "medium" || mapped.GroupCount != 2 || mapped.CandidateCount != 5 || len(mapped.GroupProbabilities) != 2 {
		t.Fatalf("group trace mapping lost fields: %+v", mapped)
	}
}

func TestAutomaticRoutingRetriesConfiguredStatusBeforeRelaying(t *testing.T) {
	firstBody := io.NopCloser(strings.NewReader("first response must not reach the client"))
	secondBody := io.NopCloser(strings.NewReader(`{"id":"second"}`))
	executor := &responseSequenceExecutor{responses: []*providers.Response{
		{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: firstBody},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: secondBody},
	}}
	autoRouter := &statusFailoverRouter{
		first: auto.Target{ProviderKey: "p1", Model: "m1", UpstreamModel: "m1", SelectedGroup: auto.GroupSimple, Attempts: 1, MaxAttempts: 2},
		next:  auto.Target{ProviderKey: "p2", Model: "m2", UpstreamModel: "m2", SelectedGroup: auto.GroupSimple, Attempts: 2, MaxAttempts: 2},
	}
	handler := New(Options{ProviderConfigured: true, Executor: executor, AutoRouter: autoRouter})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"second"`) {
		t.Fatalf("client response = %d %q, want the second upstream response", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "first response must not reach") {
		t.Fatalf("intermediate retry response leaked to client: %q", response.Body.String())
	}
	if len(executor.models) != 2 || executor.models[0] != "m1" || executor.models[1] != "m2" {
		t.Fatalf("upstream models = %v, want [m1 m2]", executor.models)
	}
	if len(autoRouter.failures) != 1 || autoRouter.failures[0].StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("failover failures = %+v, want one 503 failure", autoRouter.failures)
	}
}

func TestNativeAutomaticRoutingRetriesConfiguredStatusBeforeRelaying(t *testing.T) {
	providerA := models.Provider{Key: "provider-a", Kind: models.ProviderAnthropic, DisplayName: "Provider A", BaseURL: "https://provider-a.example", Enabled: true, Source: models.SourceLocal}
	providerB := models.Provider{Key: "provider-b", Kind: models.ProviderAnthropic, DisplayName: "Provider B", BaseURL: "https://provider-b.example", Enabled: true, Source: models.SourceLocal}
	catalog, err := models.NewCatalog(0, []models.Provider{providerA, providerB}, []models.Model{
		{ID: "model-a", DisplayName: "Model A", Enabled: true, Source: models.SourceLocal},
		{ID: "model-b", DisplayName: "Model B", Enabled: true, Source: models.SourceLocal},
	}, []models.Pair{
		{ProviderKey: providerA.Key, ModelID: "model-a", UpstreamModelID: "model-a", ContextWindow: 8192, Enabled: true, Source: models.SourceLocal},
		{ProviderKey: providerB.Key, ModelID: "model-b", UpstreamModelID: "model-b", ContextWindow: 8192, Enabled: true, Source: models.SourceLocal},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := models.NewStore()
	store.Swap(catalog)
	executor := &responseSequenceExecutor{responses: []*providers.Response{
		{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader("first response must not reach the client"))},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"native-second"}`))},
	}}
	autoRouter := &statusFailoverRouter{
		first: auto.Target{ProviderKey: "provider-a", Model: "model-a", UpstreamModel: "model-a", SelectedGroup: auto.GroupSimple, Attempts: 1, MaxAttempts: 2},
		next:  auto.Target{ProviderKey: "provider-b", Model: "model-b", UpstreamModel: "model-b", SelectedGroup: auto.GroupSimple, Attempts: 2, MaxAttempts: 2},
	}
	handler := New(Options{Catalog: store, ProviderConfigured: true, Executor: executor, AutoRouter: autoRouter})
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeAnthropicMessages(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "native-second") {
		t.Fatalf("native response = %d %q, want second upstream response", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "first response must not reach") || len(executor.models) != 2 || executor.models[0] != "model-a" || executor.models[1] != "model-b" {
		t.Fatalf("native failover did not replace the selected provider/model: body=%q models=%v", response.Body.String(), executor.models)
	}
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
