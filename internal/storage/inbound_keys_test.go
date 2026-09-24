package storage

import (
	"errors"
	"testing"
)

func TestValidateCredentialName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{name: "a"},
		{name: "provider-1"},
		{name: "not.valid_name"},
		{name: "", wantErr: true},
		{name: "Uppercase", wantErr: true},
		{name: "has space", wantErr: true},
		{name: "-starts-with-punctuation", wantErr: true},
		{name: string(make([]byte, 65)), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateCredentialName(test.name)
			if test.wantErr && !errors.Is(err, ErrCredentialNameInvalid) {
				t.Fatalf("validateCredentialName(%q) error = %v, want ErrCredentialNameInvalid", test.name, err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("validateCredentialName(%q) error = %v, want nil", test.name, err)
			}
		})
	}
}
