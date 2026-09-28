package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/logging"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
	"github.com/nextroad-dev/Auto-Router/internal/proxy"
	"github.com/nextroad-dev/Auto-Router/internal/router/auto"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

type noEligibleTestRouter struct{}

func (noEligibleTestRouter) Route(context.Context, auto.Request) (auto.Target, error) {
	return auto.Target{}, &auto.RefusalError{Status: http.StatusUnprocessableEntity, Code: auto.CodeNoEligibleCandidate, Message: "no eligible model is available"}
}
func (noEligibleTestRouter) Failover(context.Context, auto.Target, auto.AttemptFailure) (auto.Target, bool) {
	return auto.Target{}, false
}

type unusedTestExecutor struct{}

func (unusedTestExecutor) Do(context.Context, *providers.Request) (*providers.Response, error) {
	return nil, nil
}

func TestNoEligibleCandidateIsPersistedAndReturnedByLogsAPI(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Options{Path: ":memory:", BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := storage.NewRoutingLog(db)
	writer := logging.New(store, logging.Config{BatchSize: 1}, nil)
	forwarder := proxy.New(proxy.Options{
		Executor:   unusedTestExecutor{},
		AutoRouter: noEligibleTestRouter{},
		Recorder:   writer,
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	forwarder.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("forwarding status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
	flushCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := writer.Close(flushCtx); err != nil {
		t.Fatalf("flush routing log: %v", err)
	}
	if writer.Written() != 1 || writer.Dropped() != 0 || writer.Failed() != 0 {
		t.Fatalf("writer stats: written=%d dropped=%d failed=%d", writer.Written(), writer.Dropped(), writer.Failed())
	}

	admin := NewAdmin(AdminOptions{DB: db})
	logRequest := httptest.NewRequest(http.MethodGet, "/admin/v1/logs?error_code=no_eligible_candidate", nil)
	logResponse := httptest.NewRecorder()
	admin.ServeHTTP(logResponse, logRequest)
	if logResponse.Code != http.StatusOK {
		t.Fatalf("logs status = %d, body = %s", logResponse.Code, logResponse.Body.String())
	}
	var page struct {
		Items []struct {
			RequestID      string  `json:"request_id"`
			RequestedModel *string `json:"requested_model"`
			RoutingMode    *string `json:"routing_mode"`
			Status         int     `json:"status"`
			ErrorCode      *string `json:"error_code"`
		} `json:"items"`
	}
	if err := json.Unmarshal(logResponse.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("logs returned %d matching events, body=%s", len(page.Items), logResponse.Body.String())
	}
	got := page.Items[0]
	if got.ErrorCode == nil || *got.ErrorCode != auto.CodeNoEligibleCandidate || got.Status != http.StatusUnprocessableEntity {
		t.Fatalf("log error/status = %v / %d, want %q / %d", got.ErrorCode, got.Status, auto.CodeNoEligibleCandidate, http.StatusUnprocessableEntity)
	}
	if got.RequestID == "" || got.RequestedModel == nil || *got.RequestedModel != "auto" || got.RoutingMode == nil || *got.RoutingMode != "auto" {
		t.Fatalf("log routing fields = %+v, want request_id and auto / auto", got)
	}
}

func TestLogsAPIReportsGroupLevelJevTrace(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Options{Path: ":memory:", BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	confidence := 0.8
	store := storage.NewRoutingLog(db)
	if err := store.Insert(ctx, []logging.Event{{
		RequestID: "group-api-trace", StartedAt: time.Now().UTC(), Protocol: "chat_completions",
		RoutingMode: logging.RoutingModeAuto, SelectionMode: logging.SelectionModeFirstEligible,
		RequestedModel: "auto", EffectiveModel: "model-b", Status: http.StatusOK, GatewayAttempts: 1,
		JevStatus: logging.JevStatusOK, Confidence: &confidence, ConfidenceBand: logging.ConfidenceBandHigh,
		FallbackReason: logging.FallbackReasonNone, Usage: logging.Usage{Status: logging.UsageStatusAbsent},
		Jev: &logging.JevTrace{
			Status: logging.JevStatusOK, InputMode: logging.InputModeContent,
			CandidateCount: 3, CandidateGroups: []string{"simple", "medium"}, GroupCount: 2,
			RecommendedGroup: "medium", SelectedGroup: "medium", Confidence: &confidence,
			ConfidenceBand: logging.ConfidenceBandHigh, FallbackReason: logging.FallbackReasonNone,
			GroupProbabilities: []logging.GroupProbability{{Group: "medium", Probability: 0.8}, {Group: "simple", Probability: 0.2}},
		},
	}}); err != nil {
		t.Fatalf("insert trace event: %v", err)
	}
	admin := NewAdmin(AdminOptions{DB: db})
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/logs?request_id=group-api-trace", nil)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("logs status=%d body=%s", response.Code, response.Body.String())
	}
	var page struct {
		Items []struct {
			Trace *struct {
				CandidateGroups    []string `json:"candidate_groups"`
				GroupCount         int      `json:"group_count"`
				RecommendedGroup   *string  `json:"recommended_group"`
				SelectedGroup      *string  `json:"selected_group"`
				GroupProbabilities []struct {
					Group string `json:"group"`
				} `json:"group_probabilities"`
			} `json:"jev_trace"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Trace == nil {
		t.Fatalf("log trace missing: %s", response.Body.String())
	}
	trace := page.Items[0].Trace
	if trace.GroupCount != 2 || len(trace.CandidateGroups) != 2 || trace.RecommendedGroup == nil || *trace.RecommendedGroup != "medium" || trace.SelectedGroup == nil || *trace.SelectedGroup != "medium" || len(trace.GroupProbabilities) != 2 {
		t.Fatalf("API group trace = %+v", trace)
	}
}
