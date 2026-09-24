package analyzer

import (
	"errors"
	"testing"
)

func TestResolvePreferenceUsesGlobalDefaultUnlessHeaderOverrides(t *testing.T) {
	preference, source, err := ResolvePreference("", PreferenceCost)
	if err != nil {
		t.Fatal(err)
	}
	if preference != PreferenceCost || source != SourceDefault {
		t.Fatalf("default preference = %q from %q, want cost from default", preference, source)
	}

	preference, source, err = ResolvePreference(" QUALITY ", PreferenceCost)
	if err != nil {
		t.Fatal(err)
	}
	if preference != PreferenceQuality || source != SourceHeader {
		t.Fatalf("header preference = %q from %q, want quality from header", preference, source)
	}

	if _, _, err := ResolvePreference("unknown", PreferenceCost); !errors.Is(err, ErrInvalidPreference) {
		t.Fatalf("invalid header error = %v, want ErrInvalidPreference", err)
	}
}
