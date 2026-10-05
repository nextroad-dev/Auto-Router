package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nextroad-dev/Auto-Router/internal/logging"
	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
)

func TestNativeUsageObserverReadsLargeBodies(t *testing.T) {
	for name, usage := range map[string]string{
		"anthropic": `"usage":{"input_tokens":7,"output_tokens":8,"total_tokens":15}`,
		"gemini":    `"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":8,"totalTokenCount":15}`,
	} {
		t.Run(name, func(t *testing.T) {
			observer := newNativeUsageObserver("application/json", false)
			body := `{"content":[{"text":"` + strings.Repeat("x", 2<<20) + `"}],` + usage + `}`
			feedInPieces(observer, body, 32<<10)
			requireUsage(t, observer.finish(false), 7, 8, 15)
		})
	}
}

func TestNativeUsageObserverReadsLargeEvents(t *testing.T) {
	for name, payload := range map[string]string{
		"anthropic": `{"message":{"content":[{"text":"` + strings.Repeat("x", 2<<20) + `"}],"usage":{"input_tokens":7,"output_tokens":8,"total_tokens":15}}}`,
		"gemini":    `{"candidates":[{"text":"` + strings.Repeat("x", 2<<20) + `"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":8,"totalTokenCount":15}}`,
	} {
		for _, newline := range []string{"\n", "\r\n"} {
			for _, size := range []int{7, 32 << 10} {
				t.Run(fmt.Sprintf("%s/newline=%q/chunk=%d", name, newline, size), func(t *testing.T) {
					observer := newNativeUsageObserver("text/event-stream", false)
					feedInPieces(observer, "data: "+payload+newline+newline, size)
					requireUsage(t, observer.finish(false), 7, 8, 15)
				})
			}
		}
	}
}

func TestNativeUsageObserverMergesAnthropicStreamCounts(t *testing.T) {
	observer := newNativeUsageObserver("application/json", true)
	body := `event: message_start` + "\n" + `data: {"message":{"usage":{"input_tokens":12,"output_tokens":0}}}` + "\n\n" +
		`event: message_delta` + "\n" + `data: {"usage":{"output_tokens":34}}` + "\n\n"
	feedInPieces(observer, body, 7)
	got := observer.finish(false)
	if got.Status != logging.UsageStatusObserved || got.InputTokens == nil || *got.InputTokens != 12 || got.OutputTokens == nil || *got.OutputTokens != 34 {
		t.Fatalf("stream counts were lost: %+v", got)
	}
	if got.TotalTokens != nil {
		t.Fatal("a total absent from upstream must not be synthesized")
	}
}

func TestNativeUsageObserverReadsCRLFStreams(t *testing.T) {
	observer := newNativeUsageObserver("application/json", true)
	stream := `data: {"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":6,"totalTokenCount":11}}` + "\r\n\r\n" +
		`data: [DONE]` + "\r\n\r\n"
	feedInPieces(observer, stream, 1)
	requireUsage(t, observer.finish(false), 5, 6, 11)
}

func TestNativeUsageObserverPreservesCountsOnInterruption(t *testing.T) {
	observer := newNativeUsageObserver("application/json", true)
	observer.observe([]byte(`data: {"usage":{"input_tokens":12}}` + "\n\n"))
	got := observer.finish(true)
	if got.Status != logging.UsageStatusObserved || got.InputTokens == nil || *got.InputTokens != 12 {
		t.Fatalf("reported count was discarded on interruption: %+v", got)
	}
}

func TestNativeUsageObserverIgnoresUsageInContent(t *testing.T) {
	body := `{"content":[{"usage":{"input_tokens":999},"usageMetadata":{"promptTokenCount":999},"text":"\"usage\":{\"input_tokens\":999}` + strings.Repeat("x", 2<<20) + `"}]}`
	observer := newNativeUsageObserver("application/json", false)
	feedInPieces(observer, body, 32<<10)
	if got := observer.finish(false); got.Status != logging.UsageStatusAbsent {
		t.Fatalf("content was mistaken for usage: %+v", got)
	}
}

func TestNativeUsageObserverResumesAfterLargeEvent(t *testing.T) {
	observer := newNativeUsageObserver("text/event-stream", true)
	body := `data: {"delta":{"text":"` + strings.Repeat("x", 2<<20) + `"}}` + "\n\n" +
		`data: {"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":8,"totalTokenCount":15}}` + "\n\n"
	feedInPieces(observer, body, 32<<10)
	requireUsage(t, observer.finish(false), 7, 8, 15)
}

func TestNativeUsageObserverRetainsUsageMetadataBound(t *testing.T) {
	observer := newNativeUsageObserver("application/json", false)
	body := `{"text":"` + strings.Repeat("x", 2<<20) + `","usageMetadata":{"promptTokenCount":7,"pad":"` + strings.Repeat("x", 8<<10) + `"}}`
	feedInPieces(observer, body, 32<<10)
	if got := observer.finish(false); got.Status != logging.UsageStatusOversized {
		t.Fatalf("status = %q, want oversized metadata", got.Status)
	}
}

func TestNativeRelayLogsLargeResponsesWithoutChangingBytes(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			body := `{"content":[{"text":"` + strings.Repeat("x", 2<<20) + `"}],"usage":{"input_tokens":7,"output_tokens":8,"total_tokens":15}}`
			contentType := "application/json"
			if stream {
				body = "data: " + body + "\r\n\r\n"
				contentType = "text/event-stream"
			}
			executor := &responseSequenceExecutor{responses: []*providers.Response{{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body)),
			}}}
			recorder := &eventRecorder{}
			handler := New(Options{
				Catalog:  singleProviderStore(t, models.ProviderAnthropic, "https://a.example"),
				Executor: executor, Recorder: recorder,
			})
			requestBody := fmt.Sprintf(`{"model":"model-a","max_tokens":8,"messages":[{"role":"user","content":"hi"}],"stream":%t}`, stream)
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(requestBody))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeAnthropicMessages(response, request)
			if response.Code != http.StatusOK || response.Body.String() != body {
				t.Fatal("the relay changed the upstream response")
			}
			if len(recorder.events) != 1 {
				t.Fatalf("recorded events = %d, want 1", len(recorder.events))
			}
			requireUsage(t, recorder.events[0].Usage, 7, 8, 15)
			if err := recorder.events[0].Valid(); err != nil {
				t.Fatalf("invalid routing event: %v", err)
			}
		})
	}
}
