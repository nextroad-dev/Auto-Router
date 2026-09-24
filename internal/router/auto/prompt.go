package auto

import (
	"fmt"
	"strings"

	"github.com/nextroad-dev/Auto-Router/internal/router/analyzer"
	"github.com/nextroad-dev/Auto-Router/internal/router/jev"
)

// Fixed bounds of the automatic routing pipeline. They are named constants
// rather than settings for the same reason the analyzer's bounds are: a decision
// must be reproducible across deployments, and a deployment that could raise the
// Jev attempt count or the number of models in one question could make routing
// behavior differ for reasons that never appear in a decision record.
const (
	// minModelsForJev is the number of distinct eligible logical models below
	// which Jev is not asked. With one model a recommendation cannot express a
	// choice, so the call would cost money to answer a question whose answer is
	// already known.
	minModelsForJev = 2
	// maxJevModels is the verified limit of the Choice primitive (255 options).
	// A larger set could only be rejected upstream, so it is skipped locally and
	// reported as such instead of being truncated into a different question.
	maxJevModels = 255
)

// prompt is the conversation part of one Jev request.
type prompt struct {
	system   string
	messages []jev.Message
	tools    []string
}

// buildPrompt renders the Jev conversation for the configured input mode,
// returning false when the mode leaves no usable evidence.
//
// The modes are a privacy/quality trade-off the operator chooses, and none of
// them changes the wire format: they only decide what goes into the same verified
// fields.
func (r *Router) buildPrompt(features analyzer.Features, view analyzer.View, envelopes []modelEnvelope, inputMode jev.InputMode) (prompt, bool) {
	switch inputMode {
	case jev.InputModeRedacted:
		system := redactedSystemPrompt(view.SystemPrompt)
		messages := redactedMessages(view.Messages)
		if system == "" && len(messages) == 0 {
			return prompt{}, false
		}
		return prompt{system: system, messages: messages, tools: view.Tools}, true

	case jev.InputModeFeaturesOnly:
		// The message is written by this process from counts and enumerations
		// only. Nothing the client sent is copied into it, which is what makes
		// this mode evidence-independent: it is always available.
		return prompt{messages: []jev.Message{{Role: "user", Content: featuresMessage(features, envelopes)}}}, true

	default:
		if strings.TrimSpace(view.SystemPrompt) == "" && !hasTextMessages(view.Messages) {
			return prompt{}, false
		}
		return prompt{system: view.SystemPrompt, messages: view.Messages, tools: view.Tools}, true
	}
}

// hasTextMessages reports whether a view carries at least one non-empty turn.
// The analyzer already drops empty text turns, so this is the exact condition the
// content mode needs: a view with nothing in it would ask Jev to judge an empty
// conversation.
func hasTextMessages(messages []jev.Message) bool {
	for _, message := range messages {
		if text, ok := message.Content.(string); ok && strings.TrimSpace(text) != "" {
			return true
		}
	}
	return false
}

// featuresMessage renders the features-only question: length class and token
// estimate, protocol, preference, tool count, and the media booleans. Every value
// is a count, an enumeration or a boolean derived from the request, so the string
// is a description of the request's shape rather than a copy of it.
//
// The token estimate is omitted when it is at least the smallest eligible context
// window. That is not a claim that the request is small; it is the only case where
// the number could be read as such, and the estimate is a heuristic that the
// model must not treat as a measurement.
func featuresMessage(features analyzer.Features, envelopes []modelEnvelope) string {
	var builder strings.Builder
	builder.WriteString("Automatic routing request. No conversation text is available by configuration; judge from these facts only.\n")
	fmt.Fprintf(&builder, "protocol: %s\n", features.Protocol)
	fmt.Fprintf(&builder, "preference: %s\n", features.Preference)
	fmt.Fprintf(&builder, "length_class: %s\n", features.Length)
	if smallest := smallestContextWindow(envelopes); smallest == 0 || features.InputTokensEstimate < smallest {
		fmt.Fprintf(&builder, "input_tokens_estimate: %d\n", features.InputTokensEstimate)
	} else {
		builder.WriteString("input_tokens_estimate: at least the smallest candidate context window\n")
	}
	fmt.Fprintf(&builder, "tools_offered: %d\n", features.ToolCount)
	fmt.Fprintf(&builder, "tool_choice_forced: %t\n", features.ForcedToolChoice)
	fmt.Fprintf(&builder, "multimodal: images=%t image_url=%t base64_image=%t file=%t audio=%t\n",
		features.ImageCount > 0, features.HasImageURL, features.HasBase64Image, features.HasFile, features.HasAudio)
	fmt.Fprintf(&builder, "stream_requested: %t\n", features.StreamRequested)
	fmt.Fprintf(&builder, "reasoning_likely: %t\n", features.ReasoningLikely)
	fmt.Fprintf(&builder, "code_likely: %t\n", features.HasCode)
	fmt.Fprintf(&builder, "truncated_analysis: %t\n", features.Truncated)
	builder.WriteString("candidate_count: ")
	fmt.Fprintf(&builder, "%d\n", len(envelopes))
	return builder.String()
}

// smallestContextWindow returns the smallest context window among the eligible
// model envelopes, or zero when there are none.
func smallestContextWindow(envelopes []modelEnvelope) int {
	smallest := 0
	for index, envelope := range envelopes {
		if index == 0 || envelope.ContextWindow < smallest {
			smallest = envelope.ContextWindow
		}
	}
	return smallest
}
