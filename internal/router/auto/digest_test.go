package auto

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nextroad-dev/Auto-Router/internal/providers"
	"github.com/nextroad-dev/Auto-Router/internal/router/analyzer"
	"github.com/nextroad-dev/Auto-Router/internal/router/jev"
)

// agentRequest builds a Responses request shaped like a long coding-agent
// session: long instructions, several user prompts, and hundreds of tool calls
// with large outputs between them. The latest user prompt comes before a final
// agent loop, as it does while an agent is working.
func agentRequest(t *testing.T, userTurns, loopsPerTurn, outputBytes int) []byte {
	t.Helper()
	input := make([]any, 0)
	for turn := 0; turn < userTurns; turn++ {
		input = append(input, map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": fmt.Sprintf("USER-PROMPT-%d: refactor the router. %s", turn, strings.Repeat("detail ", 50))}}})
		for loop := 0; loop < loopsPerTurn; loop++ {
			input = append(input,
				map[string]any{"type": "message", "role": "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": fmt.Sprintf("ASSISTANT-%d-%d: reading files", turn, loop)}}},
				map[string]any{"type": "function_call", "call_id": fmt.Sprintf("c%d-%d", turn, loop), "name": "shell", "arguments": "{}"},
				map[string]any{"type": "function_call_output", "call_id": fmt.Sprintf("c%d-%d", turn, loop), "output": "TOOL-OUTPUT " + strings.Repeat("x", outputBytes)},
			)
		}
	}
	body, err := json.Marshal(map[string]any{
		"model":        "auto",
		"instructions": "AGENT-INSTRUCTIONS " + strings.Repeat("You are a coding agent. ", 1000),
		"input":        input,
		"tools":        []any{map[string]any{"type": "function", "name": "shell", "parameters": map[string]any{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func analyze(t *testing.T, body []byte) analyzer.Result {
	t.Helper()
	result, err := analyzer.New().Analyze(analyzer.Input{Protocol: providers.ProtocolResponses, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func promptBytes(p prompt) int {
	total := len(p.system)
	for _, message := range p.messages {
		total += len(message.Content.(string))
	}
	for _, name := range p.tools {
		total += len(name)
	}
	return total
}

func promptText(p prompt) string {
	parts := []string{p.system}
	for _, message := range p.messages {
		parts = append(parts, message.Role+": "+message.Content.(string))
	}
	return strings.Join(parts, "\n")
}

func TestDigestBoundsALongAgentConversation(t *testing.T) {
	// About 3 MiB of conversation, far more than the view keeps.
	result := analyze(t, agentRequest(t, 8, 40, 8<<10))
	if !result.View.Truncated {
		t.Fatal("the view should have evicted old turns")
	}
	built, ok := (&Router{}).buildPrompt(result.Features, result.View, nil, jev.InputModeContent)
	if !ok {
		t.Fatal("digest reported no evidence")
	}
	if size := promptBytes(built); size > digestBudgetBytes {
		t.Fatalf("digest is %d bytes, budget %d", size, digestBudgetBytes)
	}
	text := promptText(built)
	for _, want := range []string{"USER-PROMPT-7", "AGENT-INSTRUCTIONS", "[routing digest]", "[omitted turns:", "ASSISTANT-7-39"} {
		if !strings.Contains(text, want) {
			t.Fatalf("digest lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "TOOL-OUTPUT") {
		t.Fatal("tool output text must be summarized, not forwarded")
	}
	if len(strings.Split(built.system, "\n\n")[0]) > digestSystemBytes {
		t.Fatalf("system excerpt exceeds %d bytes", digestSystemBytes)
	}
	users := 0
	for _, message := range built.messages {
		if message.Role == "user" {
			users++
		}
	}
	if users < 1 || users > digestEarlierUserTurns+1 {
		t.Fatalf("digest keeps %d user turns", users)
	}
	if built.messages[len(built.messages)-1].Role != "assistant" {
		t.Fatal("the agent loop after the latest prompt should end the digest")
	}
}

func TestViewKeepsTheLatestTurns(t *testing.T) {
	// More turns than maxViewMessages: the latest prompt must survive.
	result := analyze(t, agentRequest(t, 3, 120, 16))
	last := result.View.Messages[len(result.View.Messages)-1].Content.(string)
	if !strings.Contains(last, "TOOL-OUTPUT") {
		t.Fatalf("last view message = %q, want the final tool output", last)
	}
	found := false
	for _, message := range result.View.Messages {
		if strings.Contains(message.Content.(string), "USER-PROMPT-2") {
			found = true
		}
	}
	if !found || !result.View.Truncated {
		t.Fatalf("latest user prompt kept = %t, truncated = %t", found, result.View.Truncated)
	}
}

func TestDigestDropsEarlierUserTurnsToFitTheBudget(t *testing.T) {
	var messages []jev.Message
	for turn := 0; turn < 10; turn++ {
		messages = append(messages, jev.Message{Role: "user", Content: fmt.Sprintf("PROMPT-%d %s", turn, strings.Repeat("p", 6<<10))})
	}
	view := analyzer.View{SystemPrompt: "sys", Messages: messages}
	built, ok := buildDigest(analyzer.Features{MessageCount: 10}, view, contentShape)
	if !ok {
		t.Fatal("no evidence")
	}
	text := promptText(built)
	if !strings.Contains(text, "PROMPT-9") || strings.Contains(text, "PROMPT-5") {
		t.Fatalf("digest should keep the latest prompts only:\n%.300s", text)
	}
	if size := promptBytes(built); size > digestBudgetBytes {
		t.Fatalf("digest is %d bytes", size)
	}
}

func TestDigestRedactedModeRedactsEveryBlock(t *testing.T) {
	view := analyzer.View{
		SystemPrompt: "contact admin@example.com",
		Messages: []jev.Message{
			{Role: "user", Content: "my key is sk-abcdefghijklmnopqrstuvwx " + strings.Repeat("q", 4096)},
			{Role: "assistant", Content: "see https://internal.example/path"},
		},
	}
	built, ok := buildDigest(analyzer.Features{}, view, redactedShape)
	if !ok {
		t.Fatal("no evidence")
	}
	text := promptText(built)
	for _, leaked := range []string{"admin@example.com", "sk-abcdefghijklmnopqrstuvwx", "https://internal.example"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("redacted digest leaks %q", leaked)
		}
	}
	if user := built.messages[0].Content.(string); len(user) > redactedExcerptBytes {
		t.Fatalf("redacted block is %d bytes", len(user))
	}
}

func TestDigestSummarizesAViewWithoutUserTurns(t *testing.T) {
	view := analyzer.View{Messages: []jev.Message{
		{Role: "assistant", Content: "working"},
		{Role: "tool", Content: strings.Repeat("o", 1000)},
	}}
	built, ok := buildDigest(analyzer.Features{}, view, contentShape)
	if !ok || len(built.messages) != 1 || !strings.Contains(built.messages[0].Content.(string), "1 tool (1000 bytes)") {
		t.Fatalf("digest = %+v", built.messages)
	}
}

func TestExcerptKeepsBothEndsWithinLimit(t *testing.T) {
	text := "START " + strings.Repeat("中文", 3000) + " END"
	got := excerpt(text, 1024)
	if len(got) > 1024 || !utf8.ValidString(got) {
		t.Fatalf("excerpt is %d bytes, valid=%t", len(got), utf8.ValidString(got))
	}
	if !strings.HasPrefix(got, "START") || !strings.HasSuffix(got, "END") || !strings.Contains(got, "bytes omitted") {
		t.Fatalf("excerpt = %q", got)
	}
	if short := excerpt("  short  ", 1024); short != "short" {
		t.Fatalf("short excerpt = %q", short)
	}
}
