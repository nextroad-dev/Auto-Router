package auto

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
	"github.com/nextroad-dev/Auto-Router/internal/router/analyzer"
	"github.com/nextroad-dev/Auto-Router/internal/router/decision"
	"github.com/nextroad-dev/Auto-Router/internal/router/jev"
)

type groupJevStub struct {
	selected   string
	confidence float64
	candidates []jev.Candidate
}

func (j *groupJevStub) Route(_ context.Context, request jev.Request) (jev.Result, error) {
	j.candidates = append([]jev.Candidate(nil), request.Candidates...)
	probabilities := make([]jev.CandidateProbability, 0, len(request.Candidates))
	for _, candidate := range request.Candidates {
		probabilities = append(probabilities, jev.CandidateProbability{Model: candidate.Model, Probability: 1 / float64(len(request.Candidates))})
	}
	return jev.Result{Selected: j.selected, Confidence: j.confidence, Probabilities: probabilities}, nil
}

func TestRouteUsesOnlyAvailableGroupsAndFallsBackOnLowConfidence(t *testing.T) {
	catalog := testAutoCatalog(t)
	engine, err := config.Defaults().PolicyEngine()
	if err != nil {
		t.Fatal(err)
	}
	caller := &groupJevStub{selected: string(GroupSimple), confidence: 0.2}
	router := New(Options{
		Catalog: catalog, Analyzer: analyzer.New(), Engine: engine, Jev: caller,
		InputMode:     jev.InputModeContent,
		DefaultGroup:  GroupMedium,
		LowConfidence: 0.3,
		GroupConfig: GroupConfig{
			Simple:  []GroupMember{{ProviderKey: "provider-a", ModelID: "model-a"}},
			Medium:  []GroupMember{{ProviderKey: "provider-a", ModelID: "model-b"}, {ProviderKey: "provider-a", ModelID: "model-c"}},
			Complex: nil,
		},
	})
	body, _ := json.Marshal(map[string]any{"model": "auto", "messages": []any{map[string]any{"role": "user", "content": "Summarize this."}}})
	target, err := router.Route(context.Background(), Request{Protocol: providers.ProtocolChatCompletions, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if target.SelectedGroup != GroupMedium || target.Model != "model-b" {
		t.Fatalf("selected target = %s/%s, want medium's first member model-b", target.SelectedGroup, target.Model)
	}
	if target.MaxAttempts != 2 || target.Attempts != 1 {
		t.Fatalf("attempt snapshot = %d/%d, want 1 of 2", target.Attempts, target.MaxAttempts)
	}
	if target.JevTrace == nil || target.JevTrace.RecommendedGroup != GroupSimple || target.JevTrace.SelectedGroup != GroupMedium || target.JevTrace.FallbackReason != "confidence_low" {
		t.Fatalf("unexpected Jev group trace: %+v", target.JevTrace)
	}
	if target.JevTrace.GroupCount != 2 || target.JevTrace.CandidateCount != 3 {
		t.Fatalf("group and pair counts were conflated: groups=%d pairs=%d", target.JevTrace.GroupCount, target.JevTrace.CandidateCount)
	}
	if len(target.JevTrace.GroupProbabilities) != 2 || target.JevTrace.GroupProbabilities[0].Group != GroupSimple || target.JevTrace.GroupProbabilities[1].Group != GroupMedium {
		t.Fatalf("group probability tie order = %+v, want simple then medium", target.JevTrace.GroupProbabilities)
	}
	if len(caller.candidates) != 2 || caller.candidates[0].Model != "simple" || caller.candidates[1].Model != "medium" {
		t.Fatalf("Jev candidates = %+v, want only the non-empty simple and medium groups", caller.candidates)
	}
	if got := target.Decision; got.ConfidenceBand != decision.BandLow || !got.Fallback.Used || got.Fallback.Reason != decision.ReasonConfidenceLow || got.Confidence != 0.2 {
		t.Fatalf("decision = band %q fallback %+v confidence %v, want low/confidence_low/0.2", got.ConfidenceBand, got.Fallback, got.Confidence)
	}
}

func TestRouteRecordsAdoptedGroupInDecision(t *testing.T) {
	catalog := testAutoCatalog(t)
	engine, err := config.Defaults().PolicyEngine()
	if err != nil {
		t.Fatal(err)
	}
	router := New(Options{
		Catalog: catalog, Analyzer: analyzer.New(), Engine: engine,
		Jev:       &groupJevStub{selected: string(GroupSimple), confidence: 0.9},
		InputMode: jev.InputModeContent, DefaultGroup: GroupMedium, LowConfidence: 0.3,
		GroupConfig: GroupConfig{
			Simple: []GroupMember{{ProviderKey: "provider-a", ModelID: "model-a"}},
			Medium: []GroupMember{{ProviderKey: "provider-a", ModelID: "model-b"}},
		},
	})
	body, _ := json.Marshal(map[string]any{"model": "auto", "messages": []any{map[string]any{"role": "user", "content": "Hi."}}})
	target, err := router.Route(context.Background(), Request{Protocol: providers.ProtocolChatCompletions, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	got := target.Decision
	if target.SelectedGroup != GroupSimple || target.Model != "model-a" {
		t.Fatalf("selected target = %s/%s, want simple/model-a", target.SelectedGroup, target.Model)
	}
	if got.ConfidenceBand != decision.BandHigh || got.Fallback.Used || got.Fallback.Reason != decision.ReasonNone || got.Confidence != 0.9 {
		t.Fatalf("decision = band %q fallback %+v confidence %v, want high/none/0.9", got.ConfidenceBand, got.Fallback, got.Confidence)
	}
	if !strings.Contains(got.Reason, "band=high;origin=first_eligible;fallback=none") {
		t.Fatalf("reason = %q", got.Reason)
	}
}

func TestRouteFailsWhenConfiguredDefaultGroupHasNoEligibleMembers(t *testing.T) {
	catalog := testAutoCatalog(t)
	engine, err := config.Defaults().PolicyEngine()
	if err != nil {
		t.Fatal(err)
	}
	router := New(Options{
		Catalog: catalog, Analyzer: analyzer.New(), Engine: engine,
		InputMode:    jev.InputModeContent,
		DefaultGroup: GroupComplex,
		GroupConfig: GroupConfig{
			Simple: []GroupMember{{ProviderKey: "provider-a", ModelID: "model-a"}},
			Medium: []GroupMember{{ProviderKey: "provider-a", ModelID: "model-b"}},
		},
	})
	body := []byte(`{"model":"auto","messages":[{"role":"user","content":"hello"}]}`)
	_, err = router.Route(context.Background(), Request{Protocol: providers.ProtocolChatCompletions, Body: body})
	var refusal *RefusalError
	if !errors.As(err, &refusal) || refusal.Code != CodeNoEligibleCandidate {
		t.Fatalf("route error = %v, want no-eligible-candidate refusal without changing groups", err)
	}
}

func TestRouteUsesConfiguredDefaultGroupWhenJevIsDisabled(t *testing.T) {
	catalog := testAutoCatalog(t)
	engine, err := config.Defaults().PolicyEngine()
	if err != nil {
		t.Fatal(err)
	}
	router := New(Options{
		Catalog: catalog, Analyzer: analyzer.New(), Engine: engine,
		InputMode:    jev.InputModeContent,
		DefaultGroup: GroupSimple,
		GroupConfig: GroupConfig{
			Simple: []GroupMember{{ProviderKey: "provider-a", ModelID: "model-a"}},
			Medium: []GroupMember{{ProviderKey: "provider-a", ModelID: "model-b"}},
		},
	})
	body := []byte(`{"model":"auto","messages":[{"role":"user","content":"hello"}]}`)
	target, err := router.Route(context.Background(), Request{Protocol: providers.ProtocolChatCompletions, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if target.SelectedGroup != GroupSimple || target.Model != "model-a" {
		t.Fatalf("target = %s/%s, want configured simple default model-a", target.SelectedGroup, target.Model)
	}
	if target.JevStatus != JevStatusDisabled || target.JevTrace.FallbackReason != "not_requested" {
		t.Fatalf("Jev state = %q/%q, want disabled/not_requested", target.JevStatus, target.JevTrace.FallbackReason)
	}
}

func TestFailoverHonorsAttemptBudgetAndStaysInSelectedGroup(t *testing.T) {
	catalog := testAutoCatalog(t)
	engine, err := config.Defaults().PolicyEngine()
	if err != nil {
		t.Fatal(err)
	}
	router := New(Options{
		Catalog: catalog, Analyzer: analyzer.New(), Engine: engine,
		InputMode:    jev.InputModeContent,
		DefaultGroup: GroupSimple,
		Failover:     FailoverPolicy{Enabled: true, MaxAttempts: 2, RetryPreRequestFailure: true, RetryStatusCodes: []int{503}},
		GroupConfig: GroupConfig{
			Simple:  []GroupMember{{ProviderKey: "provider-a", ModelID: "model-a"}, {ProviderKey: "provider-a", ModelID: "model-b"}},
			Complex: []GroupMember{{ProviderKey: "provider-a", ModelID: "model-c"}},
		},
	})
	body := []byte(`{"model":"auto","messages":[{"role":"user","content":"hello"}]}`)
	first, err := router.Route(context.Background(), Request{Protocol: providers.ProtocolChatCompletions, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if first.Model != "model-a" || first.Attempts != 1 || first.MaxAttempts != 2 {
		t.Fatalf("initial target = %+v", first)
	}
	next, retry := router.Failover(context.Background(), first, AttemptFailure{StatusCode: 503})
	if !retry {
		t.Fatal("configured status code did not trigger failover")
	}
	if next.Model != "model-b" || next.SelectedGroup != GroupSimple || next.Attempts != 2 || next.MaxAttempts != 2 {
		t.Fatalf("failover target = %+v, want second simple member within the two-attempt budget", next)
	}
	if _, retry := router.Failover(context.Background(), next, AttemptFailure{StatusCode: 503}); retry {
		t.Fatal("failover exceeded the configured total attempt budget")
	}
}

func TestGroupProbabilityOrderUsesConfiguredGroupOrderOnTies(t *testing.T) {
	got := sortedGroupProbabilities([]jev.CandidateProbability{
		{Model: string(GroupComplex), Probability: 1.0 / 3},
		{Model: string(GroupMedium), Probability: 1.0 / 3},
		{Model: string(GroupSimple), Probability: 1.0 / 3},
	})
	want := []GroupName{GroupSimple, GroupMedium, GroupComplex}
	if len(got) != len(want) {
		t.Fatalf("group probabilities = %+v", got)
	}
	for i := range want {
		if got[i].Group != want[i] {
			t.Fatalf("group probability order = %+v, want %v", got, want)
		}
	}
}

func TestRetryAllowedIsOptInByFailureClass(t *testing.T) {
	policy := FailoverPolicy{RetryPreRequestFailure: true, RetryStatusCodes: []int{429, 503}}
	for _, test := range []struct {
		name    string
		failure AttemptFailure
		want    bool
	}{
		{name: "pre-request", failure: AttemptFailure{Cause: providers.ErrPreRequestFailure}, want: true},
		{name: "pre-request timeout", failure: AttemptFailure{Cause: errors.Join(providers.ErrUpstreamTimeout, providers.ErrPreRequestFailure)}, want: true},
		{name: "uncertain timeout is off", failure: AttemptFailure{Cause: providers.ErrUpstreamTimeout}},
		{name: "cancellation is never retried", failure: AttemptFailure{Cause: providers.ErrCanceled}},
		{name: "configured status", failure: AttemptFailure{StatusCode: 503}, want: true},
		{name: "unconfigured status", failure: AttemptFailure{StatusCode: 502}},
		{name: "non-transient status cannot be enabled", failure: AttemptFailure{StatusCode: 401}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := retryAllowed(policy, test.failure); got != test.want {
				t.Fatalf("retryAllowed(%+v) = %t, want %t", test.failure, got, test.want)
			}
		})
	}
	policy.RetryTimeout = true
	if !retryAllowed(policy, AttemptFailure{Cause: providers.ErrUpstreamTimeout}) {
		t.Fatal("explicitly enabled timeout retry was refused")
	}
}

func testAutoCatalog(t *testing.T) *models.Store {
	t.Helper()
	provider := models.Provider{Key: "provider-a", Kind: models.ProviderOpenAICompatible, DisplayName: "Provider A", BaseURL: "https://provider-a.example/v1", Enabled: true, Source: models.SourceLocal}
	catalogModels := []models.Model{
		{ID: "model-a", DisplayName: "Model A", Enabled: true, Source: models.SourceLocal},
		{ID: "model-b", DisplayName: "Model B", Enabled: true, Source: models.SourceLocal},
		{ID: "model-c", DisplayName: "Model C", Enabled: true, Source: models.SourceLocal},
	}
	pairs := make([]models.Pair, 0, len(catalogModels))
	for _, model := range catalogModels {
		pairs = append(pairs, models.Pair{ProviderKey: provider.Key, ModelID: model.ID, UpstreamModelID: model.ID, ContextWindow: 8192, Enabled: true, Source: models.SourceLocal})
	}
	catalog, err := models.NewCatalog(0, []models.Provider{provider}, catalogModels, pairs)
	if err != nil {
		t.Fatal(err)
	}
	store := models.NewStore()
	store.Swap(catalog)
	return store
}
