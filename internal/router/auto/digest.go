package auto

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/nextroad-dev/Auto-Router/internal/router/analyzer"
	"github.com/nextroad-dev/Auto-Router/internal/router/jev"
)

// Digest bounds. Like the redaction bounds they are constants, not settings:
// what leaves the process for a routing question must be the same in every
// deployment, and a change to it must be a reviewable code change.
//
// The routing question is "how hard is the turn being asked for", and the
// evidence for that is the latest retained user prompt plus the shape of the work around
// it. Assistant turns and tool outputs — in an agent conversation, almost all of
// the bytes — are summarized as counts, so a Jev request stays small however
// long the conversation grows.
const (
	// digestBudgetBytes bounds the text of one digest: system excerpt, messages
	// and tool names together. Earlier user turns are dropped, oldest first,
	// until the digest fits; the latest user turn available in the view stays.
	digestBudgetBytes = 16 << 10
	// digestLatestUserBytes bounds the latest user turn.
	digestLatestUserBytes = 8 << 10
	// digestEarlierUserBytes bounds each earlier user turn, and
	// digestEarlierUserTurns how many of them are kept.
	digestEarlierUserBytes = 1 << 10
	digestEarlierUserTurns = 3
	// digestSystemBytes bounds the system prompt excerpt. Agent instructions are
	// long and identical on every request; the opening says what kind of client
	// this is. Content mode keeps both ends; redacted mode keeps a redacted prefix.
	digestSystemBytes = 1 << 10
	// digestAssistantExcerptBytes bounds the excerpt of the last assistant text
	// in one summarized run of non-user turns.
	digestAssistantExcerptBytes = 256
	// digestMaxTools bounds how many tool names are forwarded.
	digestMaxTools = 32
)

// shapeFunc turns one retained text block into what is sent: the content mode
// cuts it to an excerpt, the redacted mode redacts it.
type shapeFunc func(text string, limit int) string

func contentShape(text string, limit int) string { return excerpt(text, limit) }

// redactedShape applies the redacted mode's own 512-byte per-block contract,
// independently of content-mode limits (including the 256-byte assistant excerpt).
func redactedShape(text string, _ int) string { return strings.TrimSpace(redactText(text)) }

// digestSegment is one kept user turn followed by the summary of the non-user
// turns up to the next kept user turn. Budget enforcement drops whole segments.
type digestSegment struct {
	messages []jev.Message
}

func (s digestSegment) bytes() int {
	total := 0
	for _, message := range s.messages {
		if text, ok := message.Content.(string); ok {
			total += len(text)
		}
	}
	return total
}

// buildDigest renders the bounded conversation digest of the view. It returns
// false when the view carries no evidence at all.
func buildDigest(features analyzer.Features, view analyzer.View, shape shapeFunc) (prompt, bool) {
	if strings.TrimSpace(view.SystemPrompt) == "" && !hasTextMessages(view.Messages) {
		return prompt{}, false
	}
	messages := view.Messages
	users := make([]int, 0, digestEarlierUserTurns+1)
	for index := len(messages) - 1; index >= 0 && len(users) <= digestEarlierUserTurns; index-- {
		if messages[index].Role == "user" {
			users = append(users, index)
		}
	}
	// users holds the kept user turns latest first; segments are built oldest
	// first so the digest reads in conversation order.
	// Turns before the oldest kept user turn are left to the note's counts. With
	// no user turn in the view at all — an agent loop whose prompt fell out of
	// it — the whole view is summarized instead.
	var lead []jev.Message
	if len(users) == 0 {
		if summary, ok := summarizeRun(messages, shape); ok {
			lead = append(lead, summary)
		}
	}
	segments := make([]digestSegment, 0, len(users))
	for position := len(users) - 1; position >= 0; position-- {
		index := users[position]
		end := len(messages)
		if position > 0 {
			end = users[position-1]
		}
		limit := digestEarlierUserBytes
		if position == 0 {
			limit = digestLatestUserBytes
		}
		var segment digestSegment
		text, _ := messages[index].Content.(string)
		if shaped := shape(text, limit); shaped != "" {
			segment.messages = append(segment.messages, jev.Message{Role: "user", Content: shaped})
		}
		if summary, ok := summarizeRun(messages[index+1:end], shape); ok {
			segment.messages = append(segment.messages, summary)
		}
		segments = append(segments, segment)
	}

	system := shape(view.SystemPrompt, digestSystemBytes)
	tools := view.Tools
	if len(tools) > digestMaxTools {
		tools = tools[:digestMaxTools]
	}
	fixed := len(system) + len(digestNote(features, len(segments))) + 2
	for _, message := range lead {
		fixed += len(message.Content.(string))
	}
	for _, name := range tools {
		fixed += len(name)
	}
	total := fixed
	for _, segment := range segments {
		total += segment.bytes()
	}
	for len(segments) > 1 && total > digestBudgetBytes {
		total -= segments[0].bytes()
		segments = segments[1:]
	}
	note := digestNote(features, len(segments))
	if system != "" {
		system += "\n\n" + note
	} else {
		system = note
	}
	out := append([]jev.Message(nil), lead...)
	for _, segment := range segments {
		out = append(out, segment.messages...)
	}
	return prompt{system: system, messages: out, tools: tools}, true
}

// summarizeRun collapses a run of non-user turns into one assistant message:
// counts and sizes per role, and an excerpt of the last assistant text, which
// says what the agent was doing when the request was made. It reports false for
// an empty run.
func summarizeRun(run []jev.Message, shape shapeFunc) (jev.Message, bool) {
	if len(run) == 0 {
		return jev.Message{}, false
	}
	counts := make(map[string]int)
	sizes := make(map[string]int)
	lastAssistant := ""
	for _, message := range run {
		text, _ := message.Content.(string)
		counts[message.Role]++
		sizes[message.Role] += len(text)
		if message.Role == "assistant" && strings.TrimSpace(text) != "" {
			lastAssistant = text
		}
	}
	roles := make([]string, 0, len(counts))
	for role := range counts {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	parts := make([]string, 0, len(roles))
	for _, role := range roles {
		parts = append(parts, fmt.Sprintf("%d %s (%d bytes)", counts[role], role, sizes[role]))
	}
	var builder strings.Builder
	builder.WriteString("[omitted turns: ")
	builder.WriteString(strings.Join(parts, ", "))
	builder.WriteString("]")
	if lastAssistant != "" {
		if shaped := shape(lastAssistant, digestAssistantExcerptBytes); shaped != "" {
			builder.WriteString("\n[last assistant text]\n")
			builder.WriteString(shaped)
		}
	}
	return jev.Message{Role: "assistant", Content: builder.String()}, true
}

// digestNote tells Jev that it is reading a digest, and what the whole request
// looked like. Every value is a count or an estimate derived by this process.
func digestNote(features analyzer.Features, keptUserTurns int) string {
	roles := make([]string, 0, len(features.RoleCounts))
	for role, count := range features.RoleCounts {
		if count > 0 {
			roles = append(roles, fmt.Sprintf("%s=%d", role, count))
		}
	}
	sort.Strings(roles)
	return fmt.Sprintf("[routing digest] The conversation below is a bounded digest: the latest %d user turn(s) as excerpts, other turns summarized. "+
		"Whole request: messages=%d (%s), input_tokens_estimate=%d, tools_offered=%d, chained_tool_use=%t.",
		keptUserTurns, features.MessageCount, strings.Join(roles, " "), features.InputTokensEstimate, features.ToolCount, features.ChainedToolUse)
}

// excerpt cuts a text block to at most limit bytes, keeping its opening and its
// end around an omission marker: a prompt states its task at the start and often
// its actual question at the end. The result is always valid UTF-8.
func excerpt(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	marker := fmt.Sprintf("\n…[%d bytes omitted]…\n", len(text))
	room := limit - len(marker)
	if room <= 0 {
		return truncateUTF8(text, limit)
	}
	head := truncateUTF8(text, room*2/3)
	tailStart := len(text) - (room - len(head))
	for tailStart < len(text) && !utf8.RuneStart(text[tailStart]) {
		tailStart++
	}
	tail := text[tailStart:]
	omitted := len(text) - len(head) - len(tail)
	return head + fmt.Sprintf("\n…[%d bytes omitted]…\n", omitted) + tail
}
