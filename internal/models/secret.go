package models

import (
	"log/slog"
)

// MaskSecret renders a credential for logs. Short secrets are fully hidden;
// longer ones reveal at most a recognisable prefix and the last four
// characters. The full value is never returned, so an accidental
// slog.Any("provider", provider) cannot leak a key.
func MaskSecret(secret string) string {
	if secret == "" {
		return ""
	}
	runes := []rune(secret)
	if len(runes) < 16 {
		return "***"
	}
	return string(runes[:3]) + "…" + string(runes[len(runes)-4:])
}

// LogValue implements slog.LogValuer so credentials are masked even when a
// caller logs the whole struct. Base URLs are included because they are not
// secrets; API keys never are.
func (p Provider) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("key", p.Key),
		slog.String("display_name", p.DisplayName),
		slog.String("base_url", p.BaseURL),
		slog.String("api_key", MaskSecret(p.APIKey)),
		slog.Bool("enabled", p.Enabled),
		slog.Int("priority", p.Priority),
		slog.String("source", string(p.Source)),
	)
}
