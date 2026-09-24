package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
	"github.com/nextroad-dev/Auto-Router/internal/router/analyzer"
	"github.com/nextroad-dev/Auto-Router/internal/router/decision"
	"github.com/nextroad-dev/Auto-Router/internal/router/policy"
)

// This file implements the -policy-check maintenance mode: an offline, purely local
// report of how the policy engine decides over one request body, a candidate list
// and a recommendation.
//
// The mode is a proof tool. It runs before the database is opened, starts no
// listener and performs no network call of any kind: the Jev result is supplied as
// a file or taken from the built-in samples, and the candidate list is supplied as
// a file or taken from the built-in set. The output prints identifiers, numbers and
// closed enumerations only — never a prompt, an inline payload or a credential.
//
// Exit codes are part of the contract, because a maintenance command that only ever
// exits 0 cannot be used to gate anything:
//
//	0 success (a decision was produced)
//	1 usage, configuration or I/O error
//	2 no eligible candidate
//	3 truncated evidence refused

// The exit codes, named so the tests and the help text cannot drift apart.
const (
	policyCheckOK          = 0
	policyCheckFailure     = 1
	policyCheckNoCandidate = 2
	policyCheckTruncated   = 3
)

// policyCheckFlags collects the flags that belong to the check, so the argument
// parser stays readable and each flag can be validated individually.
type policyCheckFlags struct {
	enabled        bool
	protocol       string
	preference     string
	file           string
	candidatesFile string
	jevFile        string
}

func registerPolicyCheckFlags(flags *flag.FlagSet, check *policyCheckFlags) {
	flags.BoolVar(&check.enabled, "policy-check", false, "evaluate the routing policy offline over one request body, then exit")
	flags.StringVar(&check.protocol, "policy-protocol", string(providers.ProtocolChatCompletions), "protocol of the analyzed body: chat_completions or responses")
	flags.StringVar(&check.preference, "policy-preference", "", "routing preference to resolve: balanced, quality, cost or latency (defaults to routing.default_preference)")
	flags.StringVar(&check.file, "policy-file", "", "optional path to a JSON request body; omitting it uses the built-in sample")
	flags.StringVar(&check.candidatesFile, "policy-candidates-file", "", "optional path to a JSON candidate list; omitting it uses the built-in candidate set")
	flags.StringVar(&check.jevFile, "policy-jev-file", "", "optional path to a JSON Jev result; omitting it runs the built-in high/medium/low samples")
}

// validate checks the check's own arguments. It runs after flag parsing and before
// configuration loading, so a typo fails fast without touching the database.
func (c policyCheckFlags) validate(explicitlySet map[string]bool) error {
	// The subordinate flags are meaningless on their own; accepting them would
	// silently do nothing, which is worse than refusing them.
	for _, name := range []string{"policy-protocol", "policy-preference", "policy-file", "policy-candidates-file", "policy-jev-file"} {
		if explicitlySet[name] && !c.enabled {
			return fmt.Errorf("-%s requires -policy-check", name)
		}
	}
	if !c.enabled {
		return nil
	}
	if _, ok := analyzeProtocol(c.protocol); !ok {
		return errors.New("-policy-protocol must be chat_completions or responses")
	}
	return nil
}

// policyCheckCandidate is the JSON shape of one candidate in the candidates file.
// It mirrors decision.Candidate and the debug endpoint's DTO, so an operator can
// move a candidate list between the two tools unchanged.
type policyCheckCandidate struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// GatewayModel/gateway_model are historical names; the value is provider-native.
	GatewayModel       string `json:"gateway_model"`
	ContextWindow      int    `json:"context_window"`
	MaxOutput          *int   `json:"max_output"`
	SupportsTools      bool   `json:"supports_tools"`
	SupportsVision     bool   `json:"supports_vision"`
	SupportsReasoning  bool   `json:"supports_reasoning"`
	SupportsAudioInput bool   `json:"supports_audio_input"`
	PairPriority       int    `json:"pair_priority"`
	ProviderPriority   int    `json:"provider_priority"`
}

// policyCheckJev is the JSON shape of a Jev result in the Jev file.
type policyCheckJev struct {
	Available     *bool              `json:"available"`
	Failure       string             `json:"failure"`
	Selected      string             `json:"selected"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

// policyCheckScenario is one evaluation: a label, the features input and the Jev
// input. The built-in run uses three scenarios that exercise the three bands.
type policyCheckScenario struct {
	Name string
	Jev  policy.JevSignal
}

// runPolicyCheck loads the body, candidate list and recommendation, evaluates the
// policy and prints the deterministic report. It never opens the database, never
// builds an executor or a Jev client, and never starts the HTTP server.
//
// The returned error is already classified: the caller turns it into an exit code.
func runPolicyCheck(cfg config.Config, check policyCheckFlags, output io.Writer) error {
	protocol, ok := analyzeProtocol(check.protocol)
	if !ok {
		return errors.New("-policy-protocol must be chat_completions or responses")
	}
	body, bodySource, err := analyzeBody(check.file, protocol)
	if err != nil {
		return err
	}
	candidates, candidateSource, err := policyCandidates(check.candidatesFile)
	if err != nil {
		return err
	}
	scenarios, scenarioSource, err := policyScenarios(check.jevFile)
	if err != nil {
		return err
	}
	engine, err := cfg.PolicyEngine()
	if err != nil {
		// A configuration that cannot be compiled is a usage failure, not a routing
		// outcome: the operator has to fix the policy settings.
		return err
	}
	headerPreference := ""
	if check.preference != "" {
		headerPreference = check.preference
	}
	result, err := analyzer.Analyze(analyzer.Input{
		Protocol:          protocol,
		Body:              body,
		HeaderPreference:  headerPreference,
		DefaultPreference: cfg.Routing.DefaultPreference,
	})
	if err != nil {
		return fmt.Errorf("analyze %s: %w", bodySource, err)
	}

	fmt.Fprintf(output, "policy check: %s from %s\n", protocol, bodySource)
	fmt.Fprintf(output, "candidates from %s, recommendations from %s\n", candidateSource, scenarioSource)
	fmt.Fprintf(output, "no upstream call, no agent, the request was not modified, the database was not opened\n\n")
	writePolicyInputs(output, result.Features, candidates)

	var refusals []error
	for _, scenario := range scenarios {
		outcome, err := engine.Evaluate(result.Features, candidates, scenario.Jev)
		if err != nil {
			refusals = append(refusals, err)
			writePolicyRefusal(output, scenario, err)
			continue
		}
		writePolicyDecision(output, scenario, outcome)
	}
	// A refusal is reported as its own exit code, so the command can gate a
	// deployment: a candidate set that cannot serve the request is a real finding.
	if len(refusals) > 0 {
		return classifyPolicyRefusals(refusals)
	}
	return nil
}

// classifyPolicyRefusals returns the most specific refusal, so the exit code names
// the first thing the operator has to fix.
func classifyPolicyRefusals(refusals []error) error {
	for _, err := range refusals {
		if errors.Is(err, policy.ErrTruncatedEvidence) {
			return &policyCheckRefusal{err: err, code: policyCheckTruncated}
		}
	}
	for _, err := range refusals {
		if errors.Is(err, policy.ErrNoEligibleCandidate) {
			return &policyCheckRefusal{err: err, code: policyCheckNoCandidate}
		}
	}
	return &policyCheckRefusal{err: refusals[0], code: policyCheckFailure}
}

// policyCheckRefusal carries the exit code a refusal maps onto, so the caller does
// not re-inspect the error.
type policyCheckRefusal struct {
	err  error
	code int
}

func (r *policyCheckRefusal) Error() string { return r.err.Error() }
func (r *policyCheckRefusal) Unwrap() error { return r.err }

// exitCode reports the process exit code for a check outcome.
func exitCode(err error) int {
	if err == nil {
		return policyCheckOK
	}
	var refusal *policyCheckRefusal
	if errors.As(err, &refusal) {
		return refusal.code
	}
	return policyCheckFailure
}

// policyCandidates returns the candidate list and a description of its source. Note
// that the description is a path or the phrase "built-in set", never a file's
// content.
func policyCandidates(path string) ([]decision.Candidate, string, error) {
	if path == "" {
		return builtinPolicyCandidates(), "built-in set", nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read the candidate list: %w", err)
	}
	var entries []policyCheckCandidate
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entries); err != nil {
		return nil, "", errors.New("the candidate list must be a JSON array of candidate objects")
	}
	candidates := make([]decision.Candidate, 0, len(entries))
	for _, entry := range entries {
		candidates = append(candidates, decision.Candidate{
			ModelID:            entry.Model,
			ProviderKey:        entry.Provider,
			GatewayModel:       entry.GatewayModel,
			ContextWindow:      entry.ContextWindow,
			MaxOutput:          entry.MaxOutput,
			SupportsTools:      entry.SupportsTools,
			SupportsVision:     entry.SupportsVision,
			SupportsReasoning:  entry.SupportsReasoning,
			SupportsAudioInput: entry.SupportsAudioInput,
			PairPriority:       entry.PairPriority,
			ProviderPriority:   entry.ProviderPriority,
		})
	}
	return candidates, fmt.Sprintf("file %s", path), nil
}

// policyScenarios returns the recommendations to evaluate and a description of
// their source. Without a file, three samples exercise the three confidence bands,
// because a check that only shows one band does not demonstrate the thresholds.
func policyScenarios(path string) ([]policyCheckScenario, string, error) {
	candidates := builtinPolicyCandidates()
	if path != "" {
		signal, err := readPolicyJev(path, candidates)
		if err != nil {
			return nil, "", err
		}
		return []policyCheckScenario{{Name: "file", Jev: signal}}, fmt.Sprintf("file %s", path), nil
	}
	return []policyCheckScenario{
		{
			Name: "high confidence (adopted)",
			Jev: policy.JevSignal{
				Available:  true,
				Selected:   "gpt-4o",
				Confidence: 0.92,
				Probabilities: map[string]float64{
					"gpt-4o": 0.87, "gpt-4o-mini": 0.05, "o4-mini": 0.04, "llama-3.3-70b": 0.04,
				},
			},
		}, {
			Name: "medium confidence (blended)",
			Jev: policy.JevSignal{
				Available:  true,
				Selected:   "o4-mini",
				Confidence: 0.60,
				Probabilities: map[string]float64{
					"gpt-4o": 0.25, "gpt-4o-mini": 0.10, "o4-mini": 0.50, "llama-3.3-70b": 0.15,
				},
			},
		}, {
			Name: "low confidence (fallback)",
			Jev: policy.JevSignal{
				Available:  true,
				Selected:   "llama-3.3-70b",
				Confidence: 0.20,
				Probabilities: map[string]float64{
					"gpt-4o": 0.20, "gpt-4o-mini": 0.20, "o4-mini": 0.20, "llama-3.3-70b": 0.40,
				},
			},
		},
	}, "built-in samples (high, medium, low)", nil
}

func readPolicyJev(path string, candidates []decision.Candidate) (policy.JevSignal, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return policy.JevSignal{}, fmt.Errorf("read the Jev result: %w", err)
	}
	var entry policyCheckJev
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entry); err != nil {
		return policy.JevSignal{}, errors.New("the Jev result must be a JSON object with selected, confidence and probabilities fields")
	}
	available := true
	if entry.Available != nil {
		available = *entry.Available
	}
	if !available {
		failure := decision.ReasonNotRequested
		if entry.Failure != "" {
			if !decision.FallbackReason(entry.Failure).Valid() {
				return policy.JevSignal{}, errors.New("failure must be a known failure classification")
			}
			failure = decision.FallbackReason(entry.Failure)
		}
		return policy.JevSignal{Available: false, Failure: failure}, nil
	}
	if entry.Selected == "" {
		return policy.JevSignal{}, errors.New("selected is required when the result is available")
	}
	return policy.JevSignal{
		Available:     true,
		Selected:      entry.Selected,
		Confidence:    entry.Confidence,
		Probabilities: entry.Probabilities,
	}, nil
}

// builtinPolicyCandidates is a small, self-contained candidate set. Its purpose is
// to prove the check runs and to exercise the filters, not to describe a real
// deployment: it deliberately contains one candidate that fails a capability
// filter for a request that carries an image.
func builtinPolicyCandidates() []decision.Candidate {
	output := 16384
	return []decision.Candidate{
		{
			ModelID: "gpt-4o", ProviderKey: "openai", GatewayModel: "gpt-4o",
			ContextWindow: 128000, MaxOutput: &output,
			SupportsTools: true, SupportsVision: true,
			PairPriority: 0, ProviderPriority: 0,
		}, {
			ModelID: "gpt-4o-mini", ProviderKey: "openai", GatewayModel: "gpt-4o-mini",
			ContextWindow: 128000, MaxOutput: &output,
			SupportsTools: true, SupportsVision: true,
			PairPriority: 1, ProviderPriority: 0,
		}, {
			ModelID: "o4-mini", ProviderKey: "openai", GatewayModel: "o4-mini",
			ContextWindow: 200000, MaxOutput: &output,
			SupportsTools: true, SupportsReasoning: true,
			PairPriority: 2, ProviderPriority: 0,
		}, {
			ModelID: "llama-3.3-70b", ProviderKey: "groq", GatewayModel: "llama-3.3-70b-versatile",
			ContextWindow: 131072, MaxOutput: &output,
			SupportsTools: true,
			PairPriority:  3, ProviderPriority: 1,
		},
	}
}

// writePolicyInputs prints what was evaluated, in a fixed order. Every line is an
// identifier, a count or an enumeration.
func writePolicyInputs(output io.Writer, features analyzer.Features, candidates []decision.Candidate) {
	fmt.Fprintf(output, "inputs\n")
	fmt.Fprintf(output, "  preference:             %s (%s)\n", features.Preference, features.PreferenceSource)
	fmt.Fprintf(output, "  input_tokens_estimate:  %d\n", features.InputTokensEstimate)
	fmt.Fprintf(output, "  length:                 %s\n", features.Length)
	fmt.Fprintf(output, "  truncated:              %t\n", features.Truncated)
	fmt.Fprintf(output, "  tools:                  %d\n", features.ToolCount)
	fmt.Fprintf(output, "  media:                  images=%d url=%t base64=%t file=%t audio=%t\n",
		features.ImageCount, features.HasImageURL, features.HasBase64Image, features.HasFile, features.HasAudio)
	fmt.Fprintf(output, "  reasoning_likely:       %t\n", features.ReasoningLikely)
	fmt.Fprintf(output, "  stream_requested:       %t\n", features.StreamRequested)
	fmt.Fprintf(output, "  max_output_tokens:      %s\n", formatOptionalInt(features.MaxOutputTokensRequested))
	fmt.Fprintf(output, "  candidates:             %d\n", len(candidates))
	for _, candidate := range candidates {
		fmt.Fprintf(output, "    %-28s %-12s context=%d max_output=%s tools=%t vision=%t reasoning=%t audio=%t\n",
			candidate.ModelID, candidate.ProviderKey, candidate.ContextWindow, formatOptionalInt(candidate.MaxOutput),
			candidate.SupportsTools, candidate.SupportsVision, candidate.SupportsReasoning, candidate.SupportsAudioInput)
	}
	fmt.Fprintf(output, "\n")
}

// writePolicyDecision prints one decision and its explanation. The exclusion list is
// summarized by code and count: a maintenance report is a summary, and the detail
// strings carry only numbers anyway.
func writePolicyDecision(output io.Writer, scenario policyCheckScenario, outcome policy.Outcome) {
	fmt.Fprintf(output, "scenario: %s\n", scenario.Name)
	fmt.Fprintf(output, "  selected:               %s/%s\n", outcome.Provider, outcome.Model)
	fmt.Fprintf(output, "  gateway_model:          %s\n", outcome.GatewayModel)
	fmt.Fprintf(output, "  confidence:             %.6f\n", outcome.Confidence)
	fmt.Fprintf(output, "  confidence_band:        %s\n", outcome.ConfidenceBand)
	fmt.Fprintf(output, "  reason:                 %s\n", outcome.Reason)
	fmt.Fprintf(output, "  fallback:               used=%t reason=%s requested=%s eligible=%t detail=%s\n",
		outcome.Fallback.Used, outcome.Fallback.Reason, orNone(outcome.Fallback.RequestedModel),
		outcome.Fallback.Eligible, orNone(outcome.Fallback.Detail))
	fmt.Fprintf(output, "  exclusions:             %d total, %d listed\n", outcome.ExcludedCount, len(outcome.Excluded))
	for _, code := range exclusionCodeCounts(outcome.Excluded) {
		fmt.Fprintf(output, "    %-24s %d\n", code.code, code.count)
	}
	fmt.Fprintf(output, "  signals:\n")
	for _, signal := range outcome.Signals {
		fmt.Fprintf(output, "    %-24s %.6f\n", signal.Name, signal.Value)
	}
	fmt.Fprintf(output, "  evidence_hash:          %s\n\n", outcome.EvidenceHash)
}

// writePolicyRefusal prints a refusal with the same vocabulary a decision uses, so
// the two outputs can be read the same way.
func writePolicyRefusal(output io.Writer, scenario policyCheckScenario, err error) {
	refusal := policy.RefusalOf(err)
	code := "failure"
	switch {
	case errors.Is(err, policy.ErrNoEligibleCandidate):
		code = "no_eligible_candidate"
	case errors.Is(err, policy.ErrTruncatedEvidence):
		code = "truncated_evidence"
	case errors.Is(err, policy.ErrInvalidCandidates):
		code = "invalid_candidates"
	}
	fmt.Fprintf(output, "scenario: %s\n", scenario.Name)
	fmt.Fprintf(output, "  refused:                %s\n", code)
	fmt.Fprintf(output, "  fallback_reason:        %s\n", orNone(string(refusal.Reason)))
	fmt.Fprintf(output, "  exclusions:             %d total, %d listed\n", refusal.Count, len(refusal.Exclusions))
	for _, item := range exclusionCodeCounts(refusal.Exclusions) {
		fmt.Fprintf(output, "    %-24s %d\n", item.code, item.count)
	}
	fmt.Fprintf(output, "\n")
}

type exclusionCount struct {
	code  string
	count int
}

// exclusionCodeCounts summarizes an exclusion list by code, in a fixed order so two
// runs produce byte-identical output.
func exclusionCodeCounts(exclusions []decision.Exclusion) []exclusionCount {
	counts := map[string]int{}
	for _, item := range exclusions {
		counts[string(item.Code)]++
	}
	codes := make([]string, 0, len(counts))
	for code := range counts {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	summary := make([]exclusionCount, 0, len(codes))
	for _, code := range codes {
		summary = append(summary, exclusionCount{code: code, count: counts[code]})
	}
	return summary
}

func orNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}
