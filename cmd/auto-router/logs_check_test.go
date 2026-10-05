package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nextroad-dev/Auto-Router/internal/logging"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

func TestLogsCheckJSONUsesCompactRecommendationSemantics(t *testing.T) {
	zero := int64(0)
	confidence := 0.0
	for _, status := range []string{logging.JevStatusOK, logging.JevStatusDisabled, "failure:jev_timeout"} {
		entry := storage.StoredEvent{Event: logging.Event{JevStatus: status, Confidence: &confidence, EvidenceHash: "audit", Usage: logging.Usage{Status: logging.UsageStatusObserved, InputTokens: &zero}}}
		var output bytes.Buffer
		if err := writeLogsJSON(&output, []storage.StoredEvent{entry}); err != nil {
			t.Fatal(err)
		}
		var got struct {
			Event map[string]json.RawMessage `json:"event"`
		}
		if err := json.Unmarshal(output.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if _, ok := got.Event["confidence"]; ok != (status == logging.JevStatusOK) {
			t.Errorf("bad confidence presence for %s: %s", status, output.String())
		}
		for _, key := range []string{"confidence_band", "evidence_hash", "client_ip", "error_code", "jev_latency_ms"} {
			if _, ok := got.Event[key]; ok {
				t.Errorf("unused %s: %s", key, output.String())
			}
		}
		if !strings.Contains(string(got.Event["usage"]), `"input_tokens":0`) || !strings.Contains(string(got.Event["diagnostics"]), `"evidence_hash":"audit"`) {
			t.Errorf("zero/audit lost: %s", output.String())
		}
		output.Reset()
		writeLogsReport(&output, []storage.StoredEvent{entry})
		if strings.Contains(output.String(), "confidence=") != (status == logging.JevStatusOK) {
			t.Errorf("text report differs from JSON confidence semantics: %s", output.String())
		}
	}
}

func TestLogsCheckDistinguishesUsageFromRequestOutcome(t *testing.T) {
	var output bytes.Buffer
	writeLogsReport(&output, nil)
	for _, hint := range []string{"status is the client HTTP result", "usage oversized is not context overflow", "none means unknown, not zero", "may apply schema migrations"} {
		if !strings.Contains(output.String(), hint) {
			t.Errorf("missing operator guidance %q: %s", hint, output.String())
		}
	}
}

func TestLogsCheckJSONOmitsEmptyLegacyFieldsAndPreservesHistoricalData(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		trace := &logging.JevTrace{Status: logging.JevStatusOK, InputMode: logging.InputModeContent}
		if legacy {
			trace.ModelCount, trace.CandidateModels, trace.Selected = 1, []string{"old-model"}, "old-model"
			trace.Probabilities = []logging.ModelProbability{{Model: "old-model", Probability: 1}}
		} else {
			trace.GroupCount, trace.CandidateGroups, trace.SelectedGroup = 1, []string{"medium"}, "medium"
		}
		var output bytes.Buffer
		if err := writeLogsJSON(&output, []storage.StoredEvent{{JevTrace: trace}}); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"candidate_models", "model_count", "selected_model", "probabilities"} {
			if present := strings.Contains(output.String(), `"`+key+`":`); present != legacy {
				t.Errorf("legacy=%t: field %s presence=%t: %s", legacy, key, present, output.String())
			}
		}
		if legacy && !strings.Contains(output.String(), `"probabilities":[{"model":"old-model","probability":1}]`) {
			t.Fatalf("legacy distribution lost model identity: %s", output.String())
		}
	}
}

func TestLogsCheckKeepsLegacyTraceReadableWithoutEmptyModelFields(t *testing.T) {
	var output bytes.Buffer
	writeStoredTrace(&output, &logging.JevTrace{
		Status: logging.JevStatusOK, InputMode: logging.InputModeRedacted,
		CandidateCount: 2, CandidateGroups: []string{"medium"}, GroupCount: 1,
		RecommendedGroup: "medium", SelectedGroup: "medium",
	})
	if strings.Contains(output.String(), "model_count") || strings.Contains(output.String(), "legacy_") {
		t.Fatalf("current group trace still displays unused model fields: %s", output.String())
	}
	if !strings.Contains(output.String(), "candidate_count=2 candidate_groups=medium recommended_group=medium") {
		t.Fatalf("group trace missing: %s", output.String())
	}
	output.Reset()
	writeStoredTrace(&output, &logging.JevTrace{
		Status: logging.JevStatusOK, InputMode: logging.InputModeContent,
		ModelCount: 1, CandidateModels: []string{"old-model"}, Selected: "old-model",
		Probabilities: []logging.ModelProbability{{Model: "old-model", Probability: 1}},
	})
	if !strings.Contains(output.String(), "legacy_selected_model=old-model") || !strings.Contains(output.String(), "legacy_model_distribution=old-model=1.000000") {
		t.Fatalf("historical trace was lost or not identified: %s", output.String())
	}
}
