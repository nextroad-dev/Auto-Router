// Package config loads and validates process configuration. Routing and provider
// settings will be introduced with their owning development stages.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Duration represents a Go duration encoded as a JSON string, e.g. "5s".
type Duration time.Duration

func (d *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return errors.New("duration must be a string such as \"5s\"")
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return errors.New("duration must be a valid Go duration such as \"5s\"")
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

type Config struct {
	HTTP     HTTPConfig     `json:"http"`
	Database DatabaseConfig `json:"database"`
	Log      LogConfig      `json:"log"`
	Registry RegistryConfig `json:"registry"`
}

type HTTPConfig struct {
	Address           string   `json:"address"`
	ReadHeaderTimeout Duration `json:"read_header_timeout"`
	IdleTimeout       Duration `json:"idle_timeout"`
	ReadinessTimeout  Duration `json:"readiness_timeout"`
	ShutdownTimeout   Duration `json:"shutdown_timeout"`
}

type DatabaseConfig struct {
	Path        string   `json:"path"`
	BusyTimeout Duration `json:"busy_timeout"`
}

type LogConfig struct {
	Level string `json:"level"`
}

func Defaults() Config {
	return Config{
		HTTP: HTTPConfig{
			Address:           "127.0.0.1:8080",
			ReadHeaderTimeout: Duration(5 * time.Second),
			IdleTimeout:       Duration(60 * time.Second),
			ReadinessTimeout:  Duration(2 * time.Second),
			ShutdownTimeout:   Duration(10 * time.Second),
		},
		Database: DatabaseConfig{
			Path:        "data/auto-router.db",
			BusyTimeout: Duration(5 * time.Second),
		},
		Log:      LogConfig{Level: "info"},
		Registry: defaultRegistryConfig(),
	}
}

// Load applies defaults, an optional JSON file, then explicit environment
// overrides. Relative database paths are resolved from the process directory.
// An explicitly provided but empty environment variable is an error, not a
// request to silently fall back to the file or defaults.
func Load(path string) (Config, error) {
	return load(path, os.LookupEnv)
}

func load(path string, lookup func(string) (string, bool)) (Config, error) {
	cfg := Defaults()
	if path != "" {
		if err := loadFile(path, &cfg); err != nil {
			return Config{}, err
		}
	}
	if err := cfg.Registry.normalize(lookup); err != nil {
		return Config{}, err
	}
	stringsToSet := []struct {
		name   string
		target *string
	}{
		{"AUTO_ROUTER_HTTP_ADDRESS", &cfg.HTTP.Address},
		{"AUTO_ROUTER_DATABASE_PATH", &cfg.Database.Path},
		{"AUTO_ROUTER_LOG_LEVEL", &cfg.Log.Level},
	}
	for _, env := range stringsToSet {
		if value, ok := lookup(env.name); ok {
			*env.target = value
		}
	}
	durationsToSet := []struct {
		name   string
		target *Duration
	}{
		{"AUTO_ROUTER_READ_HEADER_TIMEOUT", &cfg.HTTP.ReadHeaderTimeout},
		{"AUTO_ROUTER_IDLE_TIMEOUT", &cfg.HTTP.IdleTimeout},
		{"AUTO_ROUTER_READINESS_TIMEOUT", &cfg.HTTP.ReadinessTimeout},
		{"AUTO_ROUTER_SHUTDOWN_TIMEOUT", &cfg.HTTP.ShutdownTimeout},
		{"AUTO_ROUTER_DATABASE_BUSY_TIMEOUT", &cfg.Database.BusyTimeout},
	}
	for _, env := range durationsToSet {
		if value, ok := lookup(env.name); ok {
			parsed, err := time.ParseDuration(value)
			if err != nil {
				return Config{}, fmt.Errorf("%s must be a Go duration such as 5s", env.name)
			}
			*env.target = Duration(parsed)
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

func loadFile(path string, cfg *Config) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()
	const maxConfigBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return fmt.Errorf("read configuration: %w", err)
	}
	if len(data) > maxConfigBytes {
		return errors.New("configuration exceeds 1 MiB")
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return errors.New("configuration must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(cfg); err != nil {
		return fmt.Errorf("decode configuration: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("configuration must contain exactly one JSON object")
	}
	return nil
}

func (c Config) Validate() error {
	_, port, err := net.SplitHostPort(c.HTTP.Address)
	if err != nil || strings.TrimSpace(c.HTTP.Address) != c.HTTP.Address {
		return errors.New("http.address must be host:port (for example 127.0.0.1:8080)")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return errors.New("http.address port must be between 0 and 65535")
	}
	if strings.TrimSpace(c.Database.Path) == "" || strings.ContainsRune(c.Database.Path, '\x00') {
		return errors.New("database.path must be a non-empty filesystem path or :memory:")
	}
	if strings.HasPrefix(strings.ToLower(c.Database.Path), "file:") {
		return errors.New("database.path must be a filesystem path, not a SQLite URI")
	}
	for _, timeout := range []struct {
		name  string
		value Duration
	}{
		{"http.read_header_timeout", c.HTTP.ReadHeaderTimeout},
		{"http.idle_timeout", c.HTTP.IdleTimeout},
		{"http.readiness_timeout", c.HTTP.ReadinessTimeout},
		{"http.shutdown_timeout", c.HTTP.ShutdownTimeout},
	} {
		if timeout.value <= 0 {
			return fmt.Errorf("%s must be positive", timeout.name)
		}
	}
	busy := time.Duration(c.Database.BusyTimeout)
	if busy < time.Millisecond || busy%time.Millisecond != 0 || busy > (1<<31-1)*time.Millisecond {
		return errors.New("database.busy_timeout must be a whole number of milliseconds between 1 and 2147483647")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("log.level must be debug, info, warn or error")
	}
	if err := c.Registry.Validate(); err != nil {
		return err
	}
	return nil
}
