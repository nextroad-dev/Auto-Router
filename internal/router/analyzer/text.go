package analyzer

import (
	"strings"
)

// This file extracts text and media from the content shapes shared by both
// protocols. Task difficulty is judged by Jev from the configured routing input;
// the analyzer records protocol facts without scanning prose for keywords.

// recordText records a text input kind and returns the text for the view.
func (w *walk) recordText(text string) string {
	if text == "" {
		return ""
	}
	w.addKind(KindText)
	return text
}

// partTypeOf returns the lowercased type tag of a part or item. A missing tag
// yields "", which callers treat separately from an unknown tag.
func partTypeOf(n node) string {
	raw, ok := n.member("type")
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(raw.stringValue()))
}

// deepText resolves the text of a value that may be a string or a small object
// wrapper, descending at most maxRecursionDepth levels. Both protocols wrap
// text in at most one extra object ({"text": {"value": "..."}} appears in some
// vendored shapes); anything deeper is treated as unrecognized.
func (w *walk) deepText(value node, depth int) (string, bool) {
	switch value.kind {
	case kindString, kindNumber:
		return value.stringValue(), true
	case kindObject:
		if depth >= maxRecursionDepth {
			return "", false
		}
		for _, name := range []string{"text", "value", "content"} {
			if nested, ok := value.member(name); ok {
				if text, ok := w.deepText(nested, depth+1); ok {
					return text, true
				}
			}
		}
		return "", false
	case kindInvalid:
		return "", false
	default:
		return "", false
	}
}

// contentText extracts the text of one content value: a plain string, or the
// concatenation of the text parts of an array. It reports whether the shape was
// recognized at all, so an unknown shape is counted instead of silently treated
// as empty text.
func (w *walk) contentText(content node) (string, bool) {
	switch content.kind {
	case kindString:
		return w.recordText(content.text()), true
	case kindArray:
		parts := content.items()
		if len(parts) > maxContentPartsPerMessage {
			w.noteTruncated()
			parts = parts[:maxContentPartsPerMessage]
		}
		collected := make([]string, 0, len(parts))
		for _, part := range parts {
			if text, ok := w.partText(part, 1); ok && text != "" {
				collected = append(collected, text)
			}
		}
		return strings.Join(collected, "\n"), true
	case kindInvalid:
		return "", false
	default:
		w.noteUnrecognized()
		return "", false
	}
}

// partText interprets one content part. It reports the part's text (empty for
// non-text parts) and whether the part type was recognized. Recognition updates
// the media and chained-tool-use signals as a side effect.
func (w *walk) partText(part node, depth int) (string, bool) {
	if part.kind != kindObject {
		if part.kind != kindInvalid {
			w.noteUnrecognized()
		}
		return "", false
	}
	switch partTypeOf(part) {
	case "text", "input_text", "output_text", "summary_text":
		text, ok := w.partTextValue(part, depth)
		if !ok {
			return "", true
		}
		return w.recordText(text), true
	case "refusal":
		// A refusal is assistant text in both protocols; the field name differs
		// between the chat and Responses shapes.
		text, ok := w.partTextValue(part, depth)
		if !ok {
			return "", true
		}
		return w.recordText(text), true
	case "image_url", "input_image":
		// Chat nests the reference in an object ({"image_url": {"url": ...}});
		// a bare string is accepted too, because both shapes appear in the wild.
		w.recordImageRef(part.mediaReference("image_url", "url", "image_url"))
		return "", true
	case "input_file", "file":
		w.recordFileRef(part.mediaReference("file_data", "file_url", "file_id", "filename", "file", "input_file"))
		return "", true
	case "input_audio", "audio":
		w.recordAudioRef()
		return "", true
	case "tool_call", "function_call":
		w.features.ChainedToolUse = true
		return "", true
	case "reasoning", "reasoning_summary", "reasoning_text", "summary":
		// Provider reasoning is protocol machinery; it is not copied into the
		// routing view and does not establish the difficulty of the next turn.
		return "", true
	case "":
		if _, ok := part.member("text"); ok {
			// A part without a type tag but with text is still text: refusing it
			// would under-report a request the router can plainly handle.
			text, ok := w.partTextValue(part, depth)
			if !ok {
				return "", true
			}
			return w.recordText(text), true
		}
		w.noteUnrecognized()
		return "", false
	default:
		w.noteUnrecognized()
		return "", false
	}
}

// partTextValue reads the text of a text part. The chat shape uses "text"; the
// Responses shape sometimes wraps it in an object, which deepText unwraps
// within the recursion budget.
func (w *walk) partTextValue(part node, depth int) (string, bool) {
	for _, name := range []string{"text", "refusal", "content"} {
		raw, ok := part.member(name)
		if !ok || raw.isNull() {
			continue
		}
		if text, ok := w.deepText(raw, depth); ok {
			return text, true
		}
	}
	return "", false
}

// mediaReference resolves the media reference of a part. It tries every field
// name the two protocols use, in a fixed order, and returns the first
// non-empty reference. A nested object ({"image_url": {"url": ...}}) is
// unwrapped once, which is the only nesting either protocol uses.
func (n node) mediaReference(names ...string) string {
	for _, name := range names {
		raw, ok := n.member(name)
		if !ok || raw.isNull() {
			continue
		}
		if raw.kind == kindString {
			if value := strings.TrimSpace(raw.text()); value != "" {
				return value
			}
			continue
		}
		if raw.kind != kindObject {
			continue
		}
		for _, nested := range []string{"url", "file_url", "file_id", "data", "file_data", "filename"} {
			inner, ok := raw.member(nested)
			if !ok || inner.kind != kindString {
				continue
			}
			// A data: payload is reported as inline; the value itself is never
			// retained, and it is only read far enough to classify it.
			if value := strings.TrimSpace(inner.text()); value != "" {
				return value
			}
		}
	}
	return ""
}

// textMember reads one string member, tolerating a number (some clients send
// identifiers as numbers) and ignoring every other shape.
func (n node) textMember(name string) string {
	raw, ok := n.member(name)
	if !ok {
		return ""
	}
	return raw.stringValue()
}

// recordImageRef records one image reference. The distinction that matters for
// routing is whether the bytes travel inline (base64) or are fetched by the
// provider (an http(s) URL); an inline payload never reaches the feature set or
// a log line.
func (w *walk) recordImageRef(reference string) {
	w.features.ImageCount++
	w.addKind(KindImage)
	reference = strings.TrimSpace(reference)
	switch {
	case reference == "":
		w.addMediaRef(refKindBase64, redactedLocation)
		w.features.HasBase64Image = true
	case isInlinePayload(reference):
		w.addMediaRef(refKindBase64, redactedLocation)
		w.features.HasBase64Image = true
	default:
		w.addMediaRef(refKindURL, reference)
		w.features.HasImageURL = true
	}
}

// recordFileRef records one file reference. Only an http(s) URL is retained;
// an opaque identifier or an inline payload is redacted, so a file id or file
// content can never reach a log line.
func (w *walk) recordFileRef(reference string) {
	w.addKind(KindFile)
	w.features.HasFile = true
	reference = strings.TrimSpace(reference)
	if isHTTPReference(reference) {
		w.addMediaRef(refKindFile, reference)
		return
	}
	w.addMediaRef(refKindFile, redactedLocation)
}

// recordAudioRef records one audio part. Audio never carries a reference worth
// keeping: the chat shape embeds the payload and the Responses shape uses a
// file id, so only the fact that audio is present is reported.
func (w *walk) recordAudioRef() {
	w.addKind(KindAudio)
	w.features.HasAudio = true
}

// addMediaRef appends one media reference, truncating its location to the
// bounded length.
func (w *walk) addMediaRef(kind, location string) {
	if len(w.features.Images) >= maxImages {
		w.noteTruncated()
		return
	}
	w.features.Images = append(w.features.Images, ImageRef{
		Kind:     kind,
		Location: truncateBytes(location, maxImageRefBytes),
	})
}

// isInlinePayload reports an embedded payload: a data: URI or a bare base64
// blob. Neither may be retained, logged or compared.
func isInlinePayload(reference string) bool {
	lowered := strings.ToLower(reference)
	if strings.HasPrefix(lowered, "data:") {
		return true
	}
	// A URL always carries a scheme separator or a path separator; a bare blob
	// carries neither. Anything else is treated as opaque and redacted.
	if strings.HasPrefix(lowered, "http://") || strings.HasPrefix(lowered, "https://") {
		return false
	}
	return !strings.ContainsAny(reference, "/:.?")
}

// isHTTPReference reports a reference a provider can fetch. Everything else is
// treated as opaque, and opaque values are redacted rather than recorded.
func isHTTPReference(reference string) bool {
	lowered := strings.ToLower(strings.TrimSpace(reference))
	return strings.HasPrefix(lowered, "http://") || strings.HasPrefix(lowered, "https://")
}
