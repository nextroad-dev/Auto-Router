// Package analyzer turns one raw model request into protocol-independent
// routing features plus a bounded, in-memory view of the conversation for the
// Jev client.
//
// The contract this package guarantees:
//
//   - Pure: Analyze is a function over bytes. It performs no I/O, writes no
//     log, mutates neither the input slice nor any global state, and depends on
//     no HTTP layer, registry, Provider transport, configuration loader or SQLite.
//     A source-level import guard test pins that dependency set, which is how
//     "the analyzer never calls an agent and never sends a network request" is
//     proven rather than asserted.
//   - Content never fails: malformed, empty, JSON-`null` or oversized bodies
//     produce zero-value features (plus Truncated when the body was not fully
//     seen) and no error. Only the protocol identity and the routing preference
//     can be rejected, because those are router-owned inputs, not client text.
//     The HTTP boundary rejects a malformed body with 400 long before this
//     package sees it, so this branch is only reachable by direct callers and
//     the -analyze-check CLI.
//   - Bounded: at most maxAnalyzedBytes of the body is read, and node counts,
//     list sizes, recursion depth, message counts and view text are all capped
//     by named constants. Truncated reports that the analyzer did not see the
//     whole request. That is not the same as "the request does not contain it":
//     stage 6 and stage 7 must never treat an unobserved feature as absent.
//   - No weights: this package reports normalized facts. Scoring, thresholds
//     and weighted preferences belong to stage 6 and are deliberately absent
//     here.
package analyzer

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/nextroad-dev/Auto-Router/internal/providers"
	"github.com/nextroad-dev/Auto-Router/internal/router/jev"
)

// Bounds fixed in code. They are not configuration: the feature contract must
// stay comparable across deployments, and stage 6 may only turn a selected
// threshold into a setting without changing this contract.
const (
	// maxAnalyzedBytes bounds how much of a request body one analysis reads.
	// The window exists because a 16 MiB request with a base64 image would
	// otherwise cost far more than the routing decision is worth.
	maxAnalyzedBytes = 4 << 20
	// maxRetainedNodes bounds the number of JSON nodes retained while walking a
	// body. Reaching it stops the walk and sets Truncated; the nodes already
	// retained keep their values, so a partially scanned messages array still
	// reports what was seen.
	maxRetainedNodes = 1 << 16
	// maxParseDepth bounds how deep the walker descends while retaining nodes.
	// Deeper values are skipped without being retained (and counted as
	// unrecognized), which keeps hostile nesting from exhausting the stack.
	maxParseDepth = 32
	// maxMessages and maxItems bound how many message/input entries contribute
	// features.
	maxMessages = 4096
	maxItems    = 4096
	// maxContentPartsPerMessage bounds how many content parts of one message
	// contribute features.
	maxContentPartsPerMessage = 64
	// maxRecursionDepth bounds how deep the feature walk follows nested content
	// shapes (for example content -> part -> text -> value) before it stops.
	maxRecursionDepth = 3
	// maxToolNames bounds the tool-name list; each name is truncated to
	// maxToolNameBytes.
	maxToolNames     = 256
	maxToolNameBytes = 128
	// maxImages bounds the media-reference list; each reference location is
	// truncated to maxImageRefBytes.
	maxImages        = 64
	maxImageRefBytes = 512
	// maxViewMessages bounds how many messages the returned view carries.
	maxViewMessages = 256
	// maxViewBytes bounds the text the view carries in total (system prompt and
	// messages together). Exceeding it sets View.Truncated.
	maxViewBytes = 1 << 20
	// maxTextScanBytes bounds how much text is scanned for the code and
	// reasoning marker phrases. The markers are heuristics, so the bound is a
	// scan budget rather than a completeness claim.
	maxTextScanBytes = 1 << 20

	// tokenEstimateDivisor is the bytes-per-token divisor of the crude input
	// size estimate. InputTokensEstimate is a heuristic for classification
	// only, never a billing or usage number: stage 8 takes usage from the
	// upstream response alone.
	tokenEstimateDivisor = 4
	// Length thresholds, compared against InputTokensEstimate.
	shortThreshold    = 2_000
	longThreshold     = 32_000
	veryLongThreshold = 128_000
)

// FastResponseMaxOutputTokens is the output-token ceiling below which a
// streaming request looks like a latency-sensitive one. The analyzer only
// reports StreamRequested and MaxOutputTokensRequested; stage 6 reads this
// constant to turn the two facts into a signal, which keeps the constant and
// its use in one documented place.
const FastResponseMaxOutputTokens = 512

// Preference is the routing preference in effect for one request. Stage 5 only
// extracts and forwards it: how a preference influences a score is stage 6.
type Preference string

// The accepted preferences. PreferenceQuality favors the strongest model,
// PreferenceCost the cheapest, PreferenceLatency the fastest, and
// PreferenceBalanced leaves the trade-off to the policy weights.
const (
	PreferenceBalanced Preference = "balanced"
	PreferenceQuality  Preference = "quality"
	PreferenceCost     Preference = "cost"
	PreferenceLatency  Preference = "latency"
)

// preferenceOrder fixes the order preferences are listed in, so an error
// message and a CLI report never depend on map iteration.
var preferenceOrder = []Preference{PreferenceBalanced, PreferenceQuality, PreferenceCost, PreferenceLatency}

// LengthClass is the coarse input-size bucket stage 6 filters on.
type LengthClass string

const (
	LengthShort    LengthClass = "short"
	LengthMedium   LengthClass = "medium"
	LengthLong     LengthClass = "long"
	LengthVeryLong LengthClass = "very_long"
)

// InputKind is one kind of input the request carries. The set is closed: a new
// kind is a feature-contract change, not a runtime discovery.
type InputKind string

const (
	KindText  InputKind = "text"
	KindImage InputKind = "image"
	KindFile  InputKind = "file"
	KindAudio InputKind = "audio"
)

// PreferenceSource reports where the effective preference came from.
type PreferenceSource string

const (
	// SourceDefault means routing.default_preference applied.
	SourceDefault PreferenceSource = "default"
	// SourceHeader means X-Routing-Preference applied.
	SourceHeader PreferenceSource = "header"
)

// ImageRef is one media reference found in the request. Kind is one of "url"
// (an http(s) reference), "base64" (an inline payload) or "file" (an opaque
// file reference). An inline payload never reaches Location or a log line: it
// is reported as "[redacted]", because the only facts routing needs are that
// the request carries an image and how.
type ImageRef struct {
	Kind     string
	Location string
}

// Features are the normalized, protocol-independent facts about one request.
// Every field is derived only from the request body (plus the resolved
// preference) and is stable for the same bytes.
type Features struct {
	Protocol providers.Protocol

	Preference       Preference
	PreferenceSource PreferenceSource

	SystemPromptPresent bool
	SystemPromptBytes   int
	MessageCount        int
	// RoleCounts always carries all seven keys, zero-filled, so a consumer can
	// read a role without a presence check.
	RoleCounts map[string]int
	// InputBytes is the number of body bytes that were analyzed (at most
	// maxAnalyzedBytes, including JSON structure), and InputTokensEstimate is
	// the crude ceil(InputBytes/4) heuristic derived from it.
	InputBytes          int
	InputTokensEstimate int
	Length              LengthClass
	// Truncated reports that the analyzer did not see the whole request.
	Truncated bool

	// TextOnly means text was found and no image, file or audio part was.
	TextOnly bool
	// Kinds is the de-duplicated, sorted set of input kinds.
	Kinds []InputKind
	// ImageCount counts image parts; Images is the bounded list of media
	// references (images and files) in encounter order.
	ImageCount     int
	Images         []ImageRef
	HasImageURL    bool
	HasBase64Image bool
	HasFile        bool
	HasAudio       bool

	HasTools bool
	// ToolCount counts offered tools; ToolNames is de-duplicated, sorted and
	// bounded. ToolsTruncated reports a dropped or truncated name.
	ToolCount      int
	ToolNames      []string
	ToolsTruncated bool
	// ForcedToolChoice means the client explicitly named the tool to call; the
	// values auto, required and none are not a forced choice.
	ForcedToolChoice bool

	HasCode         bool
	ReasoningLikely bool
	ChainedToolUse  bool
	FileSearchUsed  bool

	StreamRequested          bool
	MaxOutputTokensRequested *int
	// UnrecognizedParts counts content parts, content shapes, message entries
	// and input items the analyzer could not classify. It is a signal that the
	// client used a newer protocol revision, not an error.
	UnrecognizedParts int
}

// View is the bounded, in-memory conversation a stage 7 orchestration can hand
// to the Jev client without re-parsing the body. It never leaves the process on
// its own: nothing here is logged, stored or included in an error message.
type View struct {
	SystemPrompt string
	// Messages is a plain conversation: chat text and Responses message /
	// function_call_output items. Tool schemas are deliberately not carried,
	// only tool names, because the routing question is about capability.
	Messages []jev.Message
	Tools    []string
	// Truncated reports that the view is incomplete because a size or count
	// bound was reached. A consumer must not treat an omitted message as an
	// absent one.
	Truncated bool
}

// Result is one analysis. Features and View are derived from the same single
// pass, so they can never disagree about the request.
type Result struct {
	Features Features
	View     View
}

// Input is one analysis request. HeaderPreference is the raw
// X-Routing-Preference value the HTTP boundary already validated (empty when the
// client sent none); DefaultPreference is routing.default_preference.
type Input struct {
	Protocol          providers.Protocol
	Body              []byte
	HeaderPreference  string
	DefaultPreference Preference
}

// The only two failures Analyze reports. Both describe router-owned input, not
// client content.
var (
	// ErrUnsupportedProtocol means the caller asked for a protocol that does not
	// exist; there is nothing sensible to extract.
	ErrUnsupportedProtocol = errors.New("unsupported protocol")
	// ErrInvalidPreference means a preference value is not one of the four
	// accepted values. Errors never repeat the offending value, so arbitrary
	// client text cannot be pushed into an error line.
	ErrInvalidPreference = errors.New("invalid routing preference")
)

// PreferenceValues lists the accepted preferences in a fixed order. It exists
// so the configuration validator, the HTTP boundary and the CLI all describe
// the same closed set.
func PreferenceValues() []string {
	values := make([]string, 0, len(preferenceOrder))
	for _, preference := range preferenceOrder {
		values = append(values, string(preference))
	}
	return values
}

// Valid reports whether the preference is one of the four accepted values.
func (p Preference) Valid() bool {
	for _, candidate := range preferenceOrder {
		if p == candidate {
			return true
		}
	}
	return false
}

// String returns the wire spelling of the preference.
func (p Preference) String() string { return string(p) }

// ParsePreference normalizes one preference value: surrounding whitespace is
// insignificant and the comparison is case-insensitive. An unknown value is
// rejected without echoing it.
func ParsePreference(value string) (Preference, error) {
	normalized := Preference(strings.ToLower(strings.TrimSpace(value)))
	if !normalized.Valid() {
		return "", fmt.Errorf("%w: must be one of %s", ErrInvalidPreference, strings.Join(PreferenceValues(), ", "))
	}
	return normalized, nil
}

// ResolvePreference applies the fixed precedence: a request-level header
// overrides the configured default, and the source of the effective value is
// reported alongside it. An empty header means "the client did not ask".
func ResolvePreference(header string, fallback Preference) (Preference, PreferenceSource, error) {
	if strings.TrimSpace(header) == "" {
		if !fallback.Valid() {
			return "", "", fmt.Errorf("%w: configured default must be one of %s", ErrInvalidPreference, strings.Join(PreferenceValues(), ", "))
		}
		return fallback, SourceDefault, nil
	}
	preference, err := ParsePreference(header)
	if err != nil {
		return "", "", err
	}
	return preference, SourceHeader, nil
}

// Analyzer is the concrete, stateless implementation of an Extractor. It
// exists so the composition root can name the production extractor without
// reaching for a function literal, and so a future replacement can be swapped
// in at one place.
type Analyzer struct{}

// New returns the production analyzer. It holds no state: every analysis is a
// pure function of its input, which is what makes it safe to share between
// concurrent requests.
func New() Analyzer { return Analyzer{} }

// Analyze implements Extractor.
func (Analyzer) Analyze(input Input) (Result, error) { return Analyze(input) }

// Analyze extracts features and the conversation view from one raw request
// body. It never fails on content: an unparseable body yields zero-value
// features, an empty view and no error.
func Analyze(input Input) (Result, error) {
	if !input.Protocol.Valid() {
		return Result{}, fmt.Errorf("%w: %q", ErrUnsupportedProtocol, string(input.Protocol))
	}
	preference, source, err := ResolvePreference(input.HeaderPreference, input.DefaultPreference)
	if err != nil {
		return Result{}, err
	}
	window := input.Body
	if len(window) > maxAnalyzedBytes {
		window = window[:maxAnalyzedBytes]
	}
	walk := newWalk(input.Protocol, preference, source, window, len(input.Body) > len(window))
	parser := newParser(window, len(input.Body) > len(window))
	if root, ok := parser.document(); ok {
		switch input.Protocol {
		case providers.ProtocolChatCompletions:
			walk.chat(root)
		case providers.ProtocolResponses:
			walk.responses(root)
		}
	}
	if parser.truncated {
		walk.features.Truncated = true
	}
	return walk.result(), nil
}

// walk accumulates features and the view during the single pass over the body.
type walk struct {
	features Features
	view     View

	roles     map[string]int
	kinds     map[InputKind]struct{}
	toolNames map[string]struct{}

	systemBytes int
	systemParts []string
	viewBytes   int
	textScanned int
}

func newWalk(protocol providers.Protocol, preference Preference, source PreferenceSource, window []byte, windowTruncated bool) *walk {
	roles := make(map[string]int, len(roleNames))
	for _, role := range roleNames {
		roles[role] = 0
	}
	return &walk{
		features: Features{
			Protocol:            protocol,
			Preference:          preference,
			PreferenceSource:    source,
			RoleCounts:          roles,
			InputBytes:          len(window),
			InputTokensEstimate: (len(window) + tokenEstimateDivisor - 1) / tokenEstimateDivisor,
			Length:              lengthClass((len(window) + tokenEstimateDivisor - 1) / tokenEstimateDivisor),
			Truncated:           windowTruncated,
			Kinds:               []InputKind{},
			Images:              []ImageRef{},
			ToolNames:           []string{},
		},
		view: View{
			Messages: []jev.Message{},
			Tools:    []string{},
		},
		roles:     roles,
		kinds:     make(map[InputKind]struct{}, 4),
		toolNames: make(map[string]struct{}),
	}
}

// roleNames is the closed set of role buckets. Unknown or missing roles are
// counted as "other" rather than dropped, so the counts always add up to
// MessageCount.
var roleNames = []string{"system", "developer", "user", "assistant", "tool", "function", "other"}

// systemRoles are the roles whose text forms the system prompt.
var systemRoles = map[string]struct{}{"system": {}, "developer": {}}

// NormalizeRole folds a protocol role into one of the buckets in roleNames. It
// is exported so the debug endpoint and the CLI label a role exactly as the
// features count it.
func NormalizeRole(role string) string {
	normalized := strings.ToLower(strings.TrimSpace(role))
	if _, ok := systemRoles[normalized]; ok {
		return normalized
	}
	switch normalized {
	case "user", "assistant", "tool", "function":
		return normalized
	default:
		return "other"
	}
}

// IsNoReasoning reports a reasoning control value that explicitly asks for no
// reasoning. The literal is the only spelling either protocol uses, so the
// comparison is case-insensitive and nothing else is treated as "off".
func IsNoReasoning(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "none")
}

func lengthClass(tokens int) LengthClass {
	switch {
	case tokens < shortThreshold:
		return LengthShort
	case tokens < longThreshold:
		return LengthMedium
	case tokens < veryLongThreshold:
		return LengthLong
	default:
		return LengthVeryLong
	}
}

// result finalizes the derived fields and returns the analysis.
func (w *walk) result() Result {
	kinds := make([]InputKind, 0, len(w.kinds))
	for kind := range w.kinds {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	w.features.Kinds = kinds
	if len(kinds) > 0 {
		w.features.TextOnly = kinds[0] == KindText && len(kinds) == 1
	}

	names := make([]string, 0, len(w.toolNames))
	for name := range w.toolNames {
		names = append(names, name)
	}
	sort.Strings(names)
	w.features.ToolNames = names
	w.view.Tools = names

	w.features.SystemPromptPresent = w.systemBytes > 0
	w.features.SystemPromptBytes = w.systemBytes
	w.view.SystemPrompt = strings.Join(w.systemParts, "\n\n")
	return Result{Features: w.features, View: w.view}
}

// addRole counts one message under its role bucket.
func (w *walk) addRole(role string) {
	if _, ok := w.roles[role]; !ok {
		role = "other"
	}
	w.roles[role]++
}

// addKind records one input kind. The first observed kind is remembered so an
// empty request never looks like a text-only one.
func (w *walk) addKind(kind InputKind) {
	w.kinds[kind] = struct{}{}
}

// noteUnrecognized counts a construct the analyzer could not classify.
func (w *walk) noteUnrecognized() { w.features.UnrecognizedParts++ }

// noteTruncated records that a bound stopped the analysis short.
func (w *walk) noteTruncated() { w.features.Truncated = true }

// Reference kinds of ImageRef. They describe how a provider would obtain the
// media, which is the only thing routing needs to know about it.
const (
	refKindURL       = "url"
	refKindBase64    = "base64"
	refKindFile      = "file"
	redactedLocation = "[redacted]"
)

// addSystem records one system/developer text block. The statistics count the
// whole text; only the retained view is bounded by the shared text budget.
func (w *walk) addSystem(text string) {
	if text == "" {
		return
	}
	w.systemBytes += len(text)
	if len(w.systemParts) >= maxViewMessages {
		w.view.Truncated = true
		return
	}
	if remaining := maxViewBytes - w.viewBytes; remaining <= 0 {
		w.view.Truncated = true
		return
	} else if len(text) > remaining {
		text = truncateBytes(text, remaining)
		w.view.Truncated = true
	}
	w.viewBytes += len(text)
	w.systemParts = append(w.systemParts, text)
}

// setMaxOutput records a requested output ceiling. The first accepted value
// wins, so a body with two competing fields yields a deterministic result.
func (w *walk) setMaxOutput(value int) {
	if w.features.MaxOutputTokensRequested == nil {
		w.features.MaxOutputTokensRequested = &value
	}
}

// addToolName records one offered tool name, bounded and de-duplicated.
func (w *walk) addToolName(name string) {
	if name == "" {
		return
	}
	if len(name) > maxToolNameBytes {
		w.features.ToolsTruncated = true
		name = truncateBytes(name, maxToolNameBytes)
	}
	if len(w.toolNames) >= maxToolNames {
		w.features.ToolsTruncated = true
		return
	}
	w.toolNames[name] = struct{}{}
}

// recordTurn places one message in the view: system and developer turns become
// the system prompt, everything else becomes a conversation turn. Keeping the
// two apart means the view maps onto the Jev request shape (SystemPrompt plus
// Messages) without sending the same instructions twice.
func (w *walk) recordTurn(role, text string) {
	if _, isSystem := systemRoles[role]; isSystem {
		w.addSystem(text)
		return
	}
	w.addViewMessage(role, text)
}

// addViewMessage appends one conversation turn to the bounded view. The text
// budget is shared by the system prompt and the messages; running out of it
// only shortens the view and sets View.Truncated.
func (w *walk) addViewMessage(role, text string) {
	if len(w.view.Messages) >= maxViewMessages {
		// A view that ran out of room is incomplete: the consumer must not read
		// "no further turns" into it.
		w.view.Truncated = true
		return
	}
	if text == "" {
		// An empty text turn carries no information for the routing question,
		// so it is left out of the view. The message itself is still counted in
		// MessageCount and RoleCounts.
		return
	}
	if remaining := maxViewBytes - w.viewBytes; remaining <= 0 {
		w.view.Truncated = true
		return
	} else if len(text) > remaining {
		text = truncateBytes(text, remaining)
		w.view.Truncated = true
	}
	w.viewBytes += len(text)
	w.view.Messages = append(w.view.Messages, jev.Message{Role: role, Content: text})
}
