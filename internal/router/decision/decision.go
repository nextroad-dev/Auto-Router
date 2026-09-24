// Package decision holds the routing decision data types shared by the policy
// engine (stage 6), the auto-routing orchestration (stage 7) and the routing log
// (stage 8).
//
// The package is deliberately inert: types, closed enumerations and their
// validation only. It contains no scoring, no filtering, no I/O and no
// dependency beyond the standard library, so it can be imported by every layer
// without dragging one of them into another. The dependency set is pinned by a
// source-level import guard test.
package decision

import (
	"math"
	"strings"
)

// Candidate is one provider/model pair the caller is willing to route to,
// together with the capability facts the caller already resolved for that exact
// pair.
//
// The caller (stage 7) owns candidate derivation. Capabilities are pair-level on
// purpose: the same logical model can support tools through one provider and not
// through another, and merging the flags would silently route a request to a pair
// that cannot serve it. Order is meaningful: it is the contract order, and it is
// the final tie-break of every score comparison.
type Candidate struct {
	ModelID     string
	ProviderKey string
	// GatewayModel is a historical field name for the provider-native model
	// identifier the selected Provider receives. It is carried, never derived
	// here: the policy package does not read the registry.
	GatewayModel   string
	ContextWindow  int
	MaxOutput      *int
	SupportsTools  bool
	SupportsVision bool
	// SupportsAudioInput is a capability of its own, never a synonym for
	// SupportsVision: a text-and-audio model does not accept images, and an
	// image model does not accept audio.
	SupportsAudioInput bool
	SupportsReasoning  bool
	// PairPriority and ProviderPriority are registry priorities. They are
	// recorded for the decision log; the score uses the contract index.
	PairPriority     int
	ProviderPriority int
}

// ConfidenceBand is the coarse classification of one routing decision. It is
// closed: a new band is a contract change, not a runtime discovery.
type ConfidenceBand string

const (
	BandHigh   ConfidenceBand = "high"
	BandMedium ConfidenceBand = "medium"
	BandLow    ConfidenceBand = "low"
)

// Valid reports whether the band is one of the three defined bands.
func (b ConfidenceBand) Valid() bool {
	return b == BandHigh || b == BandMedium || b == BandLow
}

// Origin names the rule that produced the selection.
type Origin string

const (
	// OriginJev means the Jev recommendation was adopted unchanged.
	OriginJev Origin = "jev"
	// OriginBlend means the selection came from the blended final score.
	OriginBlend Origin = "blend"
	// OriginDefaultModel means the configured default model was selected by the
	// low-confidence fallback.
	OriginDefaultModel Origin = "default_model"
	// OriginFirstEligible means the low-confidence fallback fell back to the
	// first hard-filtered candidate in contract order.
	OriginFirstEligible Origin = "first_eligible"
)

// Valid reports whether the origin is one of the four defined origins.
func (o Origin) Valid() bool {
	switch o {
	case OriginJev, OriginBlend, OriginDefaultModel, OriginFirstEligible:
		return true
	default:
		return false
	}
}

// FallbackReason explains why the decision did not simply adopt a Jev
// recommendation. It is a closed enumeration so the routing log of stage 8 can
// group decisions without parsing free text.
type FallbackReason string

const (
	// ReasonNone means a recommendation was used and no fallback applied.
	ReasonNone FallbackReason = "none"
	// ReasonNotRequested means Jev was not asked for this request.
	ReasonNotRequested FallbackReason = "not_requested"
	// ReasonConfidenceLow means Jev answered, but below routing.policy.low_confidence.
	ReasonConfidenceLow FallbackReason = "confidence_low"
	// ReasonSelectedModelIneligible means Jev named a pair outside the candidate
	// set or one the hard filters excluded.
	ReasonSelectedModelIneligible FallbackReason = "selected_model_ineligible"
	// ReasonJevTimeout, ReasonJevUnavailable, ReasonJevRejected,
	// ReasonJevInvalidResult and ReasonJevCanceled mirror the Jev client's
	// sentinels. WrapFallback maps them; nothing else invents them.
	ReasonJevTimeout       FallbackReason = "jev_timeout"
	ReasonJevUnavailable   FallbackReason = "jev_unavailable"
	ReasonJevRejected      FallbackReason = "jev_rejected"
	ReasonJevInvalidResult FallbackReason = "jev_invalid_result"
	ReasonJevCanceled      FallbackReason = "jev_canceled"
	// ReasonTruncatedEvidence means the analyzer did not see the whole request and
	// routing.policy.refuse_truncated_evidence is on.
	ReasonTruncatedEvidence FallbackReason = "truncated_evidence"
	// ReasonNoCandidates means no candidate survived the hard filters.
	ReasonNoCandidates FallbackReason = "no_candidates"
)

// Valid reports whether the reason is one of the defined reasons.
func (r FallbackReason) Valid() bool {
	switch r {
	case ReasonNone, ReasonNotRequested, ReasonConfidenceLow, ReasonSelectedModelIneligible,
		ReasonJevTimeout, ReasonJevUnavailable, ReasonJevRejected, ReasonJevInvalidResult,
		ReasonJevCanceled, ReasonTruncatedEvidence, ReasonNoCandidates:
		return true
	default:
		return false
	}
}

// JevFailure reports whether the reason describes a Jev call that could not
// produce a recommendation.
func (r FallbackReason) JevFailure() bool {
	switch r {
	case ReasonJevTimeout, ReasonJevUnavailable, ReasonJevRejected, ReasonJevInvalidResult, ReasonJevCanceled:
		return true
	default:
		return false
	}
}

// ExclusionCode names one reason a candidate cannot serve a request. Codes are
// stable, bounded and free of request text; they are the machine-readable half
// of the routing explanation.
type ExclusionCode string

const (
	// ExclusionRequiresTools means the request offers tools but the pair does not
	// support them.
	ExclusionRequiresTools ExclusionCode = "requires_tools"
	// ExclusionRequiresVision means the request carries an image or a file but the
	// pair does not declare image input.
	ExclusionRequiresVision ExclusionCode = "requires_vision"
	// ExclusionRequiresAudioInput means the request carries audio but the pair does
	// not declare audio input.
	ExclusionRequiresAudioInput ExclusionCode = "requires_audio_input"
	// ExclusionContextInsufficient means the estimated input exceeds the pair's
	// context window.
	ExclusionContextInsufficient ExclusionCode = "context_insufficient"
	// ExclusionMaxOutputTooSmall means the client asked for more output tokens than
	// the pair can produce.
	ExclusionMaxOutputTooSmall ExclusionCode = "max_output_too_small"
	// ExclusionNotInAllowlist means a non-empty allow list did not name the pair,
	// model or provider.
	ExclusionNotInAllowlist ExclusionCode = "not_in_allowlist"
	// ExclusionDenied means a deny list named the pair, model or provider.
	ExclusionDenied ExclusionCode = "denied"
	// ExclusionTruncatedEvidence means the candidate was excluded only because the
	// analyzer did not see the whole request and the policy refuses to guess.
	ExclusionTruncatedEvidence ExclusionCode = "truncated_evidence"
)

// Valid reports whether the code is one of the defined codes.
func (c ExclusionCode) Valid() bool {
	switch c {
	case ExclusionRequiresTools, ExclusionRequiresVision, ExclusionRequiresAudioInput,
		ExclusionContextInsufficient, ExclusionMaxOutputTooSmall, ExclusionNotInAllowlist,
		ExclusionDenied, ExclusionTruncatedEvidence:
		return true
	default:
		return false
	}
}

// Exclusion is one candidate that a hard filter removed, with a bounded Detail
// string. Detail carries numbers and enumerations only — never request text — so
// an exclusion list can be logged and returned to a debug client safely.
type Exclusion struct {
	ModelID     string        `json:"model_id"`
	ProviderKey string        `json:"provider_key"`
	Code        ExclusionCode `json:"code"`
	Detail      string        `json:"detail,omitempty"`
}

// Signal is one named numeric value that contributed to a decision. Signals are
// recorded for explanation, not recomputation: the engine is deterministic, so
// stage 8 can replay it from the same inputs.
type Signal struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
}

// FallbackInfo describes how the low-confidence fallback resolved.
type FallbackInfo struct {
	// Used is true when the selection did not come from an adopted Jev
	// recommendation at high confidence.
	Used bool `json:"used"`
	// Reason is the closed classification of the fallback.
	Reason FallbackReason `json:"reason"`
	// RequestedModel is routing.policy.default_model, empty when unconfigured.
	RequestedModel string `json:"requested_model,omitempty"`
	// Eligible reports whether RequestedModel survived the hard filters. It is
	// only meaningful when RequestedModel is non-empty.
	Eligible bool `json:"eligible"`
	// Detail explains a non-obvious outcome with enumerations and numbers only,
	// for example "not_configured" or "not_eligible:requires_tools".
	Detail string `json:"detail,omitempty"`
}

// Decision is the complete, deterministic outcome for one routing question. It
// contains identifiers, numbers and closed enumerations: no prompt text, no
// tool schema, no credential.
type Decision struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	GatewayModel string `json:"gateway_model"`

	// Confidence is always finite and within [0, 1] on every path.
	Confidence     float64        `json:"confidence"`
	ConfidenceBand ConfidenceBand `json:"confidence_band"`
	// Reason is a machine-readable token list: band, origin, fallback reason and
	// up to three leading exclusion codes.
	Reason string `json:"reason"`

	Fallback FallbackInfo `json:"fallback"`

	// Excluded lists the hard-filter rejections in (candidate order, code) order.
	// The list is bounded; ExcludedCount reports the true total.
	Excluded      []Exclusion `json:"excluded"`
	ExcludedCount int         `json:"excluded_count"`

	// Signals holds the bounded, deterministically ordered explanation values.
	Signals []Signal `json:"signals"`

	// EvidenceHash identifies the exact inputs of this decision (protocol,
	// preference, candidate metadata and exclusion codes) so stage 8 can
	// correlate a logged decision with the request that produced it.
	EvidenceHash string `json:"evidence_hash"`
}

// Valid reports whether a decision carries the invariants every consumer may
// rely on: a named destination, a valid band and origin, a finite confidence
// inside the unit interval, and a valid fallback reason.
func (d Decision) Valid() bool {
	if d.Provider == "" || d.Model == "" || d.GatewayModel == "" {
		return false
	}
	if !d.ConfidenceBand.Valid() || !d.Fallback.Reason.Valid() {
		return false
	}
	if math.IsNaN(d.Confidence) || math.IsInf(d.Confidence, 0) {
		return false
	}
	return d.Confidence >= 0 && d.Confidence <= 1
}

// PairKey is the "<provider>/<model>" identity of a candidate. The first slash
// separates the two halves, which matches models.SplitIncludeEntry and keeps the
// format usable for model IDs that themselves contain slashes.
func (c Candidate) PairKey() string { return c.ProviderKey + "/" + c.ModelID }

// SplitPairKey splits a "<provider>/<model>" key at the first slash. A key
// without a slash, or with an empty half, is reported as invalid rather than
// guessed at.
func SplitPairKey(key string) (providerKey, modelID string, ok bool) {
	index := strings.Index(key, "/")
	if index <= 0 || index == len(key)-1 {
		return "", "", false
	}
	return key[:index], key[index+1:], true
}
