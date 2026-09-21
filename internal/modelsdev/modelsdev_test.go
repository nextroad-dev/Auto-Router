package modelsdev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/models"
)

func fixture(t *testing.T) Document {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("testdata", "api.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := Decode(payload)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func TestNewClientValidatesSource(t *testing.T) {
	if _, err := NewClient("http://models.dev/api.json", time.Second); err == nil {
		t.Fatal("remote http must be rejected")
	}
	if _, err := NewClient("https://models.dev/api.json", 0); err == nil {
		t.Fatal("zero timeout must be rejected")
	}
	client, err := NewClient("http://127.0.0.1:9999/api.json", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if client.URL() != "http://127.0.0.1:9999/api.json" {
		t.Fatalf("unexpected URL: %q", client.URL())
	}
}

func TestFetchReportsStatusTimeoutAndOversize(t *testing.T) {
	cases := map[string]struct {
		handler func(w http.ResponseWriter, r *http.Request)
		timeout time.Duration
		max     int64
		want    string
	}{
		"server error": {handler: func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) }, want: "status 500"},
		"not found":    {handler: func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, want: "status 404"},
		"slow": {handler: func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
			_, _ = w.Write([]byte("{}"))
		}, timeout: 20 * time.Millisecond, want: "fetch"},
		"oversized": {handler: func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(bytes.Repeat([]byte("a"), 4096))
		}, max: 128, want: "exceeds"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tc.handler))
			defer server.Close()
			timeout := tc.timeout
			if timeout == 0 {
				timeout = 2 * time.Second
			}
			client, err := NewClient(server.URL, timeout)
			if err != nil {
				t.Fatal(err)
			}
			if tc.max > 0 {
				client.maxBytes = tc.max
			}
			_, err = client.Fetch(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestFetchPropagatesCancellationAndReadsBody(t *testing.T) {
	released := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-released:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`{"openai":{"id":"openai","models":{}}}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.Fetch(ctx); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	close(released)
	payload, err := client.Fetch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(payload); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeRejectsMalformedPayloads(t *testing.T) {
	cases := map[string]struct {
		payload string
		want    string
	}{
		"malformed":           {payload: `{"openai":`, want: "decode"},
		"array":               {payload: `[]`, want: "decode"},
		"trailing garbage":    {payload: `{} {}`, want: "exactly one"},
		"null":                {payload: `null`, want: "JSON object"},
		"missing models":      {payload: `{"openai":{"id":"openai","name":"OpenAI"}}`, want: "no models object"},
		"provider not object": {payload: `{"openai":"nope"}`, want: "decode"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode([]byte(tc.payload)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
	// Lenient: unknown fields at every level are ignored, an empty models
	// object is structurally valid (entries simply go missing later).
	payload := `{
		"openai": {
			"id": "openai", "name": "OpenAI", "brand_new_field": {"nested": [1,2,3]},
			"models": {
				"gpt-4o": {"id": "gpt-4o", "name": "GPT-4o", "future_field": true,
					"limit": {"context": 128000, "output": 16384, "future": 1},
					"modalities": {"input": ["text"], "output": ["text"], "extra": []}}
			}
		},
		"empty": {"id": "empty", "models": {}}
	}`
	document, err := Decode([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if len(document) != 2 || len(document["openai"].Models) != 1 {
		t.Fatalf("lenient decode produced %+v", document)
	}
}

func TestMapFixture(t *testing.T) {
	document := fixture(t)
	result, err := Map(document, []string{
		"openai/gpt-4o-mini",
		"openai/gpt-4.1-nano",
		"helicone/gpt-4.1-nano",
		"groq/llama-3.3-70b-versatile",
		"groq/compound-mini",
		"openai/gpt-3.5-turbo",
		"qiniu-ai/meituan/longcat-flash-lite",
		"openai/gpt-4o-mini", // duplicate must not add a second pair
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pairs) != 7 {
		t.Fatalf("pairs = %d, want 7", len(result.Pairs))
	}
	// Six distinct logical models: gpt-4o-mini and gpt-4.1-nano are shared by
	// openai and helicone and must fold into one model row each.
	if len(result.Models) != 6 || len(result.Providers) != 4 {
		t.Fatalf("models=%d providers=%d", len(result.Models), len(result.Providers))
	}
	if result.Skipped != 0 {
		t.Fatalf("unexpected skips: %+v", result.Warnings)
	}

	byPair := map[string]models.Pair{}
	for _, pair := range result.Pairs {
		byPair[pair.ProviderKey+" "+pair.ModelID] = pair
	}
	mini := byPair["openai gpt-4o-mini"]
	if mini.ContextWindow != 128000 || mini.MaxOutput == nil || *mini.MaxOutput != 16384 {
		t.Fatalf("gpt-4o-mini mapping: %+v", mini)
	}
	if !mini.SupportsTools || !mini.SupportsVision || mini.SupportsReasoning {
		t.Fatalf("gpt-4o-mini capabilities: %+v", mini)
	}
	if mini.UpstreamModelID != "gpt-4o-mini" {
		t.Fatalf("upstream model ID = %q", mini.UpstreamModelID)
	}
	if !mini.Enabled || mini.Source != models.SourceModelsDev {
		t.Fatalf("synced pairs are enabled models.dev entries: %+v", mini)
	}
	noVision := byPair["openai gpt-3.5-turbo"]
	if noVision.SupportsVision || noVision.SupportsTools {
		t.Fatalf("gpt-3.5-turbo capabilities: %+v", noVision)
	}

	// Provider-prefixed upstream key: the allowlist names "groq/compound-mini"
	// and the upstream map key is "groq/compound-mini", which also becomes the
	// logical model ID.
	compound := byPair["groq groq/compound-mini"]
	if compound.UpstreamModelID != "groq/compound-mini" || compound.ContextWindow != 131072 {
		t.Fatalf("prefix fallback mapping: %+v", compound)
	}
	// A slash inside the model reference must be preserved.
	longcat := byPair["qiniu-ai meituan/longcat-flash-lite"]
	if longcat.ContextWindow != 256000 {
		t.Fatalf("longcat mapping: %+v", longcat)
	}
	if longcat.MaxOutput == nil || *longcat.MaxOutput != 256000 {
		t.Fatalf("max_output must be clamped to the context window: %+v", longcat.MaxOutput)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "clamped") {
		t.Fatalf("expected one clamp warning, got %v", result.Warnings)
	}

	// Provider and logical model rows are deduplicated even when several pairs
	// share them.
	providerKeys := map[string]int{}
	for _, provider := range result.Providers {
		providerKeys[provider.Key]++
	}
	if providerKeys["openai"] != 1 {
		t.Fatalf("provider rows must be deduplicated: %v", providerKeys)
	}
	modelIDs := map[string]int{}
	for _, model := range result.Models {
		modelIDs[model.ID]++
	}
	if modelIDs["gpt-4.1-nano"] != 1 {
		t.Fatalf("logical model rows must be deduplicated across providers: %v", modelIDs)
	}

	// Synchronized providers carry metadata only: no base URL, no key, and
	// disabled, so a sync alone can never forward a request.
	for _, provider := range result.Providers {
		if provider.Enabled || provider.BaseURL != "" || provider.APIKey != "" {
			t.Fatalf("synced providers must be inert until configured locally: %+v", provider)
		}
	}
	// The catalog built from this result is valid, and once the two providers
	// are configured locally the shared model routes through both.
	configured := make([]models.Provider, len(result.Providers))
	for i, provider := range result.Providers {
		provider.Enabled = true
		provider.BaseURL = "https://" + provider.Key + ".example/v1"
		configured[i] = provider
	}
	catalog, err := models.NewCatalog(0, configured, result.Models, result.Pairs)
	if err != nil {
		t.Fatal(err)
	}
	if got := catalog.PairsForModel("gpt-4.1-nano"); len(got) != 2 {
		t.Fatalf("expected two providers for gpt-4.1-nano, got %+v", got)
	}
}

func TestMappedResultContainsNoPricingOrCostFields(t *testing.T) {
	document := fixture(t)
	result, err := Map(document, []string{"openai/gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"cost", "price", "input_price", "output_price"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("mapped result unexpectedly contains %q: %s", forbidden, encoded)
		}
	}
	if bytes.Contains(encoded, []byte("0.15")) {
		t.Fatal("pricing from the fixture leaked into the mapped result")
	}
}

func TestMapFailsOnEveryMissingAllowlistEntry(t *testing.T) {
	document := fixture(t)
	_, err := Map(document, []string{"openai/gpt-4o-mini", "missing/model", "openai/does-not-exist", "ghost/gpt-4o"})
	if err == nil {
		t.Fatal("missing allowlist entries must fail the import")
	}
	for _, want := range []string{"3 allowlisted entries", "ghost/gpt-4o", "missing/model", "openai/does-not-exist"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q should mention %q", err, want)
		}
	}
	if _, err := Map(document, []string{"openai"}); err == nil {
		t.Fatal("an entry without a slash must fail")
	}
}

func TestMapSkipsModelsWithoutUsableContextWindow(t *testing.T) {
	document := fixture(t)
	zero := 0
	document["synthetic"] = Provider{
		ID:   "synthetic",
		Name: "Synthetic",
		Models: map[string]Model{
			"no-context":   {ID: "no-context", Name: "No Context"},
			"zero-context": {ID: "zero-context", Name: "Zero Context", Limit: &Limit{Context: 0}},
			"no-output":    {ID: "no-output", Name: "No Output", Limit: &Limit{Context: 4096, Output: nil}},
			"zero-output":  {ID: "zero-output", Name: "Zero Output", Limit: &Limit{Context: 4096, Output: &zero}},
		},
	}
	result, err := Map(document, []string{
		"synthetic/no-context",
		"synthetic/zero-context",
		"synthetic/no-output",
		"synthetic/zero-output",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 2 {
		t.Fatalf("skipped = %d, want 2", result.Skipped)
	}
	if len(result.Pairs) != 2 {
		t.Fatalf("pairs = %d, want 2", len(result.Pairs))
	}
	for _, pair := range result.Pairs {
		if pair.MaxOutput != nil {
			t.Fatalf("a missing or zero limit.output must stay NULL: %+v", pair)
		}
	}
	if len(result.Warnings) != 2 {
		t.Fatalf("warnings = %v", result.Warnings)
	}
}

func TestMapUsesIDAndFallbackNames(t *testing.T) {
	document := Document{
		"plain": {ID: "plain", Models: map[string]Model{
			"model": {Name: "Only Name", Limit: &Limit{Context: 100}},
		}},
		"unnamed": {ID: "unnamed", Models: map[string]Model{
			"model": {ID: "model-id", Limit: &Limit{Context: 100}},
		}},
	}
	result, err := Map(document, []string{"plain/model", "unnamed/model"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pairs) != 2 {
		t.Fatalf("pairs = %+v", result.Pairs)
	}
	if result.Pairs[0].ModelID != "model" || result.Pairs[0].UpstreamModelID != "model" {
		t.Fatalf("missing id field should fall back to the map key: %+v", result.Pairs[0])
	}
	if result.Models[0].DisplayName != "Only Name" {
		t.Fatalf("model name should be used when present: %+v", result.Models[0])
	}
	if result.Pairs[1].ModelID != "model-id" || result.Models[1].DisplayName != "model-id" {
		t.Fatalf("id field should win over the map key when present: %+v", result.Pairs[1])
	}
	// Providers without a display name fall back to their key.
	if result.Providers[0].DisplayName != "plain" || result.Providers[1].DisplayName != "unnamed" {
		t.Fatalf("provider display names: %+v", result.Providers)
	}
}

func TestIncludeDigestIsOrderAndDuplicateInvariant(t *testing.T) {
	first := IncludeDigest([]string{"openai/gpt-4o", "groq/x"})
	second := IncludeDigest([]string{"groq/x", "openai/gpt-4o", "openai/gpt-4o"})
	if first != second || first == "" {
		t.Fatalf("digest should ignore order and duplicates: %q vs %q", first, second)
	}
	if first == IncludeDigest([]string{"openai/gpt-4o"}) {
		t.Fatal("different allowlists must produce different digests")
	}
	if IncludeDigest(nil) == IncludeDigest([]string{"openai/gpt-4o"}) {
		t.Fatal("empty allowlist digest must differ")
	}
}
