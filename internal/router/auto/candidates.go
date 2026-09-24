package auto

import (
	"fmt"
	"sort"

	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/router/decision"
	"github.com/nextroad-dev/Auto-Router/internal/router/jev"
)

// Candidates derives the policy engine's candidate list from a registry
// snapshot. One enabled provider/model pair becomes exactly one candidate, in the
// snapshot's contract order (pair priority, provider priority, provider key,
// model ID ascending), which is the order the policy engine uses as its final
// tie-break. The function is pure and allocates a new slice, so a caller cannot
// mutate the snapshot through it.
//
// Capabilities are copied per pair and never merged across providers: a logical
// model that supports tools at one provider and not at another yields two
// candidates with different capability facts, because that is the truth the hard
// filters must see. A pair is routable only when the pair, its provider and its
// logical model are all enabled; models.Catalog.PairsForModel already enforces
// exactly that.
func Candidates(catalog *models.Catalog) []decision.Candidate {
	if catalog == nil {
		return nil
	}
	candidates := make([]decision.Candidate, 0, len(catalog.Pairs))
	for _, model := range catalog.Models {
		if !model.Enabled {
			continue
		}
		for _, pair := range catalog.PairsForModel(model.ID) {
			provider, ok := catalog.Provider(pair.ProviderKey)
			if !ok || !provider.Enabled {
				// PairsForModel already filters this, but the candidate is the
				// object a request is routed to: it is built only from a provider
				// the snapshot can actually resolve, so no later stage has to
				// re-check it.
				continue
			}
			candidates = append(candidates, decision.Candidate{
				ModelID:     pair.ModelID,
				ProviderKey: provider.Key,
				// GatewayModel is a historical field name in the routing contract;
				// its value is now the provider-native model identifier.
				GatewayModel:       pair.UpstreamModelID,
				ContextWindow:      pair.ContextWindow,
				MaxOutput:          copyInt(pair.MaxOutput),
				SupportsTools:      pair.SupportsTools,
				SupportsVision:     pair.SupportsVision,
				SupportsAudioInput: pair.SupportsAudioInput,
				SupportsReasoning:  pair.SupportsReasoning,
				PairPriority:       pair.Priority,
				ProviderPriority:   provider.Priority,
			})
		}
	}
	// PairsForModel returns contract order per model; iterating the sorted model
	// list therefore produces the whole set in contract order only when every
	// model's pairs outrank the next model's. Sorting by the documented keys
	// makes the order explicit instead of relying on that coincidence.
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.PairPriority != b.PairPriority {
			return a.PairPriority < b.PairPriority
		}
		if a.ProviderPriority != b.ProviderPriority {
			return a.ProviderPriority < b.ProviderPriority
		}
		if a.ProviderKey != b.ProviderKey {
			return a.ProviderKey < b.ProviderKey
		}
		return a.ModelID < b.ModelID
	})
	return candidates
}

// cloneGroupConfig takes an immutable, bounded snapshot for one route.
func cloneGroupConfig(groups GroupConfig) GroupConfig {
	clone := func(members []GroupMember) []GroupMember {
		if len(members) > maxGroupMembers {
			members = members[:maxGroupMembers]
		}
		return append([]GroupMember(nil), members...)
	}
	return GroupConfig{Simple: clone(groups.Simple), Medium: clone(groups.Medium), Complex: clone(groups.Complex)}
}

// orderedGroupCandidates intersects one configured group with the already
// hard-filtered registry candidates, preserving the administrator's pair order.
func orderedGroupCandidates(groups GroupConfig, group GroupName, eligible []decision.Candidate) []decision.Candidate {
	members := groups.members(group)
	if len(members) > maxGroupMembers {
		members = members[:maxGroupMembers]
	}
	byPair := make(map[string]decision.Candidate, len(eligible))
	for _, candidate := range eligible {
		byPair[candidate.ProviderKey+"\x00"+candidate.ModelID] = candidate
	}
	ordered := make([]decision.Candidate, 0, len(members))
	seen := make(map[string]struct{}, len(members))
	for _, member := range members {
		key := member.ProviderKey + "\x00" + member.ModelID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if candidate, ok := byPair[key]; ok {
			ordered = append(ordered, candidate)
		}
	}
	return ordered
}

// copyInt copies the optional output ceiling so a caller cannot mutate the
// registry snapshot through a candidate.
func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	limit := *value
	return &limit
}

// modelEnvelope is one logical model as Jev sees it: the conservative guarantee
// that is true for every currently eligible pair of that model. A recommendation
// is made about a model, so the model has to be described by what it can
// definitely do — not by what its best provider can do.
type modelEnvelope struct {
	Model              string
	ContextWindow      int
	MaxOutput          *int
	SupportsTools      bool
	SupportsVision     bool
	SupportsAudioInput bool
	SupportsReasoning  bool
}

// modelEnvelopes collapses an eligible candidate set into one envelope per
// logical model, in ascending model-ID order so the Jev request is deterministic.
//
// The combination rules are conservative and one-directional:
//
//   - a capability is kept only when every pair declares it (AND), so a model is
//     never advertised as able to do something one of its providers cannot;
//   - ContextWindow is the minimum;
//   - MaxOutput is the minimum of the declared values, and unknown when any pair
//     leaves it undeclared, because an undeclared ceiling is not a guarantee;
//   - nothing is synthesized: there is no OR, no maximum and no cross-provider
//     capability merging.
//
// The result is therefore a guarantee across the model's currently eligible
// pairs, which is exactly what the recommendation is allowed to assume.
func modelEnvelopes(candidates []decision.Candidate) []modelEnvelope {
	byModel := make(map[string]*modelEnvelope, len(candidates))
	for _, candidate := range candidates {
		envelope, ok := byModel[candidate.ModelID]
		if !ok {
			byModel[candidate.ModelID] = &modelEnvelope{
				Model:              candidate.ModelID,
				ContextWindow:      candidate.ContextWindow,
				MaxOutput:          copyInt(candidate.MaxOutput),
				SupportsTools:      candidate.SupportsTools,
				SupportsVision:     candidate.SupportsVision,
				SupportsAudioInput: candidate.SupportsAudioInput,
				SupportsReasoning:  candidate.SupportsReasoning,
			}
			continue
		}
		if candidate.ContextWindow < envelope.ContextWindow {
			envelope.ContextWindow = candidate.ContextWindow
		}
		switch {
		case envelope.MaxOutput == nil || candidate.MaxOutput == nil:
			// An undeclared ceiling anywhere makes the model's ceiling unknown.
			// Reporting the better pair's number would be a guarantee the model
			// does not actually offer.
			envelope.MaxOutput = nil
		case *candidate.MaxOutput < *envelope.MaxOutput:
			limit := *candidate.MaxOutput
			envelope.MaxOutput = &limit
		}
		envelope.SupportsTools = envelope.SupportsTools && candidate.SupportsTools
		envelope.SupportsVision = envelope.SupportsVision && candidate.SupportsVision
		envelope.SupportsAudioInput = envelope.SupportsAudioInput && candidate.SupportsAudioInput
		envelope.SupportsReasoning = envelope.SupportsReasoning && candidate.SupportsReasoning
	}
	envelopes := make([]modelEnvelope, 0, len(byModel))
	for _, envelope := range byModel {
		envelopes = append(envelopes, *envelope)
	}
	sort.Slice(envelopes, func(i, j int) bool { return envelopes[i].Model < envelopes[j].Model })
	return envelopes
}

// jevCandidates renders the envelopes in the Jev client's own candidate type. The
// mapping is total: the client's validation rejects only empty identifiers,
// non-positive context windows and impossible output ceilings, none of which a
// registry snapshot can contain.
//
// Each candidate carries an explicit description, because the derived summary
// would describe a logical model as if it had one provider's capabilities, while
// the envelope is the conservative guarantee across every provider pair that is
// currently eligible for it.
func jevCandidates(envelopes []modelEnvelope) []jev.Candidate {
	candidates := make([]jev.Candidate, 0, len(envelopes))
	for _, envelope := range envelopes {
		candidates = append(candidates, jev.Candidate{
			Model:             envelope.Model,
			ContextWindow:     envelope.ContextWindow,
			MaxOutput:         envelope.MaxOutput,
			SupportsTools:     envelope.SupportsTools,
			SupportsVision:    envelope.SupportsVision,
			SupportsReasoning: envelope.SupportsReasoning,
			Description:       envelopeDescription(envelope),
		})
	}
	return candidates
}

// envelopeDescription renders the capability summary shown next to a model name
// in the Jev request. It states the guarantee explicitly, because "context window
// 128000" alone would read as a property of the model rather than as the floor
// across its eligible providers.
func envelopeDescription(envelope modelEnvelope) string {
	output := "unknown"
	if envelope.MaxOutput != nil {
		output = fmt.Sprintf("%d", *envelope.MaxOutput)
	}
	return fmt.Sprintf("context window %d tokens, max output %s tokens, tools %s, images %s, audio %s, reasoning %s, guaranteed across all currently eligible provider pairs",
		envelope.ContextWindow, output,
		yesNo(envelope.SupportsTools), yesNo(envelope.SupportsVision),
		yesNo(envelope.SupportsAudioInput), yesNo(envelope.SupportsReasoning))
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
