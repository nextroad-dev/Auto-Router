package config

import (
	"os"
	"path/filepath"
)

// Source labels where an effective setting came from.
const (
	SourceDefault = "default"
	SourceFile    = "file"
	SourceEnv     = "env"
)

// Sources is a compatibility type for configuration-loading callers. The serving
// process now starts from code defaults and applies a SQLite runtime overlay; it
// does not load values from a file or environment variables.
type Sources struct {
	// Path is empty for the fixed serving configuration.
	Path string
}

// Source reports the provenance of one dotted setting path. The fixed startup
// configuration is composed of code defaults; runtime overlay provenance is
// tracked by internal/settings instead.
func (Sources) Source(_ string) string { return SourceDefault }

// EnvVariable is retained for source compatibility. No environment variable
// supplies serving configuration, so it always returns an empty string.
func (Sources) EnvVariable(_ string) string { return "" }

// File reports the configuration file, if any. The fixed serving process does
// not read one, so the result is empty.
func (s Sources) File() string { return s.Path }

// LoadWithSources returns the fixed code defaults and an empty provenance report.
// path is ignored, matching Load and the serving process's startup behavior.
//
// Deprecated: Use Defaults and manage live settings through the Admin API.
func LoadWithSources(path string) (Config, Sources, error) {
	_ = path
	cfg := Defaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, Sources{}, err
	}
	return cfg, Sources{}, nil
}

// ConfigDir returns the process working directory when no configuration file was
// used, or the directory of a retained compatibility Path value.
func (s Sources) ConfigDir() string {
	if s.Path == "" {
		working, err := os.Getwd()
		if err != nil {
			return ""
		}
		return working
	}
	return filepath.Dir(s.Path)
}
