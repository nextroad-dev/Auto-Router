package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/netx"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
	"github.com/nextroad-dev/Auto-Router/internal/router/auto"
)

// capturingExecutor records every upstream request and answers 200. onCall, when
// set, runs before the answer so a test can change shared state mid-request.
type capturingExecutor struct {
	requests []providers.Request
	status   []int
	onCall   func(call int)
}

func (e *capturingExecutor) Do(_ context.Context, request *providers.Request) (*providers.Response, error) {
	e.requests = append(e.requests, *request)
	call := len(e.requests)
	if e.onCall != nil {
		e.onCall(call)
	}
	status := http.StatusOK
	if call <= len(e.status) {
		status = e.status[call-1]
	}
	return &providers.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"ok"}`))}, nil
}

func singleProviderStore(t *testing.T, kind models.ProviderKind, baseURL string) *models.Store {
	t.Helper()
	provider := models.Provider{Key: "provider-a", Kind: kind, DisplayName: "Provider A", BaseURL: baseURL, Enabled: true, Source: models.SourceLocal}
	catalog, err := models.NewCatalog(0, []models.Provider{provider}, []models.Model{
		{ID: "model-a", DisplayName: "Model A", Enabled: true, Source: models.SourceLocal},
	}, []models.Pair{
		{ProviderKey: provider.Key, ModelID: "model-a", UpstreamModelID: "model-a", ContextWindow: 8192, Enabled: true, Source: models.SourceLocal},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := models.NewStore()
	store.Swap(catalog)
	return store
}

func postJSON(handler http.Handler, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestRequestBodyMustBeExactlyOneJSONObject(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		for _, tc := range []struct {
			name string
			body string
			want int
		}{
			{"single object", `{"model":"model-a","messages":[],"vendor_extension":{"x":1}}`, http.StatusOK},
			{"trailing whitespace", "{\"model\":\"model-a\",\"messages\":[]}\n\t \n", http.StatusOK},
			{"concatenated documents", "{\"model\":\"model-a\",\"messages\":[]}\n{\"extra\":\"document\"}", http.StatusBadRequest},
			{"trailing garbage", `{"model":"model-a","messages":[]}garbage`, http.StatusBadRequest},
			{"trailing scalar", `{"model":"model-a","messages":[]} 1`, http.StatusBadRequest},
		} {
			executor := &capturingExecutor{}
			handler := New(Options{Catalog: singleProviderStore(t, models.ProviderOpenAICompatible, "https://a.example/v1"), Executor: executor})
			response := postJSON(handler, path, tc.body)
			if response.Code != tc.want {
				t.Fatalf("%s %s: status = %d, body = %s; want %d", path, tc.name, response.Code, response.Body.String(), tc.want)
			}
			if tc.want != http.StatusOK && len(executor.requests) != 0 {
				t.Fatalf("%s %s: a rejected body reached the upstream", path, tc.name)
			}
			if tc.want == http.StatusOK && !strings.Contains(string(executor.requests[0].Body), "vendor_extension") && strings.Contains(tc.body, "vendor_extension") {
				t.Fatalf("%s %s: unknown field was not passed through: %s", path, tc.name, executor.requests[0].Body)
			}
		}
	}
}

func TestStreamFieldMustBeBooleanForEveryProviderKind(t *testing.T) {
	kinds := []models.ProviderKind{models.ProviderOpenAICompatible, models.ProviderAnthropic, models.ProviderGemini}
	for _, kind := range kinds {
		for _, tc := range []struct {
			stream     string
			wantStatus int
			wantStream bool
		}{
			{"", http.StatusOK, false},
			{`,"stream":true`, http.StatusOK, true},
			{`,"stream":false`, http.StatusOK, false},
			{`,"stream":"true"`, http.StatusBadRequest, false},
			{`,"stream":1`, http.StatusBadRequest, false},
			{`,"stream":null`, http.StatusBadRequest, false},
			{`,"stream":{}`, http.StatusBadRequest, false},
		} {
			executor := &capturingExecutor{}
			handler := New(Options{Catalog: singleProviderStore(t, kind, "https://a.example"), Executor: executor})
			response := postJSON(handler, "/v1/chat/completions", `{"model":"model-a","messages":[{"role":"user","content":"hi"}]`+tc.stream+`}`)
			if response.Code != tc.wantStatus {
				t.Fatalf("%s stream%q: status = %d, body = %s; want %d", kind, tc.stream, response.Code, response.Body.String(), tc.wantStatus)
			}
			if tc.wantStatus != http.StatusOK {
				if len(executor.requests) != 0 {
					t.Fatalf("%s stream%q: invalid request reached the provider", kind, tc.stream)
				}
				continue
			}
			if executor.requests[0].Stream != tc.wantStream {
				t.Fatalf("%s stream%q: Stream = %v; want %v", kind, tc.stream, executor.requests[0].Stream, tc.wantStream)
			}
		}
	}
}

func TestNativeAnthropicStreamFieldMustBeBoolean(t *testing.T) {
	executor := &capturingExecutor{}
	handler := New(Options{Catalog: singleProviderStore(t, models.ProviderAnthropic, "https://a.example"), Executor: executor})
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"model-a","max_tokens":8,"messages":[{"role":"user","content":"hi"}],"stream":"yes"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeAnthropicMessages(response, request)
	if response.Code != http.StatusBadRequest || len(executor.requests) != 0 {
		t.Fatalf("status = %d, upstream calls = %d; want 400 and none", response.Code, len(executor.requests))
	}
}

func TestClientIPIgnoresForwardingHeadersFromUntrustedPeers(t *testing.T) {
	trusted, err := netx.ParseTrustedProxies([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		trusted *netx.TrustedProxies
		remote  string
		xff     string
		want    string
	}{
		{"direct client spoofing", nil, "203.0.113.9:5000", "1.2.3.4", "203.0.113.9"},
		{"untrusted peer spoofing", trusted, "203.0.113.9:5000", "1.2.3.4", "203.0.113.9"},
		{"trusted proxy", trusted, "10.0.0.2:5000", "198.51.100.7", "198.51.100.7"},
		{"multi-hop skips trusted hops", trusted, "10.0.0.2:5000", "1.2.3.4, 198.51.100.7, 10.1.1.1", "198.51.100.7"},
		{"no header", trusted, "10.0.0.2:5000", "", "10.0.0.2"},
	} {
		executor := &capturingExecutor{}
		handler := New(Options{Catalog: singleProviderStore(t, models.ProviderOpenAICompatible, "https://a.example/v1"), Executor: executor, TrustedProxies: tc.trusted})
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model-a","messages":[]}`))
		request.Header.Set("Content-Type", "application/json")
		request.RemoteAddr = tc.remote
		if tc.xff != "" {
			request.Header.Set("X-Forwarded-For", tc.xff)
		}
		handler.ServeHTTP(httptest.NewRecorder(), request)
		if got := executor.requests[0].ClientIP; got != tc.want {
			t.Fatalf("%s: client IP = %q; want %q", tc.name, got, tc.want)
		}
	}
}

// A registry reload between the first attempt and the failover must not move the
// request to a different catalog generation.
func TestRequestKeepsOneCatalogGenerationAcrossFailover(t *testing.T) {
	store := singleProviderStore(t, models.ProviderOpenAICompatible, "https://a.example/v1")
	pinned := store.Load().Generation
	executor := &capturingExecutor{status: []int{http.StatusServiceUnavailable}}
	executor.onCall = func(call int) {
		if call == 1 {
			next, err := models.NewCatalog(0, store.Load().Providers, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			store.Swap(next)
		}
	}
	var routedCatalog *models.Catalog
	router := &catalogCapturingRouter{statusFailoverRouter: statusFailoverRouter{
		first: auto.Target{ProviderKey: "provider-a", Model: "model-a", UpstreamModel: "model-a", Attempts: 1, MaxAttempts: 2},
		next:  auto.Target{ProviderKey: "provider-a", Model: "model-a", UpstreamModel: "model-a", Attempts: 2, MaxAttempts: 2},
	}, seen: &routedCatalog}
	handler := New(Options{Catalog: store, Executor: executor, AutoRouter: router})
	response := postJSON(handler, "/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
	if response.Code != http.StatusOK || len(executor.requests) != 2 {
		t.Fatalf("status = %d, attempts = %d; want 200 after two attempts", response.Code, len(executor.requests))
	}
	if routedCatalog == nil || routedCatalog.Generation != pinned {
		t.Fatalf("router did not receive the request-start catalog")
	}
	for i, request := range executor.requests {
		if request.CatalogGeneration != pinned {
			t.Fatalf("attempt %d used catalog generation %d; want %d", i+1, request.CatalogGeneration, pinned)
		}
	}
	if store.Load().Generation == pinned {
		t.Fatal("test did not publish a new generation mid-request")
	}
}

type catalogCapturingRouter struct {
	statusFailoverRouter
	seen **models.Catalog
}

func (r *catalogCapturingRouter) Route(ctx context.Context, request auto.Request) (auto.Target, error) {
	*r.seen = request.Catalog
	return r.statusFailoverRouter.Route(ctx, request)
}
