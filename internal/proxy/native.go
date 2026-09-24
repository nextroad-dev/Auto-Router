package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/logging"
	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
	"github.com/nextroad-dev/Auto-Router/internal/router/auto"
)

// These protocol identifiers are intentionally local to the proxy boundary.
// The executor receives an explicit upstream path and the native body unchanged.
const (
	protocolAnthropicMessages providers.Protocol = providers.ProtocolAnthropicMessages
	protocolGeminiGenerate    providers.Protocol = providers.ProtocolGeminiGenerateContent
	protocolGeminiStream      providers.Protocol = providers.ProtocolGeminiStreamGenerateContent
)

const maxNativeObservedResponseBytes = 1 << 20
const maxNativeAutoInputBytes = 2 << 20

// ServeAnthropicMessages mounts the native Anthropic Messages ingress.
func (h *Handler) ServeAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	requestID := RequestIDFor(r)
	w.Header().Set(RequestIDHeader, requestID)
	h.native(w, r, protocolAnthropicMessages, requestID, "", "/v1/messages")
}

// ServeGemini mounts the native Gemini generateContent ingress, including its
// streaming operation. The body and query are passed through unchanged.
func (h *Handler) ServeGemini(w http.ResponseWriter, r *http.Request) {
	requestID := RequestIDFor(r)
	w.Header().Set(RequestIDHeader, requestID)
	path := r.URL.EscapedPath()
	operation := ":generateContent"
	protocol := protocolGeminiGenerate
	if strings.HasSuffix(path, ":streamGenerateContent") {
		operation = ":streamGenerateContent"
		protocol = protocolGeminiStream
	} else if !strings.HasSuffix(path, operation) {
		http.NotFound(w, r)
		return
	}
	encodedModel := strings.TrimSuffix(strings.TrimPrefix(path, "/v1beta/models/"), operation)
	model, err := url.PathUnescape(encodedModel)
	if err != nil || model == "" || strings.Contains(model, "/") {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request_error", "a valid model path is required")
		return
	}
	upstreamPath, err := nativeGeminiPath(protocol, model)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request_error", "a valid model path is required")
		return
	}
	h.native(w, r, protocol, requestID, model, upstreamPath)
}

// native handles a request whose payload already uses a provider-native API.
// Native payloads are never converted to another protocol: a mismatched provider
// returns a stable client error before credentials or request data reach upstream.
func (h *Handler) native(w http.ResponseWriter, r *http.Request, protocol providers.Protocol, requestID, requestedModel, upstreamPath string) {
	if h.live != nil {
		r = r.WithContext(context.WithValue(r.Context(), liveSnapshotContextKey{}, h.live.Snapshot()))
	}
	started := time.Now()
	record := &requestLog{
		RequestID: requestID, Protocol: "native", protocol: protocol,
		ClientIP: clientIP(r), RequestedModel: requestedModel,
		RoutingMode: routingModeExplicit,
	}
	defer func() {
		record.emit(h.logger, started)
		h.recordEvent(r, record, started, time.Since(started))
	}()

	w.Header().Set(RequestIDHeader, requestID)
	if !h.providerConfigured || h.executor == nil {
		record.fail(http.StatusServiceUnavailable, "provider_not_configured")
		writeError(w, http.StatusServiceUnavailable, "api_error", "provider_not_configured", "no direct provider executor is configured")
		return
	}
	if contentType, ok := requestMediaType(r); !ok {
		record.fail(http.StatusUnsupportedMediaType, "unsupported_media_type")
		writeError(w, http.StatusUnsupportedMediaType, "invalid_request_error", "unsupported_media_type", contentType)
		return
	}
	if strings.Contains(r.URL.Path, "/v1beta/models/") {
		query := r.URL.Query()
		for key := range query {
			if key != "alt" || len(query[key]) != 1 || query.Get(key) != "sse" {
				record.fail(http.StatusUnprocessableEntity, "unsupported_conversion")
				writeError(w, http.StatusUnprocessableEntity, "invalid_request_error", "unsupported_conversion", "the native Gemini URL contains an unsupported query parameter")
				return
			}
		}
	}
	body, err := readBody(r, h.maxRequestBytes)
	if err != nil {
		var tooLarge *bodyTooLargeError
		if errors.As(err, &tooLarge) {
			record.fail(http.StatusRequestEntityTooLarge, "request_too_large")
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large", fmt.Sprintf("request body exceeds the %d byte limit", h.maxRequestBytes))
			return
		}
		if clientGone(r, err) {
			record.canceled()
			return
		}
		record.fail(http.StatusBadRequest, "invalid_request_error")
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request_error", "the request body could not be read")
		return
	}
	if protocol == protocolAnthropicMessages {
		bodyModel, parseErr := nativeJSONModel(body)
		if parseErr != nil {
			record.fail(http.StatusBadRequest, "invalid_request_error")
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request_error", "the request body must be a JSON object with a model")
			return
		}
		if requestedModel == "" {
			requestedModel = bodyModel
			record.RequestedModel = requestedModel
		}
		if bodyModel != requestedModel && requestedModel != models.AutoModelID {
			record.fail(http.StatusBadRequest, "invalid_request_error")
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request_error", "the model in the URL and request body must match")
			return
		}
	}
	if requestedModel == "" {
		record.fail(http.StatusBadRequest, "invalid_request_error")
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request_error", "a model is required")
		return
	}

	expectedKind := models.ProviderAnthropic
	if protocol == protocolGeminiGenerate || protocol == protocolGeminiStream {
		expectedKind = models.ProviderGemini
	}
	var target Target
	group := ""
	autoTarget := auto.Target{}
	preference, preferenceSource, preferenceErr := parsePreferenceHeader(r.Header.Values(PreferenceHeader), h.currentPreference(r))
	r.Header.Del(PreferenceHeader)
	if preferenceErr != nil {
		record.fail(http.StatusBadRequest, "invalid_routing_preference")
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_routing_preference", preferenceErr.Error())
		return
	}
	record.RoutingPreference = string(preference)
	record.PreferenceSource = string(preferenceSource)
	if requestedModel == models.AutoModelID {
		record.RoutingMode = routingModeAuto
		if h.autoRouter == nil {
			record.fail(http.StatusServiceUnavailable, "routing_unavailable")
			writeError(w, http.StatusServiceUnavailable, "api_error", "routing_unavailable", "automatic routing is not available")
			return
		}
		snapshot := h.requestSettings(r)
		var runtime *auto.RuntimeSettings
		if snapshot.HasAutoSettings {
			copy := snapshot.AutoSettings
			copy.GroupConfig = nativeCompatibleGroups(copy.GroupConfig, h.catalog.Load(), expectedKind)
			runtime = &copy
		}
		autoBody, viewErr := nativeAutoView(protocol, body)
		if viewErr != nil {
			record.fail(http.StatusUnprocessableEntity, "unsupported_conversion")
			writeError(w, http.StatusUnprocessableEntity, "invalid_request_error", "unsupported_conversion", "automatic routing requires a bounded text-only native request")
			return
		}
		autoTarget, err = h.autoRouter.Route(r.Context(), auto.Request{
			Protocol: providers.ProtocolChatCompletions, Body: autoBody, Preference: preference, PreferenceSource: preferenceSource,
			RequestID: requestID, Runtime: runtime,
		})
		if err != nil {
			h.writeAutoError(w, record, err)
			return
		}
		group = string(autoTarget.SelectedGroup)
		target = targetFromAuto(h.catalog.Load(), autoTarget)
		record.applyRoutingDecision(autoTarget)
	} else {
		resolved, resolveErr := resolveNativeTarget(h.catalog.Load(), requestedModel, expectedKind, h.allowOverride(r))
		if resolveErr != nil {
			var targetErr *TargetError
			if errors.As(resolveErr, &targetErr) {
				record.fail(targetErr.Status, targetErr.Code)
				writeError(w, targetErr.Status, errorTypeForStatus(targetErr.Status), targetErr.Code, targetErr.Message)
				return
			}
			record.fail(http.StatusInternalServerError, "internal_error")
			writeError(w, http.StatusInternalServerError, "api_error", "internal_error", "the request could not be routed")
			return
		}
		target = resolved
		record.Provider, record.UpstreamModel = target.ProviderKey, target.UpstreamModel
		record.EffectiveModel, record.SelectionMode, record.GatewayAttempts = target.Pair.ModelID, "explicit", 1
	}

	stream := protocol == protocolGeminiStream || nativeStreamRequested(body) || r.URL.Query().Get("alt") == "sse"
	if stream && protocol == protocolGeminiGenerate {
		protocol = protocolGeminiStream
	}
	if protocol == protocolGeminiGenerate || protocol == protocolGeminiStream {
		expectedKind = models.ProviderGemini
	}
	if target.Provider.Kind != expectedKind {
		record.fail(http.StatusUnprocessableEntity, "unsupported_conversion")
		writeError(w, http.StatusUnprocessableEntity, "invalid_request_error", "unsupported_conversion", "the selected provider does not support this native API")
		return
	}
	if protocol == protocolAnthropicMessages && target.UpstreamModel != requestedModel {
		body, err = replaceNativeJSONModel(body, target.UpstreamModel)
		if err != nil {
			record.fail(http.StatusInternalServerError, "internal_error")
			writeError(w, http.StatusInternalServerError, "api_error", "internal_error", "the request could not be prepared")
			return
		}
	}
	if protocol == protocolGeminiGenerate || protocol == protocolGeminiStream {
		endpoint, err := nativeGeminiPath(protocol, target.UpstreamModel)
		if err != nil {
			record.fail(http.StatusInternalServerError, "internal_error")
			writeError(w, http.StatusInternalServerError, "api_error", "internal_error", "the request could not be prepared")
			return
		}
		upstreamPath = endpoint
	}
	record.stream = stream
	upstream := &providers.Request{
		Protocol: protocol, UpstreamPath: upstreamPath, ProviderKey: target.ProviderKey,
		Model: target.UpstreamModel, Body: body, Header: r.Header,
		ClientIP: record.ClientIP, RequestID: requestID, Stream: stream,
	}
	if protocol == protocolGeminiStream {
		upstream.UpstreamQuery = "alt=sse"
	}
	for attempt := 1; ; attempt++ {
		upstream.ProviderKey = target.ProviderKey
		upstream.Model = target.UpstreamModel
		if protocol == protocolAnthropicMessages && target.UpstreamModel != requestedModel {
			upstream.Body, err = replaceNativeJSONModel(body, target.UpstreamModel)
			if err != nil {
				record.fail(http.StatusInternalServerError, "internal_error")
				writeError(w, http.StatusInternalServerError, "api_error", "internal_error", "the request could not be prepared")
				return
			}
		} else {
			upstream.Body = body
		}
		if protocol == protocolGeminiGenerate || protocol == protocolGeminiStream {
			upstream.UpstreamPath, err = nativeGeminiPath(protocol, target.UpstreamModel)
			if err != nil {
				record.fail(http.StatusInternalServerError, "internal_error")
				writeError(w, http.StatusInternalServerError, "api_error", "internal_error", "the request could not be prepared")
				return
			}
		}
		attemptStarted := time.Now()
		response, callErr := h.executor.Do(r.Context(), upstream)
		if callErr != nil {
			code := executorErrorCode(callErr)
			h.recordAttempt(r, record, attempt, group, target.ProviderKey, target.Pair.ModelID, attemptStarted, 0, code, logging.Usage{})
			if errors.Is(callErr, providers.ErrUnsupportedConversion) {
				record.fail(http.StatusUnprocessableEntity, "unsupported_conversion")
				writeError(w, http.StatusUnprocessableEntity, "invalid_request_error", "unsupported_conversion", "the selected provider cannot preserve this native request")
				return
			}
			if requestedModel == models.AutoModelID && attempt < maxAutoUpstreamAttempts && errors.Is(callErr, providers.ErrPreRequestFailure) {
				next, retry := h.autoRouter.Failover(r.Context(), autoTarget, callErr)
				if retry {
					autoTarget = next
					target = targetFromAuto(h.catalog.Load(), autoTarget)
					if target.Provider.Kind != expectedKind {
						record.fail(http.StatusUnprocessableEntity, "unsupported_conversion")
						writeError(w, http.StatusUnprocessableEntity, "invalid_request_error", "unsupported_conversion", "the selected provider does not support this native API")
						return
					}
					record.FailoverUsed = true
					record.applyRoutingDecision(autoTarget)
					continue
				}
			}
			h.writeExecutorError(w, record, callErr)
			return
		}
		defer response.Body.Close()
		record.upstreamStatus = response.StatusCode
		h.relayNative(w, r, record, response)
		h.recordAttempt(r, record, attempt, group, target.ProviderKey, target.Pair.ModelID, attemptStarted, response.StatusCode, "", record.usage)
		return
	}
}

// Native ingress cannot safely transform an arbitrary native payload into a
// different vendor protocol. Filter group membership before Jev chooses a
// group, so a mixed-provider group still tries its first compatible member.
func nativeCompatibleGroups(groups auto.GroupConfig, catalog *models.Catalog, kind models.ProviderKind) auto.GroupConfig {
	filter := func(members []auto.GroupMember) []auto.GroupMember {
		out := make([]auto.GroupMember, 0, len(members))
		if catalog == nil {
			return out
		}
		for _, member := range members {
			provider, ok := catalog.Provider(member.ProviderKey)
			if ok && provider.Kind == kind {
				out = append(out, member)
			}
		}
		return out
	}
	return auto.GroupConfig{Simple: filter(groups.Simple), Medium: filter(groups.Medium), Complex: filter(groups.Complex)}
}

func resolveNativeTarget(catalog *models.Catalog, requested string, kind models.ProviderKind, allowOverride bool) (Target, error) {
	if strings.HasPrefix(requested, models.ProviderOverridePrefix()) {
		return ResolveTarget(catalog, requested, allowOverride)
	}
	if catalog == nil {
		return Target{}, &TargetError{Status: http.StatusNotFound, Code: "model_not_found", Message: "the requested model is not available"}
	}
	if _, ok := catalog.Lookup(requested); !ok {
		return ResolveTarget(catalog, requested, allowOverride)
	}
	for _, pair := range catalog.PairsForModel(requested) {
		provider, ok := catalog.Provider(pair.ProviderKey)
		if !ok || !provider.Enabled || provider.Kind != kind {
			continue
		}
		return Target{ProviderKey: provider.Key, Provider: provider, Pair: pair, UpstreamModel: pair.UpstreamModelID}, nil
	}
	return Target{}, &TargetError{Status: http.StatusUnprocessableEntity, Code: "unsupported_conversion", Message: "the requested model has no provider for this native API"}
}

// nativeAutoView reduces the two native request shapes to a bounded text-only
// OpenAI chat view for Jev. The original bytes remain the upstream payload.
// Non-text parts and tool definitions are refused because omitting them could
// lead Jev to choose a model that cannot serve the actual request.
func nativeAutoView(protocol providers.Protocol, body []byte) ([]byte, error) {
	if len(body) > maxNativeAutoInputBytes {
		return nil, errors.New("native auto input exceeds limit")
	}
	type chatMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	messages := make([]chatMessage, 0)
	var maxTokens *int
	stream := protocol == protocolGeminiStream
	if protocol == protocolAnthropicMessages {
		var input struct {
			System    json.RawMessage `json:"system"`
			MaxTokens *int            `json:"max_tokens"`
			Stream    bool            `json:"stream"`
			Messages  []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools      json.RawMessage `json:"tools"`
			ToolChoice json.RawMessage `json:"tool_choice"`
		}
		if json.Unmarshal(body, &input) != nil || len(input.Messages) == 0 || nativeHasValue(input.Tools) || nativeHasValue(input.ToolChoice) {
			return nil, errors.New("unsupported Anthropic auto request")
		}
		maxTokens, stream = input.MaxTokens, input.Stream
		if len(input.System) > 0 && string(input.System) != "null" {
			text, ok := nativeTextContent(input.System)
			if !ok {
				return nil, errors.New("non-text system content")
			}
			messages = append(messages, chatMessage{Role: "system", Content: text})
		}
		for _, message := range input.Messages {
			role := message.Role
			if role != "user" && role != "assistant" {
				return nil, errors.New("unsupported Anthropic role")
			}
			text, ok := nativeTextContent(message.Content)
			if !ok {
				return nil, errors.New("non-text Anthropic content")
			}
			messages = append(messages, chatMessage{Role: role, Content: text})
		}
	} else {
		var input struct {
			SystemInstruction *struct {
				Parts []struct {
					Text *string `json:"text"`
				} `json:"parts"`
			} `json:"systemInstruction"`
			Contents []struct {
				Role  string `json:"role"`
				Parts []struct {
					Text *string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
			Tools            json.RawMessage `json:"tools"`
			GenerationConfig *struct {
				MaxOutputTokens *int `json:"maxOutputTokens"`
			} `json:"generationConfig"`
		}
		if json.Unmarshal(body, &input) != nil || len(input.Contents) == 0 || nativeHasValue(input.Tools) {
			return nil, errors.New("unsupported Gemini auto request")
		}
		if input.GenerationConfig != nil {
			maxTokens = input.GenerationConfig.MaxOutputTokens
		}
		if input.SystemInstruction != nil {
			text, ok := nativeGeminiTextParts(input.SystemInstruction.Parts)
			if !ok {
				return nil, errors.New("non-text Gemini system instruction")
			}
			messages = append(messages, chatMessage{Role: "system", Content: text})
		}
		for _, content := range input.Contents {
			role := "user"
			if content.Role == "model" {
				role = "assistant"
			} else if content.Role != "" && content.Role != "user" {
				return nil, errors.New("unsupported Gemini role")
			}
			text, ok := nativeGeminiTextParts(content.Parts)
			if !ok {
				return nil, errors.New("non-text Gemini content")
			}
			messages = append(messages, chatMessage{Role: role, Content: text})
		}
	}
	return json.Marshal(struct {
		Model     string        `json:"model"`
		Messages  []chatMessage `json:"messages"`
		MaxTokens *int          `json:"max_tokens,omitempty"`
		Stream    bool          `json:"stream,omitempty"`
	}{Model: models.AutoModelID, Messages: messages, MaxTokens: maxTokens, Stream: stream})
}

func nativeHasValue(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null" && string(raw) != "[]"
}

func nativeTextContent(raw json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, true
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return "", false
	}
	var result strings.Builder
	for _, block := range blocks {
		if block.Type != "text" {
			return "", false
		}
		result.WriteString(block.Text)
	}
	return result.String(), true
}

func nativeGeminiTextParts(parts []struct {
	Text *string `json:"text"`
}) (string, bool) {
	var result strings.Builder
	for _, part := range parts {
		if part.Text == nil {
			return "", false
		}
		result.WriteString(*part.Text)
	}
	return result.String(), len(parts) > 0
}

func targetFromAuto(catalog *models.Catalog, selected auto.Target) Target {
	var provider models.Provider
	var pair models.Pair
	if catalog != nil {
		provider, _ = catalog.Provider(selected.ProviderKey)
		for _, candidate := range catalog.PairsForProvider(selected.ProviderKey) {
			if candidate.ModelID == selected.Model {
				pair = candidate
				break
			}
		}
	}
	return Target{ProviderKey: selected.ProviderKey, Provider: provider, Pair: pair, UpstreamModel: selected.UpstreamModel}
}

func nativeJSONModel(body []byte) (string, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return "", errors.New("invalid JSON object")
	}
	var model string
	if err := json.Unmarshal(object["model"], &model); err != nil || strings.TrimSpace(model) == "" {
		return "", errors.New("missing model")
	}
	return model, nil
}

func replaceNativeJSONModel(body []byte, model string) ([]byte, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return nil, errors.New("invalid JSON object")
	}
	encoded, _ := json.Marshal(model)
	object["model"] = encoded
	return json.Marshal(object)
}

func nativeStreamRequested(body []byte) bool {
	var payload struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &payload)
	return payload.Stream
}

func nativeGeminiPath(protocol providers.Protocol, model string) (string, error) {
	operation := "generateContent"
	if protocol == protocolGeminiStream {
		operation = "streamGenerateContent"
	}
	if model == "" {
		return "", errors.New("missing model")
	}
	return "/v1beta/models/" + url.PathEscape(model) + ":" + operation, nil
}

// relayNative copies the upstream response as it arrives and observes only
// bounded usage metadata. The observer never changes or buffers client bytes.
func (h *Handler) relayNative(w http.ResponseWriter, r *http.Request, record *requestLog, response *providers.Response) {
	copyResponseHeaders(w.Header(), response.Header)
	w.Header().Set(RequestIDHeader, record.RequestID)
	w.WriteHeader(response.StatusCode)
	var observer *nativeUsageObserver
	if h.routingLogEnabled(r) {
		observer = &nativeUsageObserver{stream: record.stream, contentType: response.Header.Get("Content-Type")}
	}
	written, err := copyFlushing(w, response.Body, http.NewResponseController(w), observer.observe)
	record.BytesWritten = written
	if err != nil && clientGone(r, err) {
		record.canceled()
	}
	if observer != nil {
		record.usage = observer.finish(err != nil)
	}
}

type nativeUsageObserver struct {
	stream      bool
	contentType string
	buffer      []byte
	usage       logging.Usage
	seen        bool
	oversized   bool
}

func (o *nativeUsageObserver) observe(chunk []byte) {
	if o == nil || o.oversized {
		return
	}
	isStream := o.stream || strings.Contains(strings.ToLower(o.contentType), "text/event-stream")
	if isStream {
		if len(o.buffer)+len(chunk) > maxNativeObservedResponseBytes {
			o.oversized = true
			o.buffer = nil
			return
		}
		o.buffer = append(o.buffer, chunk...)
		for {
			boundary := bytes.Index(o.buffer, []byte("\n\n"))
			if boundary < 0 {
				break
			}
			if usage, ok := parseNativeUsage(o.buffer[:boundary]); ok {
				o.usage, o.seen = usage, true
			}
			o.buffer = o.buffer[boundary+2:]
		}
		if len(o.buffer) > maxNativeObservedResponseBytes {
			o.oversized = true
			o.buffer = nil
		}
		return
	}
	if len(o.buffer)+len(chunk) > maxNativeObservedResponseBytes {
		o.oversized = true
		o.buffer = nil
		return
	}
	o.buffer = append(o.buffer, chunk...)
	if usage, ok := parseNativeUsage(o.buffer); ok {
		o.usage, o.seen = usage, true
	}
}

func parseNativeUsage(raw []byte) (logging.Usage, bool) {
	line := strings.TrimSpace(string(raw))
	for _, part := range strings.Split(line, "\n") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "data:") {
			line = strings.TrimSpace(strings.TrimPrefix(part, "data:"))
			break
		}
	}
	if line == "" || line == "[DONE]" {
		return logging.Usage{}, false
	}
	var value map[string]json.RawMessage
	if json.Unmarshal([]byte(line), &value) != nil {
		return logging.Usage{}, false
	}
	if nested, ok := value["message"]; ok {
		var message map[string]json.RawMessage
		if json.Unmarshal(nested, &message) == nil {
			value = message
		}
	}
	usageRaw := value["usage"]
	if len(usageRaw) == 0 {
		usageRaw = value["usageMetadata"]
	}
	if len(usageRaw) == 0 {
		return logging.Usage{}, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(usageRaw, &fields) != nil {
		return logging.Usage{}, false
	}
	usage := logging.Usage{Status: logging.UsageStatusObserved}
	usage.InputTokens = firstNativeToken(fields, "input_tokens", "promptTokenCount", "prompt_tokens")
	usage.OutputTokens = firstNativeToken(fields, "output_tokens", "candidatesTokenCount", "completion_tokens")
	usage.TotalTokens = firstNativeToken(fields, "total_tokens", "totalTokenCount")
	if usage.InputTokens != nil && usage.OutputTokens != nil && usage.TotalTokens == nil {
		total := *usage.InputTokens + *usage.OutputTokens
		usage.TotalTokens = &total
	}
	if usage.InputTokens == nil && usage.OutputTokens == nil && usage.TotalTokens == nil {
		return logging.Usage{}, false
	}
	return usage, true
}

func firstNativeToken(fields map[string]json.RawMessage, keys ...string) *int64 {
	for _, key := range keys {
		var count int64
		if err := json.Unmarshal(fields[key], &count); err == nil && count >= 0 {
			return &count
		}
	}
	return nil
}

func (o *nativeUsageObserver) finish(interrupted bool) logging.Usage {
	if o.oversized {
		return logging.Usage{Status: logging.UsageStatusOversized}
	}
	if interrupted {
		return logging.Usage{Status: logging.UsageStatusInterrupted}
	}
	if o.stream && len(o.buffer) > 0 {
		if usage, ok := parseNativeUsage(o.buffer); ok {
			o.usage, o.seen = usage, true
		}
	}
	if o.seen {
		return o.usage
	}
	if !o.stream && !strings.Contains(strings.ToLower(o.contentType), "text/event-stream") {
		if usage, ok := parseNativeUsage(o.buffer); ok {
			return usage
		}
	}
	return logging.Usage{Status: logging.UsageStatusAbsent}
}
