package models

import (
	"fmt"
	"strings"
	"testing"
)

func provider(key string, priority int, enabled bool) Provider {
	return Provider{
		Key:         key,
		DisplayName: key,
		BaseURL:     "https://" + key + ".example/v1",
		Enabled:     enabled,
		Priority:    priority,
		Source:      SourceModelsDev,
	}
}

func model(id string, priority int, enabled bool) Model {
	return Model{ID: id, DisplayName: id, Enabled: enabled, Priority: priority, Source: SourceModelsDev}
}

func pair(providerKey, modelID string, priority int, enabled bool) Pair {
	return Pair{
		ProviderKey:     providerKey,
		ModelID:         modelID,
		UpstreamModelID: modelID,
		ContextWindow:   8192,
		Enabled:         enabled,
		Priority:        priority,
		Source:          SourceModelsDev,
	}
}

func mustCatalog(t *testing.T, providers []Provider, models []Model, pairs []Pair) *Catalog {
	t.Helper()
	catalog, err := NewCatalog(0, providers, models, pairs)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestNewCatalogRejectsInvalidSnapshots(t *testing.T) {
	cases := map[string]struct {
		providers []Provider
		models    []Model
		pairs     []Pair
	}{
		"duplicate provider": {providers: []Provider{provider("a", 0, true), provider("a", 1, true)}},
		"duplicate model":    {models: []Model{model("m", 0, true), model("m", 1, true)}},
		"dangling provider":  {providers: []Provider{provider("a", 0, true)}, models: []Model{model("m", 0, true)}, pairs: []Pair{pair("b", "m", 0, true)}},
		"dangling model":     {providers: []Provider{provider("a", 0, true)}, models: []Model{model("m", 0, true)}, pairs: []Pair{pair("a", "other", 0, true)}},
		"duplicate pair": {
			providers: []Provider{provider("a", 0, true)},
			models:    []Model{model("m", 0, true)},
			pairs:     []Pair{pair("a", "m", 0, true), pair("a", "m", 1, false)},
		},
		"invalid provider": {providers: []Provider{{Key: "A", DisplayName: "A", Source: SourceLocal}}},
		"capacity":         {providers: []Provider{provider("a", 0, true)}, models: []Model{model("m", 0, true)}, pairs: []Pair{{ProviderKey: "a", ModelID: "m", UpstreamModelID: "m", ContextWindow: 0, Source: SourceModelsDev}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewCatalog(0, tc.providers, tc.models, tc.pairs); err == nil {
				t.Fatal("expected catalog validation error")
			}
		})
	}
}

func TestNewCatalogCopiesAndSortsInputs(t *testing.T) {
	providers := []Provider{provider("b", 1, true), provider("a", 1, true), provider("c", 0, false)}
	models := []Model{model("m2", 5, true), model("m1", 5, true), model("m0", 1, true), model("m3", 1, true)}
	pairs := []Pair{
		pair("b", "m2", 1, true),
		pair("a", "m2", 1, true),
		pair("a", "m1", 1, true),
		pair("c", "m0", 0, false),
		pair("c", "m3", 1, true),
	}
	catalog := mustCatalog(t, providers, models, pairs)

	wantProviders := []string{"c", "a", "b"}
	for i, want := range wantProviders {
		if catalog.Providers[i].Key != want {
			t.Fatalf("provider order = %v, want %v", providerKeys(catalog.Providers), wantProviders)
		}
	}
	wantModels := []string{"m0", "m3", "m1", "m2"}
	for i, want := range wantModels {
		if catalog.Models[i].ID != want {
			t.Fatalf("model order = %v, want %v", catalog.Models, wantModels)
		}
	}
	type pairKey struct {
		provider string
		priority int
		model    string
	}
	wantPairs := []pairKey{
		{"c", 0, "m0"},
		{"c", 1, "m3"},
		{"a", 1, "m1"},
		{"a", 1, "m2"},
		{"b", 1, "m2"},
	}
	if len(catalog.Pairs) != len(wantPairs) {
		t.Fatalf("pair count = %d, want %d", len(catalog.Pairs), len(wantPairs))
	}
	for i, want := range wantPairs {
		got := catalog.Pairs[i]
		if got.ProviderKey != want.provider || got.ModelID != want.model || got.Priority != want.priority {
			t.Fatalf("pair %d = %s/%s@%d, want %s/%s@%d", i, got.ProviderKey, got.ModelID, got.Priority, want.provider, want.model, want.priority)
		}
	}
	// Sorting must not mutate the caller's slices: they may be reused by the caller.
	if providers[0].Key != "b" || models[0].ID != "m2" || pairs[0].ProviderKey != "b" {
		t.Fatal("NewCatalog mutated its inputs")
	}
}

func TestCatalogLookupsRespectEnabledFlags(t *testing.T) {
	catalog := mustCatalog(t,
		[]Provider{provider("a", 0, true), provider("b", 0, false)},
		[]Model{model("m", 0, true), model("off", 0, false), model("m2", 0, true)},
		[]Pair{
			pair("a", "m", 0, true),
			pair("a", "off", 0, true),
			pair("b", "m", 0, true),
			pair("a", "m2", 0, false),
		},
	)
	if _, ok := catalog.Provider("a"); !ok {
		t.Fatal("provider a should be found")
	}
	if _, ok := catalog.Provider("missing"); ok {
		t.Fatal("unknown provider must not be found")
	}
	if m, ok := catalog.Lookup("m"); !ok || !m.Enabled {
		t.Fatal("model m should be found and enabled")
	}
	if _, ok := catalog.Lookup("missing"); ok {
		t.Fatal("unknown model must not be found")
	}
	pairs := catalog.PairsForModel("m")
	if len(pairs) != 1 || pairs[0].UpstreamModelID != "m" {
		t.Fatalf("expected exactly the routable enabled pair, got %+v", pairs)
	}
	if got := catalog.PairsForModel("off"); got != nil {
		t.Fatalf("disabled model must have no routable pairs, got %+v", got)
	}
	if got := catalog.PairsForModel("missing"); got != nil {
		t.Fatalf("unknown model must have no routable pairs, got %+v", got)
	}
}

func TestCatalogWarnings(t *testing.T) {
	catalog := mustCatalog(t,
		[]Provider{provider("a", 0, false), provider("b", 0, true)},
		[]Model{model("m", 0, true), model("off", 0, false)},
		[]Pair{
			pair("a", "m", 0, true),
			pair("b", "off", 0, true),
			pair("a", "off", 0, false),
		},
	)
	warnings := catalog.Warnings()
	if len(warnings) != 2 {
		t.Fatalf("expected 2 warnings, got %v", warnings)
	}
	if !strings.Contains(warnings[0], "provider \"a\" is disabled") {
		t.Fatalf("unexpected warning order/content: %v", warnings)
	}
	if !strings.Contains(warnings[1], "model \"off\" is disabled") {
		t.Fatalf("unexpected warning order/content: %v", warnings)
	}
}

func TestCatalogWarningsAreBounded(t *testing.T) {
	providers := []Provider{provider("a", 0, false)}
	models := []Model{}
	pairs := []Pair{}
	for i := 0; i < maxWarnings+10; i++ {
		id := fmt.Sprintf("m%03d", i)
		models = append(models, model(id, 0, true))
		pairs = append(pairs, pair("a", id, 0, true))
	}
	catalog := mustCatalog(t, providers, models, pairs)
	warnings := catalog.Warnings()
	if len(warnings) != maxWarnings+1 {
		t.Fatalf("warnings should be capped at %d plus a marker, got %d", maxWarnings, len(warnings))
	}
	if !strings.Contains(warnings[len(warnings)-1], "more warnings suppressed") {
		t.Fatalf("missing truncation marker: %q", warnings[len(warnings)-1])
	}
}

func TestCatalogIndexesStayConsistentWithSlices(t *testing.T) {
	catalog := mustCatalog(t,
		[]Provider{provider("a", 0, true), provider("b", 0, false)},
		[]Model{model("m", 0, true), model("off", 0, false)},
		[]Pair{pair("a", "m", 0, true), pair("b", "m", 1, false), pair("a", "off", 1, true)},
	)
	if _, ok := catalog.providerIndex["a"]; !ok {
		t.Fatal("provider index is missing an entry")
	}
	if _, ok := catalog.modelIndex["m"]; !ok {
		t.Fatal("model index is missing an entry")
	}
	pairs := catalog.pairsByModel["m"]
	if len(pairs) != 2 {
		t.Fatalf("pair index should mirror every stored pair, including disabled ones: %+v", pairs)
	}
	// Both pairs for "m" are indexed, but only the enabled pair of the enabled
	// provider is routable.
	routable := catalog.PairsForModel("m")
	if len(routable) != 1 || routable[0].ProviderKey != "a" {
		t.Fatalf("routable pairs = %+v, want only the enabled pair of an enabled provider", routable)
	}
}

func TestCatalogCountsAndEmptiness(t *testing.T) {
	catalog := mustCatalog(t,
		[]Provider{provider("a", 0, true), provider("b", 0, false)},
		[]Model{model("m", 0, true), model("off", 0, false)},
		[]Pair{pair("a", "m", 0, true), pair("a", "off", 0, true), pair("b", "m", 0, true)},
	)
	if catalog.EnabledProviders() != 1 || catalog.EnabledModels() != 1 || catalog.EnabledPairs() != 3 {
		t.Fatalf("unexpected counts: providers=%d models=%d pairs=%d", catalog.EnabledProviders(), catalog.EnabledModels(), catalog.EnabledPairs())
	}
	if catalog.Empty() {
		t.Fatal("catalog with entries must not be empty")
	}
	empty := mustCatalog(t, nil, nil, nil)
	if !empty.Empty() {
		t.Fatal("empty catalog should report Empty")
	}
	if empty.PairsForModel("anything") != nil || len(empty.Warnings()) != 0 {
		t.Fatal("empty catalog must not panic or warn")
	}
}

func providerKeys(providers []Provider) []string {
	keys := make([]string, 0, len(providers))
	for _, item := range providers {
		keys = append(keys, item.Key)
	}
	return keys
}
