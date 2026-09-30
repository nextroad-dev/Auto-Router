package direct

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
)

func testCatalog(t *testing.T, providers ...models.Provider) *models.Catalog {
	t.Helper()
	catalog, err := models.NewCatalog(0, providers, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func testProvider(key, baseURL string) models.Provider {
	return models.Provider{Key: key, Kind: models.ProviderOpenAICompatible, DisplayName: key, BaseURL: baseURL, Enabled: true, Source: models.SourceLocal}
}

func TestSwapWithPublishesExecutorsBeforeCatalog(t *testing.T) {
	store := models.NewStore()
	registry := NewRegistry()
	first := store.SwapWith(testCatalog(t, testProvider("a", "https://a.example/v1")), registry.Build)

	second := testCatalog(t, testProvider("a", "https://a2.example/v1"), testProvider("b", "https://b.example/v1"))
	var stop sync.WaitGroup
	done := make(chan struct{})
	torn := make(chan string, 1)
	stop.Add(1)
	go func() {
		defer stop.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			catalog := store.Load()
			for _, provider := range catalog.Providers {
				if _, ok := registry.GetGeneration(catalog.Generation, provider.Key); !ok {
					select {
					case torn <- provider.Key:
					default:
					}
				}
			}
		}
	}()
	store.SwapWith(second, func(catalog *models.Catalog) {
		// Pause between "catalog built" and "catalog visible": the reader must
		// still see the previous generation, whose executors exist.
		if store.Load().Generation != first.Generation {
			t.Error("the next catalog became visible before its executors were built")
		}
		time.Sleep(20 * time.Millisecond)
		registry.Build(catalog)
		time.Sleep(20 * time.Millisecond)
	})
	time.Sleep(10 * time.Millisecond)
	close(done)
	stop.Wait()
	select {
	case key := <-torn:
		t.Fatalf("a request observed a catalog generation without executor for %q", key)
	default:
	}

	oldExecutor, ok := registry.GetGeneration(first.Generation, "a")
	if !ok {
		t.Fatal("the previous generation is no longer resolvable for in-flight requests")
	}
	newExecutor, _ := registry.GetGeneration(second.Generation, "a")
	if oldExecutor == newExecutor {
		t.Fatal("generations share one executor although the endpoint changed")
	}
	if _, ok := registry.GetGeneration(first.Generation, "b"); ok {
		t.Fatal("a provider added in a later generation leaked into an earlier one")
	}
}

func TestRegistryRetainsBoundedGenerations(t *testing.T) {
	store := models.NewStore()
	registry := NewRegistry()
	first := store.SwapWith(testCatalog(t, testProvider("a", "https://a.example/v1")), registry.Build)
	for i := 0; i < retainedGenerations; i++ {
		store.SwapWith(testCatalog(t, testProvider("a", "https://a.example/v1")), registry.Build)
	}
	if _, ok := registry.GetGeneration(first.Generation, "a"); ok {
		t.Fatal("an evicted generation still resolved")
	}
	if _, ok := registry.GetGeneration(store.Load().Generation, "a"); !ok {
		t.Fatal("the latest generation did not resolve")
	}
}

func TestOutboundURLEscapesModelPathExactlyOnce(t *testing.T) {
	var gotURI string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.RequestURI
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	for _, tc := range []struct {
		base  string
		model string
		want  string
	}{
		{server.URL + "/v1beta", "gemini-2.5-pro", "/v1beta/models/gemini-2.5-pro:generateContent"},
		{server.URL + "/v1beta", "tunedModels/foo bar%1", "/v1beta/models/tunedModels%2Ffoo%20bar%251:generateContent"},
		// An escaped base path prefix is preserved alongside the escaped override.
		{server.URL + "/pre%2Ffix", "a/b", "/pre%2Ffix/v1beta/models/a%2Fb:generateContent"},
	} {
		executor, err := New(Config{BaseURL: tc.base, Kind: models.ProviderGemini})
		if err != nil {
			t.Fatal(err)
		}
		response, err := executor.Do(context.Background(), &providers.Request{
			Protocol: providers.ProtocolGeminiGenerateContent, Model: tc.model,
			UpstreamPath: "/v1beta/models/" + url.PathEscape(tc.model) + ":generateContent",
		})
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if gotURI != tc.want {
			t.Fatalf("model %q: outbound URI = %q; want %q", tc.model, gotURI, tc.want)
		}
	}
}

func TestProxyIdentityHeadersAreNotForwarded(t *testing.T) {
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer server.Close()
	executor, err := New(Config{BaseURL: server.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{}
	for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip"} {
		header.Set(name, "spoofed")
	}
	header.Set("X-Custom", "kept")
	response, err := executor.Do(context.Background(), &providers.Request{Protocol: providers.ProtocolChatCompletions, Model: "m", Header: header})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip"} {
		// net/http's test server never adds these itself.
		if value := got.Get(name); value != "" {
			t.Fatalf("%s was forwarded upstream: %q", name, value)
		}
	}
	if got.Get("X-Custom") != "kept" {
		t.Fatal("an ordinary client header was dropped")
	}
}

func stallingServer(t *testing.T, chunks int, gap time.Duration, stallAfter bool) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for i := 0; i < chunks; i++ {
			_, _ = io.WriteString(w, "data: x\n\n")
			flusher.Flush()
			time.Sleep(gap)
		}
		if stallAfter {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
	}))
	t.Cleanup(func() { close(release); server.Close() })
	return server
}

func TestNonStreamBodyStallIsAnUpstreamTimeout(t *testing.T) {
	server := stallingServer(t, 1, 0, true)
	executor, err := New(Config{BaseURL: server.URL + "/v1", BodyTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	response, err := executor.Do(context.Background(), &providers.Request{Protocol: providers.ProtocolChatCompletions, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	started := time.Now()
	_, err = io.ReadAll(response.Body)
	if !errors.Is(err, providers.ErrUpstreamTimeout) {
		t.Fatalf("read error = %v; want ErrUpstreamTimeout", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("the stalled body was not interrupted promptly")
	}
}

func TestSlowButActiveStreamIsNotTruncated(t *testing.T) {
	server := stallingServer(t, 5, 30*time.Millisecond, false)
	executor, err := New(Config{BaseURL: server.URL + "/v1", StreamIdleTimeout: 200 * time.Millisecond, BodyTimeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	response, err := executor.Do(context.Background(), &providers.Request{Protocol: providers.ProtocolChatCompletions, Model: "m", Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("an active stream was interrupted: %v", err)
	}
	if strings.Count(string(body), "data: x") != 5 {
		t.Fatalf("stream body = %q; want 5 events", body)
	}
}

func TestClientCancellationStopsAStalledBody(t *testing.T) {
	server := stallingServer(t, 1, 0, true)
	executor, err := New(Config{BaseURL: server.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	response, err := executor.Do(ctx, &providers.Request{Protocol: providers.ProtocolChatCompletions, Model: "m", Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	time.AfterFunc(30*time.Millisecond, cancel)
	_, err = io.ReadAll(response.Body)
	if err == nil || errors.Is(err, providers.ErrUpstreamTimeout) {
		t.Fatalf("read error = %v; want a cancellation, not a timeout", err)
	}
}
