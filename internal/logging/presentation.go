package logging

// Diagnostics separates audit metadata from facts used to operate a request.
// Legacy bands explain old records; they are not current routing signals.
type Diagnostics struct {
	EvidenceHash              string `json:"evidence_hash,omitempty"`
	LegacyConfidenceBand      string `json:"legacy_confidence_band,omitempty"`
	LegacyTraceConfidenceBand string `json:"legacy_trace_confidence_band,omitempty"`
}

// RecommendationConfidence reports a value only when Jev produced a valid
// recommendation. Older policy decisions used 0 for skipped/failed calls and
// could blend the actual Jev value; prefer the original trace when available.
func (e Event) RecommendationConfidence(trace *JevTrace) *float64 {
	if e.JevStatus != JevStatusOK {
		return nil
	}
	if trace != nil && trace.Status == JevStatusOK && trace.Confidence != nil {
		return trace.Confidence
	}
	return e.Confidence
}

// DiagnosticDetails preserves historical metadata without rewriting storage.
// An empty diagnostic block is omitted by callers.
func (e Event) DiagnosticDetails(trace *JevTrace) *Diagnostics {
	d := Diagnostics{EvidenceHash: e.EvidenceHash, LegacyConfidenceBand: e.ConfidenceBand}
	if trace != nil {
		if d.EvidenceHash == "" {
			d.EvidenceHash = trace.EvidenceHash
		}
		if trace.ConfidenceBand != e.ConfidenceBand {
			d.LegacyTraceConfidenceBand = trace.ConfidenceBand
		}
	}
	if d == (Diagnostics{}) {
		return nil
	}
	return &d
}
