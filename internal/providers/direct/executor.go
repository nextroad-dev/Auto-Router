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
	"sync/atomic"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/netx"
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

// proxyIdentityHeaders carry a client-claimed network identity or scheme. Auto
// Router does not rebuild them, so they are never forwarded to a provider.
var proxyIdentityHeaders = netx.ProxyIdentityHeaders

// Config is one provider's direct connection configuration.
type Config struct {
	// BaseURL is the provider API root, for example "https://api.openai.com/v1".
	BaseURL string
	// APIKey is the provider credential. Empty means no authentication header.
	APIKey string
	// ProviderKey is used only for diagnostics and classification.
	ProviderKey string
	Kind        models.ProviderKind
	// StreamIdleTimeout bounds the gap between two reads of a streaming response
	// body. Zero disables it, so long reasoning streams are never truncated.
	StreamIdleTimeout time.Duration
	// BodyTimeout bounds the gap between two reads of a non-stream response body
	// after its headers arrived. Zero disables it.
	BodyTimeout time.Duration
}

// Executor is a ready-to-use direct executor for one provider.
type Executor struct {
	client      *http.Client
	endpoint    *url.URL
	apiKey      string
	providerKey string
	kind        models.ProviderKind
	streamIdle  time.Duration
	bodyIdle    time.Duration
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
		streamIdle:  cfg.StreamIdleTimeout,
		bodyIdle:    cfg.BodyTimeout,
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

	upstream, err := e.upstreamURL(request)
	if err != nil {
		return nil, err
	}

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
	idle := e.bodyIdle
	if request.Stream {
		idle = e.streamIdle
	}
	if idle > 0 {
		body = newIdleTimeoutBody(response.Body, idle)
	}
	return &providers.Response{
		StatusCode: response.StatusCode,
		Header:     response.Header,
		Body:       body,
	}, nil
}

// upstreamURL builds the outbound URL. UpstreamPath is an already-escaped path,
// so the join is done on escaped forms and the decoded Path is derived from it:
// setting both keeps URL.String from escaping a percent sign a second time.
func (e *Executor) upstreamURL(request *providers.Request) (*url.URL, error) {
	upstream := *e.endpoint
	escapedPath := request.Protocol.Path()
	if request.UpstreamPath != "" {
		if !strings.HasPrefix(request.UpstreamPath, "/") || strings.Contains(request.UpstreamPath, "?") || strings.Contains(request.UpstreamPath, "#") || path.Clean(request.UpstreamPath) != request.UpstreamPath {
			return nil, fmt.Errorf("%w: invalid upstream path override", providers.ErrUnsupportedConversion)
		}
		escapedPath = request.UpstreamPath
	}
	escapedPath = joinProtocolPath(e.endpoint.EscapedPath(), escapedPath)
	decodedPath, err := url.PathUnescape(escapedPath)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid upstream path override", providers.ErrUnsupportedConversion)
	}
	upstream.Path = decodedPath
	upstream.RawPath = escapedPath
	if request.UpstreamQuery != "" && request.UpstreamQuery != "alt=sse" {
		return nil, fmt.Errorf("%w: unsupported upstream query override", providers.ErrUnsupportedConversion)
	}
	upstream.RawQuery = request.UpstreamQuery
	upstream.ForceQuery = false
	return &upstream, nil
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
		if hopByHop(canonical, connectionTokens) || isClientCredential(canonical) || isProxyIdentity(canonical) {
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

func isProxyIdentity(canonicalName string) bool {
	for _, header := range proxyIdentityHeaders {
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

// idleTimeoutBody fails a read that waits longer than timeout for upstream
// bytes. Only time spent inside Read counts, so a slow downstream client that
// applies backpressure is never mistaken for a stalled provider. The failure
// wraps providers.ErrUpstreamTimeout so it keeps the upstream-timeout
// classification.
type idleTimeoutBody struct {
	body     io.ReadCloser
	timeout  time.Duration
	timer    *time.Timer
	timedOut atomic.Bool
}

func newIdleTimeoutBody(body io.ReadCloser, timeout time.Duration) *idleTimeoutBody {
	b := &idleTimeoutBody{body: body, timeout: timeout}
	// Closing the body is what unblocks a Read that is stuck on the network.
	b.timer = time.AfterFunc(timeout, func() {
		b.timedOut.Store(true)
		_ = b.body.Close()
	})
	b.timer.Stop()
	return b
}

var errStreamIdle = errors.New("upstream response body idle timeout")

func (b *idleTimeoutBody) Read(p []byte) (int, error) {
	if b.timedOut.Load() {
		return 0, b.timeoutError()
	}
	b.timer.Reset(b.timeout)
	n, err := b.body.Read(p)
	b.timer.Stop()
	if b.timedOut.Load() {
		return n, b.timeoutError()
	}
	return n, err
}

func (b *idleTimeoutBody) timeoutError() error {
	return fmt.Errorf("%w: %w after %s", providers.ErrUpstreamTimeout, errStreamIdle, b.timeout)
}

func (b *idleTimeoutBody) Close() error {
	b.timer.Stop()
	return b.body.Close()
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
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded)
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		timedOut = true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		timedOut = true
	}
	// A timeout before the transport obtained a connection is known not to have
	// delivered request bytes. Preserve the timeout classification for the client
	// and logs, while also exposing the safe pre-request retry marker.
	if trace != nil && !trace.connected() && !trace.receivedFirstByte() {
		if timedOut {
			return fmt.Errorf("%w: %w: %v", providers.ErrUpstreamTimeout, providers.ErrPreRequestFailure, err)
		}
		return fmt.Errorf("%w: %w: %v", providers.ErrUpstreamUnavailable, providers.ErrPreRequestFailure, err)
	}
	if timedOut {
		return fmt.Errorf("%w: %v", providers.ErrUpstreamTimeout, err)
	}
	return fmt.Errorf("%w: %v", providers.ErrUpstreamUnavailable, err)
}
