package bifrost

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// These tests pin the Bifrost behaviors the executor is designed around. They
// talk to Bifrost directly, before any Auto Router code wraps it, so a Bifrost
// upgrade that changes one of them fails here first rather than in a routing log.

type capabilityAccount struct {
	name     schemas.ModelProvider
	base     schemas.ModelProvider
	url      string
	apiKey   string
	timeoutS int
}

func (a *capabilityAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return nil, nil
}

func (a *capabilityAccount) GetKeysForProvider(context.Context, schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{{ID: string(a.name), Value: *schemas.NewSecretVar(a.apiKey), Models: schemas.WhiteList{"*"}, Weight: 1}}, nil
}

func (a *capabilityAccount) GetConfigForProvider(schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	return &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        a.url,
			DefaultRequestTimeoutInSeconds: a.timeoutS,
			MaxRetries:                     0,
			AllowPrivateNetwork:            true,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: 8, BufferSize: 16},
		CustomProviderConfig:     &schemas.CustomProviderConfig{BaseProviderType: a.base},
	}, nil
}

func newCapabilityClient(t *testing.T, account *capabilityAccount) *bifrost.Bifrost {
	t.Helper()
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{Account: account})
	if err != nil {
		t.Fatalf("bifrost.Init: %v", err)
	}
	t.Cleanup(client.Shutdown)
	return client
}

func TestBifrostPassthroughForwardsBodyAndReturnsUpstreamErrorUnchanged(t *testing.T) {
	var hits atomic.Int32
	var gotBody, gotAuth atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		raw, _ := io.ReadAll(r.Body)
		gotBody.Store(string(raw))
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down","type":"rate_limit","code":"rate_limited"}}`))
	}))
	defer upstream.Close()

	client := newCapabilityClient(t, &capabilityAccount{name: "cap-openai", base: schemas.OpenAI, url: upstream.URL, apiKey: "sk-provider", timeoutS: 5})
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	body := `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}],"vendor_extra":{"keep":true}}`
	response, berr := client.Passthrough(ctx, "cap-openai", &schemas.BifrostPassthroughRequest{
		Method: http.MethodPost, Path: "/v1/chat/completions", Body: []byte(body),
		SafeHeaders: map[string]string{"Content-Type": "application/json"},
	})
	if berr != nil {
		t.Fatalf("a provider 429 must be a response, got error: %+v", berr)
	}
	if response.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(response.Body), `"slow down"`) {
		t.Fatalf("response = %d %q, want the upstream 429 body unchanged", response.StatusCode, response.Body)
	}
	if got, _ := gotBody.Load().(string); got != body {
		t.Fatalf("upstream body = %q, want the request byte-for-byte %q", got, body)
	}
	if got, _ := gotAuth.Load().(string); got != "Bearer sk-provider" {
		t.Fatalf("upstream Authorization = %q, want the provider key injected", got)
	}
	if hits.Load() != 1 {
		t.Fatalf("upstream was hit %d times, want exactly 1 (no retry)", hits.Load())
	}
}

func TestBifrostPassthroughStreamDeliversRawSSEChunks(t *testing.T) {
	events := "data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" +
		"data: {\"id\":\"1\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\n" +
		"data: [DONE]\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(events))
	}))
	defer upstream.Close()

	client := newCapabilityClient(t, &capabilityAccount{name: "cap-openai-stream", base: schemas.OpenAI, url: upstream.URL, apiKey: "sk-provider", timeoutS: 5})
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, berr := client.PassthroughStream(ctx, "cap-openai-stream", &schemas.BifrostPassthroughRequest{
		Method: http.MethodPost, Path: "/v1/chat/completions", Body: []byte(`{"model":"gpt-x","stream":true}`),
		SafeHeaders: map[string]string{"Content-Type": "application/json", "Accept-Encoding": "identity"},
	})
	if berr != nil {
		t.Fatalf("PassthroughStream: %+v", berr)
	}
	var received strings.Builder
	var status int
	for chunk := range stream {
		if chunk.BifrostError != nil {
			t.Fatalf("stream error: %+v", chunk.BifrostError)
		}
		if chunk.BifrostPassthroughResponse == nil {
			continue
		}
		if chunk.BifrostPassthroughResponse.StatusCode != 0 {
			status = chunk.BifrostPassthroughResponse.StatusCode
		}
		received.Write(chunk.BifrostPassthroughResponse.Body)
	}
	if status != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", status)
	}
	if received.String() != events {
		t.Fatalf("stream bytes = %q, want the upstream bytes %q", received.String(), events)
	}
}
