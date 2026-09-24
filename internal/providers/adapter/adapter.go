// Package adapter translates the supported OpenAI chat subset to native
// Anthropic Messages and Gemini generateContent requests and back.
package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
)

// Executor adapts the OpenAI chat-completions subset for a native provider.
// OpenAI and OpenAI-compatible providers pass through byte-for-byte.
type Executor struct {
	Kind models.ProviderKind
	Next providers.Executor
}

// CloseIdleConnections forwards pool cleanup when the wrapped executor supports it.
func (e *Executor) CloseIdleConnections() {
	if closer, ok := e.Next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func New(kind models.ProviderKind, next providers.Executor) (*Executor, error) {
	if next == nil {
		return nil, errors.New("adapter requires an executor")
	}
	if !kind.Valid() {
		return nil, fmt.Errorf("unknown provider kind %q", kind)
	}
	return &Executor{Kind: kind, Next: next}, nil
}

func (e *Executor) Do(ctx context.Context, request *providers.Request) (*providers.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("%w: nil request", providers.ErrUnsupportedConversion)
	}
	if e.Kind == models.ProviderOpenAI || e.Kind == models.ProviderOpenAICompatible {
		if request.Protocol != providers.ProtocolChatCompletions && request.Protocol != providers.ProtocolResponses {
			return nil, fmt.Errorf("%w: native request does not match OpenAI provider", providers.ErrUnsupportedConversion)
		}
		return e.Next.Do(ctx, request)
	}
	if e.Kind == models.ProviderAnthropic && request.Protocol == providers.ProtocolAnthropicMessages {
		return e.Next.Do(ctx, request)
	}
	if e.Kind == models.ProviderGemini && (request.Protocol == providers.ProtocolGeminiGenerateContent || request.Protocol == providers.ProtocolGeminiStreamGenerateContent) {
		return e.Next.Do(ctx, request)
	}
	if request.Protocol != providers.ProtocolChatCompletions {
		return nil, fmt.Errorf("%w: %s cannot be converted to %s", providers.ErrUnsupportedConversion, request.Protocol, e.Kind)
	}
	body, endpoint, err := convertRequest(e.Kind, request)
	if err != nil {
		return nil, err
	}
	upstream := *request
	upstream.Body = body
	upstream.UpstreamPath = endpoint
	upstream.UpstreamQuery = request.UpstreamQuery
	if e.Kind == models.ProviderGemini && request.Stream {
		upstream.UpstreamQuery = "alt=sse"
	}
	response, err := e.Next.Do(ctx, &upstream)
	if err != nil || response == nil {
		return response, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response, nil
	}
	if request.Stream {
		response.Header = response.Header.Clone()
		response.Header.Set("Content-Type", "text/event-stream")
		response.Header.Del("Content-Length")
		response.Body = newStreamBody(response.Body, e.Kind, request.RequestID, request.Model)
		return response, nil
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: read converted response: %v", providers.ErrUpstreamUnavailable, err)
	}
	converted, err := convertResponse(e.Kind, raw, request.RequestID, request.Model)
	if err != nil {
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(converted))
	response.Header = response.Header.Clone()
	response.Header.Set("Content-Type", "application/json")
	response.Header.Del("Content-Length")
	return response, nil
}

type chatRequest struct {
	Model               string          `json:"model"`
	Messages            []chatMessage   `json:"messages"`
	MaxTokens           *int            `json:"max_tokens"`
	MaxCompletionTokens *int            `json:"max_completion_tokens"`
	Temperature         *float64        `json:"temperature"`
	TopP                *float64        `json:"top_p"`
	Stop                json.RawMessage `json:"stop"`
	Tools               json.RawMessage `json:"tools"`
	ToolChoice          json.RawMessage `json:"tool_choice"`
	ResponseFormat      json.RawMessage `json:"response_format"`
}
type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func convertRequest(kind models.ProviderKind, request *providers.Request) ([]byte, string, error) {
	var in chatRequest
	if err := json.Unmarshal(request.Body, &in); err != nil {
		return nil, "", conversionError("invalid OpenAI chat request JSON")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(request.Body, &fields); err != nil {
		return nil, "", conversionError("invalid OpenAI chat request JSON")
	}
	allowed := map[string]bool{"model": true, "messages": true, "max_tokens": true, "max_completion_tokens": true, "temperature": true, "top_p": true, "stream": true, "stop": true, "tools": true, "tool_choice": true, "response_format": true}
	for key := range fields {
		if !allowed[key] {
			return nil, "", conversionError("unsupported request field %q", key)
		}
	}
	if len(in.Messages) == 0 {
		return nil, "", conversionError("messages must not be empty")
	}
	if len(in.Tools) > 0 && string(in.Tools) != "null" && string(in.Tools) != "[]" {
		return nil, "", conversionError("tool calls are not yet representable")
	}
	if len(in.ToolChoice) > 0 && string(in.ToolChoice) != "null" && string(in.ToolChoice) != "\"none\"" {
		return nil, "", conversionError("tool_choice is not representable")
	}
	if len(in.ResponseFormat) > 0 && string(in.ResponseFormat) != "null" {
		return nil, "", conversionError("response_format is not representable")
	}
	if in.MaxTokens == nil {
		in.MaxTokens = in.MaxCompletionTokens
	}
	if kind == models.ProviderAnthropic && in.MaxTokens == nil {
		return nil, "", conversionError("Anthropic requires max_tokens")
	}
	switch kind {
	case models.ProviderAnthropic:
		messages := make([]map[string]string, 0, len(in.Messages))
		system := ""
		for _, m := range in.Messages {
			text, err := textContent(m.Content)
			if err != nil {
				return nil, "", err
			}
			switch m.Role {
			case "system", "developer":
				if system != "" {
					system += "\n"
				}
				system += text
			case "user", "assistant":
				messages = append(messages, map[string]string{"role": m.Role, "content": text})
			default:
				return nil, "", conversionError("role %q is not supported", m.Role)
			}
		}
		payload := map[string]any{"model": request.Model, "max_tokens": *in.MaxTokens, "messages": messages, "stream": request.Stream}
		if system != "" {
			payload["system"] = system
		}
		if in.Temperature != nil {
			payload["temperature"] = *in.Temperature
		}
		if in.TopP != nil {
			payload["top_p"] = *in.TopP
		}
		if len(in.Stop) > 0 {
			stop, err := parseStop(in.Stop)
			if err != nil {
				return nil, "", err
			}
			payload["stop_sequences"] = stop
		}
		b, err := json.Marshal(payload)
		return b, "/v1/messages", err
	case models.ProviderGemini:
		contents := make([]map[string]any, 0, len(in.Messages))
		systemParts := []map[string]string{}
		for _, m := range in.Messages {
			text, err := textContent(m.Content)
			if err != nil {
				return nil, "", err
			}
			switch m.Role {
			case "system", "developer":
				systemParts = append(systemParts, map[string]string{"text": text})
			case "user":
				contents = append(contents, map[string]any{"role": "user", "parts": []map[string]string{{"text": text}}})
			case "assistant":
				contents = append(contents, map[string]any{"role": "model", "parts": []map[string]string{{"text": text}}})
			default:
				return nil, "", conversionError("role %q is not supported", m.Role)
			}
		}
		payload := map[string]any{"contents": contents}
		if len(systemParts) > 0 {
			payload["systemInstruction"] = map[string]any{"parts": systemParts}
		}
		generation := map[string]any{}
		if in.MaxTokens != nil {
			generation["maxOutputTokens"] = *in.MaxTokens
		}
		if in.Temperature != nil {
			generation["temperature"] = *in.Temperature
		}
		if in.TopP != nil {
			generation["topP"] = *in.TopP
		}
		if len(in.Stop) > 0 {
			stop, err := parseStop(in.Stop)
			if err != nil {
				return nil, "", err
			}
			generation["stopSequences"] = stop
		}
		if len(generation) > 0 {
			payload["generationConfig"] = generation
		}
		b, err := json.Marshal(payload)
		method := "generateContent"
		if request.Stream {
			method = "streamGenerateContent"
		}
		endpoint := "/v1beta/models/" + url.PathEscape(request.Model) + ":" + method
		return b, endpoint, err
	default:
		return nil, "", conversionError("provider kind %q cannot be adapted", kind)
	}
}

func parseStop(raw json.RawMessage) ([]string, error) {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return []string{single}, nil
	}
	var multiple []string
	if json.Unmarshal(raw, &multiple) == nil {
		return multiple, nil
	}
	return nil, conversionError("stop must be a string or an array of strings")
}

func textContent(raw json.RawMessage) (string, error) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", conversionError("content blocks, images, audio, and other non-text modalities are unsupported")
	}
	return s, nil
}
func conversionError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", providers.ErrUnsupportedConversion, fmt.Sprintf(format, args...))
}

func convertResponse(kind models.ProviderKind, raw []byte, id, model string) ([]byte, error) {
	var out map[string]any
	switch kind {
	case models.ProviderAnthropic:
		var v struct {
			ID      string `json:"id"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			StopReason string `json:"stop_reason"`
			Usage      struct {
				Input  int `json:"input_tokens"`
				Output int `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(raw, &v) != nil {
			return nil, conversionError("invalid Anthropic response")
		}
		text := ""
		for _, c := range v.Content {
			if c.Type != "text" {
				return nil, conversionError("Anthropic response contains unsupported content block %q", c.Type)
			}
			text += c.Text
		}
		finish := "stop"
		if v.StopReason == "max_tokens" {
			finish = "length"
		}
		if v.StopReason == "tool_use" {
			return nil, conversionError("Anthropic tool response cannot be represented")
		}
		if v.ID != "" {
			id = v.ID
		}
		out = chatResponse(id, model, text, finish, v.Usage.Input, v.Usage.Output)
	case models.ProviderGemini:
		var v struct {
			Candidates []struct {
				Content struct {
					Parts []map[string]json.RawMessage `json:"parts"`
				} `json:"content"`
				Finish string `json:"finishReason"`
			} `json:"candidates"`
			Usage struct {
				Prompt int `json:"promptTokenCount"`
				Output int `json:"candidatesTokenCount"`
			} `json:"usageMetadata"`
		}
		if json.Unmarshal(raw, &v) != nil || len(v.Candidates) == 0 {
			return nil, conversionError("invalid Gemini response")
		}
		text := ""
		for _, p := range v.Candidates[0].Content.Parts {
			part, ok := p["text"]
			if !ok || len(p) != 1 {
				return nil, conversionError("Gemini response contains a non-text content part")
			}
			var value string
			if json.Unmarshal(part, &value) != nil {
				return nil, conversionError("invalid Gemini text response part")
			}
			text += value
		}
		finish := "stop"
		if v.Candidates[0].Finish == "MAX_TOKENS" {
			finish = "length"
		} else if v.Candidates[0].Finish != "STOP" && v.Candidates[0].Finish != "" {
			return nil, conversionError("Gemini finish reason %q cannot be represented", v.Candidates[0].Finish)
		}
		out = chatResponse(id, model, text, finish, v.Usage.Prompt, v.Usage.Output)
	default:
		return raw, nil
	}
	return json.Marshal(out)
}
func chatResponse(id, model, text, finish string, input, output int) map[string]any {
	if id == "" {
		id = "chatcmpl-adapted"
	}
	return map[string]any{"id": id, "object": "chat.completion", "created": 0, "model": model, "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": finish}}, "usage": map[string]int{"prompt_tokens": input, "completion_tokens": output, "total_tokens": input + output}}
}

type streamBody struct {
	source io.ReadCloser
	reader *io.PipeReader
	writer *io.PipeWriter
}

func newStreamBody(source io.ReadCloser, kind models.ProviderKind, id, model string) *streamBody {
	r, w := io.Pipe()
	b := &streamBody{source: source, reader: r, writer: w}
	go b.convert(kind, id, model)
	return b
}
func (b *streamBody) Read(p []byte) (int, error) { return b.reader.Read(p) }
func (b *streamBody) Close() error {
	_ = b.source.Close()
	_ = b.reader.Close()
	return b.writer.Close()
}
func (b *streamBody) convert(kind models.ProviderKind, id, model string) {
	defer b.source.Close()
	defer b.writer.Close()
	scanner := bufio.NewScanner(b.source)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	var data strings.Builder
	inputTokens := 0
	flush := func() error {
		raw := strings.TrimSpace(data.String())
		data.Reset()
		if raw == "" || raw == "[DONE]" {
			return nil
		}
		var chunk map[string]any
		if kind == models.ProviderAnthropic {
			var ev struct {
				Type  string `json:"type"`
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
					Stop string `json:"stop_reason"`
				} `json:"delta"`
				Message struct {
					ID    string `json:"id"`
					Usage struct {
						Input int `json:"input_tokens"`
					} `json:"usage"`
				} `json:"message"`
				Usage struct {
					Output int `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal([]byte(raw), &ev) != nil {
				return conversionError("invalid Anthropic stream event")
			}
			switch ev.Type {
			case "message_start":
				if ev.Message.ID != "" {
					id = ev.Message.ID
				}
				inputTokens = ev.Message.Usage.Input
				chunk = streamChunk(id, model, map[string]any{"role": "assistant"}, nil, 0, 0)
			case "content_block_delta":
				if ev.Delta.Type == "text_delta" {
					chunk = streamChunk(id, model, map[string]any{"content": ev.Delta.Text}, nil, 0, 0)
				}
			case "message_delta":
				finish := "stop"
				if ev.Delta.Stop == "max_tokens" {
					finish = "length"
				}
				chunk = streamChunk(id, model, map[string]any{}, finish, inputTokens, ev.Usage.Output)
			case "message_stop":
				_, _ = io.WriteString(b.writer, "data: [DONE]\n\n")
				return nil
			}
		} else {
			var ev struct {
				Candidates []struct {
					Content struct {
						Parts []map[string]json.RawMessage `json:"parts"`
					} `json:"content"`
					Finish string `json:"finishReason"`
				} `json:"candidates"`
				Usage struct {
					Input  int `json:"promptTokenCount"`
					Output int `json:"candidatesTokenCount"`
				} `json:"usageMetadata"`
			}
			if json.Unmarshal([]byte(raw), &ev) != nil {
				return conversionError("invalid Gemini stream event")
			}
			if len(ev.Candidates) > 0 {
				c := ev.Candidates[0]
				text := ""
				for _, p := range c.Content.Parts {
					part, ok := p["text"]
					if !ok || len(p) != 1 {
						return conversionError("Gemini stream contains a non-text content part")
					}
					var value string
					if json.Unmarshal(part, &value) != nil {
						return conversionError("invalid Gemini stream text part")
					}
					text += value
				}
				var finish any
				if c.Finish != "" {
					finish = "stop"
					if c.Finish == "MAX_TOKENS" {
						finish = "length"
					}
				}
				chunk = streamChunk(id, model, map[string]any{"content": text}, finish, ev.Usage.Input, ev.Usage.Output)
			}
		}
		if chunk != nil {
			encoded, err := json.Marshal(chunk)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(b.writer, "data: %s\n\n", encoded)
			return err
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				b.writer.CloseWithError(err)
				return
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		b.writer.CloseWithError(err)
	}
}
func streamChunk(id, model string, delta map[string]any, finish any, input, output int) map[string]any {
	if id == "" {
		id = "chatcmpl-adapted"
	}
	choice := map[string]any{"index": 0, "delta": delta, "finish_reason": finish}
	chunk := map[string]any{"id": id, "object": "chat.completion.chunk", "created": 0, "model": model, "choices": []any{choice}}
	if input+output > 0 {
		chunk["usage"] = map[string]int{"prompt_tokens": input, "completion_tokens": output, "total_tokens": input + output}
	}
	return chunk
}

var _ providers.Executor = (*Executor)(nil)
