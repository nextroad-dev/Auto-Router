package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nextroad-dev/Auto-Router/internal/providers"
	"github.com/nextroad-dev/Auto-Router/internal/proxy"
	"github.com/nextroad-dev/Auto-Router/internal/router/auto"
	"github.com/nextroad-dev/Auto-Router/internal/router/decision"
)

// This file implements POST /debug/route, a dry run of the automatic routing
// path. It answers "where would model=auto send this request right now?" by
// calling the same auto.Router the forwarding handler uses:
//
//   - candidates come from the live registry snapshot and the configured model
//     groups, filtered by the same hard constraints;
//   - the group is chosen exactly as in production, including the real Jev call
//     (so the request is sent to Jev under the configured input mode) and the
//     default-group fallback;
//   - the pair inside the group is the first eligible member in configured order.
//
// It never calls a Provider executor and never writes the routing log. The
// response reports the target, the group decision and the features, and never
// the request body, a prompt or an inline payload.

// DebugRouter is the routing seam of the debug endpoint. *auto.Router implements
// it; nothing else in production does.
type DebugRouter interface {
	Route(ctx context.Context, request auto.Request) (auto.Target, error)
}

// RouteDebugOptions wires the debug routing endpoint.
type RouteDebugOptions struct {
	// Router is the production automatic router.
	Router DebugRouter
	// MaxRequestBytes bounds the debug request body, using the same setting the
	// forwarding path uses.
	MaxRequestBytes int64
}

// NewDebugRoute builds the POST /debug/route handler. The composition root only
// mounts it when routing.policy_debug_endpoint is enabled and the listener is
// loopback-only.
func NewDebugRoute(options RouteDebugOptions) http.Handler {
	handler := &debugRoute{
		router:   options.Router,
		maxBytes: options.MaxRequestBytes,
	}
	if handler.maxBytes <= 0 {
		handler.maxBytes = 16 << 20
	}
	return handler
}

type debugRoute struct {
	router   DebugRouter
	maxBytes int64
}

// ServeHTTP answers one dry-run routing decision. Every response carries the
// correlation identifier and Cache-Control: no-store, matching every other
// model-facing response of this service.
func (d *debugRoute) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := proxy.RequestIDFor(r)
	w.Header().Set(proxy.RequestIDHeader, requestID)
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeDebugError(w, http.StatusMethodNotAllowed, "invalid_request", "the debug endpoint accepts POST only")
		return
	}
	if d.router == nil {
		// A handler wired without its router is a composition bug. It is reported
		// as a server error rather than answered with an invented decision.
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
	target, err := d.router.Route(r.Context(), auto.Request{Protocol: protocol, Body: request.Body, RequestID: requestID})
	if err != nil {
		if errors.Is(err, auto.ErrCanceled) {
			// The client is gone; there is nobody to answer.
			return
		}
		var refusal *auto.RefusalError
		if errors.As(err, &refusal) {
			writeDebugRouteError(w, refusal)
			return
		}
		writeDebugError(w, http.StatusInternalServerError, "internal_error", "the request could not be routed")
		return
	}

	features, _ := target.Features()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(debugRouteResponse{
		Protocol:         string(protocol),
		RequestID:        requestID,
		Target:           presentDebugTarget(target),
		Jev:              presentDebugJev(target.JevTrace),
		Decision:         target.Decision,
		RoutingLatencyMS: target.RoutingLatency.Milliseconds(),
		Features:         presentFeatures(features),
	})
}

// debugRouteRequest is the endpoint's request body. Body stays raw so a malformed
// object is a client error rather than a silently empty analysis, and so the
// router receives exactly the bytes the caller sent.
type debugRouteRequest struct {
	Protocol string          `json:"protocol"`
	Body     json.RawMessage `json:"body"`
}

// debugRouteResponse is the response. The policy decision is embedded unchanged
// so its field set stays exactly decision.Decision's, and the features use the
// same DTO /debug/analyze uses.
type debugRouteResponse struct {
	Protocol         string               `json:"protocol"`
	RequestID        string               `json:"request_id"`
	Target           debugRouteTarget     `json:"target"`
	Jev              *debugRouteJev       `json:"jev"`
	Decision         decision.Decision    `json:"decision"`
	RoutingLatencyMS int64                `json:"routing_latency_ms"`
	Features         debugAnalyzeFeatures `json:"features"`
}

// debugRouteTarget is the destination production would forward to first.
type debugRouteTarget struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	UpstreamModel string `json:"upstream_model"`
	SelectedGroup string `json:"selected_group"`
	SelectionMode string `json:"selection_mode"`
	MaxAttempts   int    `json:"max_attempts"`
}

// debugRouteJev is the group-selection trace: whether Jev was asked, what it
// answered and which group was used.
type debugRouteJev struct {
	Status             string                  `json:"status"`
	FailureReason      string                  `json:"failure_reason,omitempty"`
	InputMode          string                  `json:"input_mode"`
	CandidateGroups    []string                `json:"candidate_groups"`
	CandidateCount     int                     `json:"candidate_count"`
	RecommendedGroup   string                  `json:"recommended_group,omitempty"`
	SelectedGroup      string                  `json:"selected_group"`
	Confidence         *float64                `json:"confidence"`
	ConfidenceBand     string                  `json:"confidence_band,omitempty"`
	FallbackReason     string                  `json:"fallback_reason"`
	LatencyMS          *int64                  `json:"latency_ms"`
	GroupProbabilities []debugGroupProbability `json:"group_probabilities"`
}

type debugGroupProbability struct {
	Group       string  `json:"group"`
	Probability float64 `json:"probability"`
}

func presentDebugTarget(target auto.Target) debugRouteTarget {
	return debugRouteTarget{
		Provider:      target.ProviderKey,
		Model:         target.Model,
		UpstreamModel: target.UpstreamModel,
		SelectedGroup: string(target.SelectedGroup),
		SelectionMode: target.SelectionMode,
		MaxAttempts:   target.MaxAttempts,
	}
}

func presentDebugJev(trace *auto.JevTrace) *debugRouteJev {
	if trace == nil {
		return nil
	}
	present := &debugRouteJev{
		Status:             trace.Status,
		FailureReason:      trace.FailureReason,
		InputMode:          trace.InputMode,
		CandidateGroups:    make([]string, 0, len(trace.CandidateGroups)),
		CandidateCount:     trace.CandidateCount,
		RecommendedGroup:   string(trace.RecommendedGroup),
		SelectedGroup:      string(trace.SelectedGroup),
		Confidence:         trace.Confidence,
		ConfidenceBand:     trace.ConfidenceBand,
		FallbackReason:     trace.FallbackReason,
		LatencyMS:          trace.LatencyMS,
		GroupProbabilities: make([]debugGroupProbability, 0, len(trace.GroupProbabilities)),
	}
	for _, group := range trace.CandidateGroups {
		present.CandidateGroups = append(present.CandidateGroups, string(group))
	}
	for _, probability := range trace.GroupProbabilities {
		present.GroupProbabilities = append(present.GroupProbabilities, debugGroupProbability{
			Group: string(probability.Group), Probability: probability.Probability,
		})
	}
	return present
}

// decodeDebugRoute parses and validates the request. Every rejection names the
// field or the rule, and none of them echoes a value: an error message is not a
// place to reflect client input.
func decodeDebugRoute(body []byte) (debugRouteRequest, providers.Protocol, error) {
	var request debugRouteRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return debugRouteRequest{}, "", errors.New("the request body must be a JSON object with protocol and body fields")
	}
	if err := requireNoTrailingContent(decoder); err != nil {
		return debugRouteRequest{}, "", err
	}
	protocol, ok := debugProtocol(request.Protocol)
	if !ok {
		return debugRouteRequest{}, "", errors.New(`protocol must be "chat_completions" or "responses"`)
	}
	if len(request.Body) == 0 {
		return debugRouteRequest{}, "", errors.New("body must be the JSON request object to route")
	}
	if trimmed := bytes.TrimSpace(request.Body); len(trimmed) == 0 || trimmed[0] != '{' {
		return debugRouteRequest{}, "", errors.New("body must be a JSON object")
	}
	return request, protocol, nil
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

// writeDebugRouteError answers with the shared OpenAI error envelope and, unlike
// the client boundary, includes the policy exclusions under decision.excluded so
// a refusal is as explainable as a successful decision. The message is the
// router's own generic text, never client input.
func writeDebugRouteError(w http.ResponseWriter, refusal *auto.RefusalError) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(refusal.Status)
	envelope := struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
			Param   any    `json:"param"`
		} `json:"error"`
		Decision debugRouteRefusal `json:"decision"`
	}{}
	envelope.Error.Message = refusal.Message
	envelope.Error.Type = proxy.ErrorTypeForStatus(refusal.Status)
	envelope.Error.Code = refusal.Code
	envelope.Decision.Excluded = refusal.Refusal.Exclusions
	if envelope.Decision.Excluded == nil {
		envelope.Decision.Excluded = []decision.Exclusion{}
	}
	envelope.Decision.ExcludedCount = refusal.Refusal.Count
	envelope.Decision.Fallback.Reason = string(refusal.Refusal.Reason)
	_ = json.NewEncoder(w).Encode(envelope)
}
