package proxy

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nextroad-dev/Auto-Router/internal/logging"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
)

// feedInPieces feeds body to the observer in fixed-size chunks, so events and
// usage objects are split across chunk boundaries.
func feedInPieces(observer *usageObserver, body string, size int) {
	for len(body) > 0 {
		n := min(size, len(body))
		observer.observe([]byte(body[:n]))
		body = body[n:]
	}
}

func requireUsage(t *testing.T, got logging.Usage, input, output, total int64) {
	t.Helper()
	if got.Status != logging.UsageStatusObserved {
		t.Fatalf("status = %q, want observed", got.Status)
	}
	if got.InputTokens == nil || *got.InputTokens != input ||
		got.OutputTokens == nil || *got.OutputTokens != output ||
		got.TotalTokens == nil || *got.TotalTokens != total {
		t.Fatalf("usage = %v/%v/%v, want %d/%d/%d", got.InputTokens, got.OutputTokens, got.TotalTokens, input, output, total)
	}
}

// responsesStream builds a Responses stream whose completed event repeats an
// output of textBytes bytes next to its usage, the shape that made the event
// larger than the event buffer.
func responsesStream(textBytes int, newline string) string {
	text := strings.Repeat("a", textBytes)
	var b strings.Builder
	sep := newline + newline
	b.WriteString(`event: response.created` + newline + `data: {"type":"response.created","response":{"id":"r","usage":null}}` + sep)
	for i := 0; i < 5; i++ {
		b.WriteString(`event: response.output_text.delta` + newline + `data: {"type":"response.output_text.delta","delta":"a"}` + sep)
	}
	// The output text quotes a usage object: it is string content, not a key.
	decoy := `\"usage\":{\"input_tokens\":999,\"output_tokens\":999,\"total_tokens\":999}`
	b.WriteString(`event: response.output_item.done` + newline + `data: {"type":"response.output_item.done","item":{"content":[{"text":"` + text + decoy + `"}]}}` + sep)
	b.WriteString(`event: response.completed` + newline + `data: {"type":"response.completed","response":{"id":"r","output":[{"content":[{"text":"` + text + decoy + `"}]}],"usage":{"input_tokens":12,"input_tokens_details":{"cached_tokens":0},"output_tokens":34,"total_tokens":46},"metadata":{}}}` + sep)
	return b.String()
}

func TestUsageObserverReadsUsageFromOversizedResponsesEvent(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		for _, size := range []int{1, 7, 4096, 32 << 10} {
			t.Run(fmt.Sprintf("newline=%q/chunk=%d", newline, size), func(t *testing.T) {
				observer := newUsageObserver(providers.ProtocolResponses, "text/event-stream", true)
				feedInPieces(observer, responsesStream(600<<10, newline), size)
				got := observer.finish(false)
				requireUsage(t, got, 12, 34, 46)
				if got.Source != logging.UsageSourceStreamResponses {
					t.Fatalf("source = %q", got.Source)
				}
			})
		}
	}
}

func TestUsageObserverResumesBufferedEventsAfterOversizedEvent(t *testing.T) {
	big := `data: {"type":"x","item":{"text":"` + strings.Repeat("b", 64<<10) + `"}}` + "\n\n"
	small := `data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}` + "\n\n"
	observer := newUsageObserver(providers.ProtocolResponses, "text/event-stream", true)
	// The oversized event and the next one arrive in the same chunk.
	observer.observe([]byte(big + small))
	requireUsage(t, observer.finish(false), 1, 2, 3)
}

func TestUsageObserverHasNoEventCountLimit(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 20000; i++ {
		b.WriteString(`data: {"choices":[{"delta":{"content":"x"}}]}` + "\n\n")
	}
	b.WriteString(`data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":6,"total_tokens":11}}` + "\n\n")
	b.WriteString("data: [DONE]\n\n")
	observer := newUsageObserver(providers.ProtocolChatCompletions, "text/event-stream", true)
	feedInPieces(observer, b.String(), 1000)
	requireUsage(t, observer.finish(false), 5, 6, 11)
}

func TestUsageObserverReadsUsageFromOversizedBody(t *testing.T) {
	body := `{"id":"r","output":[{"text":"` + strings.Repeat("c", 2<<20) + `"}],"usage":{"input_tokens":7,"output_tokens":8,"total_tokens":15}}`
	observer := newUsageObserver(providers.ProtocolResponses, "application/json", false)
	feedInPieces(observer, body, 32<<10)
	got := observer.finish(false)
	requireUsage(t, got, 7, 8, 15)
	if got.Source != logging.UsageSourceResponses {
		t.Fatalf("source = %q", got.Source)
	}
}

func TestUsageObserverIgnoresNestedUsageKeys(t *testing.T) {
	// A "usage" key below the two recognized places is not the response usage.
	body := `{"output":[{"usage":{"input_tokens":9}}],"pad":"` + strings.Repeat("d", 2<<20) + `"}`
	observer := newUsageObserver(providers.ProtocolResponses, "application/json", false)
	feedInPieces(observer, body, 32<<10)
	if got := observer.finish(false); got.Status != logging.UsageStatusAbsent {
		t.Fatalf("status = %q, want absent", got.Status)
	}
}

func TestUsageObserverOversizedBodyThatIsNotAnObjectIsMalformed(t *testing.T) {
	observer := newUsageObserver(providers.ProtocolResponses, "application/json", false)
	feedInPieces(observer, strings.Repeat("e", 2<<20), 32<<10)
	if got := observer.finish(false); got.Status != logging.UsageStatusMalformed {
		t.Fatalf("status = %q, want malformed", got.Status)
	}
}

func TestUsageObserverReportsOversizedUsageObject(t *testing.T) {
	usage := `{"input_tokens":1,"pad":"` + strings.Repeat("f", 8<<10) + `"}`
	body := `data: {"response":{"text":"` + strings.Repeat("g", 32<<10) + `","usage":` + usage + `}}` + "\n\n"
	observer := newUsageObserver(providers.ProtocolResponses, "text/event-stream", true)
	feedInPieces(observer, body, 4096)
	if got := observer.finish(false); got.Status != logging.UsageStatusOversized {
		t.Fatalf("status = %q, want oversized", got.Status)
	}
}

func TestUsageObserverSmallStreamUnchanged(t *testing.T) {
	stream := `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n" +
		`data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}` + "\n\n" +
		"data: [DONE]\n\n"
	observer := newUsageObserver(providers.ProtocolChatCompletions, "text/event-stream", true)
	feedInPieces(observer, stream, 5)
	got := observer.finish(false)
	requireUsage(t, got, 3, 4, 7)
	if got.Source != logging.UsageSourceStreamChatCompletions {
		t.Fatalf("source = %q", got.Source)
	}
}
