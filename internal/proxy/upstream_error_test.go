package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nextroad-dev/Auto-Router/internal/logging"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
	"github.com/nextroad-dev/Auto-Router/internal/router/auto"
)

type attemptRecorder struct {
	eventRecorder
	attempts []logging.Attempt
}

func (r *attemptRecorder) RecordAttempt(attempt logging.Attempt) {
	r.attempts = append(r.attempts, attempt)
}

func TestUpstreamErrorDetailReadsEnvelopes(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"openai", `{"error":{"message":"Unsupported parameter: 'reasoning_effort'","type":"invalid_request_error","param":"reasoning_effort","code":"unsupported_parameter"}}`,
			"Unsupported parameter: 'reasoning_effort' (code=unsupported_parameter, param=reasoning_effort)"},
		{"openai type only", `{"error":{"message":"bad","type":"invalid_request_error"}}`, "bad (type=invalid_request_error)"},
		{"gemini numeric code", `{"error":{"code":400,"message":"Invalid argument","status":"INVALID_ARGUMENT"}}`, "Invalid argument (code=400)"},
		{"string error", `{"error":"model not found"}`, "model not found"},
		{"message", `{"message":"quota exceeded"}`, "quota exceeded"},
		{"detail", `{"detail":"not allowed"}`, "not allowed"},
		{"sse framed", "event: error\ndata: {\"error\":{\"message\":\"stream refused\"}}\n\n", "stream refused"},
		{"plain text", "Bad Request\r\n", "Bad Request"},
		{"empty", "  ", ""},
	}
	for _, tc := range cases {
		if got := upstreamErrorDetail([]byte(tc.body)); got != tc.want {
			t.Errorf("%s: detail = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestUpstreamErrorDetailRedactsAndBounds(t *testing.T) {
	body := `{"error":{"message":"Incorrect API key provided: sk-abcdefghijklmnopqrstuvwxyz0123. See https://example.com/keys"}}`
	got := upstreamErrorDetail([]byte(body))
	if strings.Contains(got, "sk-abcdef") || strings.Contains(got, "example.com") {
		t.Fatalf("detail was not redacted: %q", got)
	}
	long := `{"error":{"message":"` + strings.Repeat("é", 2000) + `"}}`
	got = upstreamErrorDetail([]byte(long))
	if len(got) > logging.MaxAttemptErrorDetailBytes || !utf8.ValidString(got) {
		t.Fatalf("detail is %d bytes (valid UTF-8: %v), want at most %d", len(got), utf8.ValidString(got), logging.MaxAttemptErrorDetailBytes)
	}
}

func TestErrorCaptureIsBounded(t *testing.T) {
	var capture errorCapture
	capture.observe([]byte(strings.Repeat("a", maxUpstreamErrorCaptureBytes-1)))
	capture.observe([]byte("bcd"))
	if len(capture.buffer) != maxUpstreamErrorCaptureBytes {
		t.Fatalf("captured %d bytes, want %d", len(capture.buffer), maxUpstreamErrorCaptureBytes)
	}
	var none *errorCapture
	none.observe([]byte("ignored"))
	if none.detail() != "" {
		t.Fatal("a nil capture reported a detail")
	}
}

func TestRelayedUpstreamErrorIsRecordedOnTheAttempt(t *testing.T) {
	errorBody := `{"error":{"message":"Unsupported parameter: 'reasoning_effort'","param":"reasoning_effort","code":"unsupported_parameter"}}`
	executor := &responseSequenceExecutor{responses: []*providers.Response{
		{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(errorBody))},
	}}
	autoRouter := &statusFailoverRouter{
		first: auto.Target{ProviderKey: "p1", Model: "m1", UpstreamModel: "m1", SelectedGroup: auto.GroupSimple, Attempts: 1, MaxAttempts: 2},
	}
	recorder := &attemptRecorder{}
	handler := New(Options{ProviderConfigured: true, Executor: executor, AutoRouter: autoRouter, Recorder: recorder})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || response.Body.String() != errorBody {
		t.Fatalf("client response = %d %q, want the upstream error unchanged", response.Code, response.Body.String())
	}
	if len(recorder.attempts) != 1 {
		t.Fatalf("recorded %d attempts, want 1", len(recorder.attempts))
	}
	want := "Unsupported parameter: 'reasoning_effort' (code=unsupported_parameter, param=reasoning_effort)"
	if got := recorder.attempts[0].ErrorDetail; got != want {
		t.Fatalf("attempt error detail = %q, want %q", got, want)
	}
}

func TestDiscardedFailoverErrorIsRecordedOnTheAttempt(t *testing.T) {
	executor := &responseSequenceExecutor{responses: []*providers.Response{
		{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"overloaded"}}`))},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"second"}`))},
	}}
	autoRouter := &statusFailoverRouter{
		first: auto.Target{ProviderKey: "p1", Model: "m1", UpstreamModel: "m1", SelectedGroup: auto.GroupSimple, Attempts: 1, MaxAttempts: 2},
		next:  auto.Target{ProviderKey: "p2", Model: "m2", UpstreamModel: "m2", SelectedGroup: auto.GroupSimple, Attempts: 2, MaxAttempts: 2},
	}
	recorder := &attemptRecorder{}
	handler := New(Options{ProviderConfigured: true, Executor: executor, AutoRouter: autoRouter, Recorder: recorder})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if len(recorder.attempts) != 2 {
		t.Fatalf("recorded %d attempts, want 2", len(recorder.attempts))
	}
	if got := recorder.attempts[0].ErrorDetail; got != "overloaded" {
		t.Fatalf("failed attempt detail = %q, want %q", got, "overloaded")
	}
	if got := recorder.attempts[1].ErrorDetail; got != "" {
		t.Fatalf("successful attempt detail = %q, want empty", got)
	}
}
