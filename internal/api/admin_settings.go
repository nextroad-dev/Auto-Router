package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/nextroad-dev/Auto-Router/internal/settings"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

// This file is the settings half of the management surface.
//
// The single most important property is that a change is a candidate, never an
// edit: the request is merged onto the current effective configuration, validated
// and compiled, and only a candidate that passes both steps is stored and
// published. A refused change therefore leaves the running process exactly as it
// was, which is what makes the management surface safe to expose.
//
// Credentials are never returned. jev.api_key is reported as "set" plus a masked
// rendering, provider credentials are managed through provider endpoints, and the
// inbound key list is reduced to its audit names, scopes and set state.

// settingsPayload is the effective settings report.
type settingsPayload struct {
	Version   int64            `json:"version"`
	Overlay   bool             `json:"overlay"`
	Settings  []settings.Field `json:"settings"`
	Effective map[string]any   `json:"effective"`
	Warnings  []string         `json:"warnings"`
}

// handleSettingsGet serves GET /admin/v1/settings.
func (h *adminHandler) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	if h.settings == nil {
		writeAdminError(w, http.StatusInternalServerError, "read_only_state",
			"the runtime settings store is not assembled in this process", "")
		return
	}
	snapshot := h.settings.Current()
	cfg := snapshot.Config
	warnings := cfg.Warnings()
	if warnings == nil {
		warnings = []string{}
	}
	writeAdminJSON(w, http.StatusOK, settingsPayload{
		Version:   snapshot.Version,
		Overlay:   h.settings.HasOverlay(),
		Settings:  settings.Fields(cfg, h.settings.Sources(), snapshot.Overlay),
		Effective: settings.EffectiveDocument(cfg, h.settings.Sources(), true),
		Warnings:  warnings,
	})
}

// handleSettingsPatch serves PATCH /admin/v1/settings. The body is a nested JSON
// document with the same shape as the configuration file, restricted to the
// runtime-mutable paths.
func (h *adminHandler) handleSettingsPatch(w http.ResponseWriter, r *http.Request) {
	if h.settings == nil {
		writeAdminError(w, http.StatusInternalServerError, "read_only_state",
			"the runtime settings store is not assembled in this process", "")
		return
	}
	var patch map[string]any
	if err := readAdminJSON(r, &patch); err != nil {
		writeBodyError(w, err)
		return
	}
	if len(patch) == 0 {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the request must change at least one setting", "body")
		return
	}
	// The catalogue is consulted before the merge, so a request that mixes a mutable
	// and an immutable setting changes nothing at all rather than applying the half
	// it is allowed to.
	if err := settings.ValidatePatch(patch); err != nil {
		if path, reason, ok := settings.IsImmutable(err); ok {
			writeAdminError(w, http.StatusUnprocessableEntity, "restart_required",
				reason+"; change it in the configuration file and restart", path)
			return
		}
		if path, ok := settings.IsUnknown(err); ok {
			writeAdminError(w, http.StatusUnprocessableEntity, "unsupported_setting",
				"this name is not a configuration setting", path)
			return
		}
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the request could not be applied", "body")
		return
	}
	result, err := h.settings.Apply(r.Context(), patch)
	switch {
	case errors.Is(err, storage.ErrSettingsConflict):
		// The stored version moved between the read and the write, so the caller's
		// change was based on a state that no longer exists. Re-reading and retrying
		// is the remedy, and only the caller can decide whether its intent still
		// applies.
		writeAdminError(w, http.StatusConflict, "settings_conflict",
			"the runtime settings changed since they were read; re-read and retry", "")
		return
	case errors.Is(err, settings.ErrReadOnly):
		writeAdminError(w, http.StatusInternalServerError, "read_only_state",
			"runtime settings cannot be persisted in this process", "")
		return
	case err != nil:
		if path, reason, ok := settings.IsImmutable(err); ok {
			writeAdminError(w, http.StatusUnprocessableEntity, "restart_required", reason, path)
			return
		}
		// Anything else is a refused value: the validation or compilation step said
		// no. The message is authored here and never repeats the value, because a
		// value echoed into a response is how a secret ends up in a log.
		if h.logger != nil {
			h.logger.Warn("the runtime settings change was refused", "error", err.Error())
		}
		writeAdminError(w, http.StatusBadRequest, "invalid_request",
			"the change was refused by configuration validation; the running configuration is unchanged", "body")
		return
	}
	warnings := result.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	writeAdminJSON(w, http.StatusOK, struct {
		Version   int64            `json:"version"`
		Changed   []string         `json:"changed"`
		Settings  []settings.Field `json:"settings"`
		Effective map[string]any   `json:"effective"`
		Warnings  []string         `json:"warnings"`
	}{
		Version:   result.Version,
		Changed:   result.Changes,
		Settings:  settings.Fields(result.Config, h.settings.Sources(), h.settings.Current().Overlay),
		Effective: result.Effective,
		Warnings:  warnings,
	})
}

// handleSettingsReset serves DELETE /admin/v1/settings: every runtime override is
// removed and the process returns to the file and default values.
func (h *adminHandler) handleSettingsReset(w http.ResponseWriter, r *http.Request) {
	if h.settings == nil {
		writeAdminError(w, http.StatusInternalServerError, "read_only_state",
			"the runtime settings store is not assembled in this process", "")
		return
	}
	result, err := h.settings.Reset(r.Context())
	if err != nil {
		if errors.Is(err, settings.ErrReadOnly) {
			writeAdminError(w, http.StatusInternalServerError, "read_only_state",
				"runtime settings cannot be persisted in this process", "")
			return
		}
		storageFailure(w, h, "reset settings", err)
		return
	}
	warnings := result.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	writeAdminJSON(w, http.StatusOK, struct {
		Version   int64            `json:"version"`
		Settings  []settings.Field `json:"settings"`
		Effective map[string]any   `json:"effective"`
		Warnings  []string         `json:"warnings"`
	}{
		Version:   result.Version,
		Settings:  settings.Fields(result.Config, h.settings.Sources(), h.settings.Current().Overlay),
		Effective: result.Effective,
		Warnings:  warnings,
	})
}

// readAdminJSON reads one request body as a JSON object with json.Number preserved,
// so mergeOverlay's nested handling sees the same value types a stored overlay does.
func readAdminJSON(r *http.Request, destination *map[string]any) error {
	if r.ContentLength > AdminBodyLimit {
		return errBodyTooLarge
	}
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, AdminBodyLimit))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			return errBodyTooLarge
		}
		return errors.New("the request body is not a valid JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("the request body must contain exactly one JSON object")
	}
	if *destination == nil {
		return errors.New("the request body must be a JSON object")
	}
	return nil
}
