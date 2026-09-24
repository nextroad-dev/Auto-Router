// Package auto orchestrates one automatic routing decision. It is the only place
// that knows the whole pipeline, and it depends on each stage through a small
// contract rather than the other way round:
//
//	Catalog snapshot → pair-level candidates
//	  → Analyzer (features + in-memory view)
//	  → hard-filter pre-check (is this request routable at all?)
//	  → Policy decision (Jev recommendation optional, at most one call)
//	  → one resolved forwarding target
//
// The contract this package guarantees:
//
//   - One decision, one target: Route returns the destination the policy engine
//     chose, its explanation and everything the routing log needs. It never
//     forwards anything, never reads a database, and never sends a request to a
//     provider: the HTTP boundary in internal/proxy owns the executor call.
//   - Hard filters before spending: the deterministic refusals (a truncated
//     analysis the policy refuses, or no eligible candidate at all) are decided
//     by policy.Engine.Eligible before any Jev call. A request that cannot be
//     routed is never sent to a metered endpoint to find that out.
//   - Jev runs at most once and only with an enabled client and usable prompt
//     evidence. Skipping is reported as a status, never as a failure.
//   - Jev selects one of three groups; the first pair in that group's configured
//     order that passes the hard filters is selected.
//   - Bounded failover advances only through that group's ordered pairs, only for
//     a transport failure known to precede delivery. Analysis and Jev run once.
//   - No client text leaves through any other door: the package never logs,
//     never persists and never echoes the request body. What the Jev input mode
//     does and does not send is documented on jev.InputMode and implemented in
//     prompt.go/redact.go.
//
// The dependency set is part of the contract and is pinned by a source-level
// import guard test: this package must not import internal/storage, internal/api,
// internal/proxy or database/sql. It reads the registry through models.Store and
// depends on a providers.Executor nowhere.
package auto

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
	"github.com/nextroad-dev/Auto-Router/internal/router/analyzer"
	"github.com/nextroad-dev/Auto-Router/internal/router/decision"
	"github.com/nextroad-dev/Auto-Router/internal/router/jev"
	"github.com/nextroad-dev/Auto-Router/internal/router/policy"
)

// GroupName identifies one of the prompt-selected routing groups.
type GroupName string

const (
	GroupSimple     GroupName = "simple"
	GroupMedium     GroupName = "medium"
	GroupComplex    GroupName = "complex"
	maxGroupMembers           = 8
)

// GroupMember identifies an ordered provider/model pair.
type GroupMember struct {
	ProviderKey string `json:"provider_key"`
	ModelID     string `json:"model_id"`
}

// GroupConfig carries the ordered membership snapshot for routing.
type GroupConfig struct {
	Simple  []GroupMember `json:"simple"`
	Medium  []GroupMember `json:"medium"`
	Complex []GroupMember `json:"complex"`
}

func (g GroupConfig) members(name GroupName) []GroupMember {
	switch name {
	case GroupSimple:
		return g.Simple
	case GroupComplex:
		return g.Complex
	default:
		return g.Medium
	}
}

// Status codes the refusal contract reports. They are plain integers so this
// package stays free of net/http — the routing layer describes outcomes, the HTTP
// boundary maps them onto the wire — and a test pins them against the standard
// library constants.
const (
	// statusUnprocessableEntity is the status of a request no candidate can
	// serve. It is a request-shaped outcome, not a routing-infrastructure fault,
	// which is why it is not a 5xx: the client can fix the request.
	statusUnprocessableEntity = 422
	// statusServiceUnavailable is the status of a routing decision this process
	// cannot make at all: an unassembled orchestrator, a missing engine, or a
	// registry with no enabled pair.
	statusServiceUnavailable = 503
	// statusInternalError is the status of a programming error in the caller
	// (an invalid candidate list), which is not a routing outcome.
	statusInternalError = 500
)

// Public error codes. They are the stable half of the client contract and are
// deliberately few: an automated client branches on them, so widening the set is
// a compatibility decision.
const (
	// CodeNoEligibleCandidate means every candidate was excluded by a hard filter.
	CodeNoEligibleCandidate = "no_eligible_candidate"
	// CodeTruncatedEvidence means the analyzer did not see the whole request and
	// the policy refuses to route on partial evidence.
	CodeTruncatedEvidence = "truncated_evidence"
	// CodeRoutingUnavailable means this process cannot evaluate any candidate.
	CodeRoutingUnavailable = "routing_unavailable"
	// CodeInternalError means the caller supplied something unusable.
	CodeInternalError = "internal_error"
)

// Jev status values. They are reported per request so a routing log distinguishes
// "Jev was off" from "Jev was not asked because it could not have said anything"
// from "Jev answered" from "Jev failed".
const (
	// JevStatusDisabled means no usable Jev client is configured.
	JevStatusDisabled = "disabled"
	// JevStatusSkippedSingleModel means fewer than minModelsForJev distinct
	// eligible models were left, so a recommendation could not express a choice.
	JevStatusSkippedSingleModel = "skipped_single_model"
	// JevStatusSkippedInsufficientEvidence means the selected input mode left
	// nothing to judge — no system text and no conversation text.
	JevStatusSkippedInsufficientEvidence = "skipped_insufficient_evidence"
	// JevStatusSkippedTooManyModels means the eligible model set exceeds the
	// verified candidate limit of the Jev API, so the question could not be asked
	// as a single Choice.
	JevStatusSkippedTooManyModels = "skipped_too_many_models"
	// JevStatusOK means a recommendation was produced and used as engine input.
	JevStatusOK = "ok"
	// JevStatusFailurePrefix prefixes the closed fallback reason of a call that
	// failed, so one log field distinguishes every failure classification without
	// a second vocabulary.
	JevStatusFailurePrefix = "failure:"
)

// ErrRoutingUnavailable is the sentinel for "this process cannot evaluate any
// candidate". It is reported as 503 routing_unavailable: a configuration problem,
// not a request problem, and never a reason to relax a constraint.
var ErrRoutingUnavailable = &RefusalError{
	Status:  statusServiceUnavailable,
	Code:    CodeRoutingUnavailable,
	Message: "automatic routing is not available",
}

// ErrCanceled means the client went away while the decision was being made. It is
// distinct from every routing outcome so the boundary can report "no response
// body" instead of an error the client never asked for.
var ErrCanceled = errors.New("automatic routing canceled")

// RefusalError is one client-facing rejection. It carries the status and the
// public code so the HTTP boundary can map it without importing this package, and
// a minimal message: the message never names a provider, a model, a candidate or
// an exclusion reason. Those details belong in the routing log and in the
// loopback-only debug endpoint, never in a response an unauthenticated client can
// read.
type RefusalError struct {
	// Status is the HTTP status the boundary writes.
	Status int
	// Code is the public, closed-set error code.
	Code string
	// Message is a short, generic explanation with no identifiers in it.
	Message string
}

func (e *RefusalError) Error() string { return e.Message }

// StatusCode implements the status half of the boundary's refusal contract.
func (e *RefusalError) StatusCode() int { return e.Status }

// ErrorCode implements the code half of the boundary's refusal contract.
func (e *RefusalError) ErrorCode() string { return e.Code }

// Analyzer is the feature-extraction seam. It matches the analyzer package's
// production implementation and the interface the policy engine already exposes,
// so a test can substitute a fake without weakening the pipeline.
type Analyzer interface {
	Analyze(input analyzer.Input) (analyzer.Result, error)
}

// JevCaller is the one method the orchestrator needs from the Jev client. It is a
// one-method interface so a fake can be substituted, and so this package does not
// depend on the client's constructor or its transport configuration.
type JevCaller interface {
	Route(ctx context.Context, request jev.Request) (jev.Result, error)
}

// Settings is the runtime configuration the orchestrator reads. It is read once
// per decision, so a change made by an administrator applies to the next request
// and never to one already being routed.
//
// It is an interface rather than fields on Options because the orchestrator must
// not depend on the settings store, and nil is a documented state: a router built
// without it uses the static values on Options and behaves exactly as it did before
// this interface existed.
//
// JevEnabled is deliberately separate from Options.Jev: the client is built once,
// at startup, and this switch decides whether a decision may spend a call on it.
// Turning it off must report "disabled", not "failure", because those are different
// facts about a request.
type RuntimeSettings struct {
	Engine            *policy.Engine
	JevEnabled        bool
	Jev               JevCaller
	InputMode         jev.InputMode
	FailoverEnabled   bool
	DefaultPreference analyzer.Preference
	GroupConfig       GroupConfig
}

type Settings interface {
	RuntimeSnapshot() RuntimeSettings
}

// Options wires the orchestrator. Catalog, Analyzer and Engine are required for a
// usable router; Jev may be nil, which is the documented "Jev is disabled" state
// and is reported as JevStatusDisabled rather than as a failure.
type Options struct {
	// Catalog is the registry snapshot source. It is read per request so a
	// snapshot swap takes effect on the next decision.
	Catalog *models.Store
	// Analyzer extracts features and the in-memory conversation view.
	Analyzer Analyzer
	// Jev is the recommendation client. Nil means disabled.
	Jev JevCaller
	// Engine is the compiled policy engine. Missing means every request is
	// refused with 503: a process that cannot evaluate candidates must not
	// pretend to have routed.
	Engine *policy.Engine
	// DefaultPreference is routing.default_preference, applied when the request
	// carried no preference header. The boundary passes the already-resolved
	// preference per request, so this is only the fallback for a caller that
	// passes none.
	DefaultPreference analyzer.Preference
	// InputMode selects how much of the conversation reaches Jev.
	InputMode jev.InputMode
	// FailoverEnabled allows the single bounded retry. It is the static fallback
	// used when Settings is nil.
	FailoverEnabled bool
	// Settings supplies the live values above. Nil means the static fields are used
	// unchanged.
	Settings Settings
	// GroupConfig is the static ordered membership snapshot used without live settings.
	GroupConfig GroupConfig
}

// Router is a compiled orchestrator. It is immutable after New and safe for
// concurrent use: every decision reads its own snapshot and the compiled engine,
// and the only mutable state (the snapshot pointer) is published atomically by
// models.Store.
type Router struct {
	catalog           *models.Store
	analyzer          Analyzer
	jev               JevCaller
	engine            *policy.Engine
	defaultPreference analyzer.Preference
	inputMode         jev.InputMode
	failoverEnabled   bool
	settings          Settings
	groupConfig       GroupConfig
}

// currentEngine returns the compiled policy engine in effect right now. It is read
// once per decision, so one decision is evaluated by exactly one engine even if an
// administrator changes the policy while the request is in flight.
func (r *Router) runtimeSnapshot() RuntimeSettings {
	if r.settings != nil {
		snapshot := r.settings.RuntimeSnapshot()
		if snapshot.Engine == nil {
			snapshot.Engine = r.engine
		}
		if !snapshot.DefaultPreference.Valid() {
			snapshot.DefaultPreference = r.defaultPreference
		}
		if _, err := jev.ParseInputMode(string(snapshot.InputMode)); err != nil {
			snapshot.InputMode = r.inputMode
		}
		snapshot.GroupConfig = cloneGroupConfig(snapshot.GroupConfig)
		return snapshot
	}
	return RuntimeSettings{Engine: r.engine, JevEnabled: r.jev != nil, Jev: r.jev, InputMode: r.inputMode, FailoverEnabled: r.failoverEnabled, DefaultPreference: r.defaultPreference, GroupConfig: cloneGroupConfig(r.groupConfig)}
}

// jevEnabled reports whether Jev may be called right now. A router without a live
// source reports the static state: a client is present or it is not.

// currentInputMode reports the input mode in effect right now.

// failoverAllowed reports whether one bounded retry is allowed right now.

// preference reports routing.default_preference right now.

// New builds the orchestrator. It never returns nil and never fails: a router
// missing a component answers every request with 503 routing_unavailable, which
// is the same contract the forwarding path uses for an unassembled orchestrator
// and is far easier to diagnose than a panic.
func New(options Options) *Router {
	preference := options.DefaultPreference
	if !preference.Valid() {
		preference = analyzer.PreferenceBalanced
	}
	inputMode := options.InputMode
	if _, err := jev.ParseInputMode(string(inputMode)); err != nil {
		inputMode = jev.InputModeRedacted
	}
	return &Router{
		catalog:           options.Catalog,
		analyzer:          options.Analyzer,
		jev:               options.Jev,
		engine:            options.Engine,
		defaultPreference: preference,
		inputMode:         inputMode,
		failoverEnabled:   options.FailoverEnabled,
		settings:          options.Settings,
		groupConfig:       cloneGroupConfig(options.GroupConfig),
	}
}

// Request is one automatic routing question: the raw body the client sent, the
// preference the HTTP boundary already resolved, and the correlation identifier.
type Request struct {
	Protocol providers.Protocol
	Body     []byte
	// Runtime is the request-start settings snapshot supplied by an integrated
	// HTTP boundary. Nil uses the router's settings source.
	Runtime *RuntimeSettings
	// Preference is the routing preference that applies to this request, already
	// resolved and validated at the HTTP boundary.
	Preference analyzer.Preference
	// PreferenceSource says whether Preference came from the request header or
	// from the configured default, so the analyzer reports the same source the
	// boundary logged.
	PreferenceSource analyzer.PreferenceSource
	// RequestID is the correlation identifier. It is carried for logging by the
	// caller; it never reaches Jev or the selected Provider from here.
	RequestID string
}

// Target is one resolved forwarding destination plus the explanation behind it.
type Target struct {
	// ProviderKey is the registry provider key of the chosen pair.
	ProviderKey string
	// Model is the chosen logical model ID.
	Model string
	// UpstreamModel is the provider-native identifier from the selected pair.
	// It is carried in the historical GatewayModel decision field without a
	// Provider prefix.
	UpstreamModel string
	// RoutingModel is the model the client asked for. It is the reserved
	// automatic identifier, carried so the routing log has the same field on both
	// paths without the boundary having to remember what it sent.
	RoutingModel string
	// SelectionMode is the closed name of the rule that produced the selection:
	// the policy origin (jev, blend, default_model, first_eligible).
	SelectionMode string
	// SelectedGroup is the validated Jev group, or medium on Jev failure.
	SelectedGroup GroupName
	// Decision is the complete policy decision: band, origin, confidence,
	// exclusions, signals and evidence hash. It is what the routing log records.
	Decision decision.Decision
	// JevStatus is one of the JevStatus constants of this package.
	JevStatus string
	// JevLatency is how long the single Jev call took. Zero when none was made.
	JevLatency time.Duration
	// RoutingLatency is how long the whole decision took, from catalog read to
	// selected target.
	RoutingLatency time.Duration
	// Attempts is how many upstream attempts the target accounts for. The first
	// target is one; a failover target is two.
	Attempts int
	// JevTrace is the diagnostic trace of the single Jev call this decision made,
	// or nil when no call was made. It carries identifiers, counts, durations and
	// the normalized distribution only: there is no field for a prompt, a tool
	// description or the raw request and response bytes, so it can be persisted
	// without a redaction step. The routing log stores it only when
	// routing.log.jev_trace.enabled is on.
	JevTrace *JevTrace
	// state is everything a bounded retry needs: the features and the
	// recommendation must not be recomputed, or a retry would consult Jev twice
	// and could pick a different model than the one being retried.
	state *routeState
}

// JevTrace is the debug trace of one Jev call. It is metadata only and is
// deliberately a distinct type from the routing log's own record: the orchestration
// layer describes what it asked and what it received, and the logging package maps
// it onto columns.
//
// The type has no field for text of any kind. That is the privacy property the
// trace is allowed to have: enabling it stores the question's shape, never the
// question.
type JevTrace struct {
	// Status is one of the JevStatus constants.
	Status string
	// FailureReason is empty or one of the five failure classifications carried by
	// a failure status.
	FailureReason string
	// InputMode is the configured jev.input_mode, recorded so a stored trace says
	// how much of the conversation the call was allowed to see.
	InputMode string
	// Selected is the recommended logical model. It is empty unless the call
	// succeeded, and it is an identifier from the candidate list rather than a
	// provider pair.
	Selected string
	// SelectedGroup records the choice without storing any prompt text.
	SelectedGroup GroupName
	// ConfidenceBand and FallbackReason mirror the decision the recommendation
	// contributed to. They are filled in after the policy engine ran.
	ConfidenceBand string
	FallbackReason string
	// EvidenceHash is the decision's evidence summary.
	EvidenceHash string
	// LatencyMS is how long the call took, nil when no call was made.
	LatencyMS *int64
	// CandidateModels lists the three group identifiers offered to Jev in fixed
	// simple, medium, complex order; it contains no prompt rendering.
	CandidateModels []string
	// ModelCount is the number of distinct logical models asked about, and
	// CandidateCount is the number of eligible pair-level candidates those models
	// were derived from.
	ModelCount     int
	CandidateCount int
	// Confidence is the recommendation confidence, nil when no recommendation was
	// produced.
	Confidence *float64
	// Probabilities is the normalized distribution, ordered by descending
	// probability with the model identifier ascending as the tie-break.
	Probabilities []ModelProbability
}

// ModelProbability is one entry of the normalized distribution.
type ModelProbability struct {
	Model       string
	Probability float64
}

// routeState is the immutable part of a decision that failover reuses.
type routeState struct {
	features        analyzer.Features
	candidates      []decision.Candidate
	jev             policy.JevSignal
	runtime         RuntimeSettings
	group           GroupName
	groupCandidates []decision.Candidate
}

// Route produces one routing decision. It performs at most one Jev call and never
// performs an upstream call.
func (r *Router) Route(ctx context.Context, request Request) (Target, error) {
	if r == nil || r.catalog == nil || r.analyzer == nil {
		return Target{}, ErrRoutingUnavailable
	}
	// The complete settings snapshot is read once: in-flight requests retain the
	// same policy, Jev client, privacy mode and failover switch throughout.
	runtime := r.runtimeSnapshot()
	if request.Runtime != nil {
		runtime = *request.Runtime
	}
	runtime.GroupConfig = cloneGroupConfig(runtime.GroupConfig)
	engine := runtime.Engine
	if engine == nil {
		return Target{}, ErrRoutingUnavailable
	}
	started := time.Now()
	// Candidate derivation comes first so a registry with no enabled pair is
	// answered before the request is even analyzed: that is a configuration
	// problem, and it costs no analyzer work to report.
	candidates := Candidates(r.catalog.Load())
	if len(candidates) == 0 {
		return Target{}, ErrRoutingUnavailable
	}
	// A client that has already disconnected is not a routing question: stop
	// before any analysis or call, and let the boundary write nothing.
	if err := ctx.Err(); err != nil {
		return Target{}, fmt.Errorf("%w: %w", ErrCanceled, err)
	}
	analysis, err := r.analyzer.Analyze(r.input(request, runtime))
	if err != nil {
		// The analyzer only rejects router-owned input (an unknown protocol or an
		// unusable preference). Reaching this branch means this process was
		// configured or wired inconsistently, which is a 500, not a refusal.
		return Target{}, &RefusalError{Status: statusInternalError, Code: CodeInternalError, Message: "the request could not be routed"}
	}
	features, view := analysis.Features, analysis.View
	eligible, err := engine.Eligible(features, candidates)
	if err != nil {
		return Target{}, refusalFor(err)
	}
	envelopes := modelEnvelopes(eligible)
	group, trace := r.recommendGroup(ctx, features, view, envelopes, len(eligible), runtime)
	groupCandidates := orderedGroupCandidates(runtime.GroupConfig, group, eligible)
	if len(groupCandidates) == 0 {
		return Target{}, refusalFor(policy.ErrNoEligibleCandidate)
	}
	status := trace.Status
	var jevLatency time.Duration
	if trace.LatencyMS != nil {
		jevLatency = time.Duration(*trace.LatencyMS) * time.Millisecond
	}
	// Cancellation wins over everything a recommendation returned: no upstream
	// call may follow a client that is already gone.
	if err := ctx.Err(); err != nil {
		return Target{}, fmt.Errorf("%w: %w", ErrCanceled, err)
	}
	outcome, err := engine.EvaluateFirstEligible(features, groupCandidates, policy.NotRequested())
	if err != nil {
		// The survivor set cannot have changed between the pre-check and this
		// call, so a refusal here repeats the pre-check's reason.
		return Target{}, refusalFor(err)
	}
	// The trace is completed once the decision exists: the band, the fallback
	// reason and the evidence hash describe the decision the recommendation
	// contributed to, and they are exactly what the routing log correlates with.
	trace.ConfidenceBand = string(outcome.Decision.ConfidenceBand)
	trace.FallbackReason = string(outcome.Decision.Fallback.Reason)
	trace.EvidenceHash = outcome.Decision.EvidenceHash
	return Target{
		ProviderKey:    outcome.Provider,
		Model:          outcome.Model,
		UpstreamModel:  outcome.GatewayModel,
		RoutingModel:   models.AutoModelID,
		SelectionMode:  string(outcome.Origin),
		SelectedGroup:  group,
		Decision:       outcome.Decision,
		JevStatus:      status,
		JevLatency:     jevLatency,
		RoutingLatency: time.Since(started),
		Attempts:       1,
		JevTrace:       trace,
		state: &routeState{
			features:        features,
			candidates:      groupCandidates,
			jev:             policy.NotRequested(),
			runtime:         runtime,
			group:           group,
			groupCandidates: groupCandidates,
		},
	}, nil
}

// Failover resolves the destination for one bounded additional upstream attempt
// after cause failed. It reports false when no further attempt may be made, and
// the caller then reports the failure it already has.
//
// The rules are fixed: only a failure known to precede delivery may advance to
// the next configured pair, and only within the selected group. Timeouts,
// cancellations and failures after response headers never trigger a retry. The
// analyzer and Jev are not called again.
func (r *Router) Failover(ctx context.Context, previous Target, cause error) (Target, bool) {
	if r == nil || previous.state == nil {
		return Target{}, false
	}
	state := previous.state
	engine := state.runtime.Engine
	if engine == nil {
		return Target{}, false
	}
	if !state.runtime.FailoverEnabled || previous.Attempts >= maxGroupMembers {
		return Target{}, false
	}
	if ctx.Err() != nil {
		return Target{}, false
	}
	if !isPreRequestFailure(cause) {
		return Target{}, false
	}
	remaining := withoutPair(state.groupCandidates, previous.ProviderKey, previous.Model)
	if len(remaining) == 0 {
		// Every candidate has now failed. There is nothing left to try, so the
		// caller reports the upstream failure it already has rather than a
		// request-shaped refusal.
		return Target{}, false
	}
	outcome, err := engine.EvaluateFirstEligible(state.features, remaining, state.jev)
	if err != nil {
		// The retry could not produce a target either. Reporting the original
		// upstream failure is more accurate than surfacing a second refusal about
		// a request the router had already accepted.
		return Target{}, false
	}
	return Target{
		ProviderKey:    outcome.Provider,
		Model:          outcome.Model,
		UpstreamModel:  outcome.GatewayModel,
		RoutingModel:   previous.RoutingModel,
		SelectionMode:  string(outcome.Origin),
		SelectedGroup:  state.group,
		Decision:       outcome.Decision,
		JevStatus:      previous.JevStatus,
		JevLatency:     previous.JevLatency,
		RoutingLatency: previous.RoutingLatency,
		Attempts:       previous.Attempts + 1,
		// The trace describes the one Jev call, which a failover does not repeat.
		// It is carried through unchanged except for the fields that describe the
		// retried decision rather than the call.
		JevTrace: traceForDecision(previous.JevTrace, outcome.Decision),
		state: &routeState{
			features:        state.features,
			candidates:      remaining,
			jev:             state.jev,
			runtime:         state.runtime,
			group:           state.group,
			groupCandidates: remaining,
		},
	}, true
}

// isPreRequestFailure reports whether a failed attempt may be repeated. Three
// facts must hold at once: the adapter must be certain the request never reached
// the provider, the failure must not be a timeout (a slow upstream may have
// accepted the request before its header deadline expired), and it must not be a
// cancellation (the client is gone, so a retry would work for nobody).
func isPreRequestFailure(cause error) bool {
	if cause == nil {
		return false
	}
	if errors.Is(cause, providers.ErrCanceled) || errors.Is(cause, providers.ErrUpstreamTimeout) {
		return false
	}
	return errors.Is(cause, providers.ErrPreRequestFailure)
}

// withoutPair copies a candidate list without one provider/model pair, keeping
// the contract order of the rest. The list stays a slice (not a set) because the
// order is the policy engine's final tie-break.
func withoutPair(candidates []decision.Candidate, providerKey, modelID string) []decision.Candidate {
	remaining := make([]decision.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ProviderKey == providerKey && candidate.ModelID == modelID {
			continue
		}
		remaining = append(remaining, candidate)
	}
	return remaining
}

// input renders the analyzer input. The preference the boundary already resolved
// is passed in the form that reproduces the same preference and the same source,
// so the analyzer never sees (and never has to re-reject) client text that has
// already been accepted.
func (r *Router) input(request Request, runtime RuntimeSettings) analyzer.Input {
	input := analyzer.Input{
		Protocol:          request.Protocol,
		Body:              request.Body,
		DefaultPreference: runtime.DefaultPreference,
	}
	switch {
	case request.PreferenceSource == analyzer.SourceHeader && request.Preference.Valid():
		input.HeaderPreference = string(request.Preference)
	case request.Preference.Valid():
		input.DefaultPreference = request.Preference
	}
	return input
}

// recommendGroup asks Jev to classify the prompt into one of the three closed
// group names. The trace contains only the choice, counts and timing metadata.
func (r *Router) recommendGroup(ctx context.Context, features analyzer.Features, view analyzer.View, envelopes []modelEnvelope, eligibleCount int, runtime RuntimeSettings) (GroupName, *JevTrace) {
	trace := &JevTrace{
		Status:          JevStatusDisabled,
		InputMode:       string(runtime.InputMode),
		CandidateModels: []string{string(GroupSimple), string(GroupMedium), string(GroupComplex)},
		ModelCount:      3,
		CandidateCount:  eligibleCount,
		SelectedGroup:   GroupMedium,
	}
	if !runtime.JevEnabled || runtime.Jev == nil {
		return GroupMedium, trace
	}
	built, ok := r.buildPrompt(features, view, envelopes, runtime.InputMode)
	if !ok {
		trace.Status = JevStatusSkippedInsufficientEvidence
		return GroupMedium, trace
	}
	start := time.Now()
	result, err := runtime.Jev.Route(ctx, jev.Request{
		Protocol: features.Protocol,
		Candidates: []jev.Candidate{
			{Model: string(GroupSimple), ContextWindow: 1000000, Description: "Choose for a straightforward request with limited reasoning."},
			{Model: string(GroupMedium), ContextWindow: 1000000, Description: "Choose for a request requiring moderate reasoning or multiple steps."},
			{Model: string(GroupComplex), ContextWindow: 1000000, Description: "Choose for a difficult request requiring extensive reasoning or complex work."},
		},
		Messages:     built.messages,
		SystemPrompt: built.system,
		Tools:        built.tools,
	})
	latency := time.Since(start).Milliseconds()
	trace.LatencyMS = &latency
	if err != nil {
		signal := policy.JevSignalFromError(err, jevSentinels())
		failure := signal.Failure
		if failure == "" || failure == decision.ReasonNotRequested {
			failure = decision.ReasonJevUnavailable
		}
		trace.Status = JevStatusFailurePrefix + string(failure)
		trace.FailureReason = string(failure)
		return GroupMedium, trace
	}
	selected := GroupName(result.Selected)
	if selected != GroupSimple && selected != GroupMedium && selected != GroupComplex {
		trace.Status = JevStatusFailurePrefix + string(decision.ReasonJevInvalidResult)
		trace.FailureReason = string(decision.ReasonJevInvalidResult)
		return GroupMedium, trace
	}
	trace.Status = JevStatusOK
	trace.Selected = result.Selected
	trace.SelectedGroup = selected
	confidence := result.Confidence
	trace.Confidence = &confidence
	trace.Probabilities = sortedProbabilities(result.Probabilities)
	return selected, trace
}

// recommend produces the policy engine's recommendation input. It makes at most
// one Jev call and reports why it made none, together with the diagnostic trace of
// the call it made.
//
// The trace is built from identifiers and counts only. It never sees the prompt:
// the prompt exists inside this method and is handed to the client directly, so a
// trace that cannot carry text is a structural property rather than a promise.
func (r *Router) recommend(ctx context.Context, features analyzer.Features, view analyzer.View, envelopes []modelEnvelope, eligibleCount int, runtime RuntimeSettings) (policy.JevSignal, *JevTrace) {
	inputMode := runtime.InputMode
	trace := &JevTrace{
		Status:          JevStatusDisabled,
		InputMode:       string(inputMode),
		CandidateModels: candidateModelIDs(envelopes),
		ModelCount:      len(envelopes),
		CandidateCount:  eligibleCount,
	}
	if !runtime.JevEnabled || runtime.Jev == nil {
		// "Jev is off" and "Jev failed" are different facts, and an administrator can
		// turn the first one on and off without a restart.
		return policy.NotRequested(), trace
	}
	trace.Status = JevStatusSkippedSingleModel
	if len(envelopes) < minModelsForJev {
		return policy.NotRequested(), trace
	}
	if len(envelopes) > maxJevModels {
		// The verified Choice primitive accepts at most 255 options, so a larger
		// set could only be rejected upstream. Skipping is honest; truncating the
		// candidate list would silently answer a different question.
		trace.Status = JevStatusSkippedTooManyModels
		return policy.NotRequested(), trace
	}
	built, ok := r.buildPrompt(features, view, envelopes, inputMode)
	if !ok {
		trace.Status = JevStatusSkippedInsufficientEvidence
		return policy.NotRequested(), trace
	}
	started := time.Now()
	result, err := runtime.Jev.Route(ctx, jev.Request{
		Protocol:     features.Protocol,
		Candidates:   jevCandidates(envelopes),
		Messages:     built.messages,
		SystemPrompt: built.system,
		Tools:        built.tools,
	})
	latency := time.Since(started).Milliseconds()
	trace.LatencyMS = &latency
	if err != nil {
		signal := policy.JevSignalFromError(err, jevSentinels())
		trace.Status = JevStatusFailurePrefix + string(signal.Failure)
		trace.FailureReason = string(signal.Failure)
		return signal, trace
	}
	probabilities := make(map[string]float64, len(result.Probabilities))
	for _, entry := range result.Probabilities {
		probabilities[entry.Model] = entry.Probability
	}
	trace.Status = JevStatusOK
	trace.Selected = result.Selected
	confidence := result.Confidence
	trace.Confidence = &confidence
	trace.Probabilities = sortedProbabilities(result.Probabilities)
	return policy.JevSignalFromResult(result.Selected, result.Confidence, probabilities), trace
}

// candidateModelIDs returns the distinct logical model identifiers of an envelope
// list. The envelopes are already sorted by model ID and already distinct, so the
// result is a copy in ascending order.
func candidateModelIDs(envelopes []modelEnvelope) []string {
	ids := make([]string, 0, len(envelopes))
	for _, envelope := range envelopes {
		ids = append(ids, envelope.Model)
	}
	return ids
}

// sortedProbabilities orders a normalized distribution by descending probability
// with the model identifier ascending as the tie-break, so two runs over the same
// reply store byte-identical JSON.
func sortedProbabilities(entries []jev.CandidateProbability) []ModelProbability {
	sorted := make([]ModelProbability, 0, len(entries))
	for _, entry := range entries {
		sorted = append(sorted, ModelProbability{Model: entry.Model, Probability: entry.Probability})
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Probability != sorted[j].Probability {
			return sorted[i].Probability > sorted[j].Probability
		}
		return sorted[i].Model < sorted[j].Model
	})
	return sorted
}

// traceForDecision copies a trace and refreshes the fields that describe the
// decision rather than the call. A nil input stays nil: a failover never invents a
// trace for a decision that made no Jev call.
func traceForDecision(trace *JevTrace, outcome decision.Decision) *JevTrace {
	if trace == nil {
		return nil
	}
	updated := *trace
	updated.ConfidenceBand = string(outcome.ConfidenceBand)
	updated.FallbackReason = string(outcome.Fallback.Reason)
	updated.EvidenceHash = outcome.EvidenceHash
	return &updated
}

// jevSentinels is the classification table the policy package owns. This package
// only supplies the values: mapping a sentinel onto a fallback reason happens in
// exactly one place (policy.WrapFallback), so two call sites cannot disagree.
func jevSentinels() policy.JevSentinels {
	return policy.JevSentinels{
		Timeout:       jev.ErrTimeout,
		Unavailable:   jev.ErrUnavailable,
		Rejected:      jev.ErrRejected,
		Canceled:      jev.ErrCanceled,
		InvalidResult: jev.ErrInvalidResult,
	}
}

// refusalFor maps a policy refusal onto the client contract. The exclusion list,
// the reasons and the candidate identities stay out of the message: they are in
// the decision (and therefore the routing log) and in the loopback debug
// endpoint, and a client learns only that the request could not be served.
func refusalFor(err error) error {
	switch {
	case errors.Is(err, policy.ErrTruncatedEvidence):
		return &RefusalError{
			Status:  statusUnprocessableEntity,
			Code:    CodeTruncatedEvidence,
			Message: "the request could not be routed on the evidence that was read",
		}
	case errors.Is(err, policy.ErrNoEligibleCandidate):
		return &RefusalError{
			Status:  statusUnprocessableEntity,
			Code:    CodeNoEligibleCandidate,
			Message: "no candidate can serve this request",
		}
	default:
		return &RefusalError{
			Status:  statusInternalError,
			Code:    CodeInternalError,
			Message: "the request could not be routed",
		}
	}
}
