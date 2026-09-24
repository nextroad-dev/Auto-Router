package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"

	"github.com/nextroad-dev/Auto-Router/internal/logging"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
)

// The observer's bounds. They are constants rather than settings for the same
// reason the analyzer's are: observation is passive, its cost must be
// predictable, and a deployment that could raise a bound could make one large
// response cost more memory than the response itself.
const (
	// maxObservedResponseBytes bounds how much of a non-streaming body is
	// accumulated for usage extraction. A body larger than this is reported as
	// oversized and observation stops; the client still receives every byte.
	maxObservedResponseBytes = 1 << 20
	// maxObservedSSEEventBytes bounds one SSE event. A larger event is not a
	// usage carrier in any protocol this router speaks, so the observer stops
	// instead of accumulating it.
	maxObservedSSEEventBytes = 16 << 10
	// maxObservedSSEEvents bounds how many SSE events one response may contain
	// before observation stops. It is large enough for a long reasoning stream
	// and small enough that a stuck upstream cannot make the observer grow.
	maxObservedSSEEvents = 4096
	// eventSeparator is the blank line that ends one SSE event.
	eventSeparator = "\n\n"
)

// The only keys read from a usage object. Anything else in it is ignored.
const (
	usageKey             = "usage"
	usageInputTokensKey  = "input_tokens"
	usageOutputTokensKey = "output_tokens"
	usageTotalTokensKey  = "total_tokens"
	// usagePromptKey and usageCompletionKey are the other spelling of the same two
	// counts. Both APIs this router speaks use the first spelling; the second is
	// accepted because a Provider may normalize the other way and reading it costs
	// nothing.
	usagePromptKey     = "prompt_tokens"
	usageCompletionKey = "completion_tokens"
	// streamDoneSentinel is the SSE framing token that is not JSON.
	streamDoneSentinel = "[DONE]"
)

// usageObserver passively reads the token usage of one upstream response. It has
// no I/O, no shared state and bounded memory: it is fed the same chunks that were
// already written to the client, so observing cannot delay, reorder or rewrite a
// single forwarded byte.
//
// It is not an interface and it deliberately answers no question the response
// depends on. Every method is safe to call on a nil receiver, which is how "no
// observer was installed" reads at the call site.
type usageObserver struct {
	// stream selects the incremental SSE reader.
	stream bool
	// source is the usage source recorded once something has been read. It is
	// derived from the protocol and the framing, never from the payload.
	source string

	mutex     sync.Mutex
	state     usageState
	usage     logging.Usage
	buffer    []byte
	events    int
	malformed bool
}

// usageState is the observer's progress. A terminal state never changes again.
type usageState int

const (
	// usageObserving means the observer is still reading.
	usageObserving usageState = iota
	// usageStopped means a bound was hit: the result is oversized.
	usageStopped
)

// newUsageObserver builds an observer for one response. The framing decides the
// reader: an SSE response is scanned incrementally, anything else is accumulated
// up to the byte bound. A caller passes the upstream Content-Type and the
// client's stream request, because either one can select the framing.
func newUsageObserver(protocol providers.Protocol, contentType string, stream bool) *usageObserver {
	framed := stream || strings.Contains(strings.ToLower(contentType), "text/event-stream")
	source := logging.UsageSourceChatCompletions
	if protocol == providers.ProtocolResponses {
		source = logging.UsageSourceResponses
	}
	if framed {
		if source == logging.UsageSourceChatCompletions {
			source = logging.UsageSourceStreamChatCompletions
		} else {
			source = logging.UsageSourceStreamResponses
		}
	}
	return &usageObserver{stream: framed, source: source, usage: logging.Usage{Status: logging.UsageStatusAbsent}}
}

// observe feeds one chunk of response bytes. It must be called with the bytes the
// client has already been given.
func (o *usageObserver) observe(chunk []byte) {
	if o == nil || len(chunk) == 0 {
		return
	}
	o.mutex.Lock()
	defer o.mutex.Unlock()
	if o.state != usageObserving {
		return
	}
	if o.stream {
		o.observeStream(chunk)
		return
	}
	if len(o.buffer)+len(chunk) > maxObservedResponseBytes {
		// The body is larger than the observation window. The client still gets
		// every byte; the record reports that usage could not be seen.
		o.state = usageStopped
		o.buffer = nil
		return
	}
	o.buffer = append(o.buffer, chunk...)
}

// observeStream scans complete SSE events out of the incremental buffer. An event
// is terminated by a blank line, so a chunk boundary inside an event is handled by
// holding the partial event until its terminator arrives.
func (o *usageObserver) observeStream(chunk []byte) {
	o.buffer = append(o.buffer, chunk...)
	for {
		index := indexOfEventSeparator(o.buffer)
		if index < 0 {
			if len(o.buffer) > maxObservedSSEEventBytes {
				o.state = usageStopped
				o.buffer = nil
			}
			return
		}
		event := o.buffer[:index]
		o.buffer = o.buffer[index:]
		o.events++
		if o.events > maxObservedSSEEvents {
			o.state = usageStopped
			o.buffer = nil
			return
		}
		o.recordEventPayloads(event)
	}
}

// indexOfEventSeparator returns the offset just past the blank line that ends one
// SSE event, or -1 when the buffer does not yet contain a complete event. Both
// "\n\n" and "\r\n\r\n" terminate an event; the earliest one wins so a CRLF stream
// is not mistaken for one long event.
func indexOfEventSeparator(buffer []byte) int {
	lf := bytes.Index(buffer, []byte(eventSeparator))
	crlf := bytes.Index(buffer, []byte("\r\n\r\n"))
	switch {
	case lf < 0 && crlf < 0:
		return -1
	case crlf >= 0 && (lf < 0 || crlf < lf):
		return crlf + 4
	default:
		return lf + 2
	}
}

// recordEventPayloads reads the JSON payloads of one SSE event. Only data lines
// carry payloads; every other field (event, id, retry, comments) is ignored.
func (o *usageObserver) recordEventPayloads(event []byte) {
	for _, line := range strings.Split(string(event), "\n") {
		line = strings.TrimSuffix(line, "\r")
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" || payload == streamDoneSentinel {
			continue
		}
		o.recordPayload([]byte(payload), false)
	}
}

// recordPayload reads the usage of one JSON payload. It looks in both places a
// usage object can appear: a chat-completions chunk carries a top-level "usage",
// and a Responses event carries "response.usage". Trying both is safe because they
// can never occur in the same payload.
//
// strict selects what a payload that is not JSON means. For a whole non-streaming
// body it is malformed: the two APIs this router speaks answer with a JSON object,
// and a body that does not parse cannot be read at all. For one SSE event it is
// legal: a comment, a keep-alive or an event type this build does not understand is
// still a valid stream, and the record only claims what it actually read.
func (o *usageObserver) recordPayload(payload []byte, strict bool) {
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(payload, &decoded); err != nil {
		if strict {
			o.malformed = true
		}
		return
	}
	if raw, ok := decoded[usageKey]; ok {
		o.recordUsage(raw)
		return
	}
	if raw, ok := decoded["response"]; ok {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(raw, &nested); err != nil {
			return
		}
		if nestedUsage, ok := nested[usageKey]; ok {
			o.recordUsage(nestedUsage)
		}
	}
}

// recordUsage parses one usage object. Only the recognized keys are read; a value
// that is not a non-negative whole number marks the observation as malformed, and
// is never rounded, coerced or reported as zero.
func (o *usageObserver) recordUsage(raw json.RawMessage) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		o.malformed = true
		return
	}
	// The source is recorded once, when a usage object is actually reached, so a
	// response without one reports no source rather than a guessed one.
	o.usage.Source = o.source
	o.usage.InputTokens = o.readCount(fields, usageInputTokensKey, usagePromptKey, o.usage.InputTokens)
	o.usage.OutputTokens = o.readCount(fields, usageOutputTokensKey, usageCompletionKey, o.usage.OutputTokens)
	o.usage.TotalTokens = o.readCount(fields, usageTotalTokensKey, "", o.usage.TotalTokens)
}

// readCount reads one count from a usage object, falling back to the alternative
// spelling. A present but unusable value marks the observation malformed; an
// absent key leaves the previous value untouched, so a later event or a later
// field can still supply it.
func (o *usageObserver) readCount(fields map[string]json.RawMessage, key, fallbackKey string, previous *int64) *int64 {
	raw, ok := fields[key]
	if !ok && fallbackKey != "" {
		raw, ok = fields[fallbackKey]
	}
	if !ok {
		return previous
	}
	value, ok := decodeCount(raw)
	if !ok {
		o.malformed = true
		return previous
	}
	return &value
}

// decodeCount accepts a non-negative whole number, and nothing else. A float, a
// string, a negative number, a boolean or a null is malformed: reporting it as
// zero would be an invented measurement.
func decodeCount(raw json.RawMessage) (int64, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] == '"' {
		// A quoted number is a string, not a count. json.Number would accept it,
		// because it is itself a string type, so the shape is checked first.
		return 0, false
	}
	var number json.Number
	if err := json.Unmarshal(trimmed, &number); err != nil {
		return 0, false
	}
	value, err := number.Int64()
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

// finish reports the observer's result and closes it. interrupted is true when the
// relay ended in an error or a cancellation, which is a different fact from a
// response that simply carried no usage.
//
// The reported statuses are exclusive and ordered: an observation that stopped at
// a bound is oversized, an observation that read at least one count is observed
// (even if a later field was malformed), a completed observation that read nothing
// from a malformed object is malformed, an interrupted relay that read nothing is
// interrupted, and everything else is absent.
func (o *usageObserver) finish(interrupted bool) logging.Usage {
	if o == nil {
		return logging.Usage{Status: logging.UsageStatusAbsent}
	}
	o.mutex.Lock()
	defer o.mutex.Unlock()
	if o.state == usageStopped {
		return logging.Usage{Status: logging.UsageStatusOversized}
	}
	// The buffered tail is only meaningful for a non-streaming body: the stream
	// path already handled every complete event, and an unterminated remainder
	// cannot be trusted to be a whole payload.
	if !o.stream && len(o.buffer) > 0 {
		o.recordPayload(o.buffer, true)
		o.buffer = nil
	}
	usage := o.usage
	if usage.InputTokens != nil || usage.OutputTokens != nil || usage.TotalTokens != nil {
		usage.Status = logging.UsageStatusObserved
		return usage
	}
	switch {
	case o.malformed:
		usage.Status = logging.UsageStatusMalformed
	case interrupted:
		usage.Status = logging.UsageStatusInterrupted
	default:
		usage.Status = logging.UsageStatusAbsent
	}
	return usage
}
