// Package direct adapts a single Provider's base_url and api_key to the
// providers.Executor contract. Every upstream request is sent directly to the
// provider endpoint and authenticated with that provider's own credential.
//
// Transport constants are deliberately fixed: exposing them would let a
// misconfiguration silently serialize or starve concurrent streams.
package direct

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
)

const (
	maxIdleConnsPerHost   = 64
	idleConnTimeout       = 90 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	expectContinueTimeout = 1 * time.Second
	dialKeepAlive         = 30 * time.Second
	responseHeaderTimeout = 60 * time.Second
	connectTimeout        = 10 * time.Second
	// A zero stream idle timeout allows slow streaming/reasoning models to
	// continue without being truncated.
	streamIdleTimeout = 0
)

// hopByHopHeaders are removed from outbound requests and relayed responses.
var hopByHopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Connection",
	"TE", "Trailer", "Transfer-Encoding", "Upgrade",
	"Proxy-Authenticate",
}

// clientCredentialHeaders are credentials supplied by the client. They must
// never be forwarded to the provider.
var clientCredentialHeaders = []string{
	"Authorization", "Cookie", "Proxy-Authorization",
	"X-Api-Key", "X-Goog-Api-Key",
	// Reject the historical Bifrost virtual-key header as well. The direct
	// executor has no gateway credential, and accepting this client-supplied
	// header would let a caller accidentally forward an unrelated secret.
	"X-Bf-Vk",
}

// transportManagedHeaders are owned by net/http and must be rebuilt from the
// request body rather than copied from the client.
var transportManagedHeaders = []string{
	"Content-Encoding", "Content-Length", "Expect",
}

// Config is one provider's direct connection configuration.
type Config struct {
	// BaseURL is the provider API root, for example "https://api.openai.com/v1".
	BaseURL string
	// APIKey is the provider credential. Empty means no authentication header.
	APIKey string
	// ProviderKey is used only for diagnostics and classification.
	ProviderKey string
	Kind        models.ProviderKind
}

// Executor is a ready-to-use direct executor for one provider.
type Executor struct {
	client      *http.Client
	endpoint    *url.URL
	apiKey      string
	providerKey string
	kind        models.ProviderKind
	streamIdle  time.Duration
}

var _ providers.Executor = (*Executor)(nil)

// New validates a provider endpoint and builds its connection pool. An empty
// endpoint is a valid stored provider state, but it has no executable client.
func New(cfg Config) (*Executor, error) {
	if cfg.BaseURL == "" {
		return nil, providers.ErrProviderNotConfigured
	}
	endpoint, err := url.Parse(cfg.BaseURL)
	if err != nil || endpoint.Host == "" {
		return nil, errors.New("provider base_url must be an absolute http(s) URL")
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, errors.New("provider base_url must use the http or https scheme")
	}
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" {
		return nil, errors.New("provider base_url must not contain credentials, a query string, or a fragment")
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   connectTimeout,
			KeepAlive: dialKeepAlive,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          maxIdleConnsPerHost,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		IdleConnTimeout:       idleConnTimeout,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ExpectContinueTimeout: expectContinueTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		DisableCompression:    true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &Executor{
		client:      &http.Client{Transport: transport},
		endpoint:    endpoint,
		apiKey:      cfg.APIKey,
		providerKey: cfg.ProviderKey,
		kind:        cfg.Kind,
		streamIdle:  streamIdleTimeout,
	}, nil
}

// Do performs one provider request without retrying.
func (e *Executor) Do(ctx context.Context, request *providers.Request) (*providers.Response, error) {
	if request == nil || !request.Protocol.Valid() {
		return nil, fmt.Errorf("%w: unsupported protocol", providers.ErrUpstreamUnavailable)
	}
	if request.Model == "" {
		return nil, fmt.Errorf("%w: missing upstream model", providers.ErrUpstreamUnavailable)
	}

	upstream := *e.endpoint
	protocolPath := request.Protocol.Path()
	if request.UpstreamPath != "" {
		if !strings.HasPrefix(request.UpstreamPath, "/") || strings.Contains(request.UpstreamPath, "?") || strings.Contains(request.UpstreamPath, "#") || path.Clean(request.UpstreamPath) != request.UpstreamPath {
			return nil, fmt.Errorf("%w: invalid upstream path override", providers.ErrUnsupportedConversion)
		}
		protocolPath = request.UpstreamPath
	}
	protocolPath = joinProtocolPath(e.endpoint.Path, protocolPath)
	upstream.Path = protocolPath
	if request.UpstreamQuery != "" && request.UpstreamQuery != "alt=sse" {
		return nil, fmt.Errorf("%w: unsupported upstream query override", providers.ErrUnsupportedConversion)
	}
	upstream.RawQuery = request.UpstreamQuery
	if e.endpoint.RawPath != "" {
		upstream.RawPath = joinProtocolPath(e.endpoint.RawPath, request.Protocol.Path())
	}
	upstream.ForceQuery = false

	outbound, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.String(), bytes.NewReader(request.Body))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", providers.ErrUpstreamUnavailable, err)
	}
	outbound, trace := installTrace(outbound)
	outbound.Header = relayableRequestHeaders(request.Header)
	outbound.Header.Set("Content-Type", "application/json")
	outbound.Header.Set("Accept", "text/event-stream, application/json")
	outbound.Header.Set("Accept-Encoding", "identity")
	if e.apiKey != "" {
		switch e.kind {
		case models.ProviderAnthropic:
			outbound.Header.Set("x-api-key", e.apiKey)
			outbound.Header.Set("anthropic-version", "2023-06-01")
		case models.ProviderGemini:
			outbound.Header.Set("x-goog-api-key", e.apiKey)
		default:
			outbound.Header.Set("Authorization", "Bearer "+e.apiKey)
		}
	}
	if request.RequestID != "" {
		outbound.Header.Set("X-Request-Id", request.RequestID)
	}

	response, err := e.client.Do(outbound)
	if err != nil {
		return nil, classify(ctx, trace, err)
	}
	var body io.ReadCloser = response.Body
	if e.streamIdle > 0 {
		body = newIdleTimeoutBody(response.Body, e.streamIdle)
	}
	return &providers.Response{
		StatusCode: response.StatusCode,
		Header:     response.Header,
		Body:       body,
	}, nil
}

// CloseIdleConnections releases this provider's pooled connections.
func (e *Executor) CloseIdleConnections() {
	if e == nil || e.client == nil {
		return
	}
	if transport, ok := e.client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

// relayableRequestHeaders copies client headers that are safe to send to the
// provider, removing hop-by-hop, connection-listed and client credential
// headers. Provider authentication is installed by Do after this copy.
func relayableRequestHeaders(header http.Header) http.Header {
	connectionTokens := headerTokens(header)
	relayed := make(http.Header, len(header))
	for name, values := range header {
		canonical := http.CanonicalHeaderKey(name)
		if hopByHop(canonical, connectionTokens) || isClientCredential(canonical) {
			continue
		}
		relayed[name] = append([]string(nil), values...)
	}
	return relayed
}

func isClientCredential(canonicalName string) bool {
	for _, header := range clientCredentialHeaders {
		if canonicalName == header {
			return true
		}
	}
	for _, header := range transportManagedHeaders {
		if canonicalName == header {
			return true
		}
	}
	return false
}

func hopByHop(canonicalName string, connectionTokens map[string]struct{}) bool {
	for _, header := range hopByHopHeaders {
		if canonicalName == header {
			return true
		}
	}
	_, listed := connectionTokens[canonicalName]
	return listed
}

func headerTokens(header http.Header) map[string]struct{} {
	tokens := make(map[string]struct{})
	for _, value := range header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			token = strings.TrimSpace(token)
			if token != "" {
				tokens[http.CanonicalHeaderKey(token)] = struct{}{}
			}
		}
	}
	return tokens
}

func joinProtocolPath(prefix, protocolPath string) string {
	// Provider base URLs commonly already end in /v1 (for example the OpenAI
	// and Groq APIs), while Gemini commonly uses /v1beta. If the requested path
	// already includes the complete base path, keep it as-is; otherwise append
	// it beneath arbitrary provider path prefixes.
	trimmed := strings.TrimRight(prefix, "/")
	if trimmed != "" && (protocolPath == trimmed || strings.HasPrefix(protocolPath, trimmed+"/")) {
		return protocolPath
	}
	suffix := protocolPath
	if strings.HasSuffix(trimmed, "/v1") {
		suffix = strings.TrimPrefix(protocolPath, "/v1")
	}
	return joinPath(prefix, suffix)
}

func joinPath(prefix, suffix string) string {
	switch {
	case prefix == "" || prefix == "/":
		return suffix
	case strings.HasSuffix(prefix, "/"):
		return prefix + strings.TrimPrefix(suffix, "/")
	default:
		return prefix + suffix
	}
}

// idleTimeoutBody is kept for parity with the old adapter. The fixed timeout is
// currently zero, but retaining the implementation keeps the transport policy
// explicit if a future provider-specific mode needs it.
type idleTimeoutBody struct {
	body      io.ReadCloser
	timeout   time.Duration
	closeOnce sync.Once
	closed    chan struct{}
}

func newIdleTimeoutBody(body io.ReadCloser, timeout time.Duration) *idleTimeoutBody {
	return &idleTimeoutBody{body: body, timeout: timeout, closed: make(chan struct{})}
}

var errStreamIdle = errors.New("upstream stream idle timeout")

type readOutcome struct {
	read int
	err  error
}

func (b *idleTimeoutBody) Read(p []byte) (int, error) {
	result := make(chan readOutcome, 1)
	go func() {
		n, err := b.body.Read(p)
		result <- readOutcome{read: n, err: err}
	}()
	timer := time.NewTimer(b.timeout)
	defer timer.Stop()
	select {
	case received := <-result:
		return received.read, received.err
	case <-timer.C:
		_ = b.Close()
		return 0, fmt.Errorf("%w after %s", errStreamIdle, b.timeout)
	case <-b.closed:
		return 0, net.ErrClosed
	}
}

func (b *idleTimeoutBody) Close() error {
	var err error
	b.closeOnce.Do(func() {
		err = b.body.Close()
		close(b.closed)
	})
	return err
}

type requestTrace struct {
	gotConn          atomic.Bool
	gotFirstResponse atomic.Bool
}

func installTrace(outbound *http.Request) (*http.Request, *requestTrace) {
	trace := &requestTrace{}
	traced := outbound.WithContext(httptrace.WithClientTrace(outbound.Context(), &httptrace.ClientTrace{
		GotConn:              func(httptrace.GotConnInfo) { trace.gotConn.Store(true) },
		GotFirstResponseByte: func() { trace.gotFirstResponse.Store(true) },
	}))
	return traced, trace
}

func (t *requestTrace) connected() bool {
	return t != nil && t.gotConn.Load()
}

func (t *requestTrace) receivedFirstByte() bool {
	return t != nil && t.gotFirstResponse.Load()
}

func classify(ctx context.Context, trace *requestTrace, err error) error {
	// A deadline is an upstream timeout; only cancellation means the client or
	// server explicitly abandoned the request. Checking the context first is
	// still important for wrapped transport errors, but it must not collapse
	// DeadlineExceeded into the cancellation bucket.
	if ctxErr := ctx.Err(); errors.Is(ctxErr, context.Canceled) {
		return fmt.Errorf("%w: %v", providers.ErrCanceled, ctxErr)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %v", providers.ErrCanceled, err)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", providers.ErrUpstreamTimeout, err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%w: %v", providers.ErrUpstreamTimeout, err)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return fmt.Errorf("%w: %v", providers.ErrUpstreamTimeout, err)
	}
	if !trace.connected() && !trace.receivedFirstByte() {
		return fmt.Errorf("%w: %w: %v", providers.ErrUpstreamUnavailable, providers.ErrPreRequestFailure, err)
	}
	return fmt.Errorf("%w: %v", providers.ErrUpstreamUnavailable, err)
}
