package main

import (
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
)

// This file implements the -analyze-check maintenance mode: an offline, purely
// local report of what the analyzer extracts from one request body.
//
// The mode is a proof tool. It runs before the database is opened, starts no
// listener and performs no network call of any kind, and it prints counts,
// enumerations and identifiers only: the request body is never echoed, and an
// inline media payload is reported as "[redacted]" exactly as the feature set
// reports it.

// analyzeCheckFlags collects the flags that belong to the check, so the
// argument parser stays readable and each flag can be validated individually.
type analyzeCheckFlags struct {
	enabled    bool
	protocol   string
	preference string
	file       string
}

func registerAnalyzeCheckFlags(flags *flag.FlagSet, check *analyzeCheckFlags) {
	flags.BoolVar(&check.enabled, "analyze-check", false, "extract routing features from one request body offline, then exit")
	flags.StringVar(&check.protocol, "analyze-protocol", string(providers.ProtocolChatCompletions), "protocol of the analyzed body: chat_completions or responses")
	flags.StringVar(&check.preference, "analyze-preference", "", "routing preference to resolve: balanced, quality, cost or latency (defaults to routing.default_preference)")
	flags.StringVar(&check.file, "analyze-file", "", "optional path to a JSON request body; omitting it uses the built-in sample")
}

// validate checks the check's own arguments. It runs after flag parsing and
// before configuration loading, so a typo fails fast without touching the
// database.
func (c analyzeCheckFlags) validate(explicitlySet map[string]bool) error {
	// The subordinate flags are meaningless on their own; accepting them would
	// silently do nothing, which is worse than refusing them.
	for _, name := range []string{"analyze-protocol", "analyze-preference", "analyze-file"} {
		if explicitlySet[name] && !c.enabled {
			return fmt.Errorf("-%s requires -analyze-check", name)
		}
	}
	if !c.enabled {
		return nil
	}
	if _, ok := analyzeProtocol(c.protocol); !ok {
		return errors.New("-analyze-protocol must be chat_completions or responses")
	}
	return nil
}

// analyzeProtocol maps the flag value onto a protocol.
func analyzeProtocol(value string) (providers.Protocol, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(providers.ProtocolChatCompletions):
		return providers.ProtocolChatCompletions, true
	case string(providers.ProtocolResponses):
		return providers.ProtocolResponses, true
	default:
		return "", false
	}
}

// runAnalyzeCheck loads a body, analyzes it and prints the deterministic report.
// It never opens the database, never builds an executor or a Jev client, and
// never starts the HTTP server.
func runAnalyzeCheck(cfg config.Config, check analyzeCheckFlags, output io.Writer) error {
	protocol, ok := analyzeProtocol(check.protocol)
	if !ok {
		return errors.New("-analyze-protocol must be chat_completions or responses")
	}
	body, source, err := analyzeBody(check.file, protocol)
	if err != nil {
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
		return fmt.Errorf("analyze %s: %w", source, err)
	}
	writeAnalyzeReport(output, protocol, source, result)
	return nil
}

// analyzeBody returns the body to analyze and a human-readable description of
// where it came from. Note that the description is a path or the phrase
// "built-in sample", never the body content.
func analyzeBody(path string, protocol providers.Protocol) ([]byte, string, error) {
	if path == "" {
		sample, ok := builtinAnalyzeSample(protocol)
		if !ok {
			return nil, "", errors.New("no built-in sample exists for that protocol")
		}
		return sample, "built-in sample", nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read the analysis body: %w", err)
	}
	return body, fmt.Sprintf("file %s", path), nil
}

// builtinAnalyzeSample is a small, self-contained request per protocol. The
// samples are deliberately trivial: their purpose is to prove the check runs,
// not to demonstrate a feature set.
func builtinAnalyzeSample(protocol providers.Protocol) ([]byte, bool) {
	switch protocol {
	case providers.ProtocolChatCompletions:
		return []byte(`{"model":"sample-model","messages":[{"role":"system","content":"Answer briefly."},{"role":"user","content":"Summarize the trade-offs of a router that reads every request."}],"stream":true,"max_completion_tokens":256}`), true
	case providers.ProtocolResponses:
		return []byte(`{"model":"sample-model","instructions":"Answer briefly.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Summarize the trade-offs of a router that reads every request."}]}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"stream":true,"max_output_tokens":256}`), true
	default:
		return nil, false
	}
}

// writeAnalyzeReport prints the features in a fixed order. Every line is a
// count, an enumeration or an identifier, and the header states what did not
// happen, because "the analyzer ran" is only meaningful together with "nothing
// left this process and the request was not touched".
func writeAnalyzeReport(output io.Writer, protocol providers.Protocol, source string, result analyzer.Result) {
	features := result.Features
	fmt.Fprintf(output, "analyze check: %s from %s\n", protocol, source)
	fmt.Fprintf(output, "no upstream call, no agent, the request was not modified\n\n")
	fmt.Fprintf(output, "protocol:                 %s\n", features.Protocol)
	fmt.Fprintf(output, "preference:               %s (%s)\n", features.Preference, features.PreferenceSource)
	fmt.Fprintf(output, "input_bytes:              %d\n", features.InputBytes)
	fmt.Fprintf(output, "input_tokens_estimate:    %d\n", features.InputTokensEstimate)
	fmt.Fprintf(output, "length:                   %s\n", features.Length)
	fmt.Fprintf(output, "truncated:                %t\n", features.Truncated)
	fmt.Fprintf(output, "message_count:            %d\n", features.MessageCount)
	fmt.Fprintf(output, "role_counts:              %s\n", formatRoleCounts(features.RoleCounts))
	fmt.Fprintf(output, "system_prompt_present:    %t\n", features.SystemPromptPresent)
	fmt.Fprintf(output, "system_prompt_bytes:      %d\n", features.SystemPromptBytes)
	fmt.Fprintf(output, "text_only:                %t\n", features.TextOnly)
	fmt.Fprintf(output, "kinds:                    %s\n", formatStrings(kindsToStrings(features.Kinds)))
	fmt.Fprintf(output, "images:                   %d (url=%t base64=%t file=%t audio=%t)\n",
		features.ImageCount, features.HasImageURL, features.HasBase64Image, features.HasFile, features.HasAudio)
	fmt.Fprintf(output, "image_refs:               %s\n", formatImageRefs(features.Images))
	fmt.Fprintf(output, "tools:                    %d (truncated=%t forced=%t file_search=%t)\n",
		features.ToolCount, features.ToolsTruncated, features.ForcedToolChoice, features.FileSearchUsed)
	fmt.Fprintf(output, "tool_names:               %s\n", formatStrings(features.ToolNames))
	fmt.Fprintf(output, "has_code:                 %t\n", features.HasCode)
	fmt.Fprintf(output, "reasoning_likely:         %t\n", features.ReasoningLikely)
	fmt.Fprintf(output, "chained_tool_use:         %t\n", features.ChainedToolUse)
	fmt.Fprintf(output, "stream_requested:         %t\n", features.StreamRequested)
	fmt.Fprintf(output, "max_output_tokens:        %s\n", formatOptionalInt(features.MaxOutputTokensRequested))
	fmt.Fprintf(output, "unrecognized_parts:       %d\n", features.UnrecognizedParts)
	fmt.Fprintf(output, "view_messages:            %d (truncated=%t tools=%d)\n", len(result.View.Messages), result.View.Truncated, len(result.View.Tools))
}

// formatRoleCounts renders all role buckets in the fixed bucket order, so two
// runs of the same body produce byte-identical output.
func formatRoleCounts(counts map[string]int) string {
	parts := make([]string, 0, len(counts))
	for _, role := range []string{"system", "developer", "user", "assistant", "tool", "function", "other"} {
		parts = append(parts, fmt.Sprintf("%s=%d", role, counts[role]))
	}
	return strings.Join(parts, " ")
}

func kindsToStrings(kinds []analyzer.InputKind) []string {
	values := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		values = append(values, string(kind))
	}
	return values
}

// formatImageRefs renders media references sorted by kind then location, so the
// report is deterministic even though the analyzer keeps them in encounter
// order. Locations are either bounded http(s) URLs or "[redacted]".
func formatImageRefs(refs []analyzer.ImageRef) string {
	if len(refs) == 0 {
		return "none"
	}
	rendered := make([]string, 0, len(refs))
	for _, ref := range refs {
		rendered = append(rendered, fmt.Sprintf("%s:%s", ref.Kind, ref.Location))
	}
	sort.Strings(rendered)
	return strings.Join(rendered, ", ")
}

func formatStrings(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

func formatOptionalInt(value *int) string {
	if value == nil {
		return "none"
	}
	return fmt.Sprintf("%d", *value)
}
