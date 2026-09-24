package api

import (
	"errors"
	"net/http"
)

var ErrSyncAllowlistEmpty = errors.New("models.dev allowlist is empty")

func (h *adminHandler) handleRegistrySync(w http.ResponseWriter, r *http.Request) {
	if h.syncRegistry == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "sync_unavailable", "registry synchronization is not available", "")
		return
	}
	result, err := h.syncRegistry(r.Context())
	if err != nil {
		if errors.Is(err, ErrSyncAllowlistEmpty) {
			writeAdminError(w, http.StatusUnprocessableEntity, "empty_allowlist", "add at least one provider/model to the models.dev allowlist in Settings before synchronizing", "registry.sync.include")
			return
		}
		if h.logger != nil {
			h.logger.Warn("models.dev synchronization failed", "error", err.Error())
		}
		writeAdminError(w, http.StatusBadGateway, "sync_failed", "models.dev synchronization failed; the registry was not changed", "")
		return
	}
	writeAdminJSON(w, http.StatusOK, result)
}
