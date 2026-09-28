package api

import "net/http"

func (h *adminHandler) handleRegistrySync(w http.ResponseWriter, r *http.Request) {
	if h.syncRegistry == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "sync_unavailable", "registry synchronization is not available", "")
		return
	}
	result, err := h.syncRegistry(r.Context())
	if err != nil {
		if h.logger != nil {
			h.logger.Warn("models.dev synchronization failed", "error", err.Error())
		}
		writeAdminError(w, http.StatusBadGateway, "sync_failed", "models.dev synchronization failed; the registry was not changed", "")
		return
	}
	writeAdminJSON(w, http.StatusOK, result)
}
