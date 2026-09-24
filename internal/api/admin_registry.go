package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

// This file is the registry half of the management surface: providers, logical
// models and the provider/model bindings between them.
//
// The shape of a row is fixed here, and three facts about it are deliberate:
//
//   - api_key is never returned. The provider listing reports only whether a key
//     is set, so a credential cannot reach a response body, browser cache or
//     screenshot.
//   - owner is derived from admin_owned and source and answers the question an
//     administrator actually asks: will the next restart undo this edit? "admin"
//     means no, "config" and "modelsdev" mean the source will rewrite it.
//   - Only administrator-owned providers can be deleted. The operation removes their
//     bindings and group memberships atomically, while historical routing logs remain.

// providerPayload is one provider as the management surface reports it.
type providerPayload struct {
	Key             string  `json:"key"`
	Kind            string  `json:"kind"`
	DisplayName     string  `json:"display_name"`
	Enabled         bool    `json:"enabled"`
	Priority        int     `json:"priority"`
	Source          string  `json:"source"`
	Owner           string  `json:"owner"`
	GatewayProvider *string `json:"gateway_provider"`
	// BaseURL is reported because it is not a secret and an operator needs it to tell
	// two providers apart.
	BaseURL string `json:"base_url"`
	// APIKeySet reports whether a direct-provider credential is stored. The key
	// itself, including a masked rendering, is never returned.
	APIKeySet bool `json:"api_key_set"`
	PairCount int  `json:"pair_count"`
}

func providerPayloadOf(view storage.ProviderView) providerPayload {
	return providerPayload{
		Key:             view.Provider.Key,
		Kind:            string(view.Provider.Kind),
		DisplayName:     view.Provider.DisplayName,
		Enabled:         view.Provider.Enabled,
		Priority:        view.Provider.Priority,
		Source:          string(view.Provider.Source),
		Owner:           view.Owner(),
		GatewayProvider: view.Provider.GatewayProvider,
		BaseURL:         view.Provider.BaseURL,
		APIKeySet:       view.Provider.APIKey != "",
		PairCount:       view.PairCount,
	}
}

// modelPayload is one logical model as the management surface reports it.
type modelPayload struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
	Priority    int    `json:"priority"`
	Source      string `json:"source"`
	Owner       string `json:"owner"`
	PairCount   int    `json:"pair_count"`
}

func modelPayloadOf(view storage.ModelView) modelPayload {
	return modelPayload{
		ID:          view.Model.ID,
		DisplayName: view.Model.DisplayName,
		Enabled:     view.Model.Enabled,
		Priority:    view.Model.Priority,
		Source:      string(view.Model.Source),
		Owner:       view.Owner(),
		PairCount:   view.PairCount,
	}
}

// pairPayload is one provider/model binding as the management surface reports it.
type pairPayload struct {
	ProviderKey        string `json:"provider"`
	ModelID            string `json:"model"`
	UpstreamModelID    string `json:"upstream_model_id"`
	ContextWindow      int    `json:"context_window"`
	MaxOutput          *int   `json:"max_output"`
	SupportsTools      bool   `json:"supports_tools"`
	SupportsVision     bool   `json:"supports_vision"`
	SupportsAudioInput bool   `json:"supports_audio_input"`
	SupportsReasoning  bool   `json:"supports_reasoning"`
	Enabled            bool   `json:"enabled"`
	Priority           int    `json:"priority"`
	Source             string `json:"source"`
	Owner              string `json:"owner"`
}

func pairPayloadOf(view storage.PairView) pairPayload {
	return pairPayload{
		ProviderKey:        view.Pair.ProviderKey,
		ModelID:            view.Pair.ModelID,
		UpstreamModelID:    view.Pair.UpstreamModelID,
		ContextWindow:      view.Pair.ContextWindow,
		MaxOutput:          view.Pair.MaxOutput,
		SupportsTools:      view.Pair.SupportsTools,
		SupportsVision:     view.Pair.SupportsVision,
		SupportsAudioInput: view.Pair.SupportsAudioInput,
		SupportsReasoning:  view.Pair.SupportsReasoning,
		Enabled:            view.Pair.Enabled,
		Priority:           view.Pair.Priority,
		Source:             string(view.Pair.Source),
		Owner:              view.Owner(),
	}
}

// handleProviderList serves GET /admin/v1/providers.
func (h *adminHandler) handleProviderList(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	limit, err := h.pageLimit(r)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", err.Error(), "limit")
		return
	}
	query := r.URL.Query()
	search, valid := adminSearchTerm(query.Get("search"))
	if !valid {
		writeAdminError(w, http.StatusBadRequest, "invalid_filter", "the search filter is invalid", "search")
		return
	}
	enabled, err := parseBoolParam(query.Get("enabled"))
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_filter", err.Error(), "enabled")
		return
	}
	fields, err := decodeCursor(query.Get("cursor"), cursorProvider, 2)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_cursor", "the cursor is not valid for this endpoint", "")
		return
	}
	priority, err := strconv.Atoi(fields[0])
	if err != nil && fields[0] != "" {
		writeAdminError(w, http.StatusBadRequest, "invalid_cursor", "the cursor is not valid for this endpoint", "")
		return
	}
	views, err := storage.ListProvidersFiltered(r.Context(), h.db, storage.ProviderFilter{Search: search, Enabled: enabled}, storage.ProviderCursor{Priority: priority, Key: fields[1]}, limit)
	if err != nil {
		storageFailure(w, h, "list providers", err)
		return
	}
	next := ""
	if len(views) > limit {
		last := views[limit-1]
		next = encodeCursor(cursorProvider, strconv.Itoa(last.Provider.Priority), last.Provider.Key)
		views = views[:limit]
	}
	payload := make([]providerPayload, 0, len(views))
	for _, view := range views {
		payload = append(payload, providerPayloadOf(view))
	}
	var cursor *string
	if next != "" {
		cursor = &next
	}
	writeAdminJSON(w, http.StatusOK, pageOf(payload, cursor))
}

// handleProviderDetail serves GET /admin/v1/providers/{key}.
func (h *adminHandler) handleProviderDetail(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	key := trimPathWildcard(r.PathValue("key"))
	view, found, err := storage.GetProvider(r.Context(), h.db, key)
	if err != nil {
		storageFailure(w, h, "read provider", err)
		return
	}
	if !found {
		unknownPathID(w, "unknown_provider", "provider")
		return
	}
	// The detail view carries the provider's bindings, so an operator can see what
	// disabling it would affect without a second request.
	pairs, err := storage.ListPairs(r.Context(), h.db, storage.PairFilter{ProviderKey: key}, storage.PairCursor{}, h.maxPageSize)
	if err != nil {
		storageFailure(w, h, "list pairs", err)
		return
	}
	bindings := make([]pairPayload, 0, len(pairs))
	for _, row := range pairs {
		bindings = append(bindings, pairPayloadOf(row.View))
	}
	writeAdminJSON(w, http.StatusOK, struct {
		Provider providerPayload `json:"provider"`
		Pairs    []pairPayload   `json:"pairs"`
	}{Provider: providerPayloadOf(view), Pairs: bindings})
}

// providerPatchRequest is the request body of PATCH /admin/v1/providers/{key}. Every
// field is a pointer so "set priority to 0" is distinguishable from "leave priority
// alone".
type providerPatchRequest struct {
	Kind        *models.ProviderKind `json:"kind"`
	DisplayName *string              `json:"display_name"`
	Enabled     *bool                `json:"enabled"`
	Priority    *int                 `json:"priority"`
	// GatewayProvider is a pointer to a pointer where the outer nil means "leave
	// alone" and an inner nil means "clear the mapping".
	GatewayProvider *string `json:"gateway_provider"`
	// Direct-provider connection fields are write-only or endpoint metadata.
	BaseURL *string `json:"base_url"`
	APIKey  *string `json:"api_key"`
}

// handleProviderPatch serves PATCH /admin/v1/providers/{key}.
func (h *adminHandler) handleProviderPatch(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	key := trimPathWildcard(r.PathValue("key"))
	var request providerPatchRequest
	present, err := readAdminBody(r, &request)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	// A field that was present with a null value is how "clear this column" is
	// expressed, so presence rather than non-nilness decides whether the patch has
	// anything to do.
	if !wasPresent(present, "kind") && !wasPresent(present, "display_name") && !wasPresent(present, "enabled") && !wasPresent(present, "priority") && !wasPresent(present, "gateway_provider") && !wasPresent(present, "base_url") && !wasPresent(present, "api_key") {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the request must change at least one field", "body")
		return
	}
	for _, field := range []struct {
		name  string
		value any
	}{
		{name: "kind", value: request.Kind},
		{name: "display_name", value: request.DisplayName},
		{name: "enabled", value: request.Enabled},
		{name: "priority", value: request.Priority},
	} {
		if wasPresent(present, field.name) && field.value == nil {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "this field cannot be null", field.name)
			return
		}
	}
	if request.Kind != nil && !request.Kind.Valid() {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the provider kind is not supported", "kind")
		return
	}
	if request.DisplayName != nil {
		if err := models.ValidateProvider(models.Provider{
			Key:             key,
			DisplayName:     *request.DisplayName,
			Enabled:         false,
			Priority:        0,
			Source:          models.SourceLocal,
			GatewayProvider: request.GatewayProvider,
		}); err != nil {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "the display_name is not usable", "display_name")
			return
		}
	}
	if request.Priority != nil && *request.Priority < 0 {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the priority must not be negative", "priority")
		return
	}
	baseURL := request.BaseURL
	if wasPresent(present, "base_url") && baseURL == nil {
		empty := ""
		baseURL = &empty
	}
	apiKey := request.APIKey
	if wasPresent(present, "api_key") && apiKey == nil {
		empty := ""
		apiKey = &empty
	}
	if baseURL != nil && *baseURL != "" {
		if err := models.ValidateBaseURL(*baseURL); err != nil {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "the base_url is not a valid provider endpoint URL", "base_url")
			return
		}
	}
	if request.GatewayProvider != nil {
		if err := models.ValidateProvider(models.Provider{
			Key:             key,
			DisplayName:     "placeholder",
			Enabled:         false,
			Source:          models.SourceLocal,
			GatewayProvider: request.GatewayProvider,
		}); err != nil {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "the gateway_provider is not a valid gateway identity", "gateway_provider")
			return
		}
	}
	// A cleared mapping is expressed as an explicit null: the presence check above
	// accepted the key, and an empty mapping is what the storage layer stores as NULL.
	gatewayProvider := request.GatewayProvider
	if wasPresent(present, "gateway_provider") && gatewayProvider == nil {
		empty := ""
		gatewayProvider = &empty
	}
	// Validate the merged provider state before writing it. This keeps the Admin
	// API from enabling a row with no executable endpoint; the lower storage layer
	// intentionally remains permissive for legacy ownership tests and migrations.
	current, found, err := storage.GetProvider(r.Context(), h.db, key)
	if err != nil {
		storageFailure(w, h, "read provider", err)
		return
	}
	if !found {
		unknownPathID(w, "unknown_provider", "provider")
		return
	}
	candidate := current.Provider
	if request.Kind != nil {
		candidate.Kind = *request.Kind
	}
	if request.DisplayName != nil {
		candidate.DisplayName = *request.DisplayName
	}
	if request.Enabled != nil {
		candidate.Enabled = *request.Enabled
	}
	if request.Priority != nil {
		candidate.Priority = *request.Priority
	}
	if baseURL != nil {
		candidate.BaseURL = *baseURL
	}
	if apiKey != nil {
		candidate.APIKey = *apiKey
	}
	if wasPresent(present, "gateway_provider") {
		if gatewayProvider == nil || *gatewayProvider == "" {
			candidate.GatewayProvider = nil
		} else {
			mapped := *gatewayProvider
			candidate.GatewayProvider = &mapped
		}
	}
	if err := models.ValidateProvider(candidate); err != nil {
		field := "body"
		switch {
		case baseURL != nil:
			field = "base_url"
		case request.Enabled != nil:
			field = "enabled"
		case request.DisplayName != nil:
			field = "display_name"
		}
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the provider change would leave the provider unusable", field)
		return
	}
	applied, err := storage.UpdateProvider(r.Context(), h.db, key, storage.ProviderPatch{
		Kind:            request.Kind,
		DisplayName:     request.DisplayName,
		Enabled:         request.Enabled,
		Priority:        request.Priority,
		BaseURL:         baseURL,
		APIKey:          apiKey,
		GatewayProvider: gatewayProvider,
	})
	if err != nil {
		storageFailure(w, h, "update provider", err)
		return
	}
	if !applied {
		unknownPathID(w, "unknown_provider", "provider")
		return
	}
	view, found, err := storage.GetProvider(r.Context(), h.db, key)
	if err != nil || !found {
		storageFailure(w, h, "read provider", err)
		return
	}
	h.reloadAfterWrite(w, r, providerPayloadOf(view))
}

// providerCreateRequest is the body of POST /admin/v1/providers. APIKey is
// write-only: it is accepted for storage but never copied into a response.
type providerCreateRequest struct {
	Key         string              `json:"key"`
	Kind        models.ProviderKind `json:"kind"`
	DisplayName string              `json:"display_name"`
	BaseURL     string              `json:"base_url"`
	APIKey      string              `json:"api_key"`
	Enabled     bool                `json:"enabled"`
	Priority    int                 `json:"priority"`
}

// handleProviderDelete serves DELETE /admin/v1/providers/{key}.
func (h *adminHandler) handleProviderDelete(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	key := trimPathWildcard(r.PathValue("key"))
	err := storage.DeleteAdminProvider(r.Context(), h.db, key)
	switch {
	case errors.Is(err, storage.ErrProviderNotFound):
		unknownPathID(w, "unknown_provider", "provider")
		return
	case errors.Is(err, storage.ErrProviderNotAdminOwned):
		writeAdminError(w, http.StatusConflict, "provider_not_deletable", "only administrator-owned providers can be deleted", "key")
		return
	case err != nil:
		storageFailure(w, h, "delete provider", err)
		return
	}

	groups, err := storage.LoadModelGroups(r.Context(), h.db)
	if err != nil {
		if h.logger != nil {
			h.logger.Error("routing groups could not be read after provider deletion", "error", err.Error())
		}
		writeAdminError(w, http.StatusInternalServerError, "snapshot_publish_failed", "the provider was deleted, but the running routing groups could not be refreshed; they will be picked up at the next restart", "")
		return
	}
	if h.reloadGroups != nil {
		if err := h.reloadGroups(r.Context(), groups); err != nil {
			if h.logger != nil {
				h.logger.Error("routing groups could not be republished after provider deletion", "error", err.Error())
			}
			writeAdminError(w, http.StatusInternalServerError, "snapshot_publish_failed", "the provider was deleted, but the running routing groups could not be republished; they will be picked up at the next restart", "")
			return
		}
	}
	if h.reloadCatalog != nil {
		if err := h.reloadCatalog(r.Context()); err != nil {
			if h.logger != nil {
				h.logger.Error("the registry snapshot could not be republished after provider deletion", "error", err.Error())
			}
			writeAdminError(w, http.StatusInternalServerError, "snapshot_publish_failed", "the provider was deleted, but the running snapshot could not be republished; it will be picked up at the next restart", "")
			return
		}
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"key": key, "deleted": true})
}

// handleProviderCreate serves POST /admin/v1/providers.
func (h *adminHandler) handleProviderCreate(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	var request providerCreateRequest
	if _, err := readAdminBody(r, &request); err != nil {
		writeBodyError(w, err)
		return
	}
	if err := models.ValidateProviderKey(request.Key); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the key is not a valid provider key", "key")
		return
	}
	if request.Kind == "" {
		request.Kind = models.ProviderOpenAICompatible
	}
	if !request.Kind.Valid() {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the provider kind is not supported", "kind")
		return
	}
	if request.BaseURL != "" {
		if err := models.ValidateBaseURL(request.BaseURL); err != nil {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "the base_url is not a valid provider endpoint URL", "base_url")
			return
		}
	}
	if request.Priority < 0 {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the priority must not be negative", "priority")
		return
	}
	displayName := request.DisplayName
	if displayName == "" {
		displayName = request.Key
	}
	if err := models.ValidateProvider(models.Provider{
		Key: request.Key, Kind: request.Kind, DisplayName: displayName, BaseURL: request.BaseURL,
		APIKey: request.APIKey, Enabled: request.Enabled, Priority: request.Priority,
		Source: models.SourceLocal,
	}); err != nil {
		field := "body"
		if request.BaseURL == "" && request.Enabled {
			field = "base_url"
		}
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the provider is not usable", field)
		return
	}
	if err := storage.CreateProvider(r.Context(), h.db, storage.NewProvider{
		Key:         request.Key,
		Kind:        request.Kind,
		DisplayName: request.DisplayName,
		BaseURL:     request.BaseURL,
		APIKey:      request.APIKey,
		Enabled:     request.Enabled,
		Priority:    request.Priority,
	}); err != nil {
		if isConflictError(err) {
			writeAdminError(w, http.StatusConflict, "provider_exists", "a provider with this key already exists", "key")
			return
		}
		storageFailure(w, h, "create provider", err)
		return
	}
	view, found, err := storage.GetProvider(r.Context(), h.db, request.Key)
	if err != nil || !found {
		storageFailure(w, h, "read provider", err)
		return
	}
	h.reloadAfterWrite(w, r, providerPayloadOf(view), http.StatusCreated)
}

// handleModelList serves GET /admin/v1/models.
func (h *adminHandler) handleModelList(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	limit, err := h.pageLimit(r)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", err.Error(), "limit")
		return
	}
	query := r.URL.Query()
	search, valid := adminSearchTerm(query.Get("search"))
	if !valid {
		writeAdminError(w, http.StatusBadRequest, "invalid_filter", "the search filter is invalid", "search")
		return
	}
	enabled, err := parseBoolParam(query.Get("enabled"))
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_filter", err.Error(), "enabled")
		return
	}
	source := query.Get("source")
	if source != "" && source != string(models.SourceLocal) && source != string(models.SourceModelsDev) {
		writeAdminError(w, http.StatusBadRequest, "invalid_filter", "the source filter is not supported", "source")
		return
	}
	fields, err := decodeCursor(query.Get("cursor"), cursorModel, 2)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_cursor", "the cursor is not valid for this endpoint", "")
		return
	}
	priority, err := strconv.Atoi(fields[0])
	if err != nil && fields[0] != "" {
		writeAdminError(w, http.StatusBadRequest, "invalid_cursor", "the cursor is not valid for this endpoint", "")
		return
	}
	views, err := storage.ListModelsFiltered(r.Context(), h.db, storage.ModelFilter{Search: search, Enabled: enabled, Source: source}, storage.ModelCursor{Priority: priority, ID: fields[1]}, limit)
	if err != nil {
		storageFailure(w, h, "list models", err)
		return
	}
	next := ""
	if len(views) > limit {
		last := views[limit-1]
		next = encodeCursor(cursorModel, strconv.Itoa(last.Model.Priority), last.Model.ID)
		views = views[:limit]
	}
	payload := make([]modelPayload, 0, len(views))
	for _, view := range views {
		payload = append(payload, modelPayloadOf(view))
	}
	var cursor *string
	if next != "" {
		cursor = &next
	}
	writeAdminJSON(w, http.StatusOK, pageOf(payload, cursor))
}

// handleModelDetail serves GET /admin/v1/models/{id...}.
func (h *adminHandler) handleModelDetail(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	id := trimPathWildcard(r.PathValue("id"))
	view, found, err := storage.GetModel(r.Context(), h.db, id)
	if err != nil {
		storageFailure(w, h, "read model", err)
		return
	}
	if !found {
		unknownPathID(w, "unknown_model", "model")
		return
	}
	pairs, err := storage.ListPairs(r.Context(), h.db, storage.PairFilter{ModelID: id}, storage.PairCursor{}, h.maxPageSize)
	if err != nil {
		storageFailure(w, h, "list pairs", err)
		return
	}
	bindings := make([]pairPayload, 0, len(pairs))
	for _, row := range pairs {
		bindings = append(bindings, pairPayloadOf(row.View))
	}
	writeAdminJSON(w, http.StatusOK, struct {
		Model modelPayload  `json:"model"`
		Pairs []pairPayload `json:"pairs"`
	}{Model: modelPayloadOf(view), Pairs: bindings})
}

// modelPatchRequest is the request body of PATCH /admin/v1/models/{id...}.
type modelPatchRequest struct {
	DisplayName *string `json:"display_name"`
	Enabled     *bool   `json:"enabled"`
	Priority    *int    `json:"priority"`
}

// handleModelPatch serves PATCH /admin/v1/models/{id...}.
func (h *adminHandler) handleModelPatch(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	id := trimPathWildcard(r.PathValue("id"))
	var request modelPatchRequest
	present, err := readAdminBody(r, &request)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	if !wasPresent(present, "display_name") && !wasPresent(present, "enabled") && !wasPresent(present, "priority") {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the request must change at least one field", "body")
		return
	}
	if request.DisplayName != nil {
		if err := models.ValidateModel(models.Model{
			ID:          id,
			DisplayName: *request.DisplayName,
			Source:      models.SourceLocal,
		}); err != nil {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "the display_name is not usable", "display_name")
			return
		}
	}
	if request.Priority != nil && *request.Priority < 0 {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the priority must not be negative", "priority")
		return
	}
	applied, err := storage.UpdateModel(r.Context(), h.db, id, storage.ModelPatch{
		DisplayName: request.DisplayName,
		Enabled:     request.Enabled,
		Priority:    request.Priority,
	})
	if err != nil {
		storageFailure(w, h, "update model", err)
		return
	}
	if !applied {
		unknownPathID(w, "unknown_model", "model")
		return
	}
	view, found, err := storage.GetModel(r.Context(), h.db, id)
	if err != nil || !found {
		storageFailure(w, h, "read model", err)
		return
	}
	h.reloadAfterWrite(w, r, modelPayloadOf(view))
}

// modelCreateRequest is the body of POST /admin/v1/models.
type modelCreateRequest struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
	Priority    int    `json:"priority"`
}

// handleModelCreate serves POST /admin/v1/models.
func (h *adminHandler) handleModelCreate(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	var request modelCreateRequest
	if _, err := readAdminBody(r, &request); err != nil {
		writeBodyError(w, err)
		return
	}
	if err := models.ValidateModelID(request.ID); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the id is not a valid model identifier", "id")
		return
	}
	if isAutoModel(request.ID) {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the automatic model identifier is reserved", "id")
		return
	}
	if request.Priority < 0 {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the priority must not be negative", "priority")
		return
	}
	if err := storage.CreateModel(r.Context(), h.db, storage.NewModel{
		ID:          request.ID,
		DisplayName: request.DisplayName,
		Enabled:     request.Enabled,
		Priority:    request.Priority,
	}); err != nil {
		if isConflictError(err) {
			writeAdminError(w, http.StatusConflict, "model_exists", "a model with this id already exists", "id")
			return
		}
		storageFailure(w, h, "create model", err)
		return
	}
	view, found, err := storage.GetModel(r.Context(), h.db, request.ID)
	if err != nil || !found {
		storageFailure(w, h, "read model", err)
		return
	}
	h.reloadAfterWrite(w, r, modelPayloadOf(view), http.StatusCreated)
}

// handlePairList serves GET /admin/v1/pairs with the documented filters.
func (h *adminHandler) handlePairList(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	limit, err := h.pageLimit(r)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", err.Error(), "limit")
		return
	}
	query := r.URL.Query()
	filter := storage.PairFilter{
		ProviderKey: query.Get("provider"),
		ModelID:     query.Get("model"),
	}
	if filter.ProviderKey != "" {
		if err := models.ValidateProviderKey(filter.ProviderKey); err != nil {
			writeAdminError(w, http.StatusBadRequest, "invalid_filter", "the provider filter is not a provider key", "provider")
			return
		}
	}
	if filter.ModelID != "" {
		if err := models.ValidateModelID(filter.ModelID); err != nil {
			writeAdminError(w, http.StatusBadRequest, "invalid_filter", "the model filter is not a model identifier", "model")
			return
		}
	}
	if filter.Enabled, err = parseBoolParam(query.Get("enabled")); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_filter", err.Error(), "enabled")
		return
	}
	if missing := query.Get("missing"); missing != "" {
		if !storage.ValidCapability(missing) {
			writeAdminError(w, http.StatusBadRequest, "invalid_filter", "the state must name one of the four capabilities", "missing")
			return
		}
		filter.Missing = missing
	}
	fields, err := decodeCursor(query.Get("cursor"), cursorPair, 4)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_cursor", "the cursor is not valid for this endpoint", "")
		return
	}
	pairPriority, err1 := strconv.Atoi(fields[0])
	providerPriority, err2 := strconv.Atoi(fields[1])
	if (err1 != nil || err2 != nil) && fields[0] != "" {
		writeAdminError(w, http.StatusBadRequest, "invalid_cursor", "the cursor is not valid for this endpoint", "")
		return
	}
	rows, err := storage.ListPairs(r.Context(), h.db, filter, storage.PairCursor{
		Priority:         pairPriority,
		ProviderPriority: providerPriority,
		ProviderKey:      fields[2],
		ModelID:          fields[3],
	}, limit)
	if err != nil {
		storageFailure(w, h, "list pairs", err)
		return
	}
	next := ""
	if len(rows) > limit {
		last := rows[limit-1]
		next = encodeCursor(cursorPair,
			strconv.Itoa(last.View.Pair.Priority),
			strconv.Itoa(last.ProviderPriority),
			last.View.Pair.ProviderKey,
			last.View.Pair.ModelID)
		rows = rows[:limit]
	}
	payload := make([]pairPayload, 0, len(rows))
	for _, row := range rows {
		payload = append(payload, pairPayloadOf(row.View))
	}
	var cursor *string
	if next != "" {
		cursor = &next
	}
	writeAdminJSON(w, http.StatusOK, pageOf(payload, cursor))
}

// handlePairDetail serves GET /admin/v1/pairs/{provider}/{model...}.
func (h *adminHandler) handlePairDetail(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	providerKey := trimPathWildcard(r.PathValue("provider"))
	modelID := trimPathWildcard(r.PathValue("model"))
	view, found, err := storage.GetPair(r.Context(), h.db, providerKey, modelID)
	if err != nil {
		storageFailure(w, h, "read pair", err)
		return
	}
	if !found {
		unknownPathID(w, "unknown_pair", "binding")
		return
	}
	writeAdminJSON(w, http.StatusOK, pairPayloadOf(view))
}

// pairPatchRequest is the request body of PATCH /admin/v1/pairs/{provider}/{model...}.
//
// MaxOutput is a pointer to a pointer for the same reason GatewayProvider is on the
// provider request: the outer nil leaves the ceiling alone and an inner nil clears
// it. Clearing a ceiling is a real operation — "this pair has no declared output
// limit" — and must be expressible.
type pairPatchRequest struct {
	Enabled            *bool   `json:"enabled"`
	Priority           *int    `json:"priority"`
	UpstreamModelID    *string `json:"upstream_model_id"`
	ContextWindow      *int    `json:"context_window"`
	MaxOutput          **int   `json:"max_output"`
	SupportsTools      *bool   `json:"supports_tools"`
	SupportsVision     *bool   `json:"supports_vision"`
	SupportsAudioInput *bool   `json:"supports_audio_input"`
	SupportsReasoning  *bool   `json:"supports_reasoning"`
}

func (p pairPatchRequest) empty() bool {
	return p.Enabled == nil && p.Priority == nil && p.UpstreamModelID == nil && p.ContextWindow == nil &&
		p.MaxOutput == nil && p.SupportsTools == nil && p.SupportsVision == nil &&
		p.SupportsAudioInput == nil && p.SupportsReasoning == nil
}

// handlePairPatch serves PATCH /admin/v1/pairs/{provider}/{model...}.
func (h *adminHandler) handlePairPatch(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	providerKey := trimPathWildcard(r.PathValue("provider"))
	modelID := trimPathWildcard(r.PathValue("model"))
	var request pairPatchRequest
	present, err := readAdminBody(r, &request)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	if request.empty() && len(present) == 0 {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the request must change at least one field", "body")
		return
	}
	// An explicit null clears the output ceiling, which is a real operation: "this pair
	// has no declared limit" is a state the registry can hold.
	maxOutput := request.MaxOutput
	if wasPresent(present, "max_output") && maxOutput == nil {
		var noCeiling *int
		maxOutput = &noCeiling
	}
	// The merged row is validated inside the storage layer, so the error there names
	// the constraint without echoing a value. The fields that can be checked on their
	// own are checked here so the response names the offending field.
	if request.Priority != nil && *request.Priority < 0 {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the priority must not be negative", "priority")
		return
	}
	if request.ContextWindow != nil && *request.ContextWindow <= 0 {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the context_window must be positive", "context_window")
		return
	}
	if request.MaxOutput != nil && *request.MaxOutput != nil && **request.MaxOutput <= 0 {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the max_output must be positive", "max_output")
		return
	}
	applied, err := storage.UpdatePair(r.Context(), h.db, providerKey, modelID, storage.PairPatch{
		Enabled:            request.Enabled,
		Priority:           request.Priority,
		UpstreamModelID:    request.UpstreamModelID,
		ContextWindow:      request.ContextWindow,
		MaxOutput:          maxOutput,
		SupportsTools:      request.SupportsTools,
		SupportsVision:     request.SupportsVision,
		SupportsAudioInput: request.SupportsAudioInput,
		SupportsReasoning:  request.SupportsReasoning,
	})
	if err != nil {
		if isValidationError(err) {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "the change would leave the binding in an unusable state", "body")
			return
		}
		storageFailure(w, h, "update pair", err)
		return
	}
	if !applied {
		unknownPathID(w, "unknown_pair", "binding")
		return
	}
	view, found, err := storage.GetPair(r.Context(), h.db, providerKey, modelID)
	if err != nil || !found {
		storageFailure(w, h, "read pair", err)
		return
	}
	h.reloadAfterWrite(w, r, pairPayloadOf(view))
}

// pairCreateRequest is the request body of POST /admin/v1/pairs.
type pairCreateRequest struct {
	Provider           string `json:"provider"`
	Model              string `json:"model"`
	UpstreamModelID    string `json:"upstream_model_id"`
	ContextWindow      int    `json:"context_window"`
	MaxOutput          *int   `json:"max_output"`
	SupportsTools      bool   `json:"supports_tools"`
	SupportsVision     bool   `json:"supports_vision"`
	SupportsAudioInput bool   `json:"supports_audio_input"`
	SupportsReasoning  bool   `json:"supports_reasoning"`
	Enabled            bool   `json:"enabled"`
	Priority           int    `json:"priority"`
}

// handlePairCreate serves POST /admin/v1/pairs. It binds a provider and a logical
// model that already exist: the endpoint creates a binding, not a provider, and not
// a model.
func (h *adminHandler) handlePairCreate(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	var request pairCreateRequest
	if _, err := readAdminBody(r, &request); err != nil {
		writeBodyError(w, err)
		return
	}
	if err := models.ValidateProviderKey(request.Provider); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the provider is not a valid provider key", "provider")
		return
	}
	if err := models.ValidateModelID(request.Model); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the model is not a valid model identifier", "model")
		return
	}
	if isAutoModel(request.Model) {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the automatic model identifier is reserved", "model")
		return
	}
	upstream := request.UpstreamModelID
	if upstream == "" {
		upstream = request.Model
	}
	candidate := storage.NewPair{
		ProviderKey:        request.Provider,
		ModelID:            request.Model,
		UpstreamModelID:    upstream,
		ContextWindow:      request.ContextWindow,
		MaxOutput:          request.MaxOutput,
		SupportsTools:      request.SupportsTools,
		SupportsVision:     request.SupportsVision,
		SupportsAudioInput: request.SupportsAudioInput,
		SupportsReasoning:  request.SupportsReasoning,
		Enabled:            request.Enabled,
		Priority:           request.Priority,
	}
	if err := storage.ValidateNewPair(candidate); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the binding is not usable", "body")
		return
	}
	providerExists, err := storage.ProviderExists(r.Context(), h.db, request.Provider)
	if err != nil {
		storageFailure(w, h, "read provider", err)
		return
	}
	if !providerExists {
		unknownPathID(w, "unknown_provider", "provider")
		return
	}
	modelExists, err := storage.ModelExists(r.Context(), h.db, request.Model)
	if err != nil {
		storageFailure(w, h, "read model", err)
		return
	}
	if !modelExists {
		unknownPathID(w, "unknown_model", "model")
		return
	}
	exists, err := storage.PairExists(r.Context(), h.db, request.Provider, request.Model)
	if err != nil {
		storageFailure(w, h, "read pair", err)
		return
	}
	if exists {
		writeAdminError(w, http.StatusConflict, "pair_exists",
			"this provider already serves this model; patch the existing binding instead", "model")
		return
	}
	if err := storage.CreatePair(r.Context(), h.db, candidate); err != nil {
		if isConflictError(err) {
			// A concurrent create won the race between the check and the insert.
			writeAdminError(w, http.StatusConflict, "pair_exists",
				"this provider already serves this model; patch the existing binding instead", "model")
			return
		}
		storageFailure(w, h, "create pair", err)
		return
	}
	view, found, err := storage.GetPair(r.Context(), h.db, request.Provider, request.Model)
	if err != nil || !found {
		storageFailure(w, h, "read pair", err)
		return
	}
	h.reloadAfterWrite(w, r, pairPayloadOf(view))
}

// isValidationError reports whether a storage error came from the registry's own
// validator rather than from the database. The storage layer returns the domain
// error unwrapped, so the distinction is a type check rather than a string search.
func isValidationError(err error) bool {
	var invalid *storage.ValidationError
	return errors.As(err, &invalid)
}

// isConflictError reports whether a write lost a race with a concurrent insert of
// the same primary key. The check is on the driver's constraint classification
// rather than on a message, so a locale or a driver upgrade cannot silently turn a
// conflict into a 500.
func isConflictError(err error) bool {
	var conflict *storage.ConflictError
	return errors.As(err, &conflict)
}
