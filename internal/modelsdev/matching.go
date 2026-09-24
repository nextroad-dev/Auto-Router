package modelsdev

import (
	"sort"
	"strings"
	"unicode"

	"github.com/nextroad-dev/Auto-Router/internal/models"
)

// MetadataMatch is the models.dev capability record selected for an upstream
// model identifier. MatchBy is one of exact, prefix, or keyword.
type MetadataMatch struct {
	ProviderID  string
	ModelID     string
	DisplayName string
	MatchBy     string
	Pair        models.Pair
}

// FindMetadata finds a models.dev model using the local provider key as a soft
// hint and the provider's actual upstream model identifier as the primary key.
// Custom upstream prefixes are tolerated, but equally plausible records with
// different capability data are rejected rather than guessed.
func FindMetadata(document Document, providerHint, upstreamID string) (MetadataMatch, bool) {
	if strings.TrimSpace(upstreamID) == "" {
		return MetadataMatch{}, false
	}
	var matches []rankedMetadataMatch
	for providerKey, provider := range document {
		providerScore := scoreProviderHint(providerHint, providerKey, provider.Name)
		for modelKey, model := range provider.Models {
			modelID := model.ID
			if strings.TrimSpace(modelID) == "" {
				modelID = modelKey
			}
			score, matchBy := bestModelMatch(upstreamID, modelKey, model.ID, model.Name)
			if score == 0 {
				continue
			}
			displayName := strings.TrimSpace(model.Name)
			if displayName == "" {
				displayName = modelID
			}
			pair := models.Pair{
				ModelID: modelID, UpstreamModelID: upstreamID,
				ContextWindow:      model.LimitContext(),
				SupportsTools:      model.ToolCall,
				SupportsVision:     supportsImageInput(model),
				SupportsAudioInput: supportsAudioInput(model),
				SupportsReasoning:  model.Reasoning,
				Source:             models.SourceModelsDev,
			}
			if model.Limit != nil {
				pair.MaxOutput = cloneInt(model.Limit.Output)
			}
			if pair.ContextWindow <= 0 {
				// A record without a usable context window cannot safely contribute
				// routing metadata, even if its name matched.
				continue
			}
			matches = append(matches, rankedMetadataMatch{
				match: MetadataMatch{ProviderID: providerKey, ModelID: modelID, DisplayName: displayName, MatchBy: matchBy, Pair: pair},
				score: score + providerScore,
			})
		}
	}
	if len(matches) == 0 {
		return MetadataMatch{}, false
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		if matches[i].match.ProviderID != matches[j].match.ProviderID {
			return matches[i].match.ProviderID < matches[j].match.ProviderID
		}
		return matches[i].match.ModelID < matches[j].match.ModelID
	})
	best := matches[0]
	for _, candidate := range matches[1:] {
		if candidate.score != best.score {
			break
		}
		if !sameCapabilityMetadata(candidate.match.Pair, best.match.Pair) {
			return MetadataMatch{}, false
		}
	}
	return best.match, true
}

type rankedMetadataMatch struct {
	match MetadataMatch
	score int
}

func (m Model) LimitContext() int {
	if m.Limit == nil {
		return 0
	}
	return m.Limit.Context
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func sameCapabilityMetadata(left, right models.Pair) bool {
	if left.ContextWindow != right.ContextWindow || left.SupportsTools != right.SupportsTools ||
		left.SupportsVision != right.SupportsVision || left.SupportsAudioInput != right.SupportsAudioInput ||
		left.SupportsReasoning != right.SupportsReasoning {
		return false
	}
	if left.MaxOutput == nil || right.MaxOutput == nil {
		return left.MaxOutput == nil && right.MaxOutput == nil
	}
	return *left.MaxOutput == *right.MaxOutput
}

func bestModelMatch(query string, aliases ...string) (int, string) {
	bestScore, bestMethod := 0, ""
	for index, alias := range aliases {
		if alias == "" {
			continue
		}
		score, method := identifierMatchScore(query, alias)
		// The provider's display name is less authoritative than its machine IDs.
		if index == len(aliases)-1 && method != "" {
			score -= 80
		}
		if score > bestScore {
			bestScore, bestMethod = score, method
		}
	}
	return bestScore, bestMethod
}

func identifierMatchScore(query, candidate string) (int, string) {
	queryRaw, candidateRaw := query, candidate
	queryLeaf := normalizeID(lastModelSegment(query))
	candidateLeaf := normalizeID(lastModelSegment(candidate))
	query = normalizeID(query)
	candidate = normalizeID(candidate)
	if query == "" || candidate == "" {
		return 0, ""
	}
	if query == candidate {
		return 1000, "exact"
	}
	if queryLeaf != "" && queryLeaf == candidateLeaf {
		return 900, "exact"
	}
	// Custom prefixes and deployment suffixes are common in OpenAI-compatible
	// gateways. Match a distinctive model identifier contained in the upstream
	// name, but rank it below a direct ID match.
	shorter := query
	if len(candidate) < len(query) {
		shorter = candidate
	}
	if len(shorter) >= 7 && (strings.Contains(query, candidate) || strings.Contains(candidate, query)) {
		return 700 + len(shorter), "prefix"
	}
	if len(queryLeaf) >= 7 && (strings.Contains(queryLeaf, candidateLeaf) || strings.Contains(candidateLeaf, queryLeaf)) {
		return 650 + len(minString(queryLeaf, candidateLeaf)), "prefix"
	}
	if keywordOverlap(queryRaw, candidateRaw) {
		return 400 + len(candidateTokens(candidate)), "keyword"
	}
	return 0, ""
}

func scoreProviderHint(hint, providerKey, providerName string) int {
	normalizedHint := normalizeID(hint)
	if normalizedHint == "" {
		return 0
	}
	if normalizedHint == normalizeID(providerKey) || normalizedHint == normalizeID(providerName) {
		return 60
	}
	if keywordOverlap(hint, providerKey) || keywordOverlap(hint, providerName) {
		return 20
	}
	return 0
}

func keywordOverlap(left, right string) bool {
	leftTokens, rightTokens := candidateTokens(left), candidateTokens(right)
	if len(leftTokens) == 0 || len(rightTokens) == 0 {
		return false
	}
	leftSet := make(map[string]struct{}, len(leftTokens))
	for _, token := range leftTokens {
		leftSet[token] = struct{}{}
	}
	shared := 0
	for _, token := range rightTokens {
		if _, ok := leftSet[token]; ok {
			shared++
		}
	}
	shorter := len(leftTokens)
	if len(rightTokens) < shorter {
		shorter = len(rightTokens)
	}
	// Single generic fragments such as "gpt" or "mini" are not identifying
	// keywords. Requiring multiple shared tokens avoids a broad false match.
	return shorter >= 2 && shared >= shorter
}

func candidateTokens(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.ToLower(field)
		if len(field) >= 2 {
			result = append(result, field)
		}
	}
	return result
}

func normalizeID(value string) string {
	var normalized strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			normalized.WriteRune(r)
		}
	}
	return normalized.String()
}

func lastModelSegment(value string) string {
	if slash := strings.LastIndexAny(value, "/:"); slash >= 0 {
		return value[slash+1:]
	}
	return value
}

func minString(left, right string) string {
	if len(left) <= len(right) {
		return left
	}
	return right
}
