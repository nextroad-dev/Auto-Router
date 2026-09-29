package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nextroad-dev/Auto-Router/internal/router/auto"
	"github.com/nextroad-dev/Auto-Router/internal/router/decision"
	"github.com/nextroad-dev/Auto-Router/internal/router/policy"
)

type fakeDebugRouter struct {
	got    auto.Request
	target auto.Target
	err    error
}

func (f *fakeDebugRouter) Route(_ context.Context, request auto.Request) (auto.Target, error) {
	f.got = request
	return f.target, f.err
}

func postDebugRoute(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/debug/route", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestDebugRouteUsesRouterDecision(t *testing.T) {
	confidence := 0.8
	router := &fakeDebugRouter{target: auto.Target{
		ProviderKey: "p1", Model: "m1", UpstreamModel: "up-m1",
		SelectionMode: string(decision.OriginFirstEligible), SelectedGroup: auto.GroupComplex, MaxAttempts: 2,
		Decision: decision.Decision{Provider: "p1", Model: "m1", GatewayModel: "up-m1", ConfidenceBand: decision.BandLow},
		JevTrace: &auto.JevTrace{
			Status: auto.JevStatusOK, InputMode: "features_only",
			CandidateGroups: []auto.GroupName{auto.GroupMedium, auto.GroupComplex}, CandidateCount: 3,
			RecommendedGroup: auto.GroupComplex, SelectedGroup: auto.GroupComplex,
			Confidence: &confidence, ConfidenceBand: "high", FallbackReason: "none",
			GroupProbabilities: []auto.GroupProbability{{Group: auto.GroupComplex, Probability: 0.8}},
		},
	}}
	handler := NewDebugRoute(RouteDebugOptions{Router: router})
	recorder := postDebugRoute(t, handler, `{"protocol":"chat_completions","body":{"model":"auto","messages":[{"role":"user","content":"hi"}]}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if router.got.Protocol != "chat_completions" || router.got.RequestID == "" {
		t.Fatalf("router request = %+v", router.got)
	}
	var response debugRouteResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Target.Provider != "p1" || response.Target.Model != "m1" || response.Target.SelectedGroup != "complex" || response.Target.UpstreamModel != "up-m1" {
		t.Fatalf("target = %+v", response.Target)
	}
	if response.Jev == nil || response.Jev.RecommendedGroup != "complex" || len(response.Jev.GroupProbabilities) != 1 || len(response.Jev.CandidateGroups) != 2 {
		t.Fatalf("jev = %+v", response.Jev)
	}
	if response.Decision.Provider != "p1" {
		t.Fatalf("decision = %+v", response.Decision)
	}
}

func TestDebugRouteReportsRefusalExclusions(t *testing.T) {
	router := &fakeDebugRouter{err: &auto.RefusalError{
		Status: http.StatusUnprocessableEntity, Code: auto.CodeNoEligibleCandidate, Message: "no candidate can serve this request",
		Refusal: policy.Refusal{
			Exclusions: []decision.Exclusion{{ModelID: "m1", ProviderKey: "p1", Code: decision.ExclusionRequiresTools}},
			Count:      1, Reason: decision.ReasonNoCandidates,
		},
	}}
	handler := NewDebugRoute(RouteDebugOptions{Router: router})
	recorder := postDebugRoute(t, handler, `{"protocol":"chat_completions","body":{"messages":[]}}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Error    struct{ Code string } `json:"error"`
		Decision debugRouteRefusal     `json:"decision"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != auto.CodeNoEligibleCandidate || envelope.Decision.ExcludedCount != 1 ||
		len(envelope.Decision.Excluded) != 1 || envelope.Decision.Fallback.Reason != "no_candidates" {
		t.Fatalf("envelope = %+v", envelope)
	}
}

func TestDebugRouteRejectsLegacyCandidateInput(t *testing.T) {
	router := &fakeDebugRouter{}
	handler := NewDebugRoute(RouteDebugOptions{Router: router})
	recorder := postDebugRoute(t, handler, `{"protocol":"chat_completions","body":{},"candidates":[{"provider":"p","model":"m"}]}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	recorder = postDebugRoute(t, handler, `{"protocol":"chat_completions","body":{},"preference":"cost"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("retired preference field status = %d, want 400", recorder.Code)
	}
}
