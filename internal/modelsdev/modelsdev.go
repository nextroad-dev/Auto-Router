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
// "<provider>/<model>", because some upstream providers key their models with
// the provider prefix. Entries that resolve to a model without a usable context
// window are skipped with a warning so one bad upstream record cannot block the
// rest of the allowlist.
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
		upstream, ok := lookupModel(provider, providerKey, modelRef)
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
		result.Pairs = append(result.Pairs, models.Pair{
			ProviderKey:       providerKey,
			ModelID:           logicalID,
			UpstreamModelID:   logicalID,
			ContextWindow:     contextWindow,
			MaxOutput:         maxOutput,
			SupportsTools:     upstream.ToolCall,
			SupportsVision:    supportsImageInput(upstream),
			SupportsReasoning: upstream.Reasoning,
			Enabled:           true,
			Source:            models.SourceModelsDev,
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

func supportsImageInput(model Model) bool {
	if model.Modalities == nil {
		return false
	}
	for _, modality := range model.Modalities.Input {
		if strings.EqualFold(modality, "image") {
			return true
		}
	}
	return false
}

func lookupModel(provider Provider, providerKey, modelRef string) (Model, bool) {
	if model, ok := provider.Models[modelRef]; ok {
		return model, true
	}
	// Some providers key the model by its full upstream ID, which already
	// includes the provider prefix (for example "groq/compound-mini").
	model, ok := provider.Models[providerKey+"/"+modelRef]
	return model, ok
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
