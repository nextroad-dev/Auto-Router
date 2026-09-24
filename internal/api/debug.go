package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/nextroad-dev/Auto-Router/internal/providers"
	"github.com/nextroad-dev/Auto-Router/internal/proxy"
	"github.com/nextroad-dev/Auto-Router/internal/router/analyzer"
)

// This file implements POST /debug/analyze, the offline counterpart of the
// forwarding path. It exists so stage 6 can observe what the analyzer extracts
// from a real request before anything consumes features in production.
//
// The endpoint returns features and nothing else. It never echoes the request
// body, never returns a prompt, and never returns an inline payload: an image
// reference is reported as "[redacted]" unless it is a bounded http(s) URL the
// caller already sent. It never touches the registry, the database or a Provider
// executor: there is exactly one analysis and zero upstream calls, which
// is what makes it usable as proof rather than as a second request path.

// DebugOptions wires the debug analyzer endpoint.
type DebugOptions struct {
	// Analyzer performs the analysis. It is required: a debug handler without
	// an analyzer would have to invent features.
	Analyzer interface {
		Analyze(input analyzer.Input) (analyzer.Result, error)
	}
	// DefaultPreference is routing.default_preference, used when the debug
	// request does not carry a preference. It is the static fallback used when Live is
	// nil.
	DefaultPreference analyzer.Preference
	// MaxRequestBytes bounds the debug request body. It is the same setting the
	// forwarding path uses, so the tool cannot buffer more than the service.
	MaxRequestBytes int64
	// Live supplies the default preference in effect right now. Nil means the static
	// field is used unchanged.
	Live interface {
		DefaultPreference() analyzer.Preference
	}
}

// NewDebugAnalyzer builds the POST /debug/analyze handler. The composition root
// only mounts it when routing.analyzer_debug_endpoint is enabled and the
// listener is loopback-only.
func NewDebugAnalyzer(options DebugOptions) http.Handler {
	handler := &debugAnalyzer{
		analyzer:          options.Analyzer,
		defaultPreference: options.DefaultPreference,
		maxBytes:          options.MaxRequestBytes,
		live:              options.Live,
	}
	if handler.maxBytes <= 0 {
		handler.maxBytes = 16 << 20
	}
	return handler
}

type debugAnalyzer struct {
	analyzer interface {
		Analyze(input analyzer.Input) (analyzer.Result, error)
	}
	defaultPreference analyzer.Preference
	maxBytes          int64
	live              interface {
		DefaultPreference() analyzer.Preference
	}
}

// currentPreference reports routing.default_preference right now, preferring the
// live source and falling back to the static value.
func (d *debugAnalyzer) currentPreference() analyzer.Preference {
	if d.live == nil {
		return d.defaultPreference
	}
	preference := d.live.DefaultPreference()
	if !preference.Valid() {
		return d.defaultPreference
	}
	return preference
}

// ServeHTTP answers one analysis. Every response carries the correlation
// identifier and Cache-Control: no-store, matching every other model-facing
// response of this service.
func (d *debugAnalyzer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := proxy.RequestIDFor(r)
	w.Header().Set(proxy.RequestIDHeader, requestID)
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeDebugError(w, http.StatusMethodNotAllowed, "invalid_request", "the debug endpoint accepts POST only")
		return
	}
	body, err := readDebugBody(r, d.maxBytes)
	if err != nil {
		writeDebugError(w, http.StatusRequestEntityTooLarge, "request_too_large", err.Error())
		return
	}
	var request debugAnalyzeRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&request); err != nil {
		writeDebugError(w, http.StatusBadRequest, "invalid_request", "the request body must be a JSON object with protocol and body fields")
		return
	}
	if err := requireNoTrailingContent(decoder); err != nil {
		writeDebugError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	protocol, ok := debugProtocol(request.Protocol)
	if !ok {
		writeDebugError(w, http.StatusBadRequest, "invalid_request", `protocol must be "chat_completions" or "responses"`)
		return
	}
	if len(request.Body) == 0 {
		writeDebugError(w, http.StatusBadRequest, "invalid_request", "body must be the JSON request object to analyze")
		return
	}
	if trimmed := bytes.TrimSpace(request.Body); len(trimmed) == 0 || trimmed[0] != '{' {
		writeDebugError(w, http.StatusBadRequest, "invalid_request", "body must be a JSON object")
		return
	}

	result, err := d.analyzer.Analyze(analyzer.Input{
		Protocol:          protocol,
		Body:              request.Body,
		HeaderPreference:  request.Preference,
		DefaultPreference: d.currentPreference(),
	})
	if err != nil {
		// The only failures are an unknown protocol (already rejected above) and
		// an unusable preference, which is a client error here.
		if errors.Is(err, analyzer.ErrInvalidPreference) {
			writeDebugError(w, http.StatusBadRequest, "invalid_routing_preference", err.Error())
			return
		}
		writeDebugError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(debugAnalyzeResponse{
		Protocol:  string(protocol),
		RequestID: requestID,
		Features:  presentFeatures(result.Features),
	})
}

// debugAnalyzeRequest is the debug endpoint's request body. Body stays raw so a
// malformed object is a client error rather than a silently empty analysis, and
// so the analyzer receives exactly the bytes the client sent.
type debugAnalyzeRequest struct {
	Protocol   string          `json:"protocol"`
	Body       json.RawMessage `json:"body"`
	Preference string          `json:"preference"`
}

// debugAnalyzeResponse is the response. Field names mirror analyzer.Features
// one-to-one with underscore naming, so a reader can move between the two
// without a mapping table. The field set and its order are fixed.
//
// Protocol appears once, in the envelope, rather than again inside features:
// the analyzed protocol is a property of the request, and duplicating it in a
// fixed DTO would create two places to keep in agreement.
type debugAnalyzeResponse struct {
	Protocol  string               `json:"protocol"`
	RequestID string               `json:"request_id"`
	Features  debugAnalyzeFeatures `json:"features"`
}

type debugAnalyzeFeatures struct {
	Preference       string `json:"preference"`
	PreferenceSource string `json:"preference_source"`

	SystemPromptPresent bool           `json:"system_prompt_present"`
	SystemPromptBytes   int            `json:"system_prompt_bytes"`
	MessageCount        int            `json:"message_count"`
	RoleCounts          map[string]int `json:"role_counts"`
	InputBytes          int            `json:"input_bytes"`
	InputTokensEstimate int            `json:"input_tokens_estimate"`
	Length              string         `json:"length"`
	Truncated           bool           `json:"truncated"`

	TextOnly       bool                   `json:"text_only"`
	Kinds          []string               `json:"kinds"`
	ImageCount     int                    `json:"image_count"`
	Images         []debugAnalyzeImageRef `json:"images"`
	HasImageURL    bool                   `json:"has_image_url"`
	HasBase64Image bool                   `json:"has_base64_image"`
	HasFile        bool                   `json:"has_file"`
	HasAudio       bool                   `json:"has_audio"`

	HasTools         bool     `json:"has_tools"`
	ToolCount        int      `json:"tool_count"`
	ToolNames        []string `json:"tool_names"`
	ToolsTruncated   bool     `json:"tools_truncated"`
	ForcedToolChoice bool     `json:"forced_tool_choice"`

	HasCode         bool `json:"has_code"`
	ReasoningLikely bool `json:"reasoning_likely"`
	ChainedToolUse  bool `json:"chained_tool_use"`
	FileSearchUsed  bool `json:"file_search_used"`

	StreamRequested          bool `json:"stream_requested"`
	MaxOutputTokensRequested *int `json:"max_output_tokens_requested"`
	UnrecognizedParts        int  `json:"unrecognized_parts"`
}

type debugAnalyzeImageRef struct {
	Kind     string `json:"kind"`
	Location string `json:"location"`
}

// presentFeatures maps the analyzer's features onto the wire DTO. Slices are
// never nil (the analyzer guarantees that too), so a client always sees an
// array, and an inline payload can never appear here: the analyzer already
// replaced it with "[redacted]".
func presentFeatures(features analyzer.Features) debugAnalyzeFeatures {
	kinds := make([]string, 0, len(features.Kinds))
	for _, kind := range features.Kinds {
		kinds = append(kinds, string(kind))
	}
	images := make([]debugAnalyzeImageRef, 0, len(features.Images))
	for _, image := range features.Images {
		images = append(images, debugAnalyzeImageRef{Kind: image.Kind, Location: image.Location})
	}
	names := make([]string, 0, len(features.ToolNames))
	names = append(names, features.ToolNames...)
	roles := make(map[string]int, len(features.RoleCounts))
	for role, count := range features.RoleCounts {
		roles[role] = count
	}
	return debugAnalyzeFeatures{
		Preference:               string(features.Preference),
		PreferenceSource:         string(features.PreferenceSource),
		SystemPromptPresent:      features.SystemPromptPresent,
		SystemPromptBytes:        features.SystemPromptBytes,
		MessageCount:             features.MessageCount,
		RoleCounts:               roles,
		InputBytes:               features.InputBytes,
		InputTokensEstimate:      features.InputTokensEstimate,
		Length:                   string(features.Length),
		Truncated:                features.Truncated,
		TextOnly:                 features.TextOnly,
		Kinds:                    kinds,
		ImageCount:               features.ImageCount,
		Images:                   images,
		HasImageURL:              features.HasImageURL,
		HasBase64Image:           features.HasBase64Image,
		HasFile:                  features.HasFile,
		HasAudio:                 features.HasAudio,
		HasTools:                 features.HasTools,
		ToolCount:                features.ToolCount,
		ToolNames:                names,
		ToolsTruncated:           features.ToolsTruncated,
		ForcedToolChoice:         features.ForcedToolChoice,
		HasCode:                  features.HasCode,
		ReasoningLikely:          features.ReasoningLikely,
		ChainedToolUse:           features.ChainedToolUse,
		FileSearchUsed:           features.FileSearchUsed,
		StreamRequested:          features.StreamRequested,
		MaxOutputTokensRequested: features.MaxOutputTokensRequested,
		UnrecognizedParts:        features.UnrecognizedParts,
	}
}

func debugProtocol(value string) (providers.Protocol, bool) {
	switch value {
	case string(providers.ProtocolChatCompletions):
		return providers.ProtocolChatCompletions, true
	case string(providers.ProtocolResponses):
		return providers.ProtocolResponses, true
	default:
		return "", false
	}
}

// readDebugBody bounds the debug request exactly like a model request, so the
// tool cannot be used to buffer more than the service would.
func readDebugBody(r *http.Request, limit int64) ([]byte, error) {
	if r.ContentLength > limit {
		return nil, errors.New("the request body exceeds the configured limit")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, errors.New("the request body could not be read")
	}
	if int64(len(body)) > limit {
		return nil, errors.New("the request body exceeds the configured limit")
	}
	return body, nil
}

// requireNoTrailingContent rejects two concatenated JSON documents, which would
// otherwise let a client smuggle a second object past the decoder.
func requireNoTrailingContent(decoder *json.Decoder) error {
	if _, err := decoder.Token(); err == nil {
		return errors.New("the request body must contain exactly one JSON object")
	}
	return nil
}

// writeDebugError answers with the same OpenAI error envelope every model path
// uses. The message is always authored here and never includes client text.
func writeDebugError(w http.ResponseWriter, status int, code, message string) {
	proxy.WriteError(w, status, proxy.ErrorTypeForStatus(status), code, message)
}
