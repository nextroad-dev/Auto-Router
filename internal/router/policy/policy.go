// Package policy turns routing features, a caller-supplied candidate set and a
// Jev recommendation into one deterministic routing decision.
//
// The contract this package guarantees:
//
//   - Pure: Evaluate is a function over its inputs. It performs no I/O, writes no
//     log, mutates neither its inputs nor any global state, and depends on no HTTP
//     layer, registry, configuration loader, Provider transport or database. The only
//     method that calls out at all is EvaluateRequest, and it calls exactly one
//     injected analyzer.Extractor. A source-level import guard test pins that
//     dependency set, which is how "the policy engine never calls an agent, never
//     reads the registry and never sends a request" is proven rather than
//     asserted.
//   - Deterministic: the same inputs always produce the same decision down to
//     every field, including the exclusion order, the signal order, the reason
//     string and the evidence hash. Ties are broken by the caller's candidate
//     order, so there is no tie the engine cannot decide.
//   - Hard constraints are not negotiable: capability, context, output-ceiling,
//     allow/deny-list and truncation filters remove candidates before any score
//     exists. A probability can never outvote them, and the low-confidence
//     fallback never widens them to reach a configured default model.
//   - Bounded: every list it returns has a named size limit, and every string it
//     returns is an identifier, a closed enumeration or a number. Request text
//     never reaches a decision, a reason string or an exclusion detail.
//
// Candidate derivation is deliberately absent. Stage 7 owns the mapping from the
// registry snapshot to an ordered candidate list, because that mapping is where
// "the same logical model can support tools at one provider and not another" is
// decided. This package accepts the caller's list and never merges capabilities
// across providers.
package policy

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/nextroad-dev/Auto-Router/internal/router/analyzer"
	"github.com/nextroad-dev/Auto-Router/internal/router/decision"
)

// Version is the only policy schema version this package implements. It is
// exported so the configuration validator refuses an unknown version instead of
// silently ignoring settings it does not understand.
const Version = 1

// Defaults for the two confidence thresholds. They are the values
// configs/example.json ships, so a deployment that never tunes them behaves
// exactly like a deployment that writes them out.
const (
	DefaultHighConfidence = 0.75
	DefaultLowConfidence  = 0.45
)

// Errors are the stable classifications callers branch on. They are distinct on
// purpose: "no candidate can serve this request" is a routing outcome, while "the
// candidate list is malformed" is a programming error, and conflating them would
// turn a caller bug into an unexplained fallback.
var (
	// ErrNoEligibleCandidate means every candidate was excluded by a hard filter.
	// The error names the leading exclusion codes so the caller can explain why
	// without re-running the engine.
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
	// ErrMissingExtractor means EvaluateRequest was called on an engine that was
	// built without one.
	ErrMissingExtractor = errors.New("policy engine has no extractor")
)

// Tier declares the position of one model or provider in a cost or latency
// ranking. Tier 0 is the best (cheapest or fastest) rank. Exactly one of Model
// and Provider must be set: a tier names either a logical model or a whole
// provider, and an entry stating both would be ambiguous.
type Tier struct {
	Model    string
	Provider string
	Tier     int
}

// The two tier namespaces cannot collide: a model ID may not contain a colon at
// all (models.ValidateModelID), and a provider key may not either.
const (
	modelTierPrefix    = "model:"
	providerTierPrefix = "provider:"
)

func (t Tier) key() (string, error) {
	switch {
	case t.Model == "" && t.Provider == "":
		return "", errors.New("must name either a model or a provider")
	case t.Model != "" && t.Provider != "":
		return "", errors.New("must not name both a model and a provider")
	case t.Model != "":
		return modelTierPrefix + t.Model, nil
	default:
		return providerTierPrefix + t.Provider, nil
	}
}

// Config is the policy configuration. Zero values are meaningful:
// RefuseTruncatedEvidence false means "route on the evidence that was observed",
// and an empty list means "no restriction" for every allow list.
type Config struct {
	// Version must equal Version. It exists so a configuration file written for a
	// future policy schema is rejected rather than partially applied.
	Version int
	// HighConfidence and LowConfidence are the band boundaries. A recommendation
	// at or above High is adopted as-is; one at or below Low enters the fallback
	// ladder; one strictly between them is blended with the static score.
	HighConfidence float64
	LowConfidence  float64
	// RefuseTruncatedEvidence makes a truncated analysis a hard failure instead of
	// a routing input.
	RefuseTruncatedEvidence bool
	// DefaultModel is the single low-confidence fallback. It is only ever used
	// when it survives the same hard filters as every other candidate.
	DefaultModel string
	// CostTiers and LatencyTiers are the operator-declared integer rankings. An
	// undeclared model or provider is "unknown" and sorts after every declared
	// tier.
	CostTiers    []Tier
	LatencyTiers []Tier
	// AllowModels, DenyModels, AllowProviders and DenyProviders are identifier
	// lists. An empty allow list means "no restriction".
	AllowModels    []string
	DenyModels     []string
	AllowProviders []string
	DenyProviders  []string
	// AllowPairs and DenyPairs are "<provider>/<model>" lists split at the first
	// slash, so a model ID containing a slash stays addressable.
	AllowPairs []string
	DenyPairs  []string
}

// DefaultConfig returns the configuration of an unconfigured deployment: the
// documented thresholds, truncation refusal on, and no lists, tiers or default
// model. It is what the check command and the tests start from.
func DefaultConfig() Config {
	return Config{
		Version:                 Version,
		HighConfidence:          DefaultHighConfidence,
		LowConfidence:           DefaultLowConfidence,
		RefuseTruncatedEvidence: true,
		CostTiers:               []Tier{},
		LatencyTiers:            []Tier{},
		AllowModels:             []string{},
		DenyModels:              []string{},
		AllowProviders:          []string{},
		DenyProviders:           []string{},
		AllowPairs:              []string{},
		DenyPairs:               []string{},
	}
}

// JevSignal is the caller's view of one Jev call. Building it from the client's
// result is the caller's job (see JevSignalFromResult), which keeps this package
// free of the client's types while still giving the engine one explicit input.
type JevSignal struct {
	// Available false means Jev was not asked or could not answer; Failure then
	// classifies why. A signal that is unavailable for an unknown reason is
	// reported as "not_requested" rather than as an invented failure.
	Available bool
	Failure   decision.FallbackReason
	// Selected is the model ID the recommendation named. It is matched against the
	// candidate set: a recommendation for a model outside the candidates, or for a
	// candidate a hard filter removed, is discarded rather than honored.
	Selected string
	// Confidence is the recommendation's own confidence. It is only read when it is
	// finite and within [0, 1]; anything else makes the whole signal unusable.
	Confidence float64
	// Probabilities maps logical model IDs to probabilities. The table is used only
	// when it agrees with the candidate set exactly: one entry per distinct model
	// among the eligible candidates, all finite and within [0, 1], and no entry for
	// a model that is not eligible. Anything else yields a zero Jev score rather
	// than a repaired one, because a partial distribution is a different question,
	// not a weaker answer. Every pair that serves a model inherits that model's
	// probability; the n of the normalization is the number of models, never the
	// number of pairs.
	Probabilities map[string]float64
}

// Outcome is one decision plus the features it was derived from, so a caller can
// log the explanation next to the facts without re-extracting them. Origin is
// repeated as a field because it is the one routing fact a log wants to group by
// without parsing the decision's reason string; the decision remains the single
// source of truth for everything else.
type Outcome struct {
	decision.Decision
	Features analyzer.Features
	Origin   decision.Origin
}

// Extractor is the analyzer seam. It matches the interface the forwarding handler
// already exposes, so the composition root passes the same value to both.
type Extractor interface {
	Analyze(input analyzer.Input) (analyzer.Result, error)
}

// Engine is a compiled policy. It is immutable after New and safe for concurrent
// use: every call reads only its own inputs and the compiled tables.
type Engine struct {
	high, low               float64
	refuseTruncatedEvidence bool
	defaultModel            string

	allowModels    map[string]struct{}
	denyModels     map[string]struct{}
	allowProviders map[string]struct{}
	denyProviders  map[string]struct{}
	allowPairs     map[string]struct{}
	denyPairs      map[string]struct{}

	costTiers    map[string]int
	latencyTiers map[string]int

	extractor Extractor
}

// New validates the configuration and compiles its lookup tables. Every error
// describes the setting, never a client-provided value, and the compiled tables
// are built in a fixed order so two engines built from equal configurations
// behave identically.
func New(cfg Config) (*Engine, error) {
	if cfg.Version != Version {
		return nil, fmt.Errorf("%w: version must be %d", ErrInvalidConfig, Version)
	}
	if !validThreshold(cfg.LowConfidence) || !validThreshold(cfg.HighConfidence) || cfg.LowConfidence >= cfg.HighConfidence {
		return nil, fmt.Errorf("%w: low_confidence and high_confidence must satisfy 0 <= low < high <= 1", ErrInvalidConfig)
	}
	engine := &Engine{
		high:                    cfg.HighConfidence,
		low:                     cfg.LowConfidence,
		refuseTruncatedEvidence: cfg.RefuseTruncatedEvidence,
		defaultModel:            cfg.DefaultModel,
	}
	var err error
	if engine.allowModels, err = identifierSet("allow_models", cfg.AllowModels); err != nil {
		return nil, err
	}
	if engine.denyModels, err = identifierSet("deny_models", cfg.DenyModels); err != nil {
		return nil, err
	}
	if engine.allowProviders, err = identifierSet("allow_providers", cfg.AllowProviders); err != nil {
		return nil, err
	}
	if engine.denyProviders, err = identifierSet("deny_providers", cfg.DenyProviders); err != nil {
		return nil, err
	}
	if engine.allowPairs, err = pairSet("allow_pairs", cfg.AllowPairs); err != nil {
		return nil, err
	}
	if engine.denyPairs, err = pairSet("deny_pairs", cfg.DenyPairs); err != nil {
		return nil, err
	}
	if engine.costTiers, err = tierTable("cost_tiers", cfg.CostTiers); err != nil {
		return nil, err
	}
	if engine.latencyTiers, err = tierTable("latency_tiers", cfg.LatencyTiers); err != nil {
		return nil, err
	}
	if cfg.DefaultModel != "" && cfg.DefaultModel != strings.TrimSpace(cfg.DefaultModel) {
		return nil, fmt.Errorf("%w: default_model must not be padded with whitespace", ErrInvalidConfig)
	}
	return engine, nil
}

// WithExtractor returns an engine that can run EvaluateRequest. The compiled
// tables are shared, never copied or mutated, so the returned engine is as
// immutable and as safe for concurrent use as the receiver.
func (e *Engine) WithExtractor(extractor Extractor) *Engine {
	clone := *e
	clone.extractor = extractor
	return &clone
}

// Thresholds returns the compiled band boundaries. They are exported for the
// check command and for tests that must state the boundary they are probing.
func (e *Engine) Thresholds() (low, high float64) { return e.low, e.high }

// Evaluate is the pure entry point: it applies the hard filters, scores what
// survives, and returns the decision. It never fails on content and never mutates
// its inputs.
func (e *Engine) Evaluate(features analyzer.Features, candidates []decision.Candidate, jevResult JevSignal) (Outcome, error) {
	if err := validateCandidates(candidates); err != nil {
		return Outcome{}, err
	}
	survivors, exclusions, err := e.filter(features, candidates)
	if err != nil {
		return Outcome{}, err
	}
	// The statistics rule can narrow the survivor set further, but only when the
	// request shows multi-step tooling and exactly one survivor can handle it.
	survivors, degradedRejections, degraded := degrade(features, survivors)
	exclusions = append(exclusions, degradedRejections...)
	if len(survivors) == 0 {
		return Outcome{}, &filterError{err: ErrNoEligibleCandidate, exclusions: exclusions, reason: decision.ReasonNoCandidates}
	}
	trace := newExplanation(features, candidates, exclusions, jevResult, degraded)
	return e.choose(trace, candidates, survivors, jevResult), nil
}

// Eligible applies the hard filters and returns the survivors in the caller's
// candidate order, together with the explanation of a refusal. It is the same
// filter chain Evaluate runs, exposed as a query so a caller can ask "is this
// request routable at all?" before spending a Jev call on it. It performs no
// scoring, reads no recommendation and allocates no explanation beyond the
// refusals themselves.
func (e *Engine) Eligible(features analyzer.Features, candidates []decision.Candidate) ([]decision.Candidate, error) {
	if err := validateCandidates(candidates); err != nil {
		return nil, err
	}
	survivors, exclusions, err := e.filter(features, candidates)
	if err != nil {
		return nil, err
	}
	// The statistics rule can narrow the survivor set further, exactly as it does
	// during Evaluate; a pre-check that ignored it would report candidates as
	// eligible that the decision then refuses to consider.
	survivors, degradedRejections, _ := degrade(features, survivors)
	exclusions = append(exclusions, degradedRejections...)
	if len(survivors) == 0 {
		return nil, &filterError{err: ErrNoEligibleCandidate, exclusions: exclusions, reason: decision.ReasonNoCandidates}
	}
	eligible := make([]decision.Candidate, 0, len(survivors))
	for _, survivor := range survivors {
		eligible = append(eligible, survivor.candidate)
	}
	return eligible, nil
}

// EvaluateFirstEligible applies the full hard-filter contract and chooses the
// first surviving candidate in caller order. It is used for externally ordered
// groups whose member order is an explicit fallback priority.
func (e *Engine) EvaluateFirstEligible(features analyzer.Features, candidates []decision.Candidate, jevResult JevSignal) (Outcome, error) {
	if err := validateCandidates(candidates); err != nil {
		return Outcome{}, err
	}
	survivors, exclusions, err := e.filter(features, candidates)
	if err != nil {
		return Outcome{}, err
	}
	var degraded bool
	survivors, degradedRejections, degraded := degrade(features, survivors)
	exclusions = append(exclusions, degradedRejections...)
	trace := newExplanation(features, candidates, exclusions, jevResult, degraded)
	for index := range survivors {
		survivors[index].score = staticScore(e, features, survivors[index].candidate, survivors[index].index, len(candidates))
	}
	chosen := survivors[0]
	_, defaultEligible := findSurvivor(survivors, e.defaultModel)
	trace.origin = decision.OriginFirstEligible
	trace.fallbackReason = decision.ReasonNotRequested
	trace.fallbackDetail = "group_order"
	return e.build(decision.Decision{
		Provider:       chosen.candidate.ProviderKey,
		Model:          chosen.candidate.ModelID,
		GatewayModel:   chosen.candidate.GatewayModel,
		Confidence:     chosen.score,
		ConfidenceBand: decision.BandLow,
		Fallback:       decision.FallbackInfo{Used: true, Reason: decision.ReasonNotRequested, RequestedModel: e.defaultModel, Eligible: defaultEligible, Detail: "group_order"},
	}, trace, candidates, survivors, chosen, 0), nil
}

// EvaluateRequest extracts features through the configured extractor and then
// evaluates them. It is the only method that calls out at all, and the call is to
// the analyzer, which is itself a pure function over the request bytes.
func (e *Engine) EvaluateRequest(input analyzer.Input, candidates []decision.Candidate, jevResult JevSignal) (Outcome, error) {
	if e.extractor == nil {
		return Outcome{}, ErrMissingExtractor
	}
	result, err := e.extractor.Analyze(input)
	if err != nil {
		// The analyzer only rejects router-owned input (an unknown protocol or an
		// unusable preference); the error is forwarded unchanged rather than
		// rewritten, so the caller keeps the shared error taxonomy.
		return Outcome{}, err
	}
	return e.Evaluate(result.Features, candidates, jevResult)
}

func validThreshold(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

// identifierSet compiles an allow or deny list. Entries must be non-empty,
// unpadded identifiers and must not repeat: a duplicate is almost always a
// copy-paste mistake, and silently de-duplicating it hides the mistake.
func identifierSet(field string, values []string) (map[string]struct{}, error) {
	set := make(map[string]struct{}, len(values))
	for index, value := range values {
		if value == "" || value != strings.TrimSpace(value) {
			return nil, fmt.Errorf("%w: %s[%d] must be a non-empty identifier without surrounding whitespace", ErrInvalidConfig, field, index)
		}
		if strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("%w: %s[%d] must not contain NUL", ErrInvalidConfig, field, index)
		}
		if _, duplicate := set[value]; duplicate {
			return nil, fmt.Errorf("%w: %s declares %q more than once", ErrInvalidConfig, field, value)
		}
		set[value] = struct{}{}
	}
	return set, nil
}

// pairSet compiles a "<provider>/<model>" list. The split happens at the first
// slash, matching models.SplitIncludeEntry, so a model ID that itself contains a
// slash stays expressible.
func pairSet(field string, values []string) (map[string]struct{}, error) {
	set := make(map[string]struct{}, len(values))
	for index, value := range values {
		providerKey, modelID, ok := decision.SplitPairKey(value)
		if !ok {
			return nil, fmt.Errorf("%w: %s[%d] must be provider/model", ErrInvalidConfig, field, index)
		}
		if providerKey != strings.TrimSpace(providerKey) || modelID != strings.TrimSpace(modelID) {
			return nil, fmt.Errorf("%w: %s[%d] must not be padded with whitespace", ErrInvalidConfig, field, index)
		}
		if _, duplicate := set[providerKey+"/"+modelID]; duplicate {
			return nil, fmt.Errorf("%w: %s declares %q more than once", ErrInvalidConfig, field, providerKey+"/"+modelID)
		}
		set[providerKey+"/"+modelID] = struct{}{}
	}
	return set, nil
}

// tierTable compiles one tier ranking. A negative tier is rejected because the
// scale is a rank from zero; a repeated key is rejected because two different
// ranks for one target have no defined meaning.
func tierTable(field string, tiers []Tier) (map[string]int, error) {
	table := make(map[string]int, len(tiers))
	for index, tier := range tiers {
		key, err := tier.key()
		if err != nil {
			return nil, fmt.Errorf("%w: %s[%d] %v", ErrInvalidConfig, field, index, err)
		}
		if tier.Tier < 0 {
			return nil, fmt.Errorf("%w: %s[%d] tier must not be negative", ErrInvalidConfig, field, index)
		}
		if _, duplicate := table[key]; duplicate {
			return nil, fmt.Errorf("%w: %s declares the same target more than once", ErrInvalidConfig, field)
		}
		table[key] = tier.Tier
	}
	return table, nil
}

// eligible is one candidate that survived the hard filters, carrying its position
// in the caller's candidate list. The position is the tie-break, so a candidate
// keeps its identity even when a caller reorders the list.
type eligible struct {
	candidate decision.Candidate
	index     int
	// score is the static score, computed once: it depends on the features and the
	// candidate only, never on the Jev result.
	score float64
	// jevScore is the normalized recommendation score, and jevAvailable reports
	// whether the probability table was usable at all.
	jevScore     float64
	jevAvailable bool
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
		if code, detail, excluded := e.listRejection(candidate); excluded {
			exclusions = append(exclusions, reject(index, candidate, code, detail))
			continue
		}
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

// listRejection applies the six identifier lists. Deny always wins: a pair named
// by a deny list is refused even when an allow list also names it, because an
// operator who denies something has made the more specific decision.
func (e *Engine) listRejection(candidate decision.Candidate) (decision.ExclusionCode, string, bool) {
	pairKey := candidate.PairKey()
	switch {
	case e.match(e.denyPairs, pairKey):
		return decision.ExclusionDenied, "deny_pairs", true
	case e.match(e.denyModels, candidate.ModelID):
		return decision.ExclusionDenied, "deny_models", true
	case e.match(e.denyProviders, candidate.ProviderKey):
		return decision.ExclusionDenied, "deny_providers", true
	}
	// A non-empty allow list is an additional AND condition: naming a model in
	// allow_models does not exempt it from allow_providers.
	if len(e.allowPairs) > 0 && !e.match(e.allowPairs, pairKey) {
		return decision.ExclusionNotInAllowlist, "allow_pairs", true
	}
	if len(e.allowModels) > 0 && !e.match(e.allowModels, candidate.ModelID) {
		return decision.ExclusionNotInAllowlist, "allow_models", true
	}
	if len(e.allowProviders) > 0 && !e.match(e.allowProviders, candidate.ProviderKey) {
		return decision.ExclusionNotInAllowlist, "allow_providers", true
	}
	return "", "", false
}

func (e *Engine) match(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

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

// choose applies the confidence bands and produces the decision.
func (e *Engine) choose(trace *explanation, candidates []decision.Candidate, survivors []eligible, jevResult JevSignal) Outcome {
	// The static score is a property of the features and the candidate only, so it
	// is computed once and reused by every band.
	for i := range survivors {
		survivors[i].score = staticScore(e, trace.features, survivors[i].candidate, survivors[i].index, len(candidates))
	}
	attachJevScores(survivors, jevResult)
	if len(survivors) == 1 {
		// A single surviving candidate makes the Jev score neutral: with one option
		// a recommendation cannot express a difference, so the recorded score is the
		// neutral value rather than a probability that happens to be 1.
		survivors[0].jevAvailable = false
		survivors[0].jevScore = neutralProbabilityScore
	}
	named, eligibleSelection := selectionState(candidates, survivors, jevResult)
	band := e.evaluateConfidence(jevResult, named, eligibleSelection)
	// The configured default model is reported on every path, together with whether
	// it survived the hard filters, so an operator can see that the fallback target
	// exists and would have been usable.
	_, defaultEligible := findSurvivor(survivors, e.defaultModel)

	switch {
	case band.AdoptJev:
		// The recommendation names a logical model, not a pair. When several
		// providers serve that model, the pair decision is made from the pair-level
		// metadata (declared tiers, capability richness and contract order) instead
		// of from the order the caller happened to list them in.
		chosen, _ := bestStaticForModel(survivors, jevResult.Selected)
		trace.origin = decision.OriginJev
		return e.build(decision.Decision{
			Provider:       chosen.candidate.ProviderKey,
			Model:          chosen.candidate.ModelID,
			GatewayModel:   chosen.candidate.GatewayModel,
			Confidence:     jevResult.Confidence,
			ConfidenceBand: band.Band,
			Fallback: decision.FallbackInfo{
				Reason:         decision.ReasonNone,
				RequestedModel: e.defaultModel,
				Eligible:       defaultEligible,
				Detail:         "adopted",
			},
		}, trace, candidates, survivors, chosen, 1)

	case band.Blend:
		trace.origin = decision.OriginBlend
		chosen, score := bestBlended(survivors, band)
		trace.finalScore = score
		trace.ineligible = band.Ineligible
		trace.ineligibleReason = band.FallbackReason
		trace.ineligibleDetail = band.Detail
		return e.build(decision.Decision{
			Provider:       chosen.candidate.ProviderKey,
			Model:          chosen.candidate.ModelID,
			GatewayModel:   chosen.candidate.GatewayModel,
			Confidence:     jevResult.Confidence,
			ConfidenceBand: band.Band,
			Fallback: decision.FallbackInfo{
				// A blend that used the recommendation is not a fallback: the
				// recommendation contributed, and the reason says how. A blend that had
				// to discard it is one, and the reason names why.
				Used:           band.Ineligible,
				Reason:         band.FallbackReason,
				Eligible:       defaultEligible,
				Detail:         band.Detail,
				RequestedModel: e.defaultModel,
			},
		}, trace, candidates, survivors, chosen, band.Alpha)

	default:
		return e.fallback(trace, candidates, survivors, band)
	}
}

// bandDecision is the pure outcome of the confidence classifier. It is a separate
// type so the band rules can be table-tested without building a whole decision.
type bandDecision struct {
	Band decision.ConfidenceBand
	// AdoptJev means the recommendation is taken as-is.
	AdoptJev bool
	// Blend means the final score interpolates between the Jev score and the static
	// score.
	Blend bool
	// UseJev means the Jev score participates in the blend. It is false when the
	// recommendation was discarded, which is how a recommendation for an ineligible
	// pair loses all influence without being reported as a failure.
	UseJev bool
	Alpha  float64
	// Ineligible records that Jev named a pair outside the candidate set or one the
	// hard filters removed.
	Ineligible     bool
	FallbackReason decision.FallbackReason
	Detail         string
}

// evaluateConfidence classifies one recommendation into a band. It is the single
// place the thresholds are interpreted, so boundary behavior has exactly one
// definition:
//
//   - at or above high: adopt the recommendation, when it names an eligible pair;
//   - strictly between low and high: blend, with alpha interpolating linearly from
//     0 at low to 1 at high;
//   - at or below low, or an unusable signal: the fallback ladder, with the reason
//     preserved so an operator can tell "Jev was unsure" from "Jev was down".
func (e *Engine) evaluateConfidence(jev JevSignal, named, eligible bool) bandDecision {
	if !jev.Available {
		return bandDecision{
			Band:           decision.BandLow,
			FallbackReason: jevFailure(jev),
			Detail:         "unusable_signal",
		}
	}
	if !finite(jev.Confidence) {
		// The call happened, but the confidence it reported is not a number. The
		// result is unusable rather than absent, and saying so keeps "Jev was down"
		// distinguishable from "Jev answered with something impossible".
		return bandDecision{
			Band:           decision.BandLow,
			FallbackReason: decision.ReasonJevInvalidResult,
			Detail:         "non_finite_confidence",
		}
	}
	switch {
	case jev.Confidence >= e.high && eligible:
		return bandDecision{
			Band: decision.BandHigh, AdoptJev: true, UseJev: true, Alpha: 1,
			FallbackReason: decision.ReasonNone, Detail: "adopted",
		}
	case jev.Confidence >= e.high:
		// High confidence, but the named pair cannot serve this request. The
		// recommendation is dropped entirely: alpha becomes 0, so the static score
		// decides, and the band drops to medium because nothing about this request
		// was confirmed by the recommendation.
		detail := "not_in_candidates"
		if named {
			detail = "hard_filtered"
		}
		return bandDecision{
			Band: decision.BandMedium, Blend: true, UseJev: false, Alpha: 0,
			Ineligible: true, FallbackReason: decision.ReasonSelectedModelIneligible, Detail: detail,
		}
	case jev.Confidence > e.low:
		return bandDecision{
			Band: decision.BandMedium, Blend: true, UseJev: true,
			Alpha:          (jev.Confidence - e.low) / (e.high - e.low),
			FallbackReason: decision.ReasonNone, Detail: "blended",
		}
	default:
		return bandDecision{
			Band:           decision.BandLow,
			FallbackReason: decision.ReasonConfidenceLow,
			Detail:         "confidence_below_low",
		}
	}
}

// selectionState reports whether the recommendation named a candidate at all and
// whether that candidate survived the hard filters.
func selectionState(candidates []decision.Candidate, survivors []eligible, jevResult JevSignal) (named, eligibleSelection bool) {
	if jevResult.Selected == "" {
		return false, false
	}
	for _, candidate := range candidates {
		if candidate.ModelID == jevResult.Selected {
			named = true
			break
		}
	}
	_, eligibleSelection = findSurvivor(survivors, jevResult.Selected)
	return named, eligibleSelection
}

// bestBlended selects the highest final score. Equal scores keep the earlier
// candidate, so the tie-break is the caller's contract order. Differences below
// scoreEpsilon are float noise and never decide a routing question.
func bestBlended(survivors []eligible, band bandDecision) (eligible, float64) {
	best := survivors[0]
	bestScore := finalScore(best, band)
	for index := 1; index < len(survivors); index++ {
		score := finalScore(survivors[index], band)
		if score > bestScore+scoreEpsilon {
			best, bestScore = survivors[index], score
		}
	}
	return best, bestScore
}

func finalScore(candidate eligible, band bandDecision) float64 {
	jev := 0.0
	if band.UseJev {
		jev = candidate.jevScore
	}
	return clamp01(band.Alpha*jev + (1-band.Alpha)*candidate.score)
}

// fallback is the bounded low-confidence ladder: the configured default model when
// it is eligible, otherwise the first surviving candidate in contract order. It
// never relaxes a hard constraint to reach the default model.
func (e *Engine) fallback(trace *explanation, candidates []decision.Candidate, survivors []eligible, band bandDecision) Outcome {
	info := decision.FallbackInfo{Used: true, Reason: band.FallbackReason, RequestedModel: e.defaultModel}
	chosen := survivors[0]
	trace.origin = decision.OriginFirstEligible
	if e.defaultModel != "" {
		if candidate, ok := findSurvivor(survivors, e.defaultModel); ok {
			chosen = candidate
			info.Eligible = true
			trace.origin = decision.OriginDefaultModel
		} else {
			// The configured default model was not eligible. It is reported as
			// ineligible together with the first exclusion code that applied to it,
			// and the decision uses the first eligible candidate instead. Hard
			// constraints are never lowered to make the default model fit.
			info.Detail = "not_eligible:" + trace.firstCode(e.defaultModel)
		}
	} else {
		info.Detail = "not_configured"
		band.Detail = "not_configured"
	}
	trace.fallbackReason = band.FallbackReason
	trace.fallbackDetail = band.Detail
	return e.build(decision.Decision{
		Provider:       chosen.candidate.ProviderKey,
		Model:          chosen.candidate.ModelID,
		GatewayModel:   chosen.candidate.GatewayModel,
		Confidence:     chosen.jevScore,
		ConfidenceBand: band.Band,
		Fallback:       info,
	}, trace, candidates, survivors, chosen, 0)
}

// build assembles the final decision: the reason string, the bounded exclusion
// list, the bounded signals and the evidence hash.
func (e *Engine) build(base decision.Decision, trace *explanation, candidates []decision.Candidate, survivors []eligible, chosen eligible, alpha float64) Outcome {
	exclusions, total := presentExclusions(trace.exclusions)
	base.Reason = renderReason(base.ConfidenceBand, trace.origin, base.Fallback.Reason, leadingCodes(trace.exclusions, maxReasonCodes), trace.degraded)
	base.Excluded = exclusions
	base.ExcludedCount = total
	base.Signals = trace.signals(e, survivors, chosen, alpha, base.ConfidenceBand)
	base.EvidenceHash = trace.evidenceHash(survivors)
	return Outcome{Decision: base, Features: trace.features, Origin: trace.origin}
}

// findSurvivor locates a candidate by model ID, preferring the earliest position in
// contract order. Two providers can serve the same logical model; the caller's
// order decides which one, and the engine never picks by map iteration.
func findSurvivor(survivors []eligible, modelID string) (eligible, bool) {
	for _, candidate := range survivors {
		if candidate.candidate.ModelID == modelID {
			return candidate, true
		}
	}
	return eligible{}, false
}

// bestStaticForModel picks the pair that serves one logical model. Candidates are
// already scored, so this is a lookup rather than a recomputation: the highest
// static score wins, and an exact tie keeps the earlier candidate, which is the
// caller's contract order. With one pair per model it is exactly findSurvivor, so
// a deployment that never maps a model to two providers sees no change.
func bestStaticForModel(survivors []eligible, modelID string) (eligible, bool) {
	var best eligible
	found := false
	for _, candidate := range survivors {
		if candidate.candidate.ModelID != modelID {
			continue
		}
		if !found || candidate.score > best.score+scoreEpsilon {
			best, found = candidate, true
		}
	}
	return best, found
}

// attachJevScores converts the recommendation's probabilities into a normalized
// score per candidate. The table is used only when it agrees with the candidate
// set exactly; otherwise every candidate scores zero, which removes the Jev
// information without inventing one. A partial or superset distribution is a
// different question, not a weaker answer.
//
// The probability table is keyed by logical model, so it is validated against
// the distinct models among the survivors, not against the pair count. Every pair
// that serves the same model inherits that model's score: a Jev probability is a
// fact about the model, and dividing it across providers would make a model's
// score depend on how many providers happen to serve it.
func attachJevScores(survivors []eligible, jevResult JevSignal) {
	if !jevResult.Available || !finite(jevResult.Confidence) {
		return
	}
	total := distinctModelCount(survivors)
	if !probabilityTableMatches(jevResult.Probabilities, survivors, total) {
		return
	}
	for i := range survivors {
		survivors[i].jevAvailable = true
		survivors[i].jevScore = jevScore(jevResult.Probabilities[survivors[i].candidate.ModelID], total)
	}
}

// distinctModelCount counts the logical models a survivor set covers. It is the
// n of the Jev normalization: probabilities are declared per model, so the
// number of pairs is the wrong denominator whenever one model has several
// providers.
func distinctModelCount(survivors []eligible) int {
	models := make(map[string]struct{}, len(survivors))
	for _, survivor := range survivors {
		models[survivor.candidate.ModelID] = struct{}{}
	}
	return len(models)
}

// modelCount counts the distinct logical models in a candidate list. It backs the
// model_count signal, which reports the denominator of the Jev normalization for
// the whole caller-supplied set.
func modelCount(candidates []decision.Candidate) int {
	models := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		models[candidate.ModelID] = struct{}{}
	}
	return len(models)
}

// probabilityTableMatches reports whether the table describes exactly the
// distinct logical models among the survivors: one finite probability in [0, 1]
// per model, and no entry for a model that is not a survivor.
func probabilityTableMatches(probabilities map[string]float64, survivors []eligible, total int) bool {
	if total == 0 || len(probabilities) != total {
		return false
	}
	models := make(map[string]struct{}, total)
	for _, survivor := range survivors {
		models[survivor.candidate.ModelID] = struct{}{}
	}
	for model, value := range probabilities {
		if _, ok := models[model]; !ok {
			return false
		}
		if !finite(value) || value < 0 || value > 1 {
			return false
		}
	}
	return true
}

// jevScore maps a probability onto a comparable score. The mapping is affine and
// pins the endpoints that matter: a uniform distribution (p = 1/n) is neutral at
// 0.5, certainty (p = 1) is 1.0, and impossibility (p = 0) is 0.0. Without the
// normalization a recommendation spread over many candidates would look weaker than
// one spread over few, which is an artifact of the candidate count rather than a
// fact about the request.
func jevScore(probability float64, total int) float64 {
	if total <= 1 {
		return neutralProbabilityScore
	}
	neutral := 1 / float64(total)
	return clamp01(0.5 + 0.5*(probability-neutral)/(1-neutral))
}
