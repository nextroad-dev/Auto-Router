package api

import (
	"errors"
	"net/http"

	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

// The three group names are fixed; only their ordered provider/model membership
// is editable. A single PUT commits all three lists together.
func (h *adminHandler) handleGroupList(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	groups, err := storage.LoadModelGroups(r.Context(), h.db)
	if err != nil {
		storageFailure(w, h, "list model groups", err)
		return
	}
	writeAdminJSON(w, http.StatusOK, groups)
}

func (h *adminHandler) handleGroupReplace(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	var groups storage.ModelGroups
	present, err := readAdminBody(r, &groups)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	for _, name := range []string{"simple", "medium", "complex"} {
		if !wasPresent(present, name) {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "all three model groups are required", name)
			return
		}
	}
	if err := storage.ReplaceModelGroups(r.Context(), h.db, groups); err != nil {
		if errors.Is(err, storage.ErrInvalidModelGroups) {
			writeAdminError(w, http.StatusBadRequest, "invalid_group", "groups must contain distinct enabled provider/model pairs, at most eight per group", "groups")
			return
		}
		storageFailure(w, h, "replace model groups", err)
		return
	}
	if h.reloadGroups != nil {
		if err := h.reloadGroups(r.Context(), groups); err != nil {
			storageFailure(w, h, "publish model groups", err)
			return
		}
	}
	writeAdminJSON(w, http.StatusOK, groups)
}
