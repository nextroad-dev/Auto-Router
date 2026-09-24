package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http"

	"github.com/nextroad-dev/Auto-Router/internal/providers"
	"github.com/nextroad-dev/Auto-Router/internal/proxy"
	"github.com/nextroad-dev/Auto-Router/internal/router/analyzer"
	"github.com/nextroad-dev/Auto-Router/internal/router/decision"
	"github.com/nextroad-dev/Auto-Router/internal/router/policy"
)

// This file implements POST /debug/route, the offline counterpart of the
// auto-routing path. It exists so stage 6's policy engine can be observed over a
// real request body before anything routes in production.
//
// The endpoint is deliberately not a second routing path:
//
//   - The candidate list comes from the caller. Stage 7 owns the mapping from the
//     registry snapshot to a candidate list, and a debug endpoint that derived its
//     own candidates would be answering a different question than the real one.
//   - The Jev result comes from the caller. Nothing here calls TypeSafe, so the
//     endpoint is usable with no credential and no network: the request body is
//     the only input that leaves the machine's memory.
//   - It never touches the registry, the database or a Provider executor. There is exactly
//     one analysis and one policy evaluation, which is what makes it proof rather
//     than a second request path.
//
// The response reports the decision, the exclusions that produced it and the
// features it was derived from, and never the request body, a prompt or an inline
// payload.

// maxDebugCandidates bounds the candidate list a debug request may carry. The
// engine itself has no limit, because a real candidate list is bounded by the
// registry; the endpoint needs one so a hostile caller cannot make one evaluation
// arbitrarily large.
const maxDebugCandidates = 256

// RouteDebugOptions wires the debug policy endpoint.
type RouteDebugOptions struct {
	// Engine evaluates the policy. It is the static fallback used when Live is nil:
	// a debug handler without an engine would have to invent decisions.
	Engine *policy.Engine
	// Analyzer extracts features from the request body. It is the same analyzer the
	// forwarding path uses, so the endpoint reports one feature set, not two.
	Analyzer interface {
		Analyze(input analyzer.Input) (analyzer.Result, error)
	}
	// DefaultPreference is routing.default_preference. It is the static fallback used
	// when Live is nil.
	DefaultPreference analyzer.Preference
	// MaxRequestBytes bounds the debug request body, using the same setting the
	// forwarding path uses.
	MaxRequestBytes int64
	// Live supplies the default preference and the compiled policy engine in effect
	// right now. Nil means the static fields above are used unchanged.
	Live interface {
		DefaultPreference() analyzer.Preference
		Engine() *policy.Engine
	}
}

// NewDebugRoute builds the POST /debug/route handler. The composition root only
// mounts it when routing.policy_debug_endpoint is enabled and the listener is
// loopback-only.
func NewDebugRoute(options RouteDebugOptions) http.Handler {
	handler := &debugRoute{
		engine:            options.Engine,
		analyzer:          options.Analyzer,
		defaultPreference: options.DefaultPreference,
		maxBytes:          options.MaxRequestBytes,
		live:              options.Live,
	}
	if handler.maxBytes <= 0 {
		handler.maxBytes = 16 << 20
	}
	return handler
}

type debugRoute struct {
	engine   *policy.Engine
	analyzer interface {
		Analyze(input analyzer.Input) (analyzer.Result, error)
	}
	defaultPreference analyzer.Preference
	maxBytes          int64
	live              interface {
		DefaultPreference() analyzer.Preference
		Engine() *policy.Engine
	}
}

// currentEngine reports the compiled policy engine in effect right now. A live
// source that has not produced one falls back to the static engine, so the endpoint
// never reports "no policy" for a process that has one.
func (d *debugRoute) currentEngine() *policy.Engine {
	if d.live == nil {
		return d.engine
	}
	if engine := d.live.Engine(); engine != nil {
		return engine
	}
	return d.engine
}

// currentPreference reports routing.default_preference right now.
func (d *debugRoute) currentPreference() analyzer.Preference {
	if d.live == nil {
		return d.defaultPreference
	}
	preference := d.live.DefaultPreference()
	if !preference.Valid() {
		return d.defaultPreference
	}
	return preference
}

// ServeHTTP answers one policy evaluation. Every response carries the correlation
// identifier and Cache-Control: no-store, matching every other model-facing
// response of this service.
func (d *debugRoute) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := proxy.RequestIDFor(r)
	w.Header().Set(proxy.RequestIDHeader, requestID)
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeDebugError(w, http.StatusMethodNotAllowed, "invalid_request", "the debug endpoint accepts POST only")
		return
	}
	if d.currentEngine() == nil || d.analyzer == nil {
		// A handler wired without its dependencies is a composition bug. It is
		// reported as a server error rather than answered with an empty decision.
		writeDebugError(w, http.StatusInternalServerError, "debug_route_unavailable", "the debug route is not fully configured")
		return
	}
	body, err := readDebugBody(r, d.maxBytes)
	if err != nil {
		writeDebugError(w, http.StatusRequestEntityTooLarge, "request_too_large", err.Error())
		return
	}
	request, protocol, err := decodeDebugRoute(body)
	if err != nil {
		writeDebugError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	result, err := d.analyzer.Analyze(analyzer.Input{
		Protocol:          protocol,
		Body:              request.Body,
		HeaderPreference:  request.Preference,
		DefaultPreference: d.currentPreference(),
	})
	if err != nil {
		if errors.Is(err, analyzer.ErrInvalidPreference) {
			writeDebugError(w, http.StatusBadRequest, "invalid_routing_preference", err.Error())
			return
		}
		writeDebugError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	outcome, err := d.currentEngine().Evaluate(result.Features, request.candidateList(), request.Jev.signal())
	if err != nil {
		// A refused evaluation is a routing outcome, not a client-input error: the
		// request was well formed, and no candidate can serve it. The exclusions are
		// returned so the caller can see why, and the error text never contains
		// client input.
		status, code := debugRouteError(err)
		writeDebugRouteError(w, status, code, err, policy.RefusalOf(err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(debugRouteResponse{
		Protocol:  string(protocol),
		RequestID: requestID,
		Decision:  outcome.Decision,
		Features:  presentFeatures(outcome.Features),
		Evidence: debugRouteEvidence{
			ExcludedCount: outcome.ExcludedCount,
			Features:      presentFeatures(outcome.Features),
		},
	})
}

// debugRouteRequest is the endpoint's request body. Body stays raw so a malformed
// object is a client error rather than a silently empty analysis, and so the
// analyzer receives exactly the bytes the caller sent.
type debugRouteRequest struct {
	Protocol   string                `json:"protocol"`
	Body       json.RawMessage       `json:"body"`
	Preference string                `json:"preference"`
	Candidates []debugRouteCandidate `json:"candidates"`
	Jev        debugRouteJev         `json:"jev"`
}

// debugRouteCandidate is one provider/model pair the caller allows. The field set
// mirrors decision.Candidate one-to-one so a caller can move between the two
// without a mapping table.
type debugRouteCandidate struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// GatewayModel/gateway_model are historical contract names; the value is
	// provider-native and is not prefixed before direct execution.
	GatewayModel       string `json:"gateway_model"`
	ContextWindow      int    `json:"context_window"`
	MaxOutput          *int   `json:"max_output"`
	SupportsTools      bool   `json:"supports_tools"`
	SupportsVision     bool   `json:"supports_vision"`
	SupportsReasoning  bool   `json:"supports_reasoning"`
	SupportsAudioInput bool   `json:"supports_audio_input"`
	PairPriority       int    `json:"pair_priority"`
	ProviderPriority   int    `json:"provider_priority"`
}

// debugRouteJev is the recommendation the caller supplies. Nothing in this package
// fetches it: the endpoint never calls TypeSafe.
type debugRouteJev struct {
	Available     bool               `json:"available"`
	Failure       string             `json:"failure"`
	Selected      string             `json:"selected"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

// debugRouteResponse is the response. The decision is embedded rather than
// flattened so its field set stays exactly decision.Decision's, and the features
// are reported through the same DTO /debug/analyze uses, so the two endpoints
// cannot drift into two feature vocabularies.
type debugRouteResponse struct {
	Protocol  string               `json:"protocol"`
	RequestID string               `json:"request_id"`
	Decision  decision.Decision    `json:"decision"`
	Evidence  debugRouteEvidence   `json:"evidence"`
	Features  debugAnalyzeFeatures `json:"features"`
}

type debugRouteEvidence struct {
	ExcludedCount int `json:"excluded_count"`
	// Features is the same view the envelope carries, kept under "evidence" so a
	// consumer that only wants the facts does not have to reach into the decision
	// envelope.
	Features debugAnalyzeFeatures `json:"features"`
}

// decodeDebugRoute parses and validates the request. Every rejection names the
// field or the rule, and none of them echoes a value: an error message is not a
// place to reflect client input.
func decodeDebugRoute(body []byte) (debugRouteRequest, providers.Protocol, error) {
	var request debugRouteRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return debugRouteRequest{}, "", errors.New("the request body must be a JSON object with protocol, body and candidates fields")
	}
	if err := requireNoTrailingContent(decoder); err != nil {
		return debugRouteRequest{}, "", err
	}
	protocol, ok := debugProtocol(request.Protocol)
	if !ok {
		return debugRouteRequest{}, "", errors.New(`protocol must be "chat_completions" or "responses"`)
	}
	if len(request.Body) == 0 {
		return debugRouteRequest{}, "", errors.New("body must be the JSON request object to analyze")
	}
	if trimmed := bytes.TrimSpace(request.Body); len(trimmed) == 0 || trimmed[0] != '{' {
		return debugRouteRequest{}, "", errors.New("body must be a JSON object")
	}
	if len(request.Candidates) == 0 {
		return debugRouteRequest{}, "", errors.New("candidates must contain at least one provider/model pair")
	}
	if len(request.Candidates) > maxDebugCandidates {
		return debugRouteRequest{}, "", errors.New("candidates must not contain more than 256 entries")
	}
	if request.Jev.Failure != "" && !decision.FallbackReason(request.Jev.Failure).Valid() {
		return debugRouteRequest{}, "", errors.New("jev.failure must be a known failure classification")
	}
	if request.Jev.Available && request.Jev.Selected == "" {
		return debugRouteRequest{}, "", errors.New("jev.selected is required when jev.available is true")
	}
	if request.Jev.Available && (math.IsNaN(request.Jev.Confidence) || math.IsInf(request.Jev.Confidence, 0)) {
		return debugRouteRequest{}, "", errors.New("jev.confidence must be a number between 0 and 1")
	}
	return request, protocol, nil
}

// candidates converts the wire form into the engine's type. The conversion is
// mechanical: the endpoint adds no capabilities and infers no priorities.
func (r debugRouteRequest) candidateList() []decision.Candidate {
	candidates := make([]decision.Candidate, 0, len(r.Candidates))
	for _, candidate := range r.Candidates {
		candidates = append(candidates, decision.Candidate{
			ModelID:            candidate.Model,
			ProviderKey:        candidate.Provider,
			GatewayModel:       candidate.GatewayModel,
			ContextWindow:      candidate.ContextWindow,
			MaxOutput:          candidate.MaxOutput,
			SupportsTools:      candidate.SupportsTools,
			SupportsVision:     candidate.SupportsVision,
			SupportsReasoning:  candidate.SupportsReasoning,
			SupportsAudioInput: candidate.SupportsAudioInput,
			PairPriority:       candidate.PairPriority,
			ProviderPriority:   candidate.ProviderPriority,
		})
	}
	return candidates
}

// signal converts the wire form into the engine's Jev input. An absent
// recommendation is reported as "not requested" rather than as a failure: the
// endpoint cannot know why the caller did not supply one.
func (j debugRouteJev) signal() policy.JevSignal {
	if !j.Available {
		failure := decision.ReasonNotRequested
		if j.Failure != "" {
			failure = decision.FallbackReason(j.Failure)
		}
		return policy.JevSignal{Available: false, Failure: failure}
	}
	return policy.JevSignal{
		Available:     true,
		Selected:      j.Selected,
		Confidence:    j.Confidence,
		Probabilities: j.Probabilities,
	}
}

// debugRouteError maps a policy refusal onto the shared error taxonomy. A refused
// routing question is a client-visible outcome (422), while a malformed candidate
// list is a caller mistake (400): the difference matters because one is a routing
// answer and the other is a bug in the caller's derivation.
func debugRouteError(err error) (int, string) {
	switch {
	case errors.Is(err, policy.ErrNoEligibleCandidate):
		return http.StatusUnprocessableEntity, "no_eligible_candidate"
	case errors.Is(err, policy.ErrTruncatedEvidence):
		return http.StatusUnprocessableEntity, "truncated_evidence"
	case errors.Is(err, policy.ErrInvalidCandidates):
		return http.StatusBadRequest, "invalid_request"
	default:
		return http.StatusBadRequest, "invalid_request"
	}
}

// debugRouteRefusal is the explanation an error answer carries. It repeats the
// shape a successful decision uses, so a caller parses one vocabulary rather than
// two.
type debugRouteRefusal struct {
	Excluded      []decision.Exclusion `json:"excluded"`
	ExcludedCount int                  `json:"excluded_count"`
	Fallback      struct {
		Reason string `json:"reason"`
	} `json:"fallback"`
}

// writeDebugRouteError answers with the shared OpenAI error envelope and, when the
// engine recorded exclusions, includes them under decision.excluded so an error
// answer is as explainable as a successful one. The message is the engine's own: it
// is built from closed enumerations and numbers, never from client text.
func writeDebugRouteError(w http.ResponseWriter, status int, code string, err error, refusal policy.Refusal) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	envelope := struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
			Param   any    `json:"param"`
		} `json:"error"`
		Decision debugRouteRefusal `json:"decision"`
	}{}
	envelope.Error.Message = err.Error()
	envelope.Error.Type = proxy.ErrorTypeForStatus(status)
	envelope.Error.Code = code
	envelope.Error.Param = nil
	envelope.Decision.Excluded = refusal.Exclusions
	envelope.Decision.ExcludedCount = refusal.Count
	envelope.Decision.Fallback.Reason = string(refusal.Reason)
	_ = json.NewEncoder(w).Encode(envelope)
}
