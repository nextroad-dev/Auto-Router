package api

import (
	"errors"
	"net/http"

	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

// handleSetupStatus is the public first-run probe. It returns no account data,
// only whether a password has been initialized.
func (h *adminHandler) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	set, err := storage.PasswordSet(r.Context(), h.db)
	if err != nil {
		storageFailure(w, h, "read password setup state", err)
		return
	}
	writeAdminJSON(w, http.StatusOK, struct {
		PasswordSet bool `json:"password_set"`
	}{set})
}

type setPasswordRequest struct {
	Password *string `json:"password"`
}

// handleSetupPassword sets the owner's password exactly once. Registration is
// intentionally left to admin.go so the setup routes can be exposed by the
// embedding application without coupling storage to its router.
func (h *adminHandler) handleSetupPassword(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	if !sameOriginRequest(r) {
		writeAdminError(w, http.StatusForbidden, "cross_site_request", "the request did not originate from this management surface", "")
		return
	}
	if h.sessions != nil && !h.sessions.AllowLogin() {
		writeAdminError(w, http.StatusTooManyRequests, "too_many_attempts", "too many password setup attempts; try again later", "")
		return
	}
	var body setPasswordRequest
	if _, err := readAdminBody(r, &body); err != nil {
		if h.sessions != nil {
			h.sessions.RecordFailure()
		}
		writeBodyError(w, err)
		return
	}
	if body.Password == nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "the request body must carry a string password field", "password")
		return
	}
	if err := storage.SetInitialPassword(r.Context(), h.db, *body.Password); err != nil {
		switch {
		case errors.Is(err, storage.ErrPasswordAlreadySet):
			writeAdminError(w, http.StatusConflict, "password_already_set", "the management password has already been set", "password")
		case errors.Is(err, storage.ErrInvalidPassword):
			writeAdminError(w, http.StatusBadRequest, "invalid_password", err.Error(), "password")
		default:
			storageFailure(w, h, "set initial management password", err)
		}
		return
	}
	if h.sessions != nil {
		h.sessions.RecordSuccess()
	}
	writeAdminJSON(w, http.StatusCreated, struct {
		PasswordSet bool `json:"password_set"`
	}{true})
}

type changePasswordRequest struct {
	CurrentPassword *string `json:"current_password"`
	Password        *string `json:"password"`
}

// handlePasswordChange replaces the password for the authenticated owner and
// revokes every existing browser session as soon as the database commit succeeds.
func (h *adminHandler) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	if _, ok := IdentityFrom(r); !ok {
		writeAdminError(w, http.StatusUnauthorized, "invalid_session", "an authenticated management session is required", "")
		return
	}
	if !sameOriginRequest(r) {
		writeAdminError(w, http.StatusForbidden, "cross_site_request", "the request did not originate from this management surface", "")
		return
	}
	var body changePasswordRequest
	if _, err := readAdminBody(r, &body); err != nil {
		writeBodyError(w, err)
		return
	}
	if body.CurrentPassword == nil || body.Password == nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "current_password and password are required", "password")
		return
	}
	if err := storage.ChangePassword(r.Context(), h.db, *body.CurrentPassword, *body.Password); err != nil {
		switch {
		case errors.Is(err, storage.ErrPasswordMismatch):
			writeAdminError(w, http.StatusUnauthorized, "invalid_password", "the current password is incorrect", "current_password")
		case errors.Is(err, storage.ErrPasswordNotSet):
			writeAdminError(w, http.StatusConflict, "password_not_set", "the management password has not been set", "current_password")
		case errors.Is(err, storage.ErrInvalidPassword):
			writeAdminError(w, http.StatusBadRequest, "invalid_password", err.Error(), "password")
		default:
			storageFailure(w, h, "change management password", err)
		}
		return
	}
	if h.sessions != nil {
		h.sessions.Clear()
	}
	clearSessionCookie(w)
	writeAdminJSON(w, http.StatusOK, struct {
		Changed bool `json:"changed"`
	}{true})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}
