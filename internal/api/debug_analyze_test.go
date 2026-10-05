package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nextroad-dev/Auto-Router/internal/router/analyzer"
)

func TestDebugAnalyzeExposesStructuralFeaturesOnly(t *testing.T) {
	handler := &debugAnalyzer{analyzer: analyzer.New(), maxBytes: 1 << 20}
	request := httptest.NewRequest(http.MethodPost, "/debug/analyze", strings.NewReader(`{
		"protocol":"responses",
		"body":{"input":"Think step by step about this code: func main() {}", "reasoning":{"effort":"high"}, "max_output_tokens":512}
	}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Features map[string]json.RawMessage `json:"features"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{"has_code", "reasoning_likely"} {
		if _, exists := payload.Features[removed]; exists {
			t.Fatalf("removed heuristic %s is still exposed", removed)
		}
	}
	if string(payload.Features["message_count"]) != "1" || string(payload.Features["max_output_tokens_requested"]) != "512" {
		t.Fatalf("structural features were lost: %s", response.Body.String())
	}
}
