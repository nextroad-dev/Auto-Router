package proxy

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/logging"
	"github.com/nextroad-dev/Auto-Router/internal/router/auto"
)

func TestRoutingLogOnlyRecordsRecommendationConfidence(t *testing.T) {
	for _, status := range []string{logging.JevStatusOK, logging.JevStatusDisabled, logging.JevStatusSkippedSingleModel, "failure:jev_timeout"} {
		t.Run(status, func(t *testing.T) {
			zero := 0.0
			target := auto.Target{JevStatus: status, JevTrace: &auto.JevTrace{Status: status, Confidence: &zero}}
			target.Decision.Confidence = 0.8
			var record requestLog
			record.applyRoutingDecision(target)
			event := record.recordEvent(time.Now(), 0)
			if (event.Confidence != nil) != (status == logging.JevStatusOK) {
				t.Fatalf("placeholder confidence recorded: %+v", event)
			}
			if event.Confidence != nil && *event.Confidence != 0 {
				t.Fatal("original zero recommendation was replaced with policy confidence")
			}
			if event.ConfidenceBand != "" || event.Jev.ConfidenceBand != "" {
				t.Fatal("new log still produces legacy bands")
			}
		})
	}
}

func TestProcessLogOmitsUnusedFieldsAndKeepsSubMillisecondCall(t *testing.T) {
	for _, routed := range []bool{false, true} {
		var output bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&output, nil))
		record := requestLog{RequestID: "request", Protocol: "responses", Status: 200, RoutingMode: "explicit"}
		if routed {
			zero := int64(0)
			record.routed, record.JevStatus, record.RoutingMode = true, logging.JevStatusOK, "auto"
			record.Confidence, record.EvidenceHash = floatPointer(0), "fingerprint"
			record.jevTrace = &logging.JevTrace{LatencyMS: &zero}
		}
		record.emit(logger, time.Now())
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"confidence_band", "evidence_hash", "routing_model", "provider", "upstream_model", "fallback_reason"} {
			if _, ok := payload[key]; ok {
				t.Errorf("unused field %s still emitted: %s", key, output.String())
			}
		}
		for _, key := range []string{"routing_latency_ms", "jev_latency_ms", "confidence", "diagnostics"} {
			if _, ok := payload[key]; ok != routed {
				t.Errorf("%s presence=%t want=%t: %s", key, ok, routed, output.String())
			}
		}
	}
}
