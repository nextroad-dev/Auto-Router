package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

func TestInvalidCredentialNameReturnsBadRequest(t *testing.T) {
	response := httptest.NewRecorder()
	writeCredentialStorageError(response, &adminHandler{}, storage.ErrCredentialNameInvalid)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	var payload struct {
		Error struct {
			Code  string `json:"code"`
			Field string `json:"field"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if payload.Error.Code != "invalid_request" || payload.Error.Field != "name" {
		t.Fatalf("error = %+v, want invalid_request on name", payload.Error)
	}
}

func TestValidateManagedScopes(t *testing.T) {
	tests := []struct {
		name    string
		scopes  []string
		wantErr bool
	}{
		{name: "inference scope", scopes: []string{ScopeInference}},
		{name: "empty scopes", scopes: []string{}, wantErr: true},
		{name: "missing scopes", scopes: nil, wantErr: true},
		{name: "duplicate scope", scopes: []string{ScopeInference, ScopeInference}, wantErr: true},
		{name: "unsupported scope", scopes: []string{"management"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateManagedScopes(test.scopes)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateManagedScopes(%v) error = %v, wantErr %t", test.scopes, err, test.wantErr)
			}
		})
	}
}
