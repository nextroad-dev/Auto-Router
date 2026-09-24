// Package providers defines the replaceable direct-provider execution contract.
// The proxy and router depend on this contract only, never on a provider SDK or
// transport implementation.
//
// The contract is deliberately protocol-neutral in one direction and
// protocol-faithful in the other: a Request carries an already finalized model
// identifier and an opaque body, a Response carries the upstream status,
// headers and a streaming body. Nothing in this package parses OpenAI semantics
// or rewrites prompts, tools, reasoning or usage fields.
package providers

import (
	"context"
	"errors"
	"io"
	"net/http"
)

// Protocol identifies the client-facing API a request arrived on. It is used to
// select the upstream path suffix and to label request logs.
type Protocol string

const (
	// ProtocolChatCompletions is POST /v1/chat/completions.
	ProtocolChatCompletions Protocol = "chat_completions"
	// ProtocolResponses is POST /v1/responses.
	ProtocolResponses Protocol = "responses"
	// Native protocol paths are accepted only by matching upstream kinds.
	ProtocolAnthropicMessages           Protocol = "anthropic_messages"
	ProtocolGeminiGenerateContent       Protocol = "gemini_generate_content"
	ProtocolGeminiStreamGenerateContent Protocol = "gemini_stream_generate_content"
)

// Path returns the fixed path suffix the protocol maps to. The direct executor
// prepends the selected provider's base path.
func (p Protocol) Path() string {
	switch p {
	case ProtocolResponses:
		return "/v1/responses"
	case ProtocolAnthropicMessages:
		return "/v1/messages"
	default:
		return "/v1/chat/completions"
	}
}

// Valid reports whether the protocol is known.
func (p Protocol) Valid() bool {
	switch p {
	case ProtocolChatCompletions, ProtocolResponses, ProtocolAnthropicMessages, ProtocolGeminiGenerateContent, ProtocolGeminiStreamGenerateContent:
		return true
	default:
		return false
	}
}

// Request is one fully resolved provider call. Model is the provider-native
// value the executor must place in the request body's "model" field.
type Request struct {
	Protocol Protocol
	// UpstreamPath overrides the protocol's default path for provider adapters.
	// It must be a path, never a URL, and is joined beneath the configured base URL.
	UpstreamPath string
	// UpstreamQuery is an adapter-generated query string appended to UpstreamPath.
	UpstreamQuery string
	// ProviderKey is the registry provider that was selected. It is used for
	// logging and diagnostics, not for endpoint construction.
	ProviderKey string
	// Model is the provider-native model identifier.
	Model string
	// Body is the request body exactly as it must reach the upstream, with the
	// top-level model field already rewritten.
	Body []byte
	// Header carries the client headers that survive hop-by-hop and credential
	// stripping, except those the executor sets itself.
	Header http.Header
	// ClientIP is the observed client address for logging.
	ClientIP string
	// RequestID is the correlation identifier echoed to the client.
	RequestID string
	// Stream reports whether the client asked for a streaming response. The
	// executor does not need it to build the request; it exists so adapters can
	// choose streaming-safe transports and so logs stay accurate.
	Stream bool
}

// Response is an upstream response. Body must be closed by the caller. The
// executor returns it unfiltered: the proxy decides what to relay.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

// Errors returned by Executor implementations. Callers classify with errors.Is
// and map them onto the stable client-facing error codes; adapters must wrap
// their underlying transport error rather than replace it.
var (
	// ErrUnsupportedConversion means an adapter cannot preserve request semantics.
	ErrUnsupportedConversion = errors.New("unsupported provider protocol conversion")
	// ErrProviderNotConfigured means the selected provider has no usable base_url
	// and therefore no direct executor. It is reported as 503 provider_not_configured.
	ErrProviderNotConfigured = errors.New("provider is not configured")
	// ErrGatewayNotConfigured is retained only so the legacy Bifrost adapter can
	// continue to compile. The A2 serving path normalizes it to provider_not_configured.
	ErrGatewayNotConfigured = errors.New("gateway is not configured")
	// ErrUpstreamUnavailable means the connection failed, TLS failed, or the
	// upstream closed before sending response headers. Reported as 502.
	ErrUpstreamUnavailable = errors.New("upstream unavailable")
	// ErrUpstreamTimeout means response headers did not arrive inside the
	// configured timeout. Reported as 504.
	ErrUpstreamTimeout = errors.New("upstream response timeout")
	// ErrCanceled means the client went away or the server is shutting down.
	// No error body is written.
	ErrCanceled = errors.New("request canceled")
	// ErrPreRequestFailure means the transport failed before the request could
	// have reached the upstream: no connection was ever established, so the
	// request was never written. It is the only class stage 7 may retry, and an
	// error carries it only when the adapter is certain of that: an established
	// connection, an answered header or an already-started stream is never
	// retried, because a retried request could duplicate a tool side effect.
	ErrPreRequestFailure = errors.New("upstream request was never delivered")
)

// Executor performs one upstream call. Implementations must honor ctx
// cancellation, must not retry, and must not buffer a streaming response.
type Executor interface {
	Do(ctx context.Context, request *Request) (*Response, error)
}
