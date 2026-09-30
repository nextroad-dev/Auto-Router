package adapter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
)

func readEvents(t *testing.T, source io.Reader) []sseEvent {
	t.Helper()
	reader := newSSEReader(source)
	var events []sseEvent
	for {
		event, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return events
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
}

func TestSSEReaderFraming(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		stream := strings.Join([]string{
			": comment line",
			"retry: 1000",
			"event: first",
			"id: 7",
			"data: {\"foo\":",
			"data:  \"bar\"}",
			"unknown: ignored",
			"",
			"event: no-data-is-not-dispatched",
			"",
			"data",
			"",
			"data: tail-without-blank-line",
		}, newline)
		// One byte per read splits every line terminator (including CRLF) across reads.
		events := readEvents(t, iotest.OneByteReader(strings.NewReader(stream)))
		if len(events) != 3 {
			t.Fatalf("newline %q: events = %+v; want 3", newline, events)
		}
		if events[0].Event != "first" || events[0].ID != "7" || events[0].Data != "{\"foo\":\n \"bar\"}" {
			t.Fatalf("newline %q: first event = %+v", newline, events[0])
		}
		if events[1].Data != "" {
			t.Fatalf("newline %q: a bare data field = %+v; want empty data", newline, events[1])
		}
		if events[2].Data != "tail-without-blank-line" {
			t.Fatalf("newline %q: final event = %+v", newline, events[2])
		}
	}
}

type fixedExecutor struct {
	status int
	body   string
}

func (e fixedExecutor) Do(context.Context, *providers.Request) (*providers.Response, error) {
	return &providers.Response{StatusCode: e.status, Header: http.Header{}, Body: io.NopCloser(iotest.HalfReader(strings.NewReader(e.body)))}, nil
}

func chatRequestFor(stream bool) *providers.Request {
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":8`
	if stream {
		body += `,"stream":true`
	}
	return &providers.Request{Protocol: providers.ProtocolChatCompletions, Model: "m", Body: []byte(body + "}"), Stream: stream}
}

func TestAnthropicStreamWithMultiLineDataFields(t *testing.T) {
	stream := "event: message_start\r\n" +
		"data: {\"type\":\"message_start\",\r\n" +
		"data: \"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":3}}}\r\n\r\n" +
		"event: content_block_delta\r\n" +
		"data: {\"type\":\"content_block_delta\",\r\n" +
		"data: \"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\r\n\r\n" +
		"event: message_stop\r\n" +
		"data: {\"type\":\"message_stop\"}\r\n\r\n"
	executor, err := New(models.ProviderAnthropic, fixedExecutor{status: http.StatusOK, body: stream})
	if err != nil {
		t.Fatal(err)
	}
	response, err := executor.Do(context.Background(), chatRequestFor(true))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("converted stream failed: %v", err)
	}
	if !strings.Contains(string(out), `"content":"hello"`) || !strings.HasSuffix(string(out), "data: [DONE]\n\n") {
		t.Fatalf("converted stream = %q", out)
	}
}

func TestGeminiStreamWithMultiLineDataFields(t *testing.T) {
	stream := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hel\"}]}}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"lo\"}]},\n" +
		"data: \"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":2}}\n\n"
	executor, err := New(models.ProviderGemini, fixedExecutor{status: http.StatusOK, body: stream})
	if err != nil {
		t.Fatal(err)
	}
	response, err := executor.Do(context.Background(), chatRequestFor(true))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("converted stream failed: %v", err)
	}
	if !strings.Contains(string(out), `"content":"hel"`) || !strings.Contains(string(out), `"content":"lo"`) || !strings.Contains(string(out), `"finish_reason":"stop"`) {
		t.Fatalf("converted stream = %q", out)
	}
}

func anthropicResponseOfSize(size int) string {
	prefix := `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"`
	suffix := `"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	return prefix + strings.Repeat("a", size-len(prefix)-len(suffix)) + suffix
}

func TestBufferedConversionIsBounded(t *testing.T) {
	const limit = 4096
	exact := anthropicResponseOfSize(limit)
	executor := &Executor{Kind: models.ProviderAnthropic, Next: fixedExecutor{status: http.StatusOK, body: exact}, MaxBufferedBytes: limit}
	response, err := executor.Do(context.Background(), chatRequestFor(false))
	if err != nil {
		t.Fatalf("a response exactly at the limit failed: %v", err)
	}
	response.Body.Close()

	executor.Next = fixedExecutor{status: http.StatusOK, body: anthropicResponseOfSize(limit + 1)}
	_, err = executor.Do(context.Background(), chatRequestFor(false))
	if !errors.Is(err, providers.ErrResponseTooLarge) || !errors.Is(err, providers.ErrUpstreamUnavailable) {
		t.Fatalf("oversized response error = %v; want ErrResponseTooLarge", err)
	}
	if strings.Contains(err.Error(), "aaaa") {
		t.Fatal("the oversized body leaked into the error")
	}
}

// endlessReader never ends; reading it fully would never return.
type endlessReader struct{ read int64 }

func (r *endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	r.read += int64(len(p))
	return len(p), nil
}

func TestBufferedConversionStopsReadingPastTheLimit(t *testing.T) {
	source := &endlessReader{}
	if _, err := readBounded(source, 1<<20); !errors.Is(err, providers.ErrResponseTooLarge) {
		t.Fatalf("error = %v; want ErrResponseTooLarge", err)
	}
	if source.read > 2<<20 {
		t.Fatalf("read %d bytes from an endless body; want about the limit", source.read)
	}
}
