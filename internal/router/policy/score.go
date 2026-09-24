package policy

import (
	"math"
	"sort"

	"github.com/nextroad-dev/Auto-Router/internal/router/analyzer"
	"github.com/nextroad-dev/Auto-Router/internal/router/decision"
)

// The static weights. They are named constants rather than configuration: the
// feature contract must stay comparable across deployments, and stage 6 only
// turns a selected threshold into a setting. A table-driven test pins each value
// and asserts that the sum is exactly 1.0, so a change here is a deliberate
// decision rather than a drift.
const (
	// weightTaskFit rewards a pair that needs no elaborate tool protocol: a
	// request that offers no tools scores highest.
	weightTaskFit = 0.20
	// weightCapability rewards a pair that declares more of the four input
	// capabilities.
	weightCapability = 0.15
	// weightPreference applies the request's preference in the static score.
	weightPreference = 0.20
	// weightTier applies the operator-declared cost or latency rank.
	weightTier = 0.15
	// weightFastResponse rewards a small streaming answer, the observable shape of
	// a latency-sensitive request.
	weightFastResponse = 0.10
	// weightReasoning rewards a reasoning-capable pair for a request whose text
	// looks like it needs reasoning.
	weightReasoning = 0.10
	// weightRegistryOrder expresses the registry's own ordering as a bounded score.
	weightRegistryOrder = 0.10

	// capabilityCount is how many capability bits the richness score counts.
	capabilityCount = 4
	// taskFitToolCeiling is the offered-tool count at which the task-fit reward
	// reaches zero. Four tools is already an elaborate protocol.
	taskFitToolCeiling = 4
	// maxScoredTier is the best-scoring-to-zero tier span. A declared tier at or
	// beyond it scores the same as an undeclared one, which is the documented
	// meaning of "no better than unknown".
	maxScoredTier = 3
	// neutralScore is the value used where a preference has no static opinion.
	neutralScore = 0.5
	// neutralProbabilityScore is the neutral position of a candidate in the Jev
	// ranking: a uniform distribution over any number of candidates maps here. It is
	// also the value reported when only one candidate exists, because with one option
	// a recommendation cannot express a difference.
	neutralProbabilityScore = 0.5
	// weightsSum is the total of the static weights. It is written as a constant
	// expression so the compiler evaluates it exactly, which makes "the weights sum
	// to one" a property of the source rather than something a running test hopes
	// for. TestStaticWeightTable asserts it.
	weightsSum = weightTaskFit + weightCapability + weightPreference + weightTier +
		weightFastResponse + weightReasoning + weightRegistryOrder

	// scoreEpsilon is the smallest difference that can decide a routing question.
	// Anything smaller is float noise: two scores that differ by less than this are
	// a tie, and a tie is broken by contract order.
	scoreEpsilon = 1e-12
)

// PreferenceSignalValues maps the four preferences onto stable signal numbers, so
// a routing log can be scanned numerically without parsing strings. The mapping is
// fixed and asserted by a test.
var preferenceSignalValues = map[analyzer.Preference]float64{
	analyzer.PreferenceBalanced: 0,
	analyzer.PreferenceQuality:  1,
	analyzer.PreferenceCost:     2,
	analyzer.PreferenceLatency:  3,
}

// preferenceSignalValue returns the signal number of a preference. An unknown
// preference yields -1 rather than a guessed value, which makes a future
// preference visible in the log instead of silently aliasing "balanced".
func preferenceSignalValue(preference analyzer.Preference) float64 {
	if value, ok := preferenceSignalValues[preference]; ok {
		return value
	}
	return -1
}

// staticScore is the deterministic, weight-based score of one candidate over the
// request features. It is the entire decision below the low threshold and a
// continuous part of it in the medium band.
func staticScore(e *Engine, features analyzer.Features, candidate decision.Candidate, index, total int) float64 {
	richness := capabilityRichness(candidate)
	score := weightTaskFit*taskFit(features) +
		weightCapability*richness +
		weightPreference*preferenceFit(e, features, candidate, richness) +
		weightTier*tierFit(e, features, candidate) +
		weightFastResponse*fastResponseFit(features) +
		weightReasoning*reasoningFit(features, candidate) +
		weightRegistryOrder*registryFit(index, total)
	return clamp01(score)
}

// taskFit rewards a request whose tooling needs are simple.
func taskFit(features analyzer.Features) float64 {
	return clamp01(1 - float64(features.ToolCount)/taskFitToolCeiling)
}

// capabilityRichness counts how many of the four declared capabilities a pair has,
// as a fraction of all four. It is a tie-breaker, not a preference: a pair is not
// penalized for a capability the request does not need beyond the aggregate score.
func capabilityRichness(candidate decision.Candidate) float64 {
	count := 0
	for _, supported := range []bool{
		candidate.SupportsTools,
		candidate.SupportsVision,
		candidate.SupportsReasoning,
		candidate.SupportsAudioInput,
	} {
		if supported {
			count++
		}
	}
	return float64(count) / capabilityCount
}

// preferenceFit applies the request's preference inside the static score.
func preferenceFit(e *Engine, features analyzer.Features, candidate decision.Candidate, richness float64) float64 {
	switch features.Preference {
	case analyzer.PreferenceQuality:
		return richness
	case analyzer.PreferenceCost, analyzer.PreferenceLatency:
		return tierFit(e, features, candidate)
	default:
		// The balanced preference deliberately has no static opinion: the trade-off
		// is what the remaining signals and the Jev blend are for.
		return neutralScore
	}
}

// tierFit scores the declared rank in the requested dimension. An undeclared model
// or provider is "unknown" and scores zero, which places it after every declared
// tier without excluding it: an operator who has not measured a model's cost
// cannot be read as saying it is expensive.
func tierFit(e *Engine, features analyzer.Features, candidate decision.Candidate) float64 {
	table := e.tierTableFor(features.Preference)
	if table == nil {
		return neutralScore
	}
	tier, ok := lookupTier(table, candidate)
	if !ok {
		return 0
	}
	if tier >= maxScoredTier {
		return 0
	}
	return 1 - float64(tier)/maxScoredTier
}

func (e *Engine) tierTableFor(preference analyzer.Preference) map[string]int {
	switch preference {
	case analyzer.PreferenceCost:
		return e.costTiers
	case analyzer.PreferenceLatency:
		return e.latencyTiers
	default:
		return nil
	}
}

// lookupTier resolves a candidate's rank in one dimension. The model's own rank
// wins over its provider's, because the more specific declaration is the more
// deliberate one.
//
// A model-level tier applies to every pair of that model, which is exactly what
// makes it usable as a pair-level tie-break: two providers of the same model share
// the model tier, so their difference is decided by the provider tier and then by
// the registry's contract order.
func lookupTier(table map[string]int, candidate decision.Candidate) (int, bool) {
	if tier, ok := table[modelTierPrefix+candidate.ModelID]; ok {
		return tier, true
	}
	tier, ok := table[providerTierPrefix+candidate.ProviderKey]
	return tier, ok
}

// tierSignalValue returns the declared rank of a candidate in one dimension, or -1
// when it is unknown. The value is reported as a signal so a mistake in a tier
// table is visible without recomputing the score by hand.
func tierSignalValue(table map[string]int, candidate decision.Candidate) float64 {
	tier, ok := lookupTier(table, candidate)
	if !ok {
		return -1
	}
	return float64(tier)
}

// fastResponseFit rewards a small streaming response. The ceiling is a named
// analyzer constant, so the threshold and its meaning stay in one place: the
// analyzer reports StreamRequested and MaxOutputTokensRequested, and this function
// is where the two become one signal.
func fastResponseFit(features analyzer.Features) float64 {
	if !features.StreamRequested || features.MaxOutputTokensRequested == nil {
		return 0
	}
	if *features.MaxOutputTokensRequested > analyzer.FastResponseMaxOutputTokens {
		return 0
	}
	return 1
}

// reasoningFit rewards a reasoning-capable pair for a request that looks like it
// needs reasoning. A marker hit on a pair that cannot reason scores zero rather
// than excluding the pair: the marker is a heuristic, so it is a preference, not a
// hard constraint.
func reasoningFit(features analyzer.Features, candidate decision.Candidate) float64 {
	if !features.ReasoningLikely || !candidate.SupportsReasoning {
		return 0
	}
	return 1
}

// registryFit expresses the registry's own ordering as a bounded score: the first
// candidate in contract order is best, the last is worst.
func registryFit(index, total int) float64 {
	if total <= 1 {
		return 1
	}
	return clamp01(1 - float64(index)/float64(total-1))
}

// chained is the "this request uses more than one tool step" measure. The formula is
// fixed in code and documented here because it is the input of the statistics rule
// below:
//
//	chained = max(0, ToolCount - 1) + ChainedToolUse + FileSearchUsed
//
// One offered tool is the single-tool baseline and therefore subtracts one, so a
// request that offers one tool and shows no chained or retrieval work measures zero.
// A chained tool use and a retrieval call each add one.
//
// The subtraction is applied inside the clamp on purpose. Subtracting the baseline
// even when the request offers no tool would measure "one tool step" for a request
// that shows a chained tool use and a retrieval call and offers no tool schema at
// all — a request that has plainly moved past single-step work. That reading makes
// the statistics rule below unreachable by construction (see its comment), so the
// baseline is discounted only when there is a baseline to discount.
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

// degrade applies the statistics rule. It is fixed in code and documented here
// because it is the one place a request shape, rather than a hard capability,
// removes candidates.
//
// The rule: when a request has clearly moved past single-step tooling
// (chained >= statsDegradedThreshold), and exactly one surviving candidate declares
// tool support or reasoning support, that candidate is the only one that can serve a
// multi-step chain, so the others are excluded with the tool-capability code.
//
// When the rule does not narrow — because several candidates declare the capability,
// or because none does — every survivor stays in play and the decision is left to
// the scores. The narrowing is recorded either way, so a routing log shows that the
// request was multi-step even when the statistics alone could not choose.
//
// The interaction with the tool hard filter is deliberate: when the request offers
// tools, every survivor already declares tool support, so the union count is the
// survivor count and this rule cannot narrow. It therefore fires where the
// statistics are actually informative — a request that shows chained or retrieval
// work without offering tools — and never overrides the hard filters.
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

// clamp01 bounds a score to the unit interval. A NaN becomes 0 rather than
// propagating: a comparison against NaN is always false, which would make the
// selection depend on iteration order.
func clamp01(value float64) float64 {
	switch {
	case math.IsNaN(value):
		return 0
	case value < 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
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
