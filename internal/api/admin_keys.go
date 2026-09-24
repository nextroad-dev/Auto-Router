package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

type keyView struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	Active    bool     `json:"active"`
	KeySet    bool     `json:"key_set"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

func (h *adminHandler) handleKeyList(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	keys, err := storage.ListInboundCredentials(r.Context(), h.db)
	if err != nil {
		storageFailure(w, h, "list inbound credentials", err)
		return
	}
	views := make([]keyView, 0, len(keys))
	for _, key := range keys {
		views = append(views, keyView{Name: key.Name, Scopes: []string{ScopeInference}, Active: key.Active, KeySet: true, CreatedAt: key.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: key.UpdatedAt.UTC().Format(time.RFC3339Nano)})
	}
	writeAdminJSON(w, http.StatusOK, struct {
		Items []keyView `json:"items"`
	}{views})
}

type createKeyRequest struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}
type oneTimeKeyPayload struct {
	Name    string   `json:"name"`
	Key     string   `json:"key"`
	Scopes  []string `json:"scopes"`
	OneTime bool     `json:"one_time"`
}

func (h *adminHandler) handleKeyCreate(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	var request createKeyRequest
	if _, err := readAdminBody(r, &request); err != nil {
		writeBodyError(w, err)
		return
	}
	if err := validateManagedScopes(request.Scopes); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_scopes", "inbound credentials carry only the inference scope", "scopes")
		return
	}
	key, digest, err := generateInboundKey([]string{ScopeInference})
	if err != nil {
		storageFailure(w, h, "generate inbound credential", err)
		return
	}
	if err := storage.CreateInboundCredential(r.Context(), h.db, storage.InboundCredential{Name: request.Name, Hash: digest, Scopes: []string{ScopeInference}, Active: true}); err != nil {
		writeCredentialStorageError(w, h, err)
		return
	}
	if err := h.publishCredentials(r); err != nil {
		storageFailure(w, h, "publish inbound credentials", err)
		return
	}
	writeAdminJSON(w, http.StatusCreated, oneTimeKeyPayload{Name: request.Name, Key: key, Scopes: []string{ScopeInference}, OneTime: true})
}

func (h *adminHandler) handleKeyRotate(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	name := r.PathValue("name")
	keys, err := storage.ListInboundCredentials(r.Context(), h.db)
	if err != nil {
		storageFailure(w, h, "read inbound credential", err)
		return
	}
	var selected *storage.InboundCredential
	for i := range keys {
		if keys[i].Name == name && keys[i].Active {
			selected = &keys[i]
			break
		}
	}
	if selected == nil {
		writeAdminError(w, http.StatusNotFound, "key_not_found", "the active inbound credential does not exist", "name")
		return
	}
	key, digest, err := generateInboundKey([]string{ScopeInference})
	if err != nil {
		storageFailure(w, h, "generate inbound credential", err)
		return
	}
	if err := storage.RotateInboundCredential(r.Context(), h.db, name, digest); err != nil {
		writeCredentialStorageError(w, h, err)
		return
	}
	if err := h.publishCredentials(r); err != nil {
		storageFailure(w, h, "publish inbound credentials", err)
		return
	}
	writeAdminJSON(w, http.StatusOK, oneTimeKeyPayload{Name: name, Key: key, Scopes: []string{ScopeInference}, OneTime: true})
}

func (h *adminHandler) handleKeyDisable(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	name := r.PathValue("name")
	if err := storage.DisableInboundCredential(r.Context(), h.db, name); err != nil {
		writeCredentialStorageError(w, h, err)
		return
	}
	if err := h.publishCredentials(r); err != nil {
		storageFailure(w, h, "publish inbound credentials", err)
		return
	}
	writeAdminJSON(w, http.StatusOK, struct {
		Disabled bool `json:"disabled"`
	}{true})
}

func (h *adminHandler) publishCredentials(r *http.Request) error {
	if h.reloadCredentials == nil {
		return errors.New("credential publisher is not assembled")
	}
	if err := h.reloadCredentials(r.Context()); err != nil {
		// The database has already committed. If the in-memory snapshot cannot be
		// refreshed, fail closed for inference keys rather than continuing to accept
		// a key the operator just rotated or disabled. The owner session is separate.
		if h.authenticator != nil {
			_ = h.authenticator.ReplaceDatabaseCredentials(nil)
		}
		return err
	}
	return nil
}

func validateManagedScopes(scopes []string) error {
	if len(scopes) != 1 || scopes[0] != ScopeInference {
		return errors.New("invalid scopes")
	}
	return nil
}

func generateInboundKey(scopes []string) (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	if err := validateManagedScopes(scopes); err != nil {
		return "", "", err
	}
	key := "ar_inference_" + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(key))
	return key, hex.EncodeToString(sum[:]), nil
}

func writeCredentialStorageError(w http.ResponseWriter, h *adminHandler, err error) {
	switch {
	case errors.Is(err, storage.ErrCredentialNameInvalid):
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the credential name must match the required format", "name")
	case errors.Is(err, storage.ErrCredentialNotFound):
		writeAdminError(w, http.StatusNotFound, "key_not_found", "the active inbound credential does not exist", "name")
	case errors.Is(err, storage.ErrCredentialNameExists):
		writeAdminError(w, http.StatusConflict, "key_name_exists", "an inbound credential with this audit name already exists", "name")
	case errors.Is(err, storage.ErrCredentialLimit):
		writeAdminError(w, http.StatusConflict, "credential_limit", "the maximum number of active inbound credentials has been reached", "scopes")
	default:
		storageFailure(w, h, "change inbound credential", err)
	}
}
