package adapter

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
)

// maxSSELineBytes bounds one SSE line and maxSSEEventBytes one reassembled
// event, so a hostile upstream cannot make the converter buffer without limit.
const (
	maxSSELineBytes  = 2 << 20
	maxSSEEventBytes = 4 << 20
)

var errSSEEventTooLarge = errors.New("upstream SSE event exceeds the size limit")

// sseEvent is one dispatched server-sent event.
type sseEvent struct {
	Event string
	Data  string
	ID    string
}

// sseReader parses the text/event-stream format:
//
//   - lines end in LF, CRLF or a lone CR, and may be split across reads;
//   - a blank line dispatches the pending event;
//   - multiple data fields are joined with "\n", as the format requires;
//   - one leading space after the colon is removed, nothing else is trimmed;
//   - comment lines (leading ':') and unknown fields such as retry are ignored;
//   - an event with no data field is not dispatched.
//
// Unlike a browser, a pending event that has data when the stream ends is still
// dispatched: several providers omit the final blank line, and dropping their
// last event would lose the finish reason.
type sseReader struct {
	scanner *bufio.Scanner
	data    strings.Builder
	hasData bool
	event   string
	id      string
	done    bool
}

func newSSEReader(source io.Reader) *sseReader {
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 4096), maxSSELineBytes)
	scanner.Split(scanSSELines)
	return &sseReader{scanner: scanner}
}

// Next returns the next event, or io.EOF once the stream is exhausted.
func (s *sseReader) Next() (sseEvent, error) {
	if s.done {
		return sseEvent{}, io.EOF
	}
	for s.scanner.Scan() {
		line := s.scanner.Text()
		if line == "" {
			if event, ok := s.dispatch(); ok {
				return event, nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if found {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "data":
			if s.hasData {
				s.data.WriteByte('\n')
			}
			s.data.WriteString(value)
			s.hasData = true
			if s.data.Len() > maxSSEEventBytes {
				s.done = true
				return sseEvent{}, errSSEEventTooLarge
			}
		case "event":
			s.event = value
		case "id":
			if !strings.ContainsRune(value, 0) {
				s.id = value
			}
		}
	}
	s.done = true
	if err := s.scanner.Err(); err != nil {
		return sseEvent{}, err
	}
	if event, ok := s.dispatch(); ok {
		return event, nil
	}
	return sseEvent{}, io.EOF
}

func (s *sseReader) dispatch() (sseEvent, bool) {
	defer func() {
		s.data.Reset()
		s.hasData = false
		s.event = ""
	}()
	if !s.hasData {
		return sseEvent{}, false
	}
	return sseEvent{Event: s.event, Data: s.data.String(), ID: s.id}, true
}

// scanSSELines is bufio.ScanLines extended with a lone CR as a line terminator.
// A CR at the end of the buffer waits for more input so a CRLF split across two
// reads is still one terminator.
func scanSSELines(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if index := bytes.IndexAny(data, "\r\n"); index >= 0 {
		if data[index] == '\n' {
			return index + 1, data[:index], nil
		}
		if index+1 < len(data) {
			if data[index+1] == '\n' {
				return index + 2, data[:index], nil
			}
			return index + 1, data[:index], nil
		}
		if atEOF {
			return index + 1, data[:index], nil
		}
		return 0, nil, nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}
