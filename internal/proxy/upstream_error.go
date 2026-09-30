package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/nextroad-dev/Auto-Router/internal/logging"
	"github.com/nextroad-dev/Auto-Router/internal/router/auto"
)

// maxUpstreamErrorCaptureBytes bounds how much of a non-2xx upstream body is held
// to extract its error message. Provider error envelopes are a few hundred bytes;
// a larger body is almost certainly not an envelope, and its opening is enough.
const maxUpstreamErrorCaptureBytes = 4 << 10

// errorCapture keeps the opening bytes of an error body. It is fed the chunks the
// client was already given, like the usage observer, so capturing never changes
// what is relayed. A nil capture ignores every call.
type errorCapture struct {
	buffer []byte
}

func (c *errorCapture) observe(chunk []byte) {
	if c == nil {
		return
	}
	room := maxUpstreamErrorCaptureBytes - len(c.buffer)
	if room <= 0 {
		return
	}
	if len(chunk) > room {
		chunk = chunk[:room]
	}
	c.buffer = append(c.buffer, chunk...)
}

func (c *errorCapture) detail() string {
	if c == nil {
		return ""
	}
	return upstreamErrorDetail(c.buffer)
}

// readErrorOpening reads the opening of an error body that will not be relayed.
// A read error just ends the excerpt early: the detail is diagnostic, and a
// partial one is still better than none.
func readErrorOpening(body io.Reader) []byte {
	opening, _ := io.ReadAll(io.LimitReader(body, maxUpstreamErrorCaptureBytes))
	return opening
}

// upstreamErrorDetail extracts a short, printable, redacted excerpt from an
// upstream error body. The OpenAI-style envelope is preferred, with its code and
// param appended because they are usually what names the offending field; any
// other body falls back to its raw opening. The result is at most
// logging.MaxAttemptErrorDetailBytes and always valid UTF-8.
func upstreamErrorDetail(body []byte) string {
	body = bytes.TrimSpace(body)
	// A streamed request can be answered with an SSE-framed error; its first data
	// line is the envelope.
	if payload, ok := firstSSEData(body); ok {
		body = payload
	}
	if len(body) == 0 {
		return ""
	}
	text := envelopeMessage(body)
	if text == "" {
		text = string(body)
	}
	text = auto.RedactText(strings.TrimSpace(printable(text)))
	return truncateToRune(text, logging.MaxAttemptErrorDetailBytes)
}

// envelopeMessage reads the common error envelopes: {"error":{"message",...}},
// {"error":"..."}, {"message":"..."} and {"detail":"..."}. It returns "" for
// anything else, including a body that is not JSON.
func envelopeMessage(body []byte) string {
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	if len(envelope.Error) > 0 {
		var text string
		if json.Unmarshal(envelope.Error, &text) == nil && text != "" {
			return text
		}
		var object struct {
			Message string          `json:"message"`
			Type    string          `json:"type"`
			Code    json.RawMessage `json:"code"`
			Param   string          `json:"param"`
		}
		if json.Unmarshal(envelope.Error, &object) == nil && object.Message != "" {
			var qualifiers []string
			if code := scalarText(object.Code); code != "" {
				qualifiers = append(qualifiers, "code="+code)
			} else if object.Type != "" {
				qualifiers = append(qualifiers, "type="+object.Type)
			}
			if object.Param != "" {
				qualifiers = append(qualifiers, "param="+object.Param)
			}
			if len(qualifiers) == 0 {
				return object.Message
			}
			return object.Message + " (" + strings.Join(qualifiers, ", ") + ")"
		}
	}
	if envelope.Message != "" {
		return envelope.Message
	}
	var detail string
	if json.Unmarshal(envelope.Detail, &detail) == nil {
		return detail
	}
	return ""
}

// scalarText renders a JSON string or number; providers disagree on which one an
// error code is.
func scalarText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return ""
}

// firstSSEData returns the payload of the first "data:" line when body is
// SSE-framed.
func firstSSEData(body []byte) ([]byte, bool) {
	if !bytes.HasPrefix(body, []byte("data:")) && !bytes.HasPrefix(body, []byte("event:")) {
		return nil, false
	}
	for _, line := range bytes.Split(body, []byte("\n")) {
		if payload, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:")); ok {
			return bytes.TrimSpace(payload), true
		}
	}
	return nil, false
}

// printable replaces control characters and invalid UTF-8 so the excerpt is safe
// for a log line and a JSON field.
func printable(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))
	for _, r := range text {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			r = ' '
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

// truncateToRune cuts text to at most limit bytes without splitting a rune.
func truncateToRune(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return strings.TrimSpace(text[:cut])
}
