package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

type discoveredModel struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Discovery reads only model identifiers and display names. Upstream responses
// are bounded, never persisted automatically, and never include the API key.
func (h *adminHandler) handleProviderDiscover(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	key := r.PathValue("key")
	view, found, err := storage.GetProvider(r.Context(), h.db, key)
	if err != nil {
		storageFailure(w, h, "read provider for discovery", err)
		return
	}
	if !found {
		unknownPathID(w, "unknown_provider", "provider")
		return
	}
	provider := view.Provider
	if provider.BaseURL == "" {
		writeAdminError(w, http.StatusConflict, "provider_not_configured", "set the provider URL before discovering models", "base_url")
		return
	}
	items, truncated, err := discoverProviderModels(r, provider)
	if err != nil {
		if h.logger != nil {
			h.logger.Warn("model discovery failed", "provider", key, "error", err)
		}
		writeAdminError(w, http.StatusBadGateway, "discovery_failed", "the provider model list could not be read", "")
		return
	}
	writeAdminJSON(w, http.StatusOK, struct {
		Items     []discoveredModel `json:"items"`
		Truncated bool              `json:"truncated"`
	}{Items: items, Truncated: truncated})
}

func discoverProviderModels(r *http.Request, provider models.Provider) ([]discoveredModel, bool, error) {
	endpoint, err := url.Parse(provider.BaseURL)
	if err != nil {
		return nil, false, err
	}
	basePath := strings.TrimRight(endpoint.Path, "/")
	if strings.HasSuffix(basePath, "/v1") || strings.HasSuffix(basePath, "/v1beta") {
		endpoint.Path = basePath + "/models"
	} else if provider.Kind == models.ProviderGemini {
		endpoint.Path = basePath + "/v1beta/models"
	} else {
		endpoint.Path = basePath + "/v1/models"
	}
	endpoint.RawPath = ""
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, false, err
	}
	request.Header.Set("Accept", "application/json")
	switch provider.Kind {
	case models.ProviderAnthropic:
		request.Header.Set("x-api-key", provider.APIKey)
		request.Header.Set("anthropic-version", "2023-06-01")
	case models.ProviderGemini:
		request.Header.Set("x-goog-api-key", provider.APIKey)
	default:
		if provider.APIKey != "" {
			request.Header.Set("Authorization", "Bearer "+provider.APIKey)
		}
	}
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, false, errors.New("provider model list returned a non-success status")
	}
	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
		HasMore       bool   `json:"has_more"`
		NextPageToken string `json:"nextPageToken"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&payload); err != nil {
		return nil, false, err
	}
	items := make([]discoveredModel, 0, len(payload.Data)+len(payload.Models))
	seen := map[string]bool{}
	add := func(id, label string) {
		if len(items) >= 500 || seen[id] || models.ValidateModelID(id) != nil {
			return
		}
		seen[id] = true
		if label == "" {
			label = id
		}
		items = append(items, discoveredModel{ID: id, Label: label})
	}
	for _, model := range payload.Data {
		add(model.ID, model.DisplayName)
	}
	for _, model := range payload.Models {
		add(strings.TrimPrefix(model.Name, "models/"), model.DisplayName)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, payload.HasMore || payload.NextPageToken != "" || len(payload.Data)+len(payload.Models) > 500, nil
}

func (h *adminHandler) handleProviderModelSelect(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	var request struct {
		Model string `json:"model"`
	}
	if _, err := readAdminBody(r, &request); err != nil {
		writeBodyError(w, err)
		return
	}
	if err := models.ValidateModelID(request.Model); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_model", "the model identifier is not valid", "model")
		return
	}
	providerKey := r.PathValue("key")
	var metadata *storage.SelectedProviderModelMetadata
	matchBy := ""
	matchedModel := ""
	metadataFound := false
	if h.resolveModelMetadata != nil {
		match, found, lookupErr := h.resolveModelMetadata(r.Context(), providerKey, request.Model)
		if lookupErr != nil {
			if h.logger != nil {
				h.logger.Warn("models.dev metadata lookup failed", "provider", providerKey, "model", request.Model, "error", lookupErr.Error())
			}
			writeAdminError(w, http.StatusBadGateway, "metadata_lookup_failed", "model capability metadata could not be loaded from models.dev", "model")
			return
		}
		if found {
			metadataFound = true
			matchBy, matchedModel = match.MatchBy, match.ProviderID+"/"+match.ModelID
			metadata = &storage.SelectedProviderModelMetadata{
				DisplayName: match.DisplayName, ContextWindow: match.Pair.ContextWindow, MaxOutput: match.Pair.MaxOutput,
				SupportsTools: match.Pair.SupportsTools, SupportsVision: match.Pair.SupportsVision,
				SupportsAudioInput: match.Pair.SupportsAudioInput, SupportsReasoning: match.Pair.SupportsReasoning,
			}
		}
	}
	metadataApplied, err := storage.SelectProviderModel(r.Context(), h.db, providerKey, request.Model, metadata)
	if err != nil {
		if errors.Is(err, storage.ErrProviderNotFound) {
			unknownPathID(w, "unknown_provider", "provider")
			return
		}
		storageFailure(w, h, "select provider model", err)
		return
	}
	metadataSource := "local-default"
	if metadataApplied {
		metadataSource = "models.dev"
	} else if metadataFound {
		metadataSource = "admin-override"
	}
	h.reloadAfterWrite(w, r, map[string]any{
		"provider": providerKey, "model": request.Model,
		"metadata_source": metadataSource, "metadata_applied": metadataApplied,
		"metadata_match": matchBy, "metadata_model": matchedModel,
	})
}
