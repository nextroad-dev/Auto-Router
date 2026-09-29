// Package policy applies the hard routing constraints to a caller-supplied
// candidate set and selects the first surviving candidate in caller order.
//
// The contract this package guarantees:
//
//   - Pure: every method is a function over its inputs. It performs no I/O,
//     writes no log, mutates neither its inputs nor any global state, and depends
//     on no HTTP layer, registry, configuration loader, Provider transport or
//     database.
//   - Deterministic: the same inputs always produce the same decision down to
//     every field, including the exclusion order, the signal order, the reason
//     string and the evidence hash.
//   - Hard constraints are not negotiable: capability, context, output-ceiling
//     and truncation filters remove candidates before anything is selected.
//   - Bounded: every list it returns has a named size limit, and every string it
//     returns is an identifier, a closed enumeration or a number. Request text
//     never reaches a decision, a reason string or an exclusion detail.
//
// Candidate derivation and group selection are deliberately absent. The
// automatic router owns the mapping from the registry snapshot and the model
// groups to an ordered candidate list, and it reports how the group was chosen
// through GroupVerdict.
package policy

import (
	"errors"
	"fmt"
	"strings"

	"github.com/nextroad-dev/Auto-Router/internal/router/analyzer"
	"github.com/nextroad-dev/Auto-Router/internal/router/decision"
)

// Version is the only policy schema version this package implements. It is
// exported so the configuration validator refuses an unknown version instead of
// silently ignoring settings it does not understand.
const Version = 1

// DefaultLowConfidence is the default routing.policy.low_confidence: a Jev group
// recommendation below it is discarded in favor of the default group.
const DefaultLowConfidence = 0.45

// Errors are the stable classifications callers branch on. They are distinct on
// purpose: "no candidate can serve this request" is a routing outcome, while "the
// candidate list is malformed" is a programming error.
var (
	// ErrNoEligibleCandidate means every candidate was excluded by a hard filter.
	ErrNoEligibleCandidate = errors.New("no eligible candidate")
	// ErrTruncatedEvidence means the analyzer did not see the whole request and
	// routing.policy.refuse_truncated_evidence refuses to route on partial
	// evidence.
	ErrTruncatedEvidence = errors.New("request evidence is truncated")
	// ErrInvalidCandidates means the caller supplied a candidate list that cannot
	// describe a routable target: an empty identifier, a missing provider model, an
	// impossible output ceiling, or a duplicated pair.
	ErrInvalidCandidates = errors.New("invalid candidate list")
	// ErrInvalidConfig means the policy configuration cannot be compiled.
	ErrInvalidConfig = errors.New("invalid policy configuration")
)

// Config is the engine configuration. RefuseTruncatedEvidence false means
// "route on the evidence that was observed".
type Config struct {
	// Version must equal Version. It exists so a configuration written for a
	// future policy schema is rejected rather than partially applied.
	Version int
	// RefuseTruncatedEvidence makes a truncated analysis a hard failure instead of
	// a routing input.
	RefuseTruncatedEvidence bool
}

// DefaultConfig returns the configuration of an unconfigured deployment:
// truncation refusal on.
func DefaultConfig() Config {
	return Config{Version: Version, RefuseTruncatedEvidence: true}
}

// GroupVerdict is how the caller chose the group the candidates come from. It is
// recorded in the decision so the routing log explains the group choice with
// the same fields it uses for everything else.
type GroupVerdict struct {
	// JevAdopted is true when a Jev recommendation selected the group.
	JevAdopted bool
	// Confidence is the Jev confidence when a recommendation was produced, and 0
	// otherwise.
	Confidence float64
	// FallbackReason is ReasonNone when the recommendation was adopted, and
	// otherwise the reason the configured default (or only) group was used.
	FallbackReason decision.FallbackReason
}

// Outcome is one decision plus the features it was derived from, so a caller can
// log the explanation next to the facts without re-extracting them.
type Outcome struct {
	decision.Decision
	Features analyzer.Features
	Origin   decision.Origin
}

// Engine is a compiled policy. It is immutable after New and safe for concurrent
// use.
type Engine struct {
	refuseTruncatedEvidence bool
}

// New validates the configuration.
func New(cfg Config) (*Engine, error) {
	if cfg.Version != Version {
		return nil, fmt.Errorf("%w: version must be %d", ErrInvalidConfig, Version)
	}
	return &Engine{refuseTruncatedEvidence: cfg.RefuseTruncatedEvidence}, nil
}

// Eligible applies the hard filters and returns the survivors in the caller's
// candidate order, together with the explanation of a refusal. It lets a caller
// ask "is this request routable at all?" before spending a Jev call on it.
func (e *Engine) Eligible(features analyzer.Features, candidates []decision.Candidate) ([]decision.Candidate, error) {
	survivors, _, err := e.survivors(features, candidates)
	if err != nil {
		return nil, err
	}
	eligible := make([]decision.Candidate, 0, len(survivors))
	for _, survivor := range survivors {
		eligible = append(eligible, survivor.candidate)
	}
	return eligible, nil
}

// Select applies the full hard-filter contract and chooses the first surviving
// candidate in caller order. The caller's order is the administrator's group
// member order, which is an explicit primary/fallback priority.
func (e *Engine) Select(features analyzer.Features, candidates []decision.Candidate, verdict GroupVerdict) (Outcome, error) {
	survivors, trace, err := e.survivors(features, candidates)
	if err != nil {
		return Outcome{}, err
	}
	trace.verdict = verdict
	chosen := survivors[0]
	band := decision.BandLow
	if verdict.JevAdopted {
		band = decision.BandHigh
	}
	fallbackReason := verdict.FallbackReason
	if !fallbackReason.Valid() {
		fallbackReason = decision.ReasonNotRequested
	}
	confidence := verdict.Confidence
	if !finite(confidence) || confidence < 0 || confidence > 1 {
		confidence = 0
	}
	exclusions, total := presentExclusions(trace.exclusions)
	outcome := decision.Decision{
		Provider:       chosen.candidate.ProviderKey,
		Model:          chosen.candidate.ModelID,
		GatewayModel:   chosen.candidate.GatewayModel,
		Confidence:     confidence,
		ConfidenceBand: band,
		Reason:         renderReason(band, decision.OriginFirstEligible, fallbackReason, leadingCodes(trace.exclusions, maxReasonCodes), trace.degraded),
		Fallback: decision.FallbackInfo{
			Used:   fallbackReason != decision.ReasonNone,
			Reason: fallbackReason,
			Detail: "group_order",
		},
		Excluded:      exclusions,
		ExcludedCount: total,
		Signals:       trace.signals(chosen, band, fallbackReason),
		EvidenceHash:  trace.evidenceHash(survivors),
	}
	return Outcome{Decision: outcome, Features: features, Origin: decision.OriginFirstEligible}, nil
}

// survivors runs validation, the hard filters and the statistics rule shared by
// Eligible and Select.
func (e *Engine) survivors(features analyzer.Features, candidates []decision.Candidate) ([]eligible, *explanation, error) {
	if err := validateCandidates(candidates); err != nil {
		return nil, nil, err
	}
	survivors, exclusions, err := e.filter(features, candidates)
	if err != nil {
		return nil, nil, err
	}
	// The statistics rule can narrow the survivor set further, but only when the
	// request shows multi-step tooling and exactly one survivor can handle it.
	survivors, degradedRejections, degraded := degrade(features, survivors)
	exclusions = append(exclusions, degradedRejections...)
	if len(survivors) == 0 {
		return nil, nil, &filterError{err: ErrNoEligibleCandidate, exclusions: exclusions, reason: decision.ReasonNoCandidates}
	}
	return survivors, &explanation{features: features, candidates: candidates, exclusions: exclusions, degraded: degraded}, nil
}

// eligible is one candidate that survived the hard filters, carrying its position
// in the caller's candidate list.
type eligible struct {
	candidate decision.Candidate
	index     int
}

// validateCandidates rejects a list that cannot describe routable targets. These
// are programming errors in the caller, not routing outcomes: a caller that builds
// a candidate without a provider model would otherwise route a request to an
// unspecified destination.
func validateCandidates(candidates []decision.Candidate) error {
	seen := make(map[string]struct{}, len(candidates))
	for index, candidate := range candidates {
		switch {
		case candidate.ProviderKey == "" || candidate.ModelID == "":
			return fmt.Errorf("%w: candidate[%d] must name both a provider and a model", ErrInvalidCandidates, index)
		case candidate.GatewayModel == "":
			return fmt.Errorf("%w: candidate[%d] must carry the provider model identifier", ErrInvalidCandidates, index)
		case candidate.ContextWindow <= 0:
			return fmt.Errorf("%w: candidate[%d] must declare a positive context window", ErrInvalidCandidates, index)
		case candidate.MaxOutput != nil && (*candidate.MaxOutput <= 0 || *candidate.MaxOutput > candidate.ContextWindow):
			return fmt.Errorf("%w: candidate[%d] must declare a max output between 1 and its context window", ErrInvalidCandidates, index)
		}
		key := candidate.PairKey()
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("%w: candidate %q appears more than once", ErrInvalidCandidates, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// filter applies every hard constraint and returns the surviving candidates in
// their original order plus every rejection. A candidate is rejected at most
// once, by the first rule that applies, and the rejection list stays in candidate
// order.
func (e *Engine) filter(features analyzer.Features, candidates []decision.Candidate) ([]eligible, []rawExclusion, error) {
	// A truncated analysis is not a fact about the request: routing on it would
	// treat "the analyzer did not see it" as "the request does not contain it".
	// Every candidate is therefore excluded with the same code before any other
	// rule runs, so the report explains the refusal candidate by candidate.
	if features.Truncated && e.refuseTruncatedEvidence {
		exclusions := make([]rawExclusion, 0, len(candidates))
		for index, candidate := range candidates {
			exclusions = append(exclusions, reject(index, candidate, decision.ExclusionTruncatedEvidence, "analysis window reached"))
		}
		return nil, exclusions, &filterError{err: ErrTruncatedEvidence, exclusions: exclusions, reason: decision.ReasonTruncatedEvidence}
	}

	exclusions := make([]rawExclusion, 0, len(candidates))
	survivors := make([]eligible, 0, len(candidates))
	for index, candidate := range candidates {
		if code, detail, excluded := capabilityRejection(features, candidate); excluded {
			exclusions = append(exclusions, reject(index, candidate, code, detail))
			continue
		}
		survivors = append(survivors, eligible{candidate: candidate, index: index})
	}
	if len(survivors) == 0 {
		return nil, exclusions, &filterError{err: ErrNoEligibleCandidate, exclusions: exclusions, reason: decision.ReasonNoCandidates}
	}
	return survivors, exclusions, nil
}

// rawExclusion is an internal rejection record. It keeps the candidate's position
// so the public list can be ordered by contract order and then bounded without
// losing the true total.
type rawExclusion struct {
	index     int
	candidate decision.Candidate
	code      decision.ExclusionCode
	detail    string
}

func reject(index int, candidate decision.Candidate, code decision.ExclusionCode, detail string) rawExclusion {
	return rawExclusion{index: index, candidate: candidate, code: code, detail: truncate(detail, maxDetailBytes)}
}

// filterError carries the exclusions a refusal produced. It exists so the
// explanation survives an error return: "no eligible candidate" without the codes
// would force a caller to re-run the filters just to say why.
type filterError struct {
	err        error
	exclusions []rawExclusion
	reason     decision.FallbackReason
}

func (f *filterError) Error() string {
	if len(f.exclusions) == 0 {
		return f.err.Error()
	}
	codes := leadingCodes(f.exclusions, maxReasonCodes)
	return fmt.Sprintf("%s: excluded=%s", f.err.Error(), strings.Join(codes, ","))
}

func (f *filterError) Unwrap() error { return f.err }

// Refusal is the explanation a refused evaluation carries. It repeats the shape a
// successful decision uses — the same bounded, ordered exclusion list and the same
// closed fallback reason — so a caller that handles both outcomes parses one
// vocabulary rather than two.
type Refusal struct {
	// Exclusions is the bounded, contract-order list of rejections.
	Exclusions []decision.Exclusion
	// Count is the true number of rejections, which may exceed len(Exclusions).
	Count int
	// Reason is the closed classification of the refusal.
	Reason decision.FallbackReason
}

// RefusalOf returns the explanation a refused evaluation produced. A caller that
// received an error can therefore still explain the refusal without re-running the
// filters, and an error answer stays as actionable as a successful one.
func RefusalOf(err error) Refusal {
	var failure *filterError
	if !errors.As(err, &failure) {
		return Refusal{}
	}
	exclusions, count := presentExclusions(failure.exclusions)
	return Refusal{Exclusions: exclusions, Count: count, Reason: failure.reason}
}

// Exclusions returns just the bounded rejection list of a refused evaluation. It is
// the common case of RefusalOf.
func Exclusions(err error) []decision.Exclusion { return RefusalOf(err).Exclusions }

// FallbackReasonFor returns the reason a refused evaluation recorded.
func FallbackReasonFor(err error) decision.FallbackReason { return RefusalOf(err).Reason }

// capabilityRejection applies the request-facing hard constraints. Each check
// states one requirement, and none of them is satisfiable by another capability.
func capabilityRejection(features analyzer.Features, candidate decision.Candidate) (decision.ExclusionCode, string, bool) {
	if requiresTools(features) && !candidate.SupportsTools {
		return decision.ExclusionRequiresTools, fmt.Sprintf("tool_count=%d", features.ToolCount), true
	}
	// A file reference is a real visual input: the analyzer classifies a part as a
	// file only when the client sent an opaque file reference, and serving it needs
	// the same image-input capability as an image URL.
	if features.HasImageURL || features.HasBase64Image || features.HasFile {
		if !candidate.SupportsVision {
			return decision.ExclusionRequiresVision, mediaDetail(features), true
		}
	}
	// Audio is never served by vision support: the two capabilities describe
	// different inputs, and a text-and-audio model accepts no images.
	if features.HasAudio && !candidate.SupportsAudioInput {
		return decision.ExclusionRequiresAudioInput, mediaDetail(features), true
	}
	if features.MaxOutputTokensRequested != nil && candidate.MaxOutput != nil && *features.MaxOutputTokensRequested > *candidate.MaxOutput {
		return decision.ExclusionMaxOutputTooSmall,
			fmt.Sprintf("requested=%d max_output=%d", *features.MaxOutputTokensRequested, *candidate.MaxOutput), true
	}
	if candidate.ContextWindow < features.InputTokensEstimate {
		return decision.ExclusionContextInsufficient,
			fmt.Sprintf("input_tokens=%d context_window=%d", features.InputTokensEstimate, candidate.ContextWindow), true
	}
	return "", "", false
}

// requiresTools reports whether the request needs tool calling. A single offered
// tool is already a requirement: the client declared that the model must be able
// to call it.
func requiresTools(features analyzer.Features) bool {
	return features.HasTools || features.ToolCount > 0
}

// mediaDetail renders which media the request carried, as booleans and counts
// only. It never includes a reference, a location or an inline payload.
func mediaDetail(features analyzer.Features) string {
	return fmt.Sprintf("images=%d url=%t base64=%t file=%t audio=%t",
		features.ImageCount, features.HasImageURL, features.HasBase64Image, features.HasFile, features.HasAudio)
}
