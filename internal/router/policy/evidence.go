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
	// maxSignals bounds the signal list.
	maxSignals = 32
)

// reasonNoneToken is the placeholder a reason uses when a section is empty, so the
// four sections are always present and a parser never has to special-case a
// missing one.
const reasonNoneToken = "none"

// explanation accumulates everything the decision needs beyond the selected
// candidate: the rejections, the statistics state, the group verdict and the
// inputs of the evidence hash.
type explanation struct {
	features   analyzer.Features
	candidates []decision.Candidate
	exclusions []rawExclusion
	// degraded records that the request showed multi-step tooling.
	degraded bool
	verdict  GroupVerdict
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
func (x *explanation) signals(chosen eligible, band decision.ConfidenceBand, fallback decision.FallbackReason) []decision.Signal {
	signals := []decision.Signal{
		{Name: "candidate_count", Value: float64(len(x.candidates))},
		{Name: "chained", Value: float64(chained(x.features))},
		{Name: "chained_tool_use", Value: boolValue(x.features.ChainedToolUse)},
		{Name: "confidence_band", Value: bandSignalValue(band)},
		{Name: "context_window", Value: float64(chosen.candidate.ContextWindow)},
		{Name: "degraded", Value: boolValue(x.degraded)},
		{Name: "excluded_count", Value: float64(len(x.exclusions))},
		{Name: "fallback", Value: fallbackSignalValue(fallback)},
		{Name: "file_search", Value: boolValue(x.features.FileSearchUsed)},
		{Name: "input_tokens_estimate", Value: float64(x.features.InputTokensEstimate)},
		{Name: "jev_adopted", Value: boolValue(x.verdict.JevAdopted)},
		{Name: "length", Value: lengthSignalValue(x.features.Length)},
		{Name: "stream_requested", Value: boolValue(x.features.StreamRequested)},
		{Name: "tool_count", Value: float64(x.features.ToolCount)},
		{Name: "truncated", Value: boolValue(x.features.Truncated)},
	}
	if len(signals) > maxSignals {
		signals = signals[:maxSignals]
	}
	return sortSignals(signals)
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

// bandSignalValue maps a confidence band onto a stable number: 0 low, 2 high. An
// undefined band yields -1.
func bandSignalValue(band decision.ConfidenceBand) float64 {
	switch band {
	case decision.BandLow:
		return 0
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

// evidenceHash binds a decision to its inputs: the protocol, the request shape,
// the candidate metadata, the exclusion codes and the group verdict. It deliberately
// excludes the request text, because the hash exists to correlate a logged
// decision with the request that produced it, not to fingerprint the prompt.
func (x *explanation) evidenceHash(survivors []eligible) string {
	candidates := x.candidates
	var builder strings.Builder
	builder.WriteString("policy=" + strconv.Itoa(Version) + "\n")
	builder.WriteString("protocol=" + string(x.features.Protocol) + "\n")
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
	// The group verdict is part of the decision input: an equal request routed
	// through a Jev-selected group and through the default group must produce
	// different identifiers.
	builder.WriteString("group_verdict=" + boolToken(x.verdict.JevAdopted) + "," + string(x.verdict.FallbackReason) + "\n")
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
