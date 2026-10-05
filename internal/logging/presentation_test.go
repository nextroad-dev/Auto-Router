package logging

import "testing"

func TestRecommendationConfidenceDistinguishesAbsentFromZero(t *testing.T) {
	zero, policy, original := 0.0, 0.7, 0.8
	for _, status := range []string{JevStatusDisabled, JevStatusSkippedSingleModel, JevStatusSkippedInsufficientEvidence, "failure:jev_timeout", ""} {
		event := Event{JevStatus: status, Confidence: &zero}
		if got := event.RecommendationConfidence(nil); got != nil {
			t.Errorf("%s exposes placeholder confidence %v", status, *got)
		}
	}
	event := Event{JevStatus: JevStatusOK, Confidence: &zero}
	if got := event.RecommendationConfidence(nil); got == nil || *got != 0 {
		t.Fatal("real zero recommendation lost")
	}
	event.Confidence = &policy
	trace := &JevTrace{Status: JevStatusOK, Confidence: &original}
	if got := event.RecommendationConfidence(trace); got == nil || *got != original {
		t.Fatal("historical blended policy overrides original Jev confidence")
	}
	if *event.Confidence != policy {
		t.Fatal("reading rewrote the stored event")
	}
}

func TestDiagnosticDetailsPreservesHistoryWithoutEmptyBlocks(t *testing.T) {
	if got := (Event{}).DiagnosticDetails(nil); got != nil {
		t.Fatal("empty diagnostics not omitted")
	}
	event := Event{ConfidenceBand: "medium"}
	trace := &JevTrace{EvidenceHash: "history", ConfidenceBand: "high"}
	got := event.DiagnosticDetails(trace)
	if got == nil || got.EvidenceHash != "history" || got.LegacyConfidenceBand != "medium" || got.LegacyTraceConfidenceBand != "high" {
		t.Fatalf("historical diagnostics lost: %+v", got)
	}
}
