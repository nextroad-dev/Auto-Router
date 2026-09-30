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
	"math"
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
	// JevStatusSkippedSingleModel means only one group had an eligible member,
	// so a recommendation could not express a choice.
	JevStatusSkippedSingleModel = "skipped_single_model"
	// JevStatusSkippedInsufficientEvidence means the selected input mode left
	// nothing to judge — no system text and no conversation text.
	JevStatusSkippedInsufficientEvidence = "skipped_insufficient_evidence"
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
	// Refusal is the policy explanation behind a request-shaped refusal. The
	// client boundary never writes it; the loopback debug endpoint does.
	Refusal policy.Refusal
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
type FailoverPolicy struct {
	Enabled                bool
	MaxAttempts            int
	RetryPreRequestFailure bool
	RetryTimeout           bool
	RetryStatusCodes       []int
}

type RuntimeSettings struct {
	Engine        *policy.Engine
	JevEnabled    bool
	Jev           JevCaller
	InputMode     jev.InputMode
	Failover      FailoverPolicy
	DefaultGroup  GroupName
	LowConfidence float64
	GroupConfig   GroupConfig
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
	// InputMode selects how much of the conversation reaches Jev.
	InputMode jev.InputMode
	// Failover, DefaultGroup and the confidence threshold are the static values
	// used when Settings is nil.
	Failover      FailoverPolicy
	DefaultGroup  GroupName
	LowConfidence float64
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
	catalog       *models.Store
	analyzer      Analyzer
	jev           JevCaller
	engine        *policy.Engine
	inputMode     jev.InputMode
	failover      FailoverPolicy
	defaultGroup  GroupName
	lowConfidence float64
	settings      Settings
	groupConfig   GroupConfig
}

// currentEngine returns the compiled policy engine in effect right now. It is read
// once per decision, so one decision is evaluated by exactly one engine even if an
// administrator changes the policy while the request is in flight.
func (r *Router) runtimeSnapshot() RuntimeSettings {
	if r.settings != nil {
		return r.normalizeRuntime(r.settings.RuntimeSnapshot())
	}
	return r.normalizeRuntime(RuntimeSettings{
		Engine: r.engine, JevEnabled: r.jev != nil, Jev: r.jev, InputMode: r.inputMode,
		Failover: r.failover, DefaultGroup: r.defaultGroup,
		LowConfidence: r.lowConfidence, GroupConfig: r.groupConfig,
	})
}

func (r *Router) normalizeRuntime(snapshot RuntimeSettings) RuntimeSettings {
	if snapshot.Engine == nil {
		snapshot.Engine = r.engine
	}
	if _, err := jev.ParseInputMode(string(snapshot.InputMode)); err != nil {
		snapshot.InputMode = r.inputMode
	}
	if snapshot.Failover.MaxAttempts <= 0 {
		snapshot.Failover = cloneFailoverPolicy(r.failover)
	}
	if snapshot.Failover.MaxAttempts > maxGroupMembers {
		snapshot.Failover.MaxAttempts = maxGroupMembers
	}
	if snapshot.DefaultGroup != GroupSimple && snapshot.DefaultGroup != GroupMedium && snapshot.DefaultGroup != GroupComplex {
		snapshot.DefaultGroup = r.defaultGroup
	}
	if !validConfidenceThreshold(snapshot.LowConfidence) {
		snapshot.LowConfidence = r.lowConfidence
	}
	snapshot.Failover = cloneFailoverPolicy(snapshot.Failover)
	snapshot.GroupConfig = cloneGroupConfig(snapshot.GroupConfig)
	return snapshot
}

func validConfidenceThreshold(low float64) bool {
	return !math.IsNaN(low) && !math.IsInf(low, 0) && low >= 0 && low <= 1
}

// jevEnabled reports whether Jev may be called right now. A router without a live
// source reports the static state: a client is present or it is not.

// currentInputMode reports the input mode in effect right now.

// failoverAllowed reports whether one bounded retry is allowed right now.

// New builds the orchestrator. It never returns nil and never fails: a router
// missing a component answers every request with 503 routing_unavailable, which
// is the same contract the forwarding path uses for an unassembled orchestrator
// and is far easier to diagnose than a panic.
func New(options Options) *Router {
	inputMode := options.InputMode
	if _, err := jev.ParseInputMode(string(inputMode)); err != nil {
		inputMode = jev.InputModeRedacted
	}
	defaultGroup := options.DefaultGroup
	if defaultGroup != GroupSimple && defaultGroup != GroupMedium && defaultGroup != GroupComplex {
		defaultGroup = GroupMedium
	}
	failover := options.Failover
	if failover.MaxAttempts == 0 {
		failover.MaxAttempts = 2
	}
	if failover.MaxAttempts > maxGroupMembers {
		failover.MaxAttempts = maxGroupMembers
	}
	lowConfidence := options.LowConfidence
	if !validConfidenceThreshold(lowConfidence) {
		lowConfidence = policy.DefaultLowConfidence
	}
	return &Router{
		catalog:       options.Catalog,
		analyzer:      options.Analyzer,
		jev:           options.Jev,
		engine:        options.Engine,
		inputMode:     inputMode,
		failover:      cloneFailoverPolicy(failover),
		defaultGroup:  defaultGroup,
		lowConfidence: lowConfidence,
		settings:      options.Settings,
		groupConfig:   cloneGroupConfig(options.GroupConfig),
	}
}

// Request is one automatic routing question: the raw body the client sent and the
// correlation identifier.
type Request struct {
	Protocol providers.Protocol
	Body     []byte
	// Runtime is the request-start settings snapshot supplied by an integrated
	// HTTP boundary. Nil uses the router's settings source.
	Runtime *RuntimeSettings
	// RequestID is the correlation identifier. It is carried for logging by the
	// caller; it never reaches Jev or the selected Provider from here.
	RequestID string
	// Catalog is the request-start catalog snapshot captured by the HTTP
	// boundary. Candidates are derived from it so the decision, the executor
	// generation and every failover use one catalog. Nil reads the router's store.
	Catalog *models.Catalog
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
	// the policy origin (always first_eligible for group routing).
	SelectionMode string
	// SelectedGroup is the accepted Jev group or configured default group.
	SelectedGroup GroupName
	// Decision is the complete policy decision: band, origin, confidence,
	// exclusions, signals and evidence hash. It is what the routing log records.
	Decision decision.Decision
	// JevStatus is one of the JevStatus constants of this package.
	JevStatus string
	// JevLatency is how long the single Jev call took. Zero when none was made.
	JevLatency time.Duration
	// JevFailureStatus and JevFailureDetail describe a Jev call the endpoint
	// answered with a non-2xx status: that status, and the Jev client's bounded,
	// sanitized excerpt of the error body. They exist for the service log, which
	// is the only place an operator can see why Jev refused; the persisted trace
	// carries no text. Both are empty on every other path.
	JevFailureStatus int
	JevFailureDetail string
	// RoutingLatency is how long the whole decision took, from catalog read to
	// selected target.
	RoutingLatency time.Duration
	// Attempts is how many upstream attempts the target accounts for. The first
	// target is one; a failover target increments it.
	Attempts int
	// MaxAttempts is the request-start limit copied from the runtime snapshot. The
	// forwarding boundary uses it as an independent retry-loop guard.
	MaxAttempts int
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
	// SelectedGroup is the group actually used for routing, including deterministic
	// fallback when Jev is disabled, unavailable or below the confidence threshold.
	SelectedGroup GroupName
	// RecommendedGroup is populated only when Jev successfully selected a group.
	RecommendedGroup GroupName
	// ConfidenceBand and FallbackReason describe the group-selection decision.
	ConfidenceBand string
	FallbackReason string
	// EvidenceHash is the decision's evidence summary.
	EvidenceHash string
	// LatencyMS is how long the call took, nil when no call was made.
	LatencyMS *int64
	// CandidateGroups lists only groups with at least one hard-filtered member.
	CandidateGroups []GroupName
	GroupCount      int
	// CandidateCount is the number of hard-filtered provider/model pairs,
	// independent of the group choice count.
	CandidateCount int
	// Confidence is the recommendation confidence, nil when no recommendation was
	// produced.
	Confidence *float64
	// GroupProbabilities is the normalized Jev distribution for group-mode traces.
	GroupProbabilities []GroupProbability
}

// GroupProbability is one entry of a normalized group distribution.
type GroupProbability struct {
	Group       GroupName
	Probability float64
}

// Features reports the request features the decision was derived from. It is
// false for a zero Target.
func (t Target) Features() (analyzer.Features, bool) {
	if t.state == nil {
		return analyzer.Features{}, false
	}
	return t.state.features, true
}

// routeState is the immutable part of a decision that failover reuses.
type routeState struct {
	features        analyzer.Features
	candidates      []decision.Candidate
	verdict         policy.GroupVerdict
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
		runtime = r.normalizeRuntime(*request.Runtime)
	}
	engine := runtime.Engine
	if engine == nil {
		return Target{}, ErrRoutingUnavailable
	}
	started := time.Now()
	// Candidate derivation comes first so a registry with no enabled pair is
	// answered before the request is even analyzed: that is a configuration
	// problem, and it costs no analyzer work to report.
	catalog := request.Catalog
	if catalog == nil {
		catalog = r.catalog.Load()
	}
	candidates := Candidates(catalog)
	if len(candidates) == 0 {
		return Target{}, ErrRoutingUnavailable
	}
	// A client that has already disconnected is not a routing question: stop
	// before any analysis or call, and let the boundary write nothing.
	if err := ctx.Err(); err != nil {
		return Target{}, fmt.Errorf("%w: %w", ErrCanceled, err)
	}
	analysis, err := r.analyzer.Analyze(analyzer.Input{Protocol: request.Protocol, Body: request.Body})
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
	groupCandidatesByName := make(map[GroupName][]decision.Candidate, 3)
	groupChoices := make([]GroupName, 0, 3)
	for _, name := range []GroupName{GroupSimple, GroupMedium, GroupComplex} {
		members := orderedGroupCandidates(runtime.GroupConfig, name, eligible)
		if len(members) == 0 {
			continue
		}
		groupCandidatesByName[name] = members
		groupChoices = append(groupChoices, name)
	}
	if len(groupChoices) == 0 {
		return Target{}, refusalFor(policy.ErrNoEligibleCandidate)
	}
	envelopes := modelEnvelopes(eligible)
	group, trace, jevErr := r.recommendGroup(ctx, features, view, envelopes, groupChoices, len(eligible), runtime)
	groupCandidates := groupCandidatesByName[group]
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
	verdict := groupVerdict(trace)
	outcome, err := engine.Select(features, groupCandidates, verdict)
	if err != nil {
		// The survivor set cannot have changed between the pre-check and this
		// call, so a refusal here repeats the pre-check's reason.
		return Target{}, refusalFor(err)
	}
	// The selected group's recommendation metadata is recorded separately from
	// the policy engine's in-group pair explanation.
	trace.EvidenceHash = outcome.Decision.EvidenceHash
	failureStatus, failureDetail := jevStatusFailure(jevErr)
	return Target{
		ProviderKey:      outcome.Provider,
		Model:            outcome.Model,
		UpstreamModel:    outcome.GatewayModel,
		RoutingModel:     models.AutoModelID,
		SelectionMode:    string(outcome.Origin),
		SelectedGroup:    group,
		Decision:         outcome.Decision,
		JevStatus:        status,
		JevLatency:       jevLatency,
		JevFailureStatus: failureStatus,
		JevFailureDetail: failureDetail,
		RoutingLatency:   time.Since(started),
		Attempts:         1,
		MaxAttempts:      runtime.Failover.MaxAttempts,
		JevTrace:         trace,
		state: &routeState{
			features:        features,
			candidates:      groupCandidates,
			verdict:         verdict,
			runtime:         runtime,
			group:           group,
			groupCandidates: groupCandidates,
		},
	}, nil
}

// AttemptFailure describes one failed upstream try. Exactly one of Cause or
// StatusCode is normally populated; status-based retries are always opt-in because
// the provider has already received and answered the request.
type AttemptFailure struct {
	Cause      error
	StatusCode int
}

// Failover resolves the destination for the next configured pair in the selected
// group. It reports false when the captured policy disallows another attempt.
// Analysis and Jev are never called again.
func (r *Router) Failover(ctx context.Context, previous Target, failure AttemptFailure) (Target, bool) {
	if r == nil || previous.state == nil {
		return Target{}, false
	}
	state := previous.state
	engine := state.runtime.Engine
	if engine == nil {
		return Target{}, false
	}
	if !state.runtime.Failover.Enabled || previous.Attempts >= state.runtime.Failover.MaxAttempts {
		return Target{}, false
	}
	if ctx.Err() != nil || !retryAllowed(state.runtime.Failover, failure) {
		return Target{}, false
	}
	remaining := withoutPair(state.groupCandidates, previous.ProviderKey, previous.Model)
	if len(remaining) == 0 {
		// Every candidate has now failed. There is nothing left to try, so the
		// caller reports the upstream failure it already has rather than a
		// request-shaped refusal.
		return Target{}, false
	}
	outcome, err := engine.Select(state.features, remaining, state.verdict)
	if err != nil {
		// The retry could not produce a target either. Reporting the original
		// upstream failure is more accurate than surfacing a second refusal about
		// a request the router had already accepted.
		return Target{}, false
	}
	return Target{
		ProviderKey:      outcome.Provider,
		Model:            outcome.Model,
		UpstreamModel:    outcome.GatewayModel,
		RoutingModel:     previous.RoutingModel,
		SelectionMode:    string(outcome.Origin),
		SelectedGroup:    state.group,
		Decision:         outcome.Decision,
		JevStatus:        previous.JevStatus,
		JevLatency:       previous.JevLatency,
		JevFailureStatus: previous.JevFailureStatus,
		JevFailureDetail: previous.JevFailureDetail,
		RoutingLatency:   previous.RoutingLatency,
		Attempts:         previous.Attempts + 1,
		MaxAttempts:      previous.MaxAttempts,
		// The trace describes the one Jev call, which a failover does not repeat.
		// It is carried through unchanged except for the fields that describe the
		// retried decision rather than the call.
		JevTrace: traceForDecision(previous.JevTrace, outcome.Decision),
		state: &routeState{
			features:        state.features,
			candidates:      remaining,
			verdict:         state.verdict,
			runtime:         state.runtime,
			group:           state.group,
			groupCandidates: remaining,
		},
	}, true
}

func retryAllowed(policy FailoverPolicy, failure AttemptFailure) bool {
	if failure.StatusCode != 0 {
		if !retryableStatus(failure.StatusCode) {
			return false
		}
		for _, status := range policy.RetryStatusCodes {
			if status == failure.StatusCode {
				return true
			}
		}
		return false
	}
	cause := failure.Cause
	if cause == nil || errors.Is(cause, providers.ErrCanceled) {
		return false
	}
	// A pre-request marker is stronger evidence than a timeout classification:
	// connect timeouts can be safely retried because no provider connection was
	// established. Other timeouts remain opt-in due to possible duplicate work.
	if errors.Is(cause, providers.ErrPreRequestFailure) {
		return policy.RetryPreRequestFailure
	}
	if errors.Is(cause, providers.ErrUpstreamTimeout) {
		return policy.RetryTimeout
	}
	return false
}

func retryableStatus(status int) bool {
	switch status {
	case 408, 425, 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
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

// groupVerdict summarizes how recommendGroup chose the group, in the policy
// engine's terms, so the decision and the routing log explain the group choice.
func groupVerdict(trace *JevTrace) policy.GroupVerdict {
	verdict := policy.GroupVerdict{FallbackReason: decision.FallbackReason(trace.FallbackReason)}
	if trace.Confidence != nil {
		verdict.Confidence = *trace.Confidence
	}
	verdict.JevAdopted = trace.Status == JevStatusOK && verdict.FallbackReason == decision.ReasonNone
	return verdict
}

// jevStatusFailure extracts the HTTP status and sanitized error excerpt of a Jev
// call the endpoint answered with a non-2xx status. Any other error, including
// nil, yields nothing.
func jevStatusFailure(err error) (int, string) {
	var failure interface {
		Status() int
		Summary() string
	}
	if !errors.As(err, &failure) {
		return 0, ""
	}
	return failure.Status(), failure.Summary()
}

// recommendGroup asks Jev to choose among groups that currently contain at least
// one hard-filtered provider/model pair. The trace records group options and pair
// counts separately because they are different dimensions. The error is the Jev
// call's own failure, returned only so its detail can reach the service log; the
// trace already records how it was classified.
func (r *Router) recommendGroup(ctx context.Context, features analyzer.Features, view analyzer.View, envelopes []modelEnvelope, choices []GroupName, eligibleCount int, runtime RuntimeSettings) (GroupName, *JevTrace, error) {
	trace := &JevTrace{
		Status:          JevStatusDisabled,
		InputMode:       string(runtime.InputMode),
		CandidateGroups: append([]GroupName(nil), choices...),
		GroupCount:      len(choices),
		CandidateCount:  eligibleCount,
	}
	fallback := func(reason string) GroupName {
		trace.FallbackReason = reason
		trace.SelectedGroup = runtime.DefaultGroup
		return runtime.DefaultGroup
	}
	if !runtime.JevEnabled || runtime.Jev == nil {
		trace.FallbackReason = string(decision.ReasonNotRequested)
		return fallback(trace.FallbackReason), trace, nil
	}
	if len(choices) == 1 {
		trace.Status = JevStatusSkippedSingleModel
		trace.FallbackReason = string(decision.ReasonNotRequested)
		trace.SelectedGroup = choices[0]
		return choices[0], trace, nil
	}
	built, ok := r.buildPrompt(features, view, envelopes, runtime.InputMode)
	if !ok {
		trace.Status = JevStatusSkippedInsufficientEvidence
		trace.FallbackReason = string(decision.ReasonNotRequested)
		return fallback(trace.FallbackReason), trace, nil
	}
	candidates := make([]jev.Candidate, 0, len(choices))
	for _, group := range choices {
		description := "Choose for a request requiring moderate reasoning or multiple steps."
		switch group {
		case GroupSimple:
			description = "Choose for a straightforward request with limited reasoning."
		case GroupComplex:
			description = "Choose for a difficult request requiring extensive reasoning or complex work."
		}
		candidates = append(candidates, jev.Candidate{Model: string(group), ContextWindow: 1000000, Description: description})
	}
	start := time.Now()
	result, err := runtime.Jev.Route(ctx, jev.Request{
		Protocol: features.Protocol, Candidates: candidates,
		Messages: built.messages, SystemPrompt: built.system, Tools: built.tools,
	})
	latency := time.Since(start).Milliseconds()
	trace.LatencyMS = &latency
	if err != nil {
		failure, ok := policy.WrapFallback(err, jevSentinels())
		if !ok || failure == decision.ReasonNone {
			failure = decision.ReasonJevUnavailable
		}
		trace.Status = JevStatusFailurePrefix + string(failure)
		trace.FailureReason = string(failure)
		trace.FallbackReason = string(failure)
		return fallback(trace.FallbackReason), trace, err
	}
	selected := GroupName(result.Selected)
	if !containsGroup(choices, selected) {
		trace.Status = JevStatusFailurePrefix + string(decision.ReasonJevInvalidResult)
		trace.FailureReason = string(decision.ReasonJevInvalidResult)
		trace.FallbackReason = string(decision.ReasonJevInvalidResult)
		return fallback(trace.FallbackReason), trace, nil
	}
	trace.Status = JevStatusOK
	trace.RecommendedGroup = selected
	trace.Confidence = float64Pointer(result.Confidence)
	trace.ConfidenceBand = groupConfidenceBand(result.Confidence, runtime.LowConfidence)
	trace.GroupProbabilities = sortedGroupProbabilities(result.Probabilities)
	if result.Confidence < runtime.LowConfidence {
		trace.FallbackReason = string(decision.ReasonConfidenceLow)
		return fallback(trace.FallbackReason), trace, nil
	}
	trace.FallbackReason = string(decision.ReasonNone)
	trace.SelectedGroup = selected
	return selected, trace, nil
}

func containsGroup(groups []GroupName, candidate GroupName) bool {
	for _, group := range groups {
		if group == candidate {
			return true
		}
	}
	return false
}

func groupConfidenceBand(confidence, low float64) string {
	if confidence >= low {
		return string(decision.BandHigh)
	}
	return string(decision.BandLow)
}

func float64Pointer(value float64) *float64 { return &value }

func sortedGroupProbabilities(entries []jev.CandidateProbability) []GroupProbability {
	sorted := make([]GroupProbability, 0, len(entries))
	for _, entry := range entries {
		sorted = append(sorted, GroupProbability{Group: GroupName(entry.Model), Probability: entry.Probability})
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Probability != sorted[j].Probability {
			return sorted[i].Probability > sorted[j].Probability
		}
		return groupRank(sorted[i].Group) < groupRank(sorted[j].Group)
	})
	return sorted
}

func groupRank(group GroupName) int {
	switch group {
	case GroupSimple:
		return 0
	case GroupMedium:
		return 1
	case GroupComplex:
		return 2
	default:
		return maxGroupMembers + 1
	}
}

// traceForDecision copies a trace and refreshes the fields that describe the
// decision rather than the call. A nil input stays nil: a failover never invents a
// trace for a decision that made no Jev call.
func traceForDecision(trace *JevTrace, outcome decision.Decision) *JevTrace {
	if trace == nil {
		return nil
	}
	updated := *trace
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
			Refusal: policy.RefusalOf(err),
		}
	case errors.Is(err, policy.ErrNoEligibleCandidate):
		return &RefusalError{
			Status:  statusUnprocessableEntity,
			Code:    CodeNoEligibleCandidate,
			Message: "no candidate can serve this request",
			Refusal: policy.RefusalOf(err),
		}
	default:
		return &RefusalError{
			Status:  statusInternalError,
			Code:    CodeInternalError,
			Message: "the request could not be routed",
		}
	}
}
