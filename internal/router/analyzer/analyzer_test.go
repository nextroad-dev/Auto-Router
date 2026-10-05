package analyzer

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/nextroad-dev/Auto-Router/internal/providers"
)

func analyzeFixture(t *testing.T, protocol providers.Protocol, name string) Result {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(body)
	result, err := Analyze(Input{Protocol: protocol, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, original) {
		t.Fatal("analysis changed the client request")
	}
	return result
}

func TestCodeAndReasoningTextRemainsOrdinaryConversation(t *testing.T) {
	result := analyzeFixture(t, providers.ProtocolChatCompletions, "chat_code_and_reasoning.json")
	if result.Features.MessageCount != 3 || !result.Features.TextOnly || !result.Features.StreamRequested {
		t.Fatalf("unexpected structural features: %+v", result.Features)
	}
	if result.Features.UnrecognizedParts != 0 || result.Features.Truncated {
		t.Fatalf("reasoning parameters must not make analysis incomplete: %+v", result.Features)
	}
	view, err := json.Marshal(result.View)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(view), "func add") || !strings.Contains(string(view), "prove that the diff") {
		t.Fatalf("ordinary client text was lost: %s", view)
	}
	if strings.Contains(string(view), "The user wants a refactor.") {
		t.Fatal("internal reasoning content entered the conversation view")
	}
}

func TestResponsesRetainsStructuralToolFeaturesWithoutReasoningContent(t *testing.T) {
	result := analyzeFixture(t, providers.ProtocolResponses, "responses_items.json")
	f := result.Features
	if f.MessageCount != 7 || !f.HasTools || f.ToolCount != 3 || !f.ForcedToolChoice || !f.ChainedToolUse || !f.FileSearchUsed {
		t.Fatalf("tool structures no longer contribute to routing: %+v", f)
	}
	if !reflect.DeepEqual(f.ToolNames, []string{"file_search", "internal_docs", "lookup"}) {
		t.Fatalf("unexpected names: %v", f.ToolNames)
	}
	if f.MaxOutputTokensRequested == nil || *f.MaxOutputTokensRequested != 512 || !f.StreamRequested {
		t.Fatalf("output/stream controls were lost: %+v", f)
	}
	if f.RoleCounts["other"] != 2 || f.UnrecognizedParts != 0 {
		t.Fatalf("reasoning/retrieval protocol items were incorrectly classified: %+v", f)
	}
	view, err := json.Marshal(result.View)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(view), "Weighing options.") || strings.Contains(string(view), "release notes") {
		t.Fatalf("internal protocol contents entered the conversation view: %s", view)
	}
}

func TestInlineMediaPayloadDoesNotEnterFeaturesOrView(t *testing.T) {
	result := analyzeFixture(t, providers.ProtocolChatCompletions, "chat_multimodal_redaction.json")
	if result.Features.ImageCount == 0 || !result.Features.HasBase64Image {
		t.Fatalf("media structure was lost: %+v", result.Features)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "CANARY-BASE64-PAYLOAD") {
		t.Fatal("inline media payload entered routing analysis")
	}
}
