package auto

import (
	"regexp"
	"unicode/utf8"
)

// Redaction bounds and replacements. They are named constants, not settings: the
// privacy behavior of the redacted input mode must be identical in every
// deployment, and a change to it must be a reviewable code change.
const (
	// redactedExcerptBytes is how much of one text block survives redaction. It
	// is applied per block (the system prompt and each message), so a long
	// conversation is bounded block by block rather than truncated as a whole.
	redactedExcerptBytes = 512
	// Redaction placeholders. They are intentionally short and carry no
	// information about what they replaced.
	placeholderURL     = "[url]"
	placeholderEmail   = "[email]"
	placeholderPayload = "[payload]"
	placeholderToken   = "[token]"
)

// The redaction patterns, in fixed application order. The order is part of the
// contract: a data URI and a long base64 run overlap, and running the URL rule
// first means the `data:` scheme is already gone before the payload rule sees it.
//
// These rules are best-effort pattern matching, deliberately not entity
// recognition and deliberately without an external dependency. Redacted mode
// reduces what leaves the process; it is not an anonymization guarantee, and the
// documentation says so.
var (
	// dataURI matches an inline `data:` payload, including the media type and the
	// base64 marker. It is listed before the URL rule so the scheme is recognized
	// as a payload rather than as a link.
	dataURIPattern = regexp.MustCompile(`(?i)\bdata:[a-z0-9!#$&^_.+-]+/[a-z0-9!#$&^_.+-]+(?:;[a-z0-9!#$&^_.+-]+=[a-z0-9!#$&^_.+-]+)*;base64,[A-Za-z0-9+/=\r\n]{16,}`)
	// urlPattern matches an absolute http(s) or ws(s) URL, with or without a
	// path. The trailing character class stops at whitespace and at the
	// punctuation that usually ends a sentence.
	urlPattern = regexp.MustCompile(`(?i)\b(?:https?|wss?)://[^\s<>"'` + "`" + `]+`)
	// emailPattern matches a conventional mail address.
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	// tokenPattern matches the credential shapes a client is most likely to paste
	// into a prompt: recognizable prefixes, `bearer`/`token`/`api_key`
	// assignments, and long JWT-style runs.
	tokenPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(?:sk|pk|rk|vk|ghp|gho|ghs|xox[baprs])[-_][A-Za-z0-9_\-]{12,}`),
		regexp.MustCompile(`(?i)\b(?:bearer|token|api[_-]?key|secret|password|passwd|authorization)\s*[:=]?\s*[A-Za-z0-9._\-+/=]{12,}`),
		regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{4,}\.[A-Za-z0-9_\-]{4,}\.[A-Za-z0-9_\-]{4,}`),
	}
	// highEntropyPattern matches long runs of base64/hex-shaped characters. A
	// short run is left alone on purpose: an ordinary word or an identifier is
	// not a payload, and replacing it would remove meaning the recommendation
	// might legitimately need.
	highEntropyPattern = regexp.MustCompile(`\b[A-Za-z0-9+/=_-]{40,}\b`)
)

// redactText is the deterministic, idempotent reduction applied to one text block
// in the redacted input mode: recognized patterns are replaced, the result is cut
// to a UTF-8-safe excerpt, and the same input always produces the same output.
// It never fails and never returns invalid UTF-8.
func redactText(text string) string {
	if text == "" {
		return ""
	}
	redacted := text
	for _, pattern := range []*regexp.Regexp{dataURIPattern, urlPattern, emailPattern} {
		redacted = pattern.ReplaceAllString(redacted, replacementFor(pattern))
	}
	for _, pattern := range tokenPatterns {
		redacted = pattern.ReplaceAllString(redacted, placeholderToken)
	}
	redacted = highEntropyPattern.ReplaceAllString(redacted, placeholderPayload)
	return truncateUTF8(redacted, redactedExcerptBytes)
}

// replacementFor maps a pattern to its placeholder. Keeping the mapping in one
// function means a new pattern cannot be added without deciding what it leaves
// behind.
func replacementFor(pattern *regexp.Regexp) string {
	switch pattern {
	case dataURIPattern:
		return placeholderPayload
	case urlPattern:
		return placeholderURL
	case emailPattern:
		return placeholderEmail
	default:
		return placeholderPayload
	}
}

// truncateUTF8 cuts a string to at most limit bytes without splitting a rune, so
// a bounded excerpt is always valid UTF-8. A cut that would land inside a rune
// moves back to the previous rune boundary.
func truncateUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}
