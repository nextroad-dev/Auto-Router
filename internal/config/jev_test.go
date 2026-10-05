package config

import (
	"testing"

	"github.com/nextroad-dev/Auto-Router/internal/router/jev"
)

func TestJevDomainInputModeMatchesPrivacyDefault(t *testing.T) {
	for _, mode := range []string{"", "unsupported"} {
		if got := (JevConfig{InputMode: mode}).DomainInputMode(); got != jev.InputModeRedacted {
			t.Errorf("programmatic mode %q falls back to %q, want redacted", mode, got)
		}
	}
	for _, literal := range jev.InputModeValues() {
		if got := (JevConfig{InputMode: literal}).DomainInputMode(); string(got) != literal {
			t.Errorf("configured mode %q became %q", literal, got)
		}
	}
}
