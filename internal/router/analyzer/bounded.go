package analyzer

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"unicode/utf8"
)

// This file holds the bounded JSON reader the feature walk is built on.
//
// The reader exists instead of plain struct decoding for three reasons:
//
//  1. Bounds. A 16 MiB body with a deeply nested or enormous array must not be
//     able to make one routing decision allocate without limit or recurse into
//     a stack overflow.
//  2. Unknown-field tolerance. Protocol DTOs would silently drop the vendor
//     extensions and newer fields that stage 5 must keep working with; a
//     generic walk sees every value and merely refuses to be surprised by it.
//  3. Bounded work per value. Values are skipped past without being
//     materialized once the retention budget is gone, so a huge unknown blob
//     costs a scan, not an allocation.
//
// A decode failure is never an error to the caller: a body the reader cannot
// understand yields zero-value features, because content-level failures are the
// HTTP boundary's business (400 there) and never the analyzer's.

// rawKind classifies a retained JSON value.
type rawKind uint8

const (
	kindInvalid rawKind = iota
	kindNull
	kindBool
	kindNumber
	kindString
	kindArray
	kindObject
)

// node is one retained JSON value. Objects keep their members as individual
// nodes so unknown and duplicate keys stay visible; arrays keep their elements.
type node struct {
	kind rawKind
	raw  json.RawMessage // null, bool, number and string values, encoded
	// members holds an object's members and array holds an array's elements.
	members  []node
	elements []node
	// key is set only on an object member.
	key string
}

// parser is a bounded document reader. It reads one JSON value with the
// standard decoder but retains only what fits inside the analyzer's budgets:
// the retention cap, the depth cap and the analyzed window. Everything beyond a
// budget is skipped past (still validating the encoding) instead of being kept,
// and truncated records that the document was not read to its end.
type parser struct {
	decoder *json.Decoder
	// retained counts the values kept so far, against maxRetainedNodes.
	retained int
	// truncated records that a budget, not the document, ended the read.
	truncated bool
	// windowTruncated records that the byte window itself was cut, which turns
	// a mid-document EOF from a parse failure into "the prefix is all there is".
	windowTruncated bool
	// cut records that the analyzed window ended inside a value. Everything
	// already read keeps its members, so the features found before the cut are
	// still reported instead of being thrown away with the broken tail.
	cut bool
}

func newParser(window []byte, windowTruncated bool) *parser {
	decoder := json.NewDecoder(bytes.NewReader(window))
	// Primitives are the only tokens this reader decodes directly; numbers stay
	// raw so nothing is routed through float64 on the way to a feature.
	decoder.UseNumber()
	return &parser{decoder: decoder, windowTruncated: windowTruncated}
}

// document reads the bounded form of the whole body. It reports false when the
// body is not a single well-formed JSON value, which the caller turns into
// zero-value features rather than an error.
func (p *parser) document() (node, bool) {
	root, ok := p.value(0)
	if !ok {
		if !p.cut {
			return node{}, false
		}
		// The window ended inside the document. Everything retained up to the
		// cut is still a usable view of the request prefix.
		return node{kind: kindObject}, true
	}
	if root.kind == kindInvalid {
		// The first value was already past a retention or depth budget.
		return node{kind: kindObject}, true
	}
	// Trailing content means the body is not one document. Nothing beyond it is
	// analyzed, and the caller is told the view is incomplete.
	if _, err := p.decoder.Token(); err == nil {
		p.truncated = true
	}
	return root, true
}

// value reads one JSON value. A value skipped because a budget was reached is
// returned as a kindInvalid node with ok=true, so an enclosing array keeps
// walking instead of mistaking a budget stop for a syntax error.
func (p *parser) value(depth int) (node, bool) {
	if p.retained >= maxRetainedNodes || depth > maxParseDepth {
		if err := p.skipValue(); err != nil {
			p.decodeFailed()
			return node{}, false
		}
		p.truncated = true
		return node{}, true
	}
	token, err := p.decoder.Token()
	if err != nil {
		p.decodeFailed()
		return node{}, false
	}
	p.retained++
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			return p.object(depth)
		case '[':
			return p.array(depth)
		default:
			// A closing delimiter here means the document is unbalanced.
			return node{}, false
		}
	case json.Number:
		return node{kind: kindNumber, raw: json.RawMessage(typed.String())}, true
	case string:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return node{}, false
		}
		return node{kind: kindString, raw: encoded}, true
	case bool:
		if typed {
			return node{kind: kindBool, raw: json.RawMessage("true")}, true
		}
		return node{kind: kindBool, raw: json.RawMessage("false")}, true
	case nil:
		return node{kind: kindNull, raw: json.RawMessage("null")}, true
	default:
		return node{}, false
	}
}

func (p *parser) object(depth int) (node, bool) {
	result := node{kind: kindObject, members: []node{}}
	for p.decoder.More() {
		key, err := p.decoder.Token()
		if err != nil {
			p.decodeFailed()
			return p.partial(result)
		}
		name, ok := key.(string)
		if !ok {
			return node{}, false
		}
		member, ok := p.value(depth + 1)
		if !ok {
			return p.partial(result)
		}
		if member.kind == kindInvalid {
			// The retention budget was reached; the member name is known but
			// its value was not kept.
			continue
		}
		member.key = name
		result.members = append(result.members, member)
	}
	if _, err := p.decoder.Token(); err != nil {
		p.decodeFailed()
		return p.partial(result)
	}
	return result, true
}

func (p *parser) array(depth int) (node, bool) {
	result := node{kind: kindArray, elements: []node{}}
	for p.decoder.More() {
		element, ok := p.value(depth + 1)
		if !ok {
			return p.partial(result)
		}
		if element.kind == kindInvalid {
			continue
		}
		result.elements = append(result.elements, element)
	}
	if _, err := p.decoder.Token(); err != nil {
		p.decodeFailed()
		return p.partial(result)
	}
	return result, true
}

// partial ends a container early. It keeps the members or elements that were
// already read so the features found before the end are still reported.
func (p *parser) partial(result node) (node, bool) {
	if !p.cut {
		return node{}, false
	}
	p.truncated = true
	return result, true
}

// decodeFailed records that the decoder could not continue.
//
// A failure inside an analyzed window cannot be classified: the prefix ends
// wherever the window ends, so a value cut in half and a malformed body look
// identical. Reporting the prefix as incomplete is the truthful answer and the
// useful one; deciding that a body is malformed is the HTTP boundary's job,
// where the whole body is available and a 400 is already written before this
// package runs. Outside a cut window a decode failure means the body really is
// malformed, and the caller sees zero-value features.
func (p *parser) decodeFailed() {
	if p.windowTruncated {
		p.cut = true
	}
}

// skipValue consumes one complete JSON value without retaining it. It is also
// the syntax check for the part of the document the analyzer does not keep.
func (p *parser) skipValue() error {
	token, err := p.decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{', '[':
		for p.decoder.More() {
			if delimiter == '{' {
				if _, err := p.decoder.Token(); err != nil {
					return err
				}
			}
			if err := p.skipValue(); err != nil {
				return err
			}
		}
		if _, err := p.decoder.Token(); err != nil {
			return err
		}
	}
	return nil
}

// member returns the first member with the given key. Duplicate keys are
// extremely rare; the first occurrence is both deterministic and the one the
// standard library's own decoding would keep.
func (n node) member(key string) (node, bool) {
	if n.kind != kindObject {
		return node{}, false
	}
	for _, entry := range n.members {
		if entry.key == key {
			return entry, true
		}
	}
	return node{}, false
}

// items returns every element of an array, or nil for any other kind.
func (n node) items() []node {
	if n.kind != kindArray {
		return nil
	}
	return n.elements
}

// text returns the decoded string value, or "" when the value is not a string.
func (n node) text() string {
	if n.kind != kindString {
		return ""
	}
	var value string
	if err := json.Unmarshal(n.raw, &value); err != nil {
		return ""
	}
	return value
}

// isNull reports an explicit JSON null, which is treated exactly like an absent
// value: a caller that sends "messages": null has no messages.
func (n node) isNull() bool { return n.kind == kindNull }

// boolValue returns the decoded boolean and whether the value was one.
func (n node) boolValue() (bool, bool) {
	if n.kind != kindBool {
		return false, false
	}
	return string(n.raw) == "true", true
}

// intValue returns an integral value. A fractional token limit is a client bug
// rather than a value to round, so it is ignored; exponent and decimal
// spellings such as 2e3 or 2000.0 are legal JSON for the same integer and are
// accepted when the value is exactly integral. Anything outside the platform
// int range is rejected rather than truncated.
func (n node) intValue() (int, bool) {
	if n.kind != kindNumber {
		return 0, false
	}
	raw := string(n.raw)
	if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if int64(int(parsed)) != parsed {
			return 0, false
		}
		return int(parsed), true
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) || value != math.Trunc(value) {
		return 0, false
	}
	// float64(math.MaxInt64) rounds up to 2^63, so the comparison must be
	// exclusive to stay inside the int64 range.
	if value >= float64(math.MaxInt64) || value <= float64(math.MinInt64) {
		return 0, false
	}
	converted := int64(value)
	if int64(int(converted)) != converted {
		return 0, false
	}
	return int(converted), true
}

// stringValue returns a decoded string, an integral number rendered as text, or
// "" for anything else. It is used for identifier-shaped fields (tool names,
// finish reasons) where some client libraries send a number.
func (n node) stringValue() string {
	if n.kind == kindNumber {
		return string(n.raw)
	}
	return n.text()
}

// truncateBytes cuts a string to at most limit bytes without splitting a UTF-8
// sequence, so a bounded value is still printable.
func truncateBytes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut]
}
