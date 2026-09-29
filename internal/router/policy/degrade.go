package policy

import (
	"math"
	"sort"

	"github.com/nextroad-dev/Auto-Router/internal/router/analyzer"
	"github.com/nextroad-dev/Auto-Router/internal/router/decision"
)

// chained is the "this request uses more than one tool step" measure:
//
//	chained = max(0, ToolCount - 1) + ChainedToolUse + FileSearchUsed
//
// One offered tool is the single-tool baseline and therefore subtracts one, so a
// request that offers one tool and shows no chained or retrieval work measures zero.
// A chained tool use and a retrieval call each add one. The baseline is
// discounted only when there is a baseline to discount; otherwise a request that
// shows chained and retrieval work without offering a tool schema could never
// reach the threshold below.
func chained(features analyzer.Features) int {
	value := features.ToolCount - 1
	if value < 0 {
		value = 0
	}
	if features.ChainedToolUse {
		value++
	}
	if features.FileSearchUsed {
		value++
	}
	return value
}

// statsDegradedThreshold is the chained value at which the request has clearly
// moved past single-step tooling.
const statsDegradedThreshold = 2

// degrade applies the statistics rule: when a request has clearly moved past
// single-step tooling (chained >= statsDegradedThreshold), and exactly one
// surviving candidate declares tool support or reasoning support, that candidate
// is the only one that can serve a multi-step chain, so the others are excluded
// with the tool-capability code.
//
// When the rule does not narrow — because several candidates declare the
// capability, or because none does — every survivor stays in play. The state is
// recorded either way, so a routing log shows that the request was multi-step.
//
// When the request offers tools, every survivor already declares tool support, so
// this rule cannot narrow; it fires where the statistics are informative — chained
// or retrieval work without offered tools — and never overrides the hard filters.
func degrade(features analyzer.Features, survivors []eligible) ([]eligible, []rawExclusion, bool) {
	if chained(features) < statsDegradedThreshold {
		return survivors, nil, false
	}
	capable := make([]eligible, 0, len(survivors))
	for _, survivor := range survivors {
		if survivor.candidate.SupportsTools || survivor.candidate.SupportsReasoning {
			capable = append(capable, survivor)
		}
	}
	if len(capable) != 1 {
		return survivors, nil, true
	}
	keep := capable[0]
	exclusions := make([]rawExclusion, 0, len(survivors)-1)
	narrowed := make([]eligible, 0, 1)
	for _, survivor := range survivors {
		if survivor.index == keep.index {
			narrowed = append(narrowed, survivor)
			continue
		}
		exclusions = append(exclusions, reject(survivor.index, survivor.candidate,
			decision.ExclusionRequiresTools,
			"stats_degraded: the only candidate declaring tool or reasoning support"))
	}
	return narrowed, exclusions, true
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// truncate cuts a detail string to a byte budget without splitting a UTF-8 rune, so
// a bounded detail can never be invalid UTF-8.
func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && value[cut]&0xC0 == 0x80 {
		cut--
	}
	return value[:cut]
}

// sortSignals orders signals by name so two runs over the same inputs produce
// byte-identical output regardless of how the list was assembled.
func sortSignals(signals []decision.Signal) []decision.Signal {
	sort.SliceStable(signals, func(i, j int) bool { return signals[i].Name < signals[j].Name })
	return signals
}
