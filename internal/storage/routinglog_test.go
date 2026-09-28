package storage

import (
	"context"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/logging"
)

func TestGroupTraceMigrationKeepsExistingModelTraceReadable(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Options{Path: ":memory:", BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := applyMigrations(ctx, db, businessMigrations[:len(businessMigrations)-1]); err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO routing_jev_calls
		(request_id, started_at, status, failure_reason, input_mode, latency_ms, candidate_count, candidates_json,
		 model_count, selected_model, confidence, confidence_band, fallback_reason, distribution_json, evidence_hash)
		VALUES ('legacy-upgrade', '2026-01-01T00:00:00Z', 'ok', NULL, 'content', 4, 1, '["model-a"]',
		 1, 'model-a', 0.9, 'high', 'none', '[{"model":"model-a","probability":1}]', 'hash')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	trace, err := NewRoutingLog(db).SelectTraceFor(ctx, "legacy-upgrade")
	if err != nil {
		t.Fatal(err)
	}
	if trace == nil || trace.Selected != "model-a" || trace.ModelCount != 1 || trace.GroupCount != 0 || len(trace.CandidateGroups) != 0 {
		t.Fatalf("pre-migration model trace was not preserved: %+v", trace)
	}
}

func TestRoutingLogRoundTripsGroupAndLegacyModelTraces(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Options{Path: ":memory:", BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	confidence := 0.2
	store := NewRoutingLog(db)
	events := []logging.Event{
		{
			RequestID: "group-trace", StartedAt: time.Now().UTC(), Protocol: "chat_completions",
			RoutingMode: logging.RoutingModeAuto, SelectionMode: logging.SelectionModeFirstEligible,
			RequestedModel: "auto", EffectiveModel: "model-b", Status: 200, GatewayAttempts: 1,
			JevStatus: logging.JevStatusOK, Confidence: &confidence, ConfidenceBand: logging.ConfidenceBandLow,
			FallbackReason: logging.FallbackReasonConfidenceLow, Usage: logging.Usage{Status: logging.UsageStatusAbsent},
			Jev: &logging.JevTrace{
				Status: logging.JevStatusOK, InputMode: logging.InputModeContent, CandidateCount: 3,
				CandidateGroups: []string{"simple", "medium"}, GroupCount: 2,
				RecommendedGroup: "simple", SelectedGroup: "medium", Confidence: &confidence,
				ConfidenceBand: logging.ConfidenceBandLow, FallbackReason: logging.FallbackReasonConfidenceLow,
				GroupProbabilities: []logging.GroupProbability{{Group: "simple", Probability: 0.4}, {Group: "medium", Probability: 0.6}},
			},
		},
		{
			RequestID: "legacy-trace", StartedAt: time.Now().UTC(), Protocol: "responses",
			RoutingMode: logging.RoutingModeAuto, SelectionMode: logging.SelectionModeJev,
			RequestedModel: "auto", EffectiveModel: "model-a", Status: 200, GatewayAttempts: 1,
			JevStatus: logging.JevStatusOK, Confidence: &confidence, ConfidenceBand: logging.ConfidenceBandLow,
			Usage: logging.Usage{Status: logging.UsageStatusAbsent},
			Jev: &logging.JevTrace{
				Status: logging.JevStatusOK, InputMode: logging.InputModeContent, CandidateCount: 3,
				CandidateModels: []string{"model-a", "model-b"}, ModelCount: 2, Selected: "model-a",
				Confidence: &confidence, ConfidenceBand: logging.ConfidenceBandLow,
				Probabilities: []logging.ModelProbability{{Model: "model-a", Probability: 0.7}, {Model: "model-b", Probability: 0.3}},
			},
		},
	}
	if err := store.Insert(ctx, events); err != nil {
		t.Fatal(err)
	}
	stored, err := store.SelectRecentEvents(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 {
		t.Fatalf("stored events = %d, want 2", len(stored))
	}
	traces := make(map[string]*logging.JevTrace, len(stored))
	for _, event := range stored {
		traces[event.Event.RequestID] = event.JevTrace
	}
	group := traces["group-trace"]
	if group == nil || group.GroupCount != 2 || len(group.CandidateGroups) != 2 || group.CandidateGroups[0] != "simple" || group.RecommendedGroup != "simple" || group.SelectedGroup != "medium" || len(group.GroupProbabilities) != 2 || group.GroupProbabilities[1].Group != "medium" {
		t.Fatalf("group trace did not round-trip: %+v", group)
	}
	legacy := traces["legacy-trace"]
	if legacy == nil || legacy.ModelCount != 2 || legacy.Selected != "model-a" || len(legacy.Probabilities) != 2 || legacy.GroupCount != 0 || len(legacy.CandidateGroups) != 0 {
		t.Fatalf("legacy model trace did not round-trip: %+v", legacy)
	}
}
