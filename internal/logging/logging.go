// Package logging owns the routing event record and the durable, bounded writer
// that persists it. It is the only definition of the routing schema: the
// forwarding path builds an Event, this package validates it, and
// internal/storage maps it onto the routing_log tables.
//
// The dependency set is part of the contract and is pinned by a source-level
// import guard test: this package must not import anything outside the standard
// library. It never imports internal/proxy, internal/storage, database/sql or
// net/http, so the record type cannot reach a credential, a request body or a
// second logging framework by accident.
//
// The record is deliberately made of identifiers, counts, durations and closed
// enumerations. There is no field for a prompt, a message, a tool schema, a
// header value or an upstream body, and the optional Jev trace keeps that
// property: it records which logical models were asked about, not what was
// asked.
package logging

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Recorder is the request path's write seam. It is implemented by *Writer and is
// deliberately one method with no return value: recording a routing event must
// never be able to fail, block or change a response. A nil Recorder means the
// routing log is off.
type Recorder interface {
	Record(Event)
}

// MaxJevJSONBytes bounds each JSON column of a Jev trace. The bound is provable
// from the closed input: at most 255 candidate identifiers of at most 128 bytes
// each, so a legitimate value can never approach it. It exists so a future
// caller cannot turn one stored diagnostic into an unbounded blob.
const MaxJevJSONBytes = 64 << 10

// The two routing modes a recorded request can carry. They are the same
// literals internal/proxy emits in the process log, named here so the stored
// column and the log cannot drift apart.
const (
	// RoutingModeExplicit is a request that named its destination.
	RoutingModeExplicit = "explicit"
	// RoutingModeAuto is a request that asked the router to choose.
	RoutingModeAuto = "auto"
)

// The selection modes. "explicit" is the direct-forward path; the other four are
// decision.Origin values, spelled here because this package may not import the
// decision package.
const (
	SelectionModeExplicit      = "explicit"
	SelectionModeJev           = "jev"
	SelectionModeBlend         = "blend"
	SelectionModeDefaultModel  = "default_model"
	SelectionModeFirstEligible = "first_eligible"
)

// The confidence bands, mirroring decision.ConfidenceBand.
const (
	ConfidenceBandHigh   = "high"
	ConfidenceBandMedium = "medium"
	ConfidenceBandLow    = "low"
)

// The fallback reasons, mirroring decision.FallbackReason. The list is closed:
// a new reason is a schema change, because the column carries a CHECK.
const (
	FallbackReasonNone                    = "none"
	FallbackReasonNotRequested            = "not_requested"
	FallbackReasonConfidenceLow           = "confidence_low"
	FallbackReasonSelectedModelIneligible = "selected_model_ineligible"
	FallbackReasonJevTimeout              = "jev_timeout"
	FallbackReasonJevUnavailable          = "jev_unavailable"
	FallbackReasonJevRejected             = "jev_rejected"
	FallbackReasonJevInvalidResult        = "jev_invalid_result"
	FallbackReasonJevCanceled             = "jev_canceled"
	FallbackReasonTruncatedEvidence       = "truncated_evidence"
	FallbackReasonNoCandidates            = "no_candidates"
	FallbackReasonModelNotEligiblePrefix  = "not_eligible:"
)

// The Jev status values, mirroring internal/router/auto. A failure carries the
// JevStatusFailurePrefix plus one of the five failure reasons.
const (
	JevStatusDisabled                    = "disabled"
	JevStatusSkippedSingleModel          = "skipped_single_model"
	JevStatusSkippedInsufficientEvidence = "skipped_insufficient_evidence"
	JevStatusSkippedTooManyModels        = "skipped_too_many_models"
	JevStatusOK                          = "ok"
	// JevStatusFailurePrefix prefixes the closed fallback reason of a call that
	// failed.
	JevStatusFailurePrefix = "failure:"
)

// The Jev input modes, mirroring internal/router/jev.
const (
	InputModeContent      = "content"
	InputModeRedacted     = "redacted"
	InputModeFeaturesOnly = "features_only"
)

// The usage statuses. The set is closed and deliberately has no "zero" member:
// an unknown token count is NULL, and NULL is never written as 0.
const (
	// UsageStatusObserved means at least one legitimate non-negative integer was
	// read from the upstream response.
	UsageStatusObserved = "observed"
	// UsageStatusAbsent means the response completed (or never started) without
	// carrying a usage object.
	UsageStatusAbsent = "absent"
	// UsageStatusMalformed means a usage object was present but unusable
	// (wrong type, negative, fractional).
	UsageStatusMalformed = "malformed"
	// UsageStatusOversized means usage extraction hit a bound. It is independent
	// of Jev status, model context capacity and HTTP success. Older builds could
	// stop on a large SSE event or event count; current scanners bound a captured
	// usage value to 4 KiB.
	UsageStatusOversized = "oversized"
	// UsageStatusInterrupted means the relay ended in an error or a cancellation
	// before usage was seen.
	UsageStatusInterrupted = "interrupted"
)

// The usage sources. They record where a usage object was read, not where the
// client sent the request.
const (
	UsageSourceChatCompletions       = "chat_completions"
	UsageSourceResponses             = "responses"
	UsageSourceStreamChatCompletions = "stream_chat_completions"
	UsageSourceStreamResponses       = "stream_responses"
)

// Usage is one passively observed token count. Every token field is optional:
// only the fields the upstream actually reported are set, and a missing field
// stays nil all the way into a NULL column. TotalTokens is never synthesized
// from the other two.
type Usage struct {
	InputTokens  *int64
	OutputTokens *int64
	TotalTokens  *int64
	// Status is one of the UsageStatus constants.
	Status string
	// Source is one of the UsageSource constants. It is empty when nothing was
	// read, and for native Anthropic/Gemini usage, which has no source literal.
	Source string
}

// MaxAttemptErrorDetailBytes bounds Attempt.ErrorDetail. The detail is an excerpt
// of an upstream error message, never a body: this bound is what keeps it one.
const MaxAttemptErrorDetailBytes = 512

// Attempt records one upstream try without storing request or response bodies.
// ErrorDetail is the one exception, and a narrow one: for an attempt the upstream
// answered with a non-2xx status it carries a bounded, redacted excerpt of the
// error message, so an operator can see why without reproducing the request.
type Attempt struct {
	RequestID    string
	AttemptIndex int
	GroupName    string
	ProviderKey  string
	ModelID      string
	StartedAt    time.Time
	CompletedAt  *time.Time
	Status       int
	ErrorCode    string
	ErrorDetail  string
	InputTokens  *int64
	OutputTokens *int64
	TotalTokens  *int64
	UsageStatus  string
}

// Valid reports whether the attempt can be stored with the routing log.
func (a Attempt) Valid() error {
	if !validIdentifier(a.RequestID) || a.AttemptIndex < 1 || a.StartedAt.IsZero() {
		return errors.New("attempt identity or start time is invalid")
	}
	if a.Status != 0 && (a.Status < 100 || a.Status > 599) {
		return errors.New("attempt status must be zero or a three digit HTTP status")
	}
	if a.CompletedAt != nil && a.CompletedAt.Before(a.StartedAt) {
		return errors.New("attempt completion precedes its start")
	}
	if !ValidUsageStatus(a.UsageStatus) {
		return errors.New("attempt usage status is invalid")
	}
	if len(a.ErrorDetail) > MaxAttemptErrorDetailBytes || !utf8.ValidString(a.ErrorDetail) {
		return errors.New("attempt error detail must be bounded UTF-8")
	}
	for _, count := range []*int64{a.InputTokens, a.OutputTokens, a.TotalTokens} {
		if count != nil && *count < 0 {
			return errors.New("token counts must not be negative")
		}
	}
	return nil
}

// Valid reports whether the usage record is inside its closed sets and carries
// no negative count.
func (u Usage) Valid() error {
	if !ValidUsageStatus(u.Status) {
		return errors.New("usage status is not one of the defined literals")
	}
	if u.Source != "" && !ValidUsageSource(u.Source) {
		return errors.New("usage source is not one of the defined literals")
	}
	for _, count := range []*int64{u.InputTokens, u.OutputTokens, u.TotalTokens} {
		if count != nil && *count < 0 {
			return errors.New("a token count must not be negative")
		}
	}
	if u.Status != UsageStatusObserved && u.Source != "" {
		return errors.New("a usage source is only meaningful when usage was observed")
	}
	return nil
}

// ModelProbability is one entry of the normalized Jev distribution.
type ModelProbability struct {
	Model       string
	Probability float64
}

// GroupProbability is one normalized Jev probability for a routing group.
type GroupProbability struct {
	Group       string  `json:"group"`
	Probability float64 `json:"probability"`
}

// JevTrace is the optional debug trace of one Jev call. It is metadata only:
// candidate identifiers, the normalized distribution, the selection and the
// latency. There is no field for a prompt, a system message, a tool description
// or the raw request and response bytes, so enabling the trace cannot leak the
// conversation.
type JevTrace struct {
	// Status is one of the JevStatus constants.
	Status string
	// FailureReason is empty or one of the five Jev failure classifications.
	FailureReason string
	// InputMode is the configured jev.input_mode.
	InputMode string
	// Selected is the legacy recommended logical model. It is empty for current
	// group-level routing; RecommendedGroup is the recommendation instead.
	Selected string
	// ConfidenceBand and FallbackReason mirror the decision the recommendation
	// contributed to.
	ConfidenceBand string
	FallbackReason string
	// EvidenceHash is the decision's evidence summary. It is a hash of
	// identifiers and enumerations, never of request text.
	EvidenceHash string
	// LatencyMS is how long the call took. Nil when no call was made.
	LatencyMS *int64
	// CandidateModels lists legacy model-mode identifiers sent to Jev. It is
	// empty for current group-mode routing.
	CandidateModels []string
	// ModelCount is always len(CandidateModels). CandidateCount is the number of
	// hard-filtered provider/model pairs in group mode, or the pair-level count
	// from which legacy model candidates were derived.
	ModelCount     int
	CandidateCount int
	// Confidence is the recommendation confidence, nil when no recommendation
	// was produced.
	Confidence *float64
	// Probabilities is the legacy model-mode distribution, ordered by descending
	// probability with the model identifier ascending as the tie-break.
	Probabilities []ModelProbability
	// Group-level fields describe the current automatic routing contract. They are
	// empty/zero for legacy model-level traces.
	CandidateGroups    []string
	GroupCount         int
	RecommendedGroup   string
	SelectedGroup      string
	GroupProbabilities []GroupProbability
}

// Valid reports whether the trace is internally consistent. It never looks at
// request content, because the type has none.
func (t JevTrace) Valid() error {
	if !ValidJevStatus(t.Status) {
		return errors.New("jev status is not one of the defined literals")
	}
	if t.FailureReason != "" && !ValidJevFailureReason(t.FailureReason) {
		return errors.New("jev failure reason is not one of the defined literals")
	}
	// A failure status and a failure reason always travel together, and the
	// status suffix is exactly the reason.
	if failureStatus, ok := strings.CutPrefix(t.Status, JevStatusFailurePrefix); ok {
		if !ok || failureStatus != t.FailureReason {
			return errors.New("a jev failure status must carry its failure reason")
		}
	} else if t.FailureReason != "" {
		return errors.New("a jev failure reason requires a failure status")
	}
	if !ValidInputMode(t.InputMode) {
		return errors.New("jev input mode is not one of the defined literals")
	}
	if t.ConfidenceBand != "" && !ValidConfidenceBand(t.ConfidenceBand) {
		return errors.New("confidence band is not one of the defined literals")
	}
	if t.FallbackReason != "" && !ValidFallbackReason(t.FallbackReason) {
		return errors.New("fallback reason is not one of the defined literals")
	}
	if t.ModelCount != len(t.CandidateModels) {
		return errors.New("model count does not match the candidate model list")
	}
	if t.GroupCount != len(t.CandidateGroups) {
		return errors.New("group count does not match the candidate group list")
	}
	if t.GroupCount == 0 {
		if t.CandidateCount < t.ModelCount {
			return errors.New("candidate count must not be smaller than the model count")
		}
		if t.RecommendedGroup != "" || t.SelectedGroup != "" || len(t.GroupProbabilities) > 0 {
			return errors.New("group recommendation fields require candidate groups")
		}
	} else {
		if t.ModelCount != 0 || len(t.CandidateModels) != 0 {
			return errors.New("group traces must not carry model-level candidates")
		}
		if t.CandidateCount < t.GroupCount {
			return errors.New("candidate count must not be smaller than the group count")
		}
		if t.Selected != "" || len(t.Probabilities) > 0 {
			return errors.New("group traces must not carry model-level recommendations")
		}
		seen := make(map[string]struct{}, len(t.CandidateGroups))
		for _, group := range t.CandidateGroups {
			if !validRoutingGroup(group) {
				return errors.New("candidate group is not a known routing group")
			}
			if _, duplicate := seen[group]; duplicate {
				return errors.New("candidate groups must be unique")
			}
			seen[group] = struct{}{}
		}
		if t.RecommendedGroup != "" && (!validRoutingGroup(t.RecommendedGroup) || !containsString(t.CandidateGroups, t.RecommendedGroup)) {
			return errors.New("recommended group must be one of the candidate groups")
		}
		if t.SelectedGroup != "" && (!validRoutingGroup(t.SelectedGroup) || !containsString(t.CandidateGroups, t.SelectedGroup)) {
			return errors.New("selected group must be one of the candidate groups")
		}
		seen = make(map[string]struct{}, len(t.GroupProbabilities))
		for _, probability := range t.GroupProbabilities {
			if !validRoutingGroup(probability.Group) || !containsString(t.CandidateGroups, probability.Group) {
				return errors.New("group probability must reference a candidate group")
			}
			if _, duplicate := seen[probability.Group]; duplicate {
				return errors.New("group probabilities must be unique")
			}
			seen[probability.Group] = struct{}{}
		}
	}
	if t.Confidence != nil && !validUnitInterval(*t.Confidence) {
		return errors.New("confidence must be a number between 0 and 1")
	}
	for _, probability := range t.Probabilities {
		if !validUnitInterval(probability.Probability) {
			return errors.New("a probability must be a number between 0 and 1")
		}
	}
	if t.Status != JevStatusOK && (t.Selected != "" || t.Probabilities != nil || t.RecommendedGroup != "" || t.GroupProbabilities != nil) {
		return errors.New("only a successful call may carry a recommendation or a distribution")
	}
	return nil
}

// Event is one durable row for a request that reached the forwarding path.
// It mirrors the SQLite columns, including historical columns retained for
// compatibility. API/CLI views omit absent facts and separate audit diagnostics.
type Event struct {
	RequestID string
	// StartedAt is when the request entered the forwarding handler. Storage
	// writes it as RFC3339Nano UTC, the format the registry already uses.
	StartedAt time.Time
	Protocol  string
	// RoutingMode is explicit or auto, and empty for a request rejected before
	// the model field was read (an unconfigured provider, an unsupported media
	// type, a rejected control header). An unknown mode is recorded as unknown
	// rather than guessed.
	RoutingMode   string
	SelectionMode string

	RequestedModel string
	// EffectiveModel is the logical model that actually took effect: the resolved
	// logical model on the explicit path (the selected pair's model_id for a
	// provider: override) and the decision's model on the automatic path. It is
	// empty, and therefore NULL, for a request that failed before any destination
	// was resolved: no model took effect, and reporting one would turn a failure
	// into a fabricated destination choice.
	EffectiveModel string
	ProviderKey    string
	UpstreamModel  string
	ErrorCode      string
	Status         int
	UpstreamStatus int
	Stream         bool

	// ClientIP is stored only when the operator opted in; the writer clears it
	// otherwise.
	ClientIP string

	JevStatus string
	// ConfidenceBand is historical only; current request producers leave it empty.
	ConfidenceBand string
	FallbackReason string
	// EvidenceHash fingerprints decision metadata, not prompt text. Presentation
	// places it under diagnostics; it is not used to select a destination.
	EvidenceHash string
	// Confidence is the original Jev recommendation, absent for skipped/failed
	// calls. Historical policy placeholders are hidden by RecommendationConfidence.
	Confidence *float64

	// GatewayAttempts is a historical SQLite/JSON field name; it counts
	// attempts against the selected Provider, not a Gateway.
	GatewayAttempts int
	FailoverUsed    bool

	DurationMS   int64
	BytesWritten int64
	// RoutingLatencyMS and JevLatencyMS are nil when the corresponding phase did
	// not happen.
	RoutingLatencyMS *int64
	JevLatencyMS     *int64

	Usage Usage
	// Jev is the optional debug trace. Nil means no trace row is written.
	Jev *JevTrace
}

// Valid reports whether an event may be persisted. The check is deliberately
// strict: the columns are STRICT and carry closed-set CHECKs, so a value that
// would be rejected by SQLite is rejected here instead, where the failure costs
// one dropped record rather than a whole batch.
func (e Event) Valid() error {
	if !validIdentifier(e.RequestID) {
		return errors.New("request id is missing or not a safe identifier")
	}
	if e.StartedAt.IsZero() {
		return errors.New("start time is missing")
	}
	if e.Protocol != "chat_completions" && e.Protocol != "responses" && e.Protocol != "native" {
		return errors.New("protocol is not one of the defined literals")
	}
	if e.RoutingMode != "" && e.RoutingMode != RoutingModeExplicit && e.RoutingMode != RoutingModeAuto {
		return errors.New("routing mode is not one of the defined literals")
	}
	if e.SelectionMode != "" && !ValidSelectionMode(e.SelectionMode) {
		return errors.New("selection mode is not one of the defined literals")
	}
	if e.Status < 100 || e.Status > 599 {
		return errors.New("status must be a three digit HTTP status")
	}
	if e.UpstreamStatus != 0 && (e.UpstreamStatus < 100 || e.UpstreamStatus > 599) {
		return errors.New("upstream status must be a three digit HTTP status")
	}
	if e.EffectiveModel != "" && !validIdentifier(e.EffectiveModel) {
		return errors.New("effective model is not a safe logical model identifier")
	}
	if e.ConfidenceBand != "" && !ValidConfidenceBand(e.ConfidenceBand) {
		return errors.New("confidence band is not one of the defined literals")
	}
	if e.FallbackReason != "" && !ValidFallbackReason(e.FallbackReason) {
		return errors.New("fallback reason is not one of the defined literals")
	}
	if e.JevStatus != "" && !ValidJevStatus(e.JevStatus) {
		return errors.New("jev status is not one of the defined literals")
	}
	if e.Confidence != nil && !validUnitInterval(*e.Confidence) {
		return errors.New("confidence must be a number between 0 and 1")
	}
	if e.GatewayAttempts < 0 {
		return errors.New("upstream attempts must not be negative")
	}
	if e.DurationMS < 0 || e.BytesWritten < 0 {
		return errors.New("durations and byte counts must not be negative")
	}
	for _, latency := range []*int64{e.RoutingLatencyMS, e.JevLatencyMS} {
		if latency != nil && *latency < 0 {
			return errors.New("a latency must not be negative")
		}
	}
	if err := e.Usage.Valid(); err != nil {
		return err
	}
	if e.Jev != nil {
		if err := e.Jev.Valid(); err != nil {
			return err
		}
	}
	return nil
}

// ValidUsageStatus reports whether a value is one of the five usage statuses.
func ValidUsageStatus(value string) bool {
	switch value {
	case UsageStatusObserved, UsageStatusAbsent, UsageStatusMalformed, UsageStatusOversized, UsageStatusInterrupted:
		return true
	default:
		return false
	}
}

// ValidUsageSource reports whether a value is one of the four usage sources.
func ValidUsageSource(value string) bool {
	switch value {
	case UsageSourceChatCompletions, UsageSourceResponses, UsageSourceStreamChatCompletions, UsageSourceStreamResponses:
		return true
	default:
		return false
	}
}

// ValidSelectionMode reports whether a value is one of the five selection modes.
func ValidSelectionMode(value string) bool {
	switch value {
	case SelectionModeExplicit, SelectionModeJev, SelectionModeBlend, SelectionModeDefaultModel, SelectionModeFirstEligible:
		return true
	default:
		return false
	}
}

// ValidConfidenceBand reports whether a value is one of the three bands.
func ValidConfidenceBand(value string) bool {
	switch value {
	case ConfidenceBandHigh, ConfidenceBandMedium, ConfidenceBandLow:
		return true
	default:
		return false
	}
}

// ValidFallbackReason reports whether a value is one of the fallback reasons.
// The prefix form the policy engine uses for an ineligible default model
// ("not_eligible:<code>") is accepted as well.
func ValidFallbackReason(value string) bool {
	switch value {
	case FallbackReasonNone, FallbackReasonNotRequested, FallbackReasonConfidenceLow,
		FallbackReasonSelectedModelIneligible, FallbackReasonJevTimeout,
		FallbackReasonJevUnavailable, FallbackReasonJevRejected,
		FallbackReasonJevInvalidResult, FallbackReasonJevCanceled,
		FallbackReasonTruncatedEvidence, FallbackReasonNoCandidates:
		return true
	default:
		return strings.HasPrefix(value, FallbackReasonModelNotEligiblePrefix) && validIdentifier(strings.TrimPrefix(value, FallbackReasonModelNotEligiblePrefix))
	}
}

// ValidJevStatus reports whether a value is one of the five statuses or a
// failure status carrying one of the five failure reasons.
func ValidJevStatus(value string) bool {
	switch value {
	case JevStatusDisabled, JevStatusSkippedSingleModel, JevStatusSkippedInsufficientEvidence,
		JevStatusSkippedTooManyModels, JevStatusOK:
		return true
	}
	if reason, ok := strings.CutPrefix(value, JevStatusFailurePrefix); ok {
		return ValidJevFailureReason(reason)
	}
	return false
}

// ValidJevFailureReason reports whether a value is one of the five Jev failure
// classifications.
func ValidJevFailureReason(value string) bool {
	switch value {
	case FallbackReasonJevTimeout, FallbackReasonJevUnavailable, FallbackReasonJevRejected,
		FallbackReasonJevInvalidResult, FallbackReasonJevCanceled:
		return true
	default:
		return false
	}
}

// ValidInputMode reports whether a value is one of the three input modes.
func ValidInputMode(value string) bool {
	switch value {
	case InputModeContent, InputModeRedacted, InputModeFeaturesOnly:
		return true
	default:
		return false
	}
}

func validRoutingGroup(value string) bool {
	switch value {
	case "simple", "medium", "complex":
		return true
	default:
		return false
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// EncodeCandidateModels renders the candidate model list for storage, reporting
// false when the encoded value would exceed the per-column bound.
func EncodeCandidateModels(models []string) (string, bool) {
	if models == nil {
		models = []string{}
	}
	return encodeBounded(models)
}

// EncodeDistribution renders the normalized probability distribution for
// storage, reporting false when the encoded value would exceed the per-column
// bound.
func EncodeDistribution(probabilities []ModelProbability) (string, bool) {
	type entry struct {
		Model       string  `json:"model"`
		Probability float64 `json:"probability"`
	}
	encoded := make([]entry, 0, len(probabilities))
	for _, probability := range probabilities {
		encoded = append(encoded, entry{Model: probability.Model, Probability: probability.Probability})
	}
	return encodeBounded(encoded)
}

// EncodeCandidateGroups renders the bounded set of group identifiers for storage.
func EncodeCandidateGroups(groups []string) (string, bool) {
	if groups == nil {
		groups = []string{}
	}
	for _, group := range groups {
		if !validRoutingGroup(group) {
			return "", false
		}
	}
	return encodeBounded(groups)
}

// EncodeGroupDistribution renders Jev's normalized group distribution for storage.
func EncodeGroupDistribution(probabilities []GroupProbability) (string, bool) {
	type entry struct {
		Group       string  `json:"group"`
		Probability float64 `json:"probability"`
	}
	encoded := make([]entry, 0, len(probabilities))
	for _, probability := range probabilities {
		encoded = append(encoded, entry{Group: probability.Group, Probability: probability.Probability})
	}
	return encodeBounded(encoded)
}

// encodeBounded marshals a value and reports whether it fits the column bound.
func encodeBounded(value any) (string, bool) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", false
	}
	if len(raw) > MaxJevJSONBytes {
		return "", false
	}
	return string(raw), true
}

// validUnitInterval reports whether a float is inside [0, 1]. NaN compares
// false against both bounds, so it is rejected by the same expression.
func validUnitInterval(value float64) bool { return value >= 0 && value <= 1 }

// validIdentifier accepts the bounded token alphabet the request identifier
// already uses. It keeps free text out of every identifier column.
func validIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-' || c == ':' || c == '/':
		default:
			return false
		}
	}
	return true
}

// String renders a compact description of an event for a test failure or a
// diagnostic message. It never includes a token count that was not observed.
func (e Event) String() string {
	return fmt.Sprintf("event{request_id=%s protocol=%s mode=%s status=%d sequence=%d}",
		e.RequestID, e.Protocol, e.RoutingMode, e.Status, e.StartedAt.UnixNano())
}
