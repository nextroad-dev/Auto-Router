// Package modelsdev fetches and maps the models.dev catalog. It performs no SQL
// and no routing: it turns a payload into registry entries the storage layer can
// persist. Pricing and other unsupported metadata is deliberately discarded.
package modelsdev

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/models"
)

// DefaultURL is the public models.dev catalog endpoint.
const DefaultURL = "https://models.dev/api.json"

// MaxPayloadBytes bounds the fetched catalog. The upstream snapshot is a few
// megabytes; 32 MiB leaves room for growth without allowing an unbounded read.
const MaxPayloadBytes = 32 << 20

// Provider is one models.dev provider entry. Unknown fields are ignored.
type Provider struct {
	ID     string           `json:"id"`
	Name   string           `json:"name"`
	Models map[string]Model `json:"models"`
}

// Model is one models.dev model entry. Only the fields the router actually uses
// are decoded; pricing, knowledge cutoffs, release dates and temperatures are
// intentionally absent.
type Model struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	ToolCall   bool        `json:"tool_call"`
	Reasoning  bool        `json:"reasoning"`
	Modalities *Modalities `json:"modalities"`
	Limit      *Limit      `json:"limit"`
}

// Modalities describes accepted and produced input types.
type Modalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

// Limit carries the context and output-token capacities.
type Limit struct {
	Context int  `json:"context"`
	Output  *int `json:"output"`
}

// Document maps provider keys to provider entries.
type Document map[string]Provider

// Client fetches the raw catalog payload.
type Client struct {
	url      string
	timeout  time.Duration
	maxBytes int64
	http     *http.Client
}

// NewClient validates the source URL and builds a bounded fetcher.
func NewClient(rawURL string, timeout time.Duration) (*Client, error) {
	if err := models.ValidateSourceURL(rawURL); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		return nil, errors.New("models.dev timeout must be positive")
	}
	return &Client{
		url:      rawURL,
		timeout:  timeout,
		maxBytes: MaxPayloadBytes,
		http:     &http.Client{Timeout: timeout},
	}, nil
}

// URL returns the configured source URL, without credentials (the URL validator
// rejects userinfo).
func (c *Client) URL() string { return c.url }

// Fetch downloads the catalog. Non-2xx responses, oversized bodies, transport
// errors and context cancellation are all reported without echoing the URL
// query (there is none) or any response body.
func (c *Client) Fetch(ctx context.Context) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("build models.dev request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", c.url, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("fetch %s: unexpected HTTP status %d", c.url, response.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, c.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", c.url, err)
	}
	if int64(len(payload)) > c.maxBytes {
		return nil, fmt.Errorf("fetch %s: catalog exceeds %d bytes", c.url, c.maxBytes)
	}
	return payload, nil
}

// Decode parses a payload leniently: unknown fields are ignored, but a payload
// that is not a JSON object, has a provider without a models object, or has
// duplicate provider/model keys is a format error rather than a partial import.
func Decode(payload []byte) (Document, error) {
	document := Document{}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode models.dev catalog: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode models.dev catalog: payload must contain exactly one JSON object")
	}
	if document == nil {
		return nil, errors.New("decode models.dev catalog: payload must be a JSON object")
	}
	for key, provider := range document {
		if provider.Models == nil {
			return nil, fmt.Errorf("decode models.dev catalog: provider %q has no models object", key)
		}
	}
	return document, nil
}

// SkipReason for a whitelisted model that upstream cannot represent as a valid
// pair.
const skipMissingContext = "missing or non-positive context window"

// Result is a mapped allowlist import. Providers carry metadata only; base URLs
// and credentials remain local configuration.
type Result struct {
	Providers []models.Provider
	Models    []models.Model
	Pairs     []models.Pair
	Warnings  []string
	Skipped   int
}

// Map selects the allowlist entries from a document and converts them into
// registry entries. Every allowlist entry must exist: an entry that cannot be
// resolved fails the whole synchronization, because a silently ignored
// allowlist entry leaves the operator believing a model is routable.
//
// Model references are matched by exact map key first, then by
// "<provider>/<model>", and finally by a unique prefix/keyword match. The final
// fallback handles upstreams that expose deployment-specific prefixes while
// models.dev catalogs the underlying model. Ambiguous matches are treated as
// missing rather than importing capabilities from the wrong model. Entries that
// resolve to a model without a usable context window are skipped with a warning.
func Map(document Document, include []string) (*Result, error) {
	result := &Result{}
	missing := make([]string, 0)
	seenInclude := make(map[string]struct{}, len(include))
	providerIndex := map[string]int{}
	modelIndex := map[string]int{}
	for _, entry := range include {
		if _, duplicate := seenInclude[entry]; duplicate {
			continue
		}
		seenInclude[entry] = struct{}{}
		providerKey, modelRef, err := models.SplitIncludeEntry(entry)
		if err != nil {
			return nil, err
		}
		provider, ok := document[providerKey]
		if !ok {
			missing = append(missing, entry)
			continue
		}
		upstream, ok, matchBy := lookupModel(provider, providerKey, modelRef)
		if !ok {
			missing = append(missing, entry)
			continue
		}
		if upstream.Limit == nil || upstream.Limit.Context <= 0 {
			result.Skipped++
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: skipped because the %s upstream is %d", entry, skipMissingContext, limitContext(upstream)))
			continue
		}
		contextWindow := upstream.Limit.Context
		maxOutput := upstream.Limit.Output
		if maxOutput != nil && *maxOutput <= 0 {
			// A non-positive output limit carries no information; keep it NULL
			// instead of inventing a capacity.
			maxOutput = nil
		}
		if maxOutput != nil && *maxOutput > contextWindow {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: max_output %d exceeds context_window %d; clamped to the context window", entry, *maxOutput, contextWindow))
			clamped := contextWindow
			maxOutput = &clamped
		}
		displayName := providerDisplayName(providerKey, provider)
		modelDisplayName := upstream.Name
		if strings.TrimSpace(modelDisplayName) == "" {
			modelDisplayName = upstream.ID
		}
		if strings.TrimSpace(modelDisplayName) == "" {
			modelDisplayName = modelRef
		}
		logicalID := upstream.ID
		if logicalID == "" {
			logicalID = modelRef
		}
		if _, ok := providerIndex[providerKey]; !ok {
			providerIndex[providerKey] = len(result.Providers)
			result.Providers = append(result.Providers, models.Provider{
				Key:         providerKey,
				DisplayName: displayName,
				Source:      models.SourceModelsDev,
			})
		}
		if _, ok := modelIndex[logicalID]; !ok {
			modelIndex[logicalID] = len(result.Models)
			result.Models = append(result.Models, models.Model{
				ID:          logicalID,
				DisplayName: modelDisplayName,
				Enabled:     true,
				Source:      models.SourceModelsDev,
			})
		}
		upstreamModelID := logicalID
		if matchBy == "prefix" || matchBy == "keyword" {
			// Preserve the ID actually exposed by this provider even when its
			// prefix differed from the models.dev record used for capabilities.
			upstreamModelID = modelRef
		}
		result.Pairs = append(result.Pairs, models.Pair{
			ProviderKey:       providerKey,
			ModelID:           logicalID,
			UpstreamModelID:   upstreamModelID,
			ContextWindow:     contextWindow,
			MaxOutput:         maxOutput,
			SupportsTools:     upstream.ToolCall,
			SupportsVision:    supportsImageInput(upstream),
			SupportsReasoning: upstream.Reasoning,
			// Audio input is mapped from the same modalities list but into its own
			// capability bit: the two never substitute for each other.
			SupportsAudioInput: supportsAudioInput(upstream),
			Enabled:            true,
			Source:             models.SourceModelsDev,
		})
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("models.dev catalog is missing %d allowlisted entr%s: %s", len(missing), plural(len(missing), "y", "ies"), formatMissing(missing))
	}
	return result, nil
}

func limitContext(model Model) int {
	if model.Limit == nil {
		return 0
	}
	return model.Limit.Context
}

func providerDisplayName(key string, provider Provider) string {
	if strings.TrimSpace(provider.Name) != "" {
		return provider.Name
	}
	return key
}

// supportsImageInput reports whether the upstream model accepts image input.
// Only the declared modalities are read, and only when the list is present: an
// absent `modalities` object means "not observed", which is mapped to false
// rather than guessed at.
func supportsImageInput(model Model) bool {
	return declaresInputModality(model, "image")
}

// supportsAudioInput reports whether the upstream model accepts audio input.
// Audio and image are independent bits: a model that accepts both reports both,
// and a model that accepts neither reports neither. Pricing and every other
// unsupported upstream field continue to be discarded by the mapper, so audio
// support does not become a second reason to widen what is stored.
func supportsAudioInput(model Model) bool {
	return declaresInputModality(model, "audio")
}

// declaresInputModality reports whether `modalities.input` lists a modality,
// case-insensitively. A missing modalities object or a missing input list is
// false, so an unknown capability can never be read as support.
func declaresInputModality(model Model, modality string) bool {
	if model.Modalities == nil {
		return false
	}
	for _, declared := range model.Modalities.Input {
		if strings.EqualFold(declared, modality) {
			return true
		}
	}
	return false
}

func lookupModel(provider Provider, providerKey, modelRef string) (Model, bool, string) {
	if model, ok := provider.Models[modelRef]; ok {
		return model, true, "exact"
	}
	// Some providers key the model by its full upstream ID, which already
	// includes the provider prefix (for example "groq/compound-mini").
	if model, ok := provider.Models[providerKey+"/"+modelRef]; ok {
		return model, true, "exact"
	}
	bestScore, bestMethod := 0, ""
	var best Model
	ambiguous := false
	for key, candidate := range provider.Models {
		score, method := bestModelMatch(modelRef, key, candidate.ID, candidate.Name)
		if score > bestScore {
			best, bestScore, bestMethod, ambiguous = candidate, score, method, false
			continue
		}
		if score == bestScore && score > 0 && !sameModelCatalogRecord(best, candidate) {
			ambiguous = true
		}
	}
	if bestScore == 0 || ambiguous {
		return Model{}, false, ""
	}
	return best, true, bestMethod
}

func sameModelCatalogRecord(left, right Model) bool {
	leftLimit, rightLimit := left.Limit, right.Limit
	if left.ToolCall != right.ToolCall || left.Reasoning != right.Reasoning ||
		supportsImageInput(left) != supportsImageInput(right) || supportsAudioInput(left) != supportsAudioInput(right) {
		return false
	}
	if leftLimit == nil || rightLimit == nil {
		return leftLimit == nil && rightLimit == nil
	}
	if leftLimit.Context != rightLimit.Context || leftLimit.Output == nil || rightLimit.Output == nil {
		return leftLimit.Context == rightLimit.Context && leftLimit.Output == nil && rightLimit.Output == nil
	}
	return *leftLimit.Output == *rightLimit.Output
}

const maxListedMissing = 20

func formatMissing(missing []string) string {
	sorted := append([]string(nil), missing...)
	sort.Strings(sorted)
	if len(sorted) <= maxListedMissing {
		return strings.Join(sorted, ", ")
	}
	return strings.Join(sorted[:maxListedMissing], ", ") + fmt.Sprintf(", and %d more", len(sorted)-maxListedMissing)
}

func plural(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

// IncludeDigest fingerprints the allowlist so operators can tell whether two
// sync runs used the same intent. The digest is order- and duplicate-invariant.
func IncludeDigest(include []string) string {
	unique := make([]string, 0, len(include))
	seen := make(map[string]struct{}, len(include))
	for _, entry := range include {
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		unique = append(unique, entry)
	}
	sort.Strings(unique)
	sum := sha256.Sum256([]byte(strings.Join(unique, "\n")))
	return hex.EncodeToString(sum[:])
}
