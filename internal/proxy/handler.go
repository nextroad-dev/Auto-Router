package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/logging"
	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
	"github.com/nextroad-dev/Auto-Router/internal/router/analyzer"
	"github.com/nextroad-dev/Auto-Router/internal/router/auto"
)

// BufferSize is the streaming chunk size. 16 KiB keeps a chunk small enough to
// preserve event boundaries and large enough to avoid a syscall per token.
const BufferSize = 16 << 10

// The two routing modes a request log can carry. They are named constants so the
// boundary and any future consumer (stage 8's routing log) agree on the spelling.
const (
	// routingModeExplicit is a request that named its destination.
	routingModeExplicit = "explicit"
	// routingModeAuto is a request that asked the router to choose.
	routingModeAuto = "auto"
)

// maxAutoUpstreamAttempts bounds how many upstream attempts one automatic request
// may make from the boundary's point of view. The orchestration layer owns the
// retry semantics and already refuses a third attempt; this constant is a local
// backstop so a misbehaving implementation cannot turn the forwarding loop into
// an unbounded retry loop.
const maxAutoUpstreamAttempts = 8

// RequestIDHeader is the correlation identifier relayed to clients and passed
// to the selected Provider.
const RequestIDHeader = "X-Request-Id"

// PreferenceHeader is the router-owned request header that selects a routing
// preference for one request. It is consumed and stripped at this boundary: the
// header is Auto Router's own control input, so it is never forwarded to the
// Provider, and whether it was accepted or rejected it never leaves the process.
// Unknown client headers are untouched.
const PreferenceHeader = "X-Routing-Preference"

// LiveValues is the runtime configuration the forwarding boundary reads per
// request. Every method is a snapshot read: the value that applies to a request is
// the one in effect when the handler starts it, so a change made while a request is
// in flight cannot alter how that request is handled.
//
// It is an interface rather than a configuration struct because the boundary must
// not depend on the settings store, and nil is a documented state: a handler built
// without it behaves exactly as it did before this interface existed, using the
// static fields on Options.
type LiveSnapshot struct {
	AllowProviderOverride bool
	DefaultPreference     analyzer.Preference
	RoutingLogEnabled     bool
	StoreClientIP         bool
	JevTraceEnabled       bool
	AutoSettings          auto.RuntimeSettings
	HasAutoSettings       bool
}

type LiveValues interface{ Snapshot() LiveSnapshot }

type liveSnapshotContextKey struct{}

// Options wires the handler. Catalog is read per request so a snapshot swap
// takes effect immediately; Executor may be nil when the process has no direct
// provider router, in which case every model request fails with 503.
type Options struct {
	Catalog *models.Store
	// Executor performs the upstream call. The A2 composition root supplies a
	// provider router that selects a direct executor per request.
	Executor providers.Executor
	// MaxRequestBytes bounds the buffered request body.
	MaxRequestBytes int64
	// Logger receives one structured record per request. Nil disables logging.
	Logger *slog.Logger
	// AllowProviderOverride enables the explicit provider override syntax.
	AllowProviderOverride bool
	// ProviderConfigured is an optional compatibility switch for embedders that
	// assemble the handler before installing an executor. In the normal A2 path
	// the executor itself is the readiness source.
	ProviderConfigured bool
	// GatewayConfigured is retained as a deprecated source-compatible alias for
	// older embedders. It has no gateway semantics in the direct-provider path.
	GatewayConfigured bool
	// DefaultPreference is routing.default_preference, applied when a request
	// carries no preference header. An invalid value is a configuration bug and
	// fails every request with 500 rather than routing on a default nobody
	// chose.
	DefaultPreference analyzer.Preference
	// AutoRouter resolves model="auto" requests. Nil means the automatic routing
	// stack is not assembled in this process, which answers model="auto" with
	// 503 routing_unavailable instead of pretending to route. A specified model
	// never touches it.
	//
	// The interface names the orchestration package's own types, exactly as
	// Analyzer names the analyzer's: the boundary depends on the decisions it
	// consumes, and the interface exists so a test can substitute one. The
	// forwarding path calls no policy, Jev or registry function itself.
	AutoRouter AutoRouter
	// Recorder receives one event per request that reaches the forwarding path.
	// Nil disables the routing log entirely: no observer is installed, no usage
	// is extracted and the response path is exactly the stage-7 path. Record is
	// called from the request's final defer, after the response has been written,
	// and must not block: the logging writer queues the event and returns.
	Recorder logging.Recorder
	// Live supplies the runtime values a request must read per request. Nil means
	// the four static fields above are used unchanged, which is the stage-8
	// behavior: an existing caller keeps working without a live source.
	Live LiveValues
	// OverrideAuthorizer decides whether this particular request may use the
	// provider override syntax beyond the AllowProviderOverride switch. It is how
	// inbound authentication adds the "override" scope as a second gate without the
	// forwarding boundary knowing what a scope is: an empty ProviderOverrideDecision
	// means "allowed", and anything else is written as the refusal.
	OverrideAuthorizer func(r *http.Request) ProviderOverrideDecision
}

// AutoRouter is the automatic routing seam. It is one decision plus one bounded
// retry, and nothing else: the forwarding path passes the request bytes it
// already buffered and receives a fully resolved destination.
//
// Both methods are called at most once per request. Route may perform one Jev
// call; Failover never performs a second one, because the features and the
// recommendation are part of the decision state it is given back.
type AutoRouter interface {
	// Route produces the first destination for one automatic request.
	Route(ctx context.Context, request auto.Request) (auto.Target, error)
	// Failover produces the destination for one additional upstream attempt
	// after cause failed, reporting false when no further attempt may be made.
	Failover(ctx context.Context, previous auto.Target, cause error) (auto.Target, bool)
}

// Handler serves POST /v1/chat/completions and POST /v1/responses.
type Handler struct {
	catalog               *models.Store
	executor              providers.Executor
	maxRequestBytes       int64
	logger                *slog.Logger
	allowProviderOverride bool
	providerConfigured    bool
	defaultPreference     analyzer.Preference
	autoRouter            AutoRouter
	recorder              logging.Recorder
	live                  LiveValues
	overrideAuthorizer    func(r *http.Request) ProviderOverrideDecision
}

// ProviderOverrideDecision is the answer to "may this request use the provider
// override syntax". An empty decision means yes; a non-empty one carries the
// client-facing refusal, so the boundary writes it without knowing why.
//
// It exists because two independent gates apply to the syntax: the configuration
// switch and, when inbound authentication is on, the caller's "override" scope.
// Both must pass, and the boundary reports the second one only when the first
// one allowed the request.
type ProviderOverrideDecision struct {
	// Allowed is true when the request may proceed.
	Allowed bool
	// Status, ErrorType, Code and Message describe the refusal. They are only read
	// when Allowed is false, and the message never contains a value the client sent.
	Status    int
	ErrorType string
	Code      string
	Message   string
}

// New builds the forwarding handler. It never returns nil: a handler without a
// direct provider router still answers GET /v1/models and reports 503 for model requests.
func New(options Options) *Handler {
	maxBytes := options.MaxRequestBytes
	if maxBytes <= 0 {
		maxBytes = 16 << 20
	}
	catalog := options.Catalog
	if catalog == nil {
		catalog = models.NewStore()
	}
	configured := options.ProviderConfigured || options.GatewayConfigured || options.Executor != nil
	preference := options.DefaultPreference
	if preference == "" {
		// A handler built without configuration still behaves like the process
		// default rather than inventing a fifth preference value.
		preference = analyzer.PreferenceBalanced
	}
	return &Handler{
		catalog:               catalog,
		executor:              options.Executor,
		maxRequestBytes:       maxBytes,
		logger:                options.Logger,
		allowProviderOverride: options.AllowProviderOverride,
		providerConfigured:    configured,
		defaultPreference:     preference,
		autoRouter:            options.AutoRouter,
		recorder:              options.Recorder,
		live:                  options.Live,
		overrideAuthorizer:    options.OverrideAuthorizer,
	}
}

// allowOverride reports whether this request may use the provider override
// syntax, reading the live switch when one is wired and the static field
// otherwise.
func (h *Handler) requestSettings(r *http.Request) LiveSnapshot {
	if r != nil {
		if snapshot, ok := r.Context().Value(liveSnapshotContextKey{}).(LiveSnapshot); ok {
			return snapshot
		}
	}
	return LiveSnapshot{AllowProviderOverride: h.allowProviderOverride, DefaultPreference: h.defaultPreference, RoutingLogEnabled: h.recorder != nil, StoreClientIP: false, JevTraceEnabled: false}
}

func (h *Handler) allowOverride(r *http.Request) bool {
	return h.requestSettings(r).AllowProviderOverride
}

// currentPreference reports the routing.default_preference in effect right now.
func (h *Handler) currentPreference(r *http.Request) analyzer.Preference {
	preference := h.requestSettings(r).DefaultPreference
	if !preference.Valid() {
		// A live source that produced an unusable value would turn every request
		// into a 500; the static default is the documented fallback.
		return h.defaultPreference
	}
	return preference
}

// routingLogEnabled reports whether a routing event is recorded right now. It is
// the switch that decides whether the usage observer is installed at all.
func (h *Handler) routingLogEnabled(r *http.Request) bool {
	return h.recorder != nil && h.requestSettings(r).RoutingLogEnabled
}

// overrideRefusal returns the refusal for this request's use of the override
// syntax, if any. It is checked before the target is resolved so a caller without
// the scope is told it lacks a permission rather than told the destination does
// not exist.
func (h *Handler) overrideRefusal(r *http.Request) *ProviderOverrideDecision {
	if h.overrideAuthorizer == nil {
		return nil
	}
	decision := h.overrideAuthorizer(r)
	if decision.Allowed {
		return nil
	}
	return &decision
}

// ServeHTTP dispatches the two model protocols.
//
// Error precedence within a request is fixed and tested: an unconfigured
// provider executor (503) is reported before the body is read, then media-type and size
// limits (415/413), then the router-owned preference header (400), then model
// parsing (400/403/404), then routing (422/500/503 for an automatic request),
// then upstream failures (502/504). Every check before the upstream call is a
// client-visible rejection with no side effect, and every one of them happens
// before the body is buffered where it can.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.live != nil {
		r = r.WithContext(context.WithValue(r.Context(), liveSnapshotContextKey{}, h.live.Snapshot()))
	}
	requestID := RequestIDFor(r)
	w.Header().Set(RequestIDHeader, requestID)
	protocol := protocolFor(r.URL.Path)
	// The router only registers the two POST paths, so reaching this branch
	// means a direct call.
	if !protocol.Valid() {
		writeError(w, http.StatusNotFound, "invalid_request_error", "invalid_request_error", "unknown endpoint")
		return
	}
	h.forward(w, r, protocol, requestID)
}

// protocolFor maps an inbound path to a protocol. The /v1/models listing is
// handled by the api package, not here: it is a catalogue read, not a forward.
func protocolFor(path string) providers.Protocol {
	switch path {
	case "/v1/chat/completions":
		return providers.ProtocolChatCompletions
	case "/v1/responses":
		return providers.ProtocolResponses
	default:
		return ""
	}
}

func (h *Handler) forward(w http.ResponseWriter, r *http.Request, protocol providers.Protocol, requestID string) {
	started := time.Now()
	record := &requestLog{
		RequestID: requestID,
		Protocol:  string(protocol),
		protocol:  protocol,
		ClientIP:  clientIP(r),
	}
	// The terminal defer is the single write point for a request: it emits the
	// process log line and hands the same facts to the routing log as one event.
	// Exactly one of the two runs, and neither is allowed to change the response.
	defer func() {
		record.emit(h.logger, started)
		h.recordEvent(r, record, started, time.Since(started))
	}()

	if !h.providerConfigured || h.executor == nil {
		// Evaluated before the body is read on purpose: a process that cannot
		// forward anything must not buffer up to the limit for every request.
		record.fail(http.StatusServiceUnavailable, "provider_not_configured")
		writeError(w, http.StatusServiceUnavailable, "api_error", "provider_not_configured", "no direct provider executor is configured")
		return
	}
	if contentType, ok := requestMediaType(r); !ok {
		record.fail(http.StatusUnsupportedMediaType, "unsupported_media_type")
		writeError(w, http.StatusUnsupportedMediaType, "invalid_request_error", "unsupported_media_type", contentType)
		return
	}
	// The preference header is read, validated and stripped here: before the
	// body is buffered, so a request that is going to be rejected never costs
	// up to http.max_request_bytes, and always, so a client-forged value can
	// never reach the selected provider regardless of what happens next.
	preference, preferenceSource, preferenceErr := parsePreferenceHeader(r.Header.Values(PreferenceHeader), h.currentPreference(r))
	r.Header.Del(PreferenceHeader)
	if preferenceErr != nil {
		record.fail(http.StatusBadRequest, "invalid_routing_preference")
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_routing_preference", preferenceErr.Error())
		return
	}
	record.RoutingPreference = string(preference)
	record.PreferenceSource = string(preferenceSource)
	// The routing mode is recorded before anything else can fail so a rejected
	// request still says whether it asked for automatic routing. A specified
	// model never changes it again.
	record.RoutingMode = routingModeExplicit

	body, err := readBody(r, h.maxRequestBytes)
	if err != nil {
		var tooLarge *bodyTooLargeError
		if errors.As(err, &tooLarge) {
			record.fail(http.StatusRequestEntityTooLarge, "request_too_large")
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large",
				fmt.Sprintf("request body exceeds the %d byte limit", h.maxRequestBytes))
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

	model, payload, err := rewriteModel(body)
	if err != nil {
		record.fail(http.StatusBadRequest, "invalid_request_error")
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request_error", err.Error())
		return
	}
	record.RequestedModel = model

	// "auto" is resolved by the orchestration layer before the explicit-target
	// resolver is consulted: ResolveTarget still refuses it, but that refusal is
	// now a programming-error guard rather than the client contract. A specified
	// model takes the unchanged direct path below.
	if model == models.AutoModelID {
		h.forwardAuto(w, r, protocol, requestID, record, body, payload, preference, preferenceSource)
		return
	}

	// The override syntax has two gates: the configuration switch, applied inside
	// ResolveTarget, and the caller's permission, applied here before anything is
	// resolved. The permission is checked first so a caller who lacks it learns that
	// rather than learning which destinations exist.
	if strings.HasPrefix(model, models.ProviderOverridePrefix()) && h.allowOverride(r) {
		if refusal := h.overrideRefusal(r); refusal != nil {
			record.fail(refusal.Status, refusal.Code)
			writeError(w, refusal.Status, refusal.ErrorType, refusal.Code, refusal.Message)
			return
		}
	}
	target, err := ResolveTarget(h.catalog.Load(), model, h.allowOverride(r))
	if err != nil {
		var targetErr *TargetError
		if errors.As(err, &targetErr) {
			record.fail(targetErr.Status, targetErr.Code)
			writeError(w, targetErr.Status, errorTypeForStatus(targetErr.Status), targetErr.Code, targetErr.Message)
			return
		}
		record.fail(http.StatusInternalServerError, "internal_error")
		writeError(w, http.StatusInternalServerError, "api_error", "internal_error", "the request could not be routed")
		return
	}
	record.Provider = target.ProviderKey
	record.UpstreamModel = target.UpstreamModel
	// The explicit path names its destination, so the effective logical model is
	// the one the client's identifier resolved to: the requested model itself, or
	// the pair's logical model for a "provider:<provider>/<model>" override.
	record.EffectiveModel = target.Pair.ModelID
	// The explicit path makes no Jev call, so it can carry no top-1
	// recommendation. Leaving both fields empty is what makes the adoption metric
	// an automatic-path measurement rather than a comparison against a value the
	// explicit path never produced.
	record.JevTopModel = ""
	// The explicit path reports the same routing fields with their explicit
	// values, so a routing log can query one schema instead of special-casing two.
	record.SelectionMode = "explicit"
	record.GatewayAttempts = 1

	finalBody, err := setModel(payload, target.UpstreamModel)
	if err != nil {
		record.fail(http.StatusInternalServerError, "internal_error")
		writeError(w, http.StatusInternalServerError, "api_error", "internal_error", "the request could not be prepared")
		return
	}

	upstreamRequest := &providers.Request{
		Protocol:    protocol,
		ProviderKey: target.ProviderKey,
		Model:       target.UpstreamModel,
		Body:        finalBody,
		Header:      r.Header,
		ClientIP:    record.ClientIP,
		RequestID:   requestID,
		Stream:      wantsStream(payload),
	}
	attemptStarted := time.Now()
	response, err := h.executor.Do(r.Context(), upstreamRequest)
	if err != nil {
		h.recordAttempt(r, record, 1, "", target.ProviderKey, target.Pair.ModelID, attemptStarted, 0, executorErrorCode(err), logging.Usage{})
		h.writeExecutorError(w, record, err)
		return
	}
	defer response.Body.Close()
	record.upstreamStatus = response.StatusCode
	record.stream = upstreamRequest.Stream
	h.relay(w, r, record, response)
	h.recordAttempt(r, record, 1, "", target.ProviderKey, target.Pair.ModelID, attemptStarted, response.StatusCode, "", record.usage)
}

// forwardAuto resolves model="auto" and forwards the result. It is separated from
// the specified-model path so the direct path keeps its single, obvious
// responsibility, and so the automatic path's extra steps (analysis, policy,
// one bounded retry) are visible in one place.
//
// The rewrite is identical to the explicit path: only the top-level model field
// changes, and everything else — nested objects, tools, reasoning and unknown
// vendor fields — is re-encoded from raw messages exactly as it arrived.
func (h *Handler) forwardAuto(w http.ResponseWriter, r *http.Request, protocol providers.Protocol, requestID string, record *requestLog, body []byte, payload map[string]json.RawMessage, preference analyzer.Preference, preferenceSource analyzer.PreferenceSource) {
	record.RoutingMode = routingModeAuto
	if h.autoRouter == nil {
		// An unassembled stack is a configuration state, not a routing outcome: it
		// is reported with the same code the orchestration layer uses so an
		// operator sees one vocabulary.
		record.fail(http.StatusServiceUnavailable, "routing_unavailable")
		writeError(w, http.StatusServiceUnavailable, "api_error", "routing_unavailable", "automatic routing is not available")
		return
	}
	requestSettings := h.requestSettings(r)
	var autoSettings *auto.RuntimeSettings
	if requestSettings.HasAutoSettings {
		snapshot := requestSettings.AutoSettings
		autoSettings = &snapshot
	}
	target, err := h.autoRouter.Route(r.Context(), auto.Request{
		Protocol: protocol, Body: body, Preference: preference,
		PreferenceSource: preferenceSource, RequestID: requestID, Runtime: autoSettings,
	})
	if err != nil {
		h.writeAutoError(w, record, err)
		return
	}
	record.applyRoutingDecision(target)
	stream := wantsStream(payload)
	// The attempt count is bounded by maxAutoUpstreamAttempts, and the bound is
	// enforced here rather than trusted to the router: the loop can only ever make
	// one more iteration than the last failure asked for.
	for attempt := 1; ; attempt++ {
		finalBody, err := setModel(payload, target.UpstreamModel)
		if err != nil {
			record.fail(http.StatusInternalServerError, "internal_error")
			writeError(w, http.StatusInternalServerError, "api_error", "internal_error", "the request could not be prepared")
			return
		}
		upstreamRequest := &providers.Request{
			Protocol:    protocol,
			ProviderKey: target.ProviderKey,
			Model:       target.UpstreamModel,
			Body:        finalBody,
			Header:      r.Header,
			ClientIP:    record.ClientIP,
			RequestID:   requestID,
			Stream:      stream,
		}
		attemptStarted := time.Now()
		response, err := h.executor.Do(r.Context(), upstreamRequest)
		if err != nil {
			h.recordAttempt(r, record, attempt, string(target.SelectedGroup), target.ProviderKey, target.Model, attemptStarted, 0, executorErrorCode(err), logging.Usage{})
			failure := err
			// The attempt bound is checked before the orchestrator is consulted, so
			// a router that would always say "retry" cannot turn this loop into an
			// unbounded one. attempt is the number of the attempt that just failed,
			// so reaching the bound means there is no next attempt.
			if attempt >= maxAutoUpstreamAttempts {
				h.writeExecutorError(w, record, failure)
				return
			}
			next, retry := h.autoRouter.Failover(r.Context(), target, failure)
			if !retry {
				h.writeExecutorError(w, record, failure)
				return
			}
			target = next
			record.FailoverUsed = true
			record.applyRoutingDecision(target)
			continue
		}
		defer response.Body.Close()
		record.upstreamStatus = response.StatusCode
		record.stream = stream
		// Once a status and a body have been relayed there is no second attempt:
		// the response may already be a stream, and splicing a new upstream
		// response into an answered request is exactly what stage 3 forbids.
		h.relay(w, r, record, response)
		h.recordAttempt(r, record, attempt, string(target.SelectedGroup), target.ProviderKey, target.Model, attemptStarted, response.StatusCode, "", record.usage)
		return
	}
}

func executorErrorCode(err error) string {
	switch {
	case errors.Is(err, providers.ErrUnsupportedConversion):
		return "unsupported_conversion"
	case errors.Is(err, providers.ErrProviderNotConfigured):
		return "provider_not_configured"
	case errors.Is(err, providers.ErrCanceled):
		return "client_closed_request"
	case errors.Is(err, providers.ErrUpstreamTimeout):
		return "upstream_timeout"
	default:
		return "upstream_unavailable"
	}
}

// recordAttempt uses the writer's optional attempt seam so existing embedded
// Recorder implementations remain compatible. It records actual upstream tries,
// including safe pre-request failures that led to a fallback.
func (h *Handler) recordAttempt(r *http.Request, request *requestLog, index int, group, provider, model string, started time.Time, status int, code string, usage logging.Usage) {
	if !h.routingLogEnabled(r) {
		return
	}
	recorder, ok := h.recorder.(interface{ RecordAttempt(logging.Attempt) })
	if !ok {
		return
	}
	completed := time.Now()
	if usage.Status == "" {
		usage.Status = logging.UsageStatusAbsent
	}
	recorder.RecordAttempt(logging.Attempt{
		RequestID: request.RequestID, AttemptIndex: index, GroupName: group,
		ProviderKey: provider, ModelID: model, StartedAt: started, CompletedAt: &completed,
		Status: status, ErrorCode: code, InputTokens: usage.InputTokens,
		OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens, UsageStatus: usage.Status,
	})
}

// writeAutoError maps an automatic routing failure onto the client contract. The
// refusal carries its own status and code when it is one of the orchestration
// layer's RefusalErrors; anything else is a process fault.
func (h *Handler) writeAutoError(w http.ResponseWriter, record *requestLog, err error) {
	if errors.Is(err, auto.ErrCanceled) {
		// The client is gone: there is nobody to tell, and writing a body here
		// would race with the already-aborted response.
		record.canceled()
		return
	}
	var refusal interface {
		error
		StatusCode() int
		ErrorCode() string
	}
	if errors.As(err, &refusal) {
		status, code := refusal.StatusCode(), refusal.ErrorCode()
		record.fail(status, code)
		writeError(w, status, errorTypeForStatus(status), code, refusal.Error())
		return
	}
	record.fail(http.StatusInternalServerError, "internal_error")
	writeError(w, http.StatusInternalServerError, "api_error", "internal_error", "the request could not be routed")
}

// writeExecutorError maps an executor failure onto the client contract.
func (h *Handler) writeExecutorError(w http.ResponseWriter, record *requestLog, err error) {
	switch {
	case errors.Is(err, providers.ErrUnsupportedConversion):
		record.fail(http.StatusUnprocessableEntity, "unsupported_conversion")
		writeError(w, http.StatusUnprocessableEntity, "invalid_request_error", "unsupported_conversion", "this provider cannot preserve the request semantics")
	case errors.Is(err, providers.ErrCanceled):
		// The client is gone: there is nobody to tell, and writing a body here
		// would race with the already-aborted response.
		record.canceled()
	case errors.Is(err, providers.ErrProviderNotConfigured):
		record.fail(http.StatusServiceUnavailable, "provider_not_configured")
		writeError(w, http.StatusServiceUnavailable, "api_error", "provider_not_configured", "the selected provider does not have a base_url configured")
	case errors.Is(err, providers.ErrGatewayNotConfigured):
		// Historical adapters may still return this sentinel. Normalize it to the
		// direct-provider client contract rather than exposing the retired gateway
		// topology.
		record.fail(http.StatusServiceUnavailable, "provider_not_configured")
		writeError(w, http.StatusServiceUnavailable, "api_error", "provider_not_configured", "the selected provider is not configured")
	case errors.Is(err, providers.ErrUpstreamTimeout):
		record.fail(http.StatusGatewayTimeout, "upstream_timeout")
		writeError(w, http.StatusGatewayTimeout, "api_error", "upstream_timeout", "the provider did not respond in time")
	default:
		record.fail(http.StatusBadGateway, "upstream_unavailable")
		writeError(w, http.StatusBadGateway, "api_error", "upstream_unavailable", "the provider could not be reached")
	}
}

// relay streams the provider response to the client. Status and headers are
// forwarded as received, including an error status and body; this boundary does
// not reshape provider errors.
func (h *Handler) relay(w http.ResponseWriter, r *http.Request, record *requestLog, response *providers.Response) {
	copyResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	controller := http.NewResponseController(w)
	// The observer is installed only when a routing log is configured, and it is
	// fed after every chunk has already been written and flushed: observation can
	// never delay, reorder or rewrite a forwarded byte, and a disabled log leaves
	// this call site empty.
	var observer *usageObserver
	if h.routingLogEnabled(r) {
		observer = newUsageObserver(record.protocol, response.Header.Get("Content-Type"), record.stream)
	}
	written, err := copyFlushing(w, response.Body, controller, observer.observe)
	record.BytesWritten = written
	if err != nil && clientGone(r, err) {
		record.canceled()
	}
	if observer != nil {
		record.usage = observer.finish(err != nil)
	}
}

// copyFlushing relays the body in bounded chunks and flushes after every write
// so an SSE event reaches the client as soon as the provider produced it. This
// is also applied to non-streaming bodies: a buffered writer would delay a
// large JSON response for no benefit.
func copyFlushing(w io.Writer, body io.Reader, controller *http.ResponseController, observe func([]byte)) (int64, error) {
	buffer := make([]byte, BufferSize)
	var written int64
	for {
		read, readErr := body.Read(buffer)
		if read > 0 {
			chunk := buffer[:read]
			n, writeErr := w.Write(chunk)
			written += int64(n)
			if writeErr != nil {
				return written, writeErr
			}
			if n != read {
				return written, io.ErrShortWrite
			}
			// A flush error means the connection is gone; the read loop would
			// fail on the next iteration anyway.
			if err := controller.Flush(); err != nil {
				return written, err
			}
			// Observation happens after the bytes are on their way to the client.
			// A chunk the client never received is still observed: usage is a
			// property of the upstream response, not of the delivery.
			if observe != nil {
				observe(chunk)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return written, nil
			}
			return written, readErr
		}
	}
}

// responseHeadersNeverRelayed are managed by the Go HTTP server; copying them
// would corrupt framing or duplicate a value the server computes itself.
var responseHeadersNeverRelayed = []string{
	"Content-Length",
	"Transfer-Encoding",
	"Trailer",
	"Connection",
	"Keep-Alive",
	"Proxy-Connection",
	"TE",
	"Upgrade",
	"Proxy-Authenticate",
}

// copyResponseHeaders relays end-to-end headers. Hop-by-hop headers and
// Connection-listed tokens are dropped, and Content-Length is dropped because
// the server recomputes framing for a flushed response.
func copyResponseHeaders(destination, source http.Header) {
	connectionTokens := headerTokens(source)
	for name, values := range source {
		canonical := http.CanonicalHeaderKey(name)
		if isHopByHop(canonical, connectionTokens) {
			continue
		}
		skip := false
		for _, blocked := range responseHeadersNeverRelayed {
			if canonical == blocked {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		for _, value := range values {
			destination.Add(canonical, value)
		}
	}
}

func isHopByHop(canonicalName string, connectionTokens map[string]struct{}) bool {
	for _, header := range responseHeadersNeverRelayed {
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
			if token == "" {
				continue
			}
			tokens[http.CanonicalHeaderKey(token)] = struct{}{}
		}
	}
	return tokens
}

// parsePreferenceHeader resolves the router-owned preference header and the
// configured default into the preference that applies to this request.
//
// The rule is deliberately small: each field value is normalized the same way
// (trimmed, lowercased), values that normalize to the same preference are
// accepted, and a conflict or an unknown value is a client error. The error
// message lists the accepted values and never repeats the client's own text,
// so arbitrary input cannot be pushed into an error body or the request log.
//
// defaultPreference is the resolved routing.default_preference. A configuration
// that never passed validation is rejected as a server error instead of being
// silently replaced with "balanced".
func parsePreferenceHeader(values []string, defaultPreference analyzer.Preference) (analyzer.Preference, analyzer.PreferenceSource, error) {
	var headerValue string
	if len(values) > 0 {
		headerValue = strings.Join(values, ",")
	}
	if strings.TrimSpace(headerValue) == "" {
		if !defaultPreference.Valid() {
			return "", "", fmt.Errorf("the configured default routing preference is not one of %s", strings.Join(analyzer.PreferenceValues(), ", "))
		}
		return defaultPreference, analyzer.SourceDefault, nil
	}
	resolved := analyzer.Preference("")
	for _, raw := range values {
		for _, candidate := range strings.Split(raw, ",") {
			if strings.TrimSpace(candidate) == "" {
				continue
			}
			preference, err := analyzer.ParsePreference(candidate)
			if err != nil {
				return "", "", err
			}
			if resolved != "" && resolved != preference {
				return "", "", fmt.Errorf("%s must not carry conflicting values: it must be one of %s", PreferenceHeader, strings.Join(analyzer.PreferenceValues(), ", "))
			}
			resolved = preference
		}
	}
	if resolved == "" {
		// Every field value was blank after trimming, which is the same as not
		// sending the header at all.
		if !defaultPreference.Valid() {
			return "", "", fmt.Errorf("the configured default routing preference is not one of %s", strings.Join(analyzer.PreferenceValues(), ", "))
		}
		return defaultPreference, analyzer.SourceDefault, nil
	}
	return resolved, analyzer.SourceHeader, nil
}

// requestMediaType enforces JSON input and rejects compressed bodies. Both
// checks exist so the model rewrite cannot silently operate on a body that is
// not what it appears to be.
func requestMediaType(r *http.Request) (string, bool) {
	if encoding := r.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return "Content-Encoding " + encoding + " is not supported", false
	}
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		mediaType := contentType
		if index := strings.IndexByte(mediaType, ';'); index >= 0 {
			mediaType = mediaType[:index]
		}
		mediaType = strings.TrimSpace(strings.ToLower(mediaType))
		if mediaType != "application/json" && mediaType != "text/json" {
			return "Content-Type must be application/json", false
		}
	}
	return "", true
}

type bodyTooLargeError struct{ limit int64 }

func (e *bodyTooLargeError) Error() string {
	return fmt.Sprintf("request body exceeds %d bytes", e.limit)
}

// readBody buffers the request body up to the configured limit. The rewrite
// requires a full JSON object, so the body cannot be streamed straight through;
// the limit makes that buffering bounded.
func readBody(r *http.Request, limit int64) ([]byte, error) {
	if r.ContentLength > limit {
		return nil, &bodyTooLargeError{limit: limit}
	}
	reader := io.LimitReader(r.Body, limit+1)
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, &bodyTooLargeError{limit: limit}
	}
	return body, nil
}

// rewriteModel decodes the request body, validates the model field and returns
// the model plus the raw JSON object so the caller can replace one key without
// re-encoding unknown fields.
func rewriteModel(body []byte) (string, map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return "", nil, errors.New("the request body must be a JSON object")
	}
	if trimmed[0] != '{' {
		return "", nil, errors.New("the request body must be a JSON object")
	}
	var payload map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	if err := decoder.Decode(&payload); err != nil {
		return "", nil, errors.New("the request body must be a JSON object")
	}
	if payload == nil {
		return "", nil, errors.New("the request body must be a JSON object")
	}
	rawModel, ok := payload["model"]
	if !ok {
		return "", nil, errors.New("the request body must contain a model field")
	}
	var model string
	if err := json.Unmarshal(rawModel, &model); err != nil {
		return "", nil, errors.New("the model field must be a string")
	}
	if strings.TrimSpace(model) == "" {
		return "", nil, errors.New("the model field must be a non-empty string")
	}
	return model, payload, nil
}

// setModel replaces only the top-level model field. Every other key, including
// tools, reasoning, response_format and unknown vendor extensions, keeps its
// decoded value: the map is re-encoded from raw messages, so no field is
// dropped, reordered into a different shape or type-coerced.
func setModel(payload map[string]json.RawMessage, model string) ([]byte, error) {
	encoded, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	payload["model"] = encoded
	return json.Marshal(payload)
}

// wantsStream reports whether the client asked for SSE. It is used for logging
// and for adapters that choose a streaming transport; the response relay itself
// is identical either way.
func wantsStream(payload map[string]json.RawMessage) bool {
	raw, ok := payload["stream"]
	if !ok {
		return false
	}
	var stream bool
	if err := json.Unmarshal(raw, &stream); err != nil {
		return false
	}
	return stream
}

// clientGone reports whether an error is explained by the client having
// disconnected, in which case no error body can be written.
func clientGone(r *http.Request, err error) bool {
	if r.Context().Err() != nil {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	return errors.Is(err, http.ErrAbortHandler) || isClosedConnection(err)
}

func isClosedConnection(err error) bool {
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	message := err.Error()
	return strings.Contains(message, "broken pipe") || strings.Contains(message, "connection reset by peer")
}

func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if index := strings.IndexByte(forwarded, ','); index >= 0 {
			return strings.TrimSpace(forwarded[:index])
		}
		return strings.TrimSpace(forwarded)
	}
	return r.RemoteAddr
}

// RequestIDFor returns the correlation identifier for a request: the inbound
// value when it is safe, a generated one otherwise. It is exported because the
// catalogue endpoint shares it, so every model path answers with the same
// hygiene and the same field name.
func RequestIDFor(r *http.Request) string {
	inbound := r.Header.Get(RequestIDHeader)
	if validRequestID(inbound) {
		return inbound
	}
	return newRequestID()
}

// newRequestID returns a random, log-safe correlation identifier. crypto/rand
// failures are practically impossible here, but a fallback keeps the handler
// from failing a request over an identifier.
func newRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "req-" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buf[:])
}

func validRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}

// ErrorTypeForStatus implements the shared error-type rule: client errors are
// invalid_request_error, a missing permission is permission_error, and server
// errors are api_error. It is exported because the debug endpoint answers with
// the same envelope and must not invent a second rule.
func ErrorTypeForStatus(status int) string {
	switch {
	case status == http.StatusForbidden:
		return "permission_error"
	case status >= 400 && status < 500:
		return "invalid_request_error"
	default:
		return "api_error"
	}
}

// errorTypeForStatus is the package-internal spelling used by the forwarding
// path.
func errorTypeForStatus(status int) string { return ErrorTypeForStatus(status) }

// errorBody is the OpenAI-compatible error envelope.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
	Param   any    `json:"param"`
}

// writeError writes the OpenAI error envelope. Messages are intentionally
// generic: they never contain the Provider base URL, a credential or an upstream
// response body.
func writeError(w http.ResponseWriter, status int, errorType, code, message string) {
	WriteError(w, status, errorType, code, message)
}

// WriteError writes the OpenAI error envelope. It is exported so the debug
// endpoint in internal/api answers with exactly the shape and headers every
// model path uses instead of reimplementing the rule.
func WriteError(w http.ResponseWriter, status int, errorType, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: errorDetail{
		Message: message,
		Type:    errorType,
		Code:    code,
		Param:   nil,
	}})
}

// requestLog is the request record. Field names are chosen to become the routing
// log schema in stage 8; nothing here is persisted yet. Every routing field is an
// identifier, a count, a duration or a closed enumeration: the record can carry
// the whole automatic-routing explanation without ever carrying request text.
type requestLog struct {
	RequestID      string
	Protocol       string
	ClientIP       string
	RequestedModel string
	Provider       string
	UpstreamModel  string
	Status         int
	ErrorCode      string
	// RoutingPreference and PreferenceSource are the resolved routing
	// preference and where it came from. They are not body content, which is why
	// they are safe to log; stage 8 reuses both field names in the routing log.
	RoutingPreference string
	PreferenceSource  string
	// The automatic routing fields. They stay zero or empty on the
	// specified-model path, which is how a routing log distinguishes a direct
	// forward from a routed one without a second event type.
	RoutingMode   string
	SelectionMode string
	RoutingModel  string
	// EffectiveModel is the logical model that actually took effect: the resolved
	// logical model on the explicit path and the decision's model on the automatic
	// path. It is empty until a destination resolved, which is how the routing log
	// distinguishes "the request had no destination" from "the destination was this
	// model".
	EffectiveModel string
	// JevTopModel is the top-1 entry of the Jev distribution of this request's
	// single recommendation. It is empty unless the call succeeded, and it is set
	// from the recommendation itself rather than from the response or the prompt.
	JevTopModel     string
	JevStatus       string
	Confidence      *float64
	ConfidenceBand  string
	FallbackReason  string
	EvidenceHash    string
	GatewayAttempts int
	FailoverUsed    bool
	RoutingLatency  time.Duration
	JevLatency      time.Duration
	// protocol and stream are the request's own protocol and stream flag. They are
	// stored on the record rather than passed around because the usage observer
	// needs both and is built inside the relay.
	protocol providers.Protocol
	// usage is the passively observed token usage. It is zero (absent) until the
	// relay finished, and it is never synthesized: a missing count stays nil.
	usage logging.Usage
	// jevTrace is the optional Jev diagnostic trace of the automatic path. It is
	// nil on the specified-model path and when the trace is disabled.
	jevTrace *logging.JevTrace
	// routed reports whether a routing decision was made for this request. It is
	// what decides the presence of routing_latency_ms in the stored row: the column
	// is NULL when no decision happened, and 0 when one happened inside the same
	// millisecond. "Did it happen" is not "was it at least a millisecond long".
	routed bool

	upstreamStatus int
	stream         bool
	BytesWritten   int64
	canceledFlag   bool
}

// recordEvent converts the request record into a routing log event. The mapping is
// total and one-directional: every stored column has exactly one source field, and
// the only field the record holds that is deliberately not stored is RoutingModel,
// which is identically equal to RequestedModel on the automatic path and is
// distinguished by RoutingMode.
func (l *requestLog) recordEvent(started time.Time, duration time.Duration) logging.Event {
	event := logging.Event{
		RequestID:         l.RequestID,
		StartedAt:         started,
		Protocol:          l.Protocol,
		RoutingMode:       l.RoutingMode,
		SelectionMode:     l.SelectionMode,
		RequestedModel:    l.RequestedModel,
		EffectiveModel:    l.EffectiveModel,
		JevTopModel:       l.JevTopModel,
		ProviderKey:       l.Provider,
		UpstreamModel:     l.UpstreamModel,
		ErrorCode:         l.ErrorCode,
		Status:            l.finalStatus(),
		UpstreamStatus:    l.upstreamStatus,
		Stream:            l.stream,
		ClientIP:          l.ClientIP,
		RoutingPreference: l.RoutingPreference,
		PreferenceSource:  l.PreferenceSource,
		JevStatus:         l.JevStatus,
		ConfidenceBand:    l.ConfidenceBand,
		FallbackReason:    l.FallbackReason,
		EvidenceHash:      l.EvidenceHash,
		Confidence:        l.Confidence,
		GatewayAttempts:   l.GatewayAttempts,
		FailoverUsed:      l.FailoverUsed,
		DurationMS:        duration.Milliseconds(),
		BytesWritten:      l.BytesWritten,
		Usage:             l.usage,
		Jev:               l.jevTrace,
	}
	if event.Usage.Status == "" {
		// A request that never reached a relay has no observation at all. The
		// stored status is still "absent" rather than empty: the column is a closed
		// set, and "nothing was seen" is a fact worth recording.
		event.Usage.Status = logging.UsageStatusAbsent
	}
	if l.routed {
		latency := l.RoutingLatency.Milliseconds()
		event.RoutingLatencyMS = &latency
	}
	// The Jev latency is taken from the trace, which records that a call was made
	// even when it finished inside the same millisecond. Reading it from the
	// duration would report a sub-millisecond call as "no call".
	if l.jevTrace != nil && l.jevTrace.LatencyMS != nil {
		latency := *l.jevTrace.LatencyMS
		event.JevLatencyMS = &latency
	}
	return event
}

// recordEvent hands one request to the routing log. A nil recorder is the
// documented "routing logging is off" state and costs one nil check; a recorder
// that panics or blocks would be a bug in the writer, which is designed never to
// block and never to return an error.
func (h *Handler) recordEvent(r *http.Request, record *requestLog, started time.Time, duration time.Duration) {
	snapshot := h.requestSettings(r)
	if !h.routingLogEnabled(r) {
		return
	}
	event := record.recordEvent(started, duration)
	if recorder, ok := h.recorder.(interface {
		RecordWithSettings(logging.Event, bool, bool, bool)
	}); ok {
		recorder.RecordWithSettings(event, true, snapshot.StoreClientIP, snapshot.JevTraceEnabled)
		return
	}
	h.recorder.Record(event)
}

// finalStatus reports the status the request ended with: the status the boundary
// wrote, or the upstream status when the relay forwarded one unchanged.
func (l *requestLog) finalStatus() int {
	if l.Status != 0 {
		return l.Status
	}
	return l.upstreamStatus
}

// applyRoutingDecision copies the routing trace onto the log record. It is called
// once per resolved target, so a failover overwrites the selection fields with
// the target that was actually used and leaves the attempt count monotonic.
func (l *requestLog) applyRoutingDecision(target auto.Target) {
	l.Provider = target.ProviderKey
	l.UpstreamModel = target.UpstreamModel
	l.RoutingModel = target.RoutingModel
	// The effective model is the decision's logical model, not the client's
	// requested value: on the automatic path the client asked for "auto", and the
	// fact worth recording is which model that resolved to.
	l.EffectiveModel = target.Model
	l.JevTopModel = jevTopModel(target)
	l.SelectionMode = target.SelectionMode
	l.Confidence = floatPointer(target.Decision.Confidence)
	l.ConfidenceBand = string(target.Decision.ConfidenceBand)
	l.FallbackReason = string(target.Decision.Fallback.Reason)
	l.EvidenceHash = target.Decision.EvidenceHash
	l.JevStatus = target.JevStatus
	l.GatewayAttempts = target.Attempts
	l.RoutingLatency = target.RoutingLatency
	l.JevLatency = target.JevLatency
	l.routed = true
	// The trace is metadata only: identifiers, counts, durations and the
	// normalized distribution. The routing log stores it when its own switch is
	// on, and this record never carries anything the trace does not.
	l.jevTrace = jevTrace(target)
}

// jevTrace converts the orchestration layer's trace into the logging record's own
// type. The two are separate types on purpose: internal/proxy must not make the
// routing log depend on the router, and internal/logging must not import it
// either. The mapping is total and field-for-field.
func jevTrace(target auto.Target) *logging.JevTrace {
	if target.JevTrace == nil {
		return nil
	}
	source := target.JevTrace
	trace := &logging.JevTrace{
		Status:          source.Status,
		FailureReason:   source.FailureReason,
		InputMode:       source.InputMode,
		Selected:        source.Selected,
		ConfidenceBand:  source.ConfidenceBand,
		FallbackReason:  source.FallbackReason,
		EvidenceHash:    source.EvidenceHash,
		LatencyMS:       source.LatencyMS,
		CandidateModels: source.CandidateModels,
		ModelCount:      source.ModelCount,
		CandidateCount:  source.CandidateCount,
		Confidence:      source.Confidence,
	}
	if len(source.Probabilities) > 0 {
		trace.Probabilities = make([]logging.ModelProbability, 0, len(source.Probabilities))
		for _, probability := range source.Probabilities {
			trace.Probabilities = append(trace.Probabilities, logging.ModelProbability{
				Model:       probability.Model,
				Probability: probability.Probability,
			})
		}
	}
	return trace
}

// jevTopModel reports the top-1 logical model of one recommendation, or the
// empty string when the call produced no distribution. The distribution is
// already ordered by descending probability with the model identifier ascending
// as the tie-break, so the first entry is the top entry by definition.
//
// This is deliberately independent of routing.log.jev_trace.enabled: that switch
// decides whether the diagnostic distribution row is persisted, not what the call
// returned, and the top-1 fact belongs to the event rather than to the trace.
func jevTopModel(target auto.Target) string {
	if target.JevTrace == nil || target.JevTrace.Status != auto.JevStatusOK {
		return ""
	}
	if len(target.JevTrace.Probabilities) == 0 {
		return ""
	}
	return target.JevTrace.Probabilities[0].Model
}

// floatPointer copies a float for the log record so a zero confidence is reported
// as 0 rather than omitted as "absent".
func floatPointer(value float64) *float64 {
	copy := value
	return &copy
}

func (l *requestLog) fail(status int, code string) {
	l.Status = status
	l.ErrorCode = code
}

func (l *requestLog) canceled() {
	l.canceledFlag = true
	if l.Status == 0 {
		l.Status = 499 // client closed request; no HTTP status is written
	}
	l.ErrorCode = "client_closed_request"
}

func (l *requestLog) emit(logger *slog.Logger, started time.Time) {
	if logger == nil {
		return
	}
	status := l.finalStatus()
	attributes := []any{
		"request_id", l.RequestID,
		"protocol", l.Protocol,
		"requested_model", l.RequestedModel,
		"provider", l.Provider,
		"upstream_model", l.UpstreamModel,
		"status", status,
		"stream", l.stream,
		"bytes_written", l.BytesWritten,
		"latency_ms", time.Since(started).Milliseconds(),
	}
	if l.RoutingPreference != "" {
		attributes = append(attributes, "routing_preference", l.RoutingPreference, "preference_source", l.PreferenceSource)
	}
	if l.RoutingMode != "" {
		// The routing trace is one block: it is only meaningful together, and a
		// routing log reader should be able to query it as a unit.
		attributes = append(attributes,
			"routing_mode", l.RoutingMode,
			"selection_mode", l.SelectionMode,
			"routing_model", l.RoutingModel,
			"jev_status", l.JevStatus,
			"confidence_band", l.ConfidenceBand,
			"fallback_reason", l.FallbackReason,
			"evidence_hash", l.EvidenceHash,
			"gateway_attempts", l.GatewayAttempts,
			"failover_used", l.FailoverUsed,
			"routing_latency_ms", l.RoutingLatency.Milliseconds(),
		)
		if l.Confidence != nil {
			attributes = append(attributes, "confidence", *l.Confidence)
		}
		if l.JevLatency != 0 {
			attributes = append(attributes, "jev_latency_ms", l.JevLatency.Milliseconds())
		}
	}
	if l.upstreamStatus != 0 && l.upstreamStatus != status {
		attributes = append(attributes, "upstream_status", l.upstreamStatus)
	}
	if l.ErrorCode != "" {
		attributes = append(attributes, "error_code", l.ErrorCode)
	}
	switch {
	case status >= 500:
		logger.Error("model request failed", attributes...)
	case status >= 400:
		logger.Warn("model request rejected", attributes...)
	default:
		logger.Info("model request completed", attributes...)
	}
}
