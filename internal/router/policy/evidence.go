package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	"github.com/nextroad-dev/Auto-Router/internal/router/analyzer"
	"github.com/nextroad-dev/Auto-Router/internal/router/decision"
)

// Bounds on everything the engine returns. They are named constants, not
// settings: an explanation must stay small enough to log and to return from a
// debug endpoint no matter how large the candidate set was.
const (
	// maxExclusions bounds the exclusion list. ExcludedCount always reports the
	// true total, so a truncated list is never mistaken for a complete one.
	maxExclusions = 256
	// maxReasonCodes bounds how many exclusion codes the one-line reason carries.
	// The structured list is the authoritative record; the reason is a summary.
	maxReasonCodes = 3
	// maxDetailBytes bounds one exclusion detail or fallback detail string.
	maxDetailBytes = 160
	// maxSignals bounds the signal list. The engine produces a fixed set below it;
	// the limit exists so a future addition cannot grow the list without bound.
	// Stage 7 added model_count, which brought the set to 24, so the bound moved
	// to the next round number: a bound that silently truncates a documented
	// signal would drop the last one (final_score, appended after the fixed set)
	// on exactly the requests that need explaining most.
	maxSignals = 32
)

// reasonNoneToken is the placeholder a reason uses when a section is empty, so the
// four sections are always present and a parser never has to special-case a
// missing one.
const reasonNoneToken = "none"

// explanation accumulates everything the decision needs beyond the selected
// candidate: the rejections, the fallback path, the statistics state, the blend
// weight and the inputs of the evidence hash.
type explanation struct {
	features   analyzer.Features
	candidates []decision.Candidate
	exclusions []rawExclusion
	// degraded records that the statistics rule narrowed the survivor set.
	degraded bool
	// truncated records that the analysis did not see the whole request, whether or
	// not that was fatal, so the explanation can say so either way.
	truncated bool
	// jevRequested records whether a recommendation was supplied at all, which is
	// what distinguishes "Jev was unsure" from "Jev was not asked". jevSelected is
	// the model it named, recorded because a recommendation is part of the decision
	// input: the evidence hash has to change when the recommendation changes.
	jevRequested bool
	jevFailure   decision.FallbackReason
	jevSelected  string

	origin         decision.Origin
	alpha          float64
	finalScore     float64
	fallbackReason decision.FallbackReason
	// fallbackDetail explains a fallback with enumerations only, never with client
	// text: "not_configured", "hard_filtered", "confidence_below_low", ...
	fallbackDetail string
	// ineligible marks the recommendation that named a pair it could not serve, so
	// the decision records the situation even though no fallback ladder ran.
	ineligible       bool
	ineligibleReason decision.FallbackReason
	ineligibleDetail string
}

func newExplanation(features analyzer.Features, candidates []decision.Candidate, exclusions []rawExclusion, jevResult JevSignal, degraded bool) *explanation {
	return &explanation{
		features:         features,
		candidates:       candidates,
		jevRequested:     jevResult.Available,
		jevFailure:       jevFailure(jevResult),
		jevSelected:      jevResult.Selected,
		exclusions:       exclusions,
		degraded:         degraded,
		truncated:        features.Truncated,
		origin:           decision.OriginFirstEligible,
		fallbackReason:   decision.ReasonNone,
		ineligibleReason: decision.ReasonNone,
	}
}

// jevFailure maps an unavailable signal onto a fallback reason. An unavailable
// signal that names no known failure is reported as "not_requested": the engine
// never invents a failure it was not told about.
func jevFailure(signal JevSignal) decision.FallbackReason {
	if signal.Available {
		return decision.ReasonNone
	}
	if signal.Failure.Valid() && signal.Failure != decision.ReasonNone {
		return signal.Failure
	}
	return decision.ReasonNotRequested
}

// presentExclusions renders the bounded, ordered exclusion list. Two runs over the
// same inputs produce the same list, because the sort is total: position first,
// code second, and descriptions never influence the order.
func presentExclusions(exclusions []rawExclusion) ([]decision.Exclusion, int) {
	sorted := append([]rawExclusion(nil), exclusions...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].index != sorted[j].index {
			return sorted[i].index < sorted[j].index
		}
		return sorted[i].code < sorted[j].code
	})
	total := len(sorted)
	if total > maxExclusions {
		sorted = sorted[:maxExclusions]
	}
	presented := make([]decision.Exclusion, 0, len(sorted))
	for _, item := range sorted {
		presented = append(presented, decision.Exclusion{
			ModelID:     item.candidate.ModelID,
			ProviderKey: item.candidate.ProviderKey,
			Code:        item.code,
			Detail:      item.detail,
		})
	}
	return presented, total
}

// leadingCodes returns the first n exclusion codes in presentation order, which is
// what the one-line reason summarizes.
func leadingCodes(exclusions []rawExclusion, limit int) []string {
	presented, _ := presentExclusions(exclusions)
	if len(presented) > limit {
		presented = presented[:limit]
	}
	codes := make([]string, 0, len(presented))
	for _, item := range presented {
		codes = append(codes, string(item.Code))
	}
	return codes
}

// firstCode reports the first exclusion code that applied to one model, which is
// what makes "the configured default model was ineligible" actionable.
func (x *explanation) firstCode(modelID string) string {
	for _, item := range x.exclusions {
		if item.candidate.ModelID == modelID {
			return string(item.code)
		}
	}
	return reasonNoneToken
}

// renderReason builds the machine-readable explanation. The format is fixed and
// stable:
//
//	band=<band>;origin=<origin>;fallback=<reason>;excluded=<code,code,code>[;stats=degraded]
//
// Every section is always present, every value is a closed enumeration or an
// identifier-free code, and the exclusion summary is bounded. A routing log can
// therefore group decisions without parsing any free text — and there is no free
// text in it to parse. The trailing statistics marker records that the request
// showed multi-step tooling; it is appended last so the four documented sections
// keep a fixed position.
func renderReason(band decision.ConfidenceBand, origin decision.Origin, fallback decision.FallbackReason, codes []string, statsDegraded bool) string {
	var builder strings.Builder
	builder.WriteString("band=" + string(band))
	builder.WriteString(";origin=" + string(origin))
	builder.WriteString(";fallback=" + string(fallback))
	builder.WriteString(";excluded=")
	if len(codes) == 0 {
		builder.WriteString(reasonNoneToken)
	} else {
		builder.WriteString(strings.Join(codes, ","))
	}
	if statsDegraded {
		builder.WriteString(";stats=degraded")
	}
	return builder.String()
}

// signals renders the bounded, name-sorted explanation values. Only numbers and
// enumerations appear: a routing log can record the whole list without any risk of
// carrying request content.
func (x *explanation) signals(e *Engine, survivors []eligible, chosen eligible, alpha float64, band decision.ConfidenceBand) []decision.Signal {
	signals := []decision.Signal{
		{Name: "alpha", Value: alpha},
		{Name: "fallback", Value: fallbackSignalValue(x.fallbackReason)},
		{Name: "ineligible_jev", Value: boolValue(x.ineligible)},
		{Name: "jev_available", Value: boolValue(x.jevRequested)},
		{Name: "candidate_count", Value: float64(len(x.candidates))},
		{Name: "confidence_band", Value: bandSignalValue(band)},
		{Name: "chained", Value: float64(chained(x.features))},
		{Name: "chained_tool_use", Value: boolValue(x.features.ChainedToolUse)},
		{Name: "context_window", Value: float64(chosen.candidate.ContextWindow)},
		{Name: "cost_tier", Value: tierSignalValue(e.costTiers, chosen.candidate)},
		{Name: "degraded", Value: boolValue(x.degraded)},
		{Name: "excluded_count", Value: float64(len(x.exclusions))},
		{Name: "fast_response", Value: fastResponseFit(x.features)},
		{Name: "file_search", Value: boolValue(x.features.FileSearchUsed)},
		{Name: "input_tokens_estimate", Value: float64(x.features.InputTokensEstimate)},
		{Name: "latency_tier", Value: tierSignalValue(e.latencyTiers, chosen.candidate)},
		{Name: "length", Value: lengthSignalValue(x.features.Length)},
		// model_count reports how many distinct logical models the caller offered.
		// It is the denominator the Jev distribution is normalized against, so an
		// operator can check the arithmetic of a decision without re-deriving the
		// candidate list.
		{Name: "model_count", Value: float64(modelCount(x.candidates))},
		{Name: "preference", Value: preferenceSignalValue(x.features.Preference)},
		{Name: "static_chosen", Value: chosen.score},
		{Name: "static_top", Value: topStaticScore(survivors)},
		{Name: "stream_requested", Value: boolValue(x.features.StreamRequested)},
		{Name: "tool_count", Value: float64(x.features.ToolCount)},
		{Name: "truncated", Value: boolValue(x.truncated)},
	}
	if x.finalScore != 0 {
		signals = append(signals, decision.Signal{Name: "final_score", Value: x.finalScore})
	}
	if len(signals) > maxSignals {
		signals = signals[:maxSignals]
	}
	return sortSignals(signals)
}

// topStaticScore reports the best static score among the surviving candidates, so
// an operator can see how much room there was between the choice and its rivals
// without replaying the engine.
func topStaticScore(survivors []eligible) float64 {
	best := 0.0
	for index, candidate := range survivors {
		if index == 0 || candidate.score > best {
			best = candidate.score
		}
	}
	return best
}

// fallbackSignalValue maps a fallback reason onto a stable number so a routing log
// can be scanned numerically. An undefined reason yields -1 rather than a guessed
// bucket.
func fallbackSignalValue(reason decision.FallbackReason) float64 {
	switch reason {
	case decision.ReasonNone:
		return 0
	case decision.ReasonNotRequested:
		return 1
	case decision.ReasonConfidenceLow:
		return 2
	case decision.ReasonSelectedModelIneligible:
		return 3
	case decision.ReasonJevTimeout:
		return 4
	case decision.ReasonJevUnavailable:
		return 5
	case decision.ReasonJevRejected:
		return 6
	case decision.ReasonJevInvalidResult:
		return 7
	case decision.ReasonJevCanceled:
		return 8
	case decision.ReasonTruncatedEvidence:
		return 9
	case decision.ReasonNoCandidates:
		return 10
	default:
		return -1
	}
}

// bandSignalValue maps a confidence band onto a stable number: 0 low, 1 medium,
// 2 high. An undefined band yields -1.
func bandSignalValue(band decision.ConfidenceBand) float64 {
	switch band {
	case decision.BandLow:
		return 0
	case decision.BandMedium:
		return 1
	case decision.BandHigh:
		return 2
	default:
		return -1
	}
}

// boolValue renders a boolean signal as 0 or 1.
func boolValue(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

// lengthSignalValue maps the analyzer's length class onto a stable number. An
// unknown class yields -1 rather than a guessed bucket.
func lengthSignalValue(length analyzer.LengthClass) float64 {
	switch length {
	case analyzer.LengthShort:
		return 0
	case analyzer.LengthMedium:
		return 1
	case analyzer.LengthLong:
		return 2
	case analyzer.LengthVeryLong:
		return 3
	default:
		return -1
	}
}

// evidenceHash binds a decision to its inputs: the protocol, the resolved
// preference, the candidate metadata and the exclusion codes. It deliberately
// excludes the request text, because the hash exists to correlate a logged
// decision with the request that produced it, not to fingerprint the prompt.
func (x *explanation) evidenceHash(survivors []eligible) string {
	candidates := x.candidates
	var builder strings.Builder
	builder.WriteString("policy=" + strconv.Itoa(Version) + "\n")
	builder.WriteString("protocol=" + string(x.features.Protocol) + "\n")
	builder.WriteString("preference=" + string(x.features.Preference) + "\n")
	builder.WriteString("preference_source=" + string(x.features.PreferenceSource) + "\n")
	builder.WriteString("truncated=" + boolToken(x.features.Truncated) + "\n")
	builder.WriteString("length=" + string(x.features.Length) + "\n")
	builder.WriteString("input_tokens=" + strconv.Itoa(x.features.InputTokensEstimate) + "\n")
	builder.WriteString("tools=" + strconv.Itoa(x.features.ToolCount) +
		",chained=" + boolToken(x.features.ChainedToolUse) +
		",file_search=" + boolToken(x.features.FileSearchUsed) +
		",stream=" + boolToken(x.features.StreamRequested) +
		",max_output=" + optionalIntToken(x.features.MaxOutputTokensRequested) +
		",image_url=" + boolToken(x.features.HasImageURL) +
		",image_base64=" + boolToken(x.features.HasBase64Image) +
		",file=" + boolToken(x.features.HasFile) +
		",audio=" + boolToken(x.features.HasAudio) + "\n")
	// The candidate list is hashed in the caller's order, because the order is part
	// of the decision: it is the tie-break.
	for _, candidate := range candidates {
		builder.WriteString("candidate=" + candidate.PairKey() +
			"|" + candidate.GatewayModel +
			"|" + strconv.Itoa(candidate.ContextWindow) +
			"|" + optionalIntToken(candidate.MaxOutput) +
			"|" + capabilityToken(candidate) + "\n")
	}
	// Excluded candidates are recorded by pair and code only: the hash identifies
	// the input, so it must include what the filters removed but not any detail text.
	presented, _ := presentExclusions(x.exclusions)
	for _, item := range presented {
		builder.WriteString("excluded=" + item.ProviderKey + "/" + item.ModelID + "|" + string(item.Code) + "\n")
	}
	// The recommendation is part of the decision input: an equal request with a
	// different recommendation, or with a failed call instead of no call, must
	// produce a different identifier. That is how a routing log relates a decision to
	// the recommendation it was based on — including the case where no recommendation
	// was produced.
	builder.WriteString("jev=" + boolToken(x.jevRequested) + "," + string(x.jevFailure) + "," + x.jevSelected + "\n")
	builder.WriteString("survivors=" + strconv.Itoa(len(survivors)) + "\n")
	sum := sha256.Sum256([]byte(builder.String()))
	// Sixteen bytes correlates decisions without producing an identifier long
	// enough to be mistaken for a security boundary.
	return hex.EncodeToString(sum[:16])
}

func capabilityToken(candidate decision.Candidate) string {
	var builder strings.Builder
	for _, flag := range []struct {
		name  byte
		value bool
	}{
		{name: 't', value: candidate.SupportsTools},
		{name: 'v', value: candidate.SupportsVision},
		{name: 'r', value: candidate.SupportsReasoning},
		{name: 'a', value: candidate.SupportsAudioInput},
	} {
		if flag.value {
			builder.WriteByte(flag.name)
		} else {
			builder.WriteByte('-')
		}
	}
	return builder.String()
}

func optionalIntToken(value *int) string {
	if value == nil {
		return reasonNoneToken
	}
	return strconv.Itoa(*value)
}

func boolToken(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
