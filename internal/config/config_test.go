package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func noEnv(string) (string, bool) { return "", false }

func fromEnv(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func configFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultsAndExample(t *testing.T) {
	for _, path := range []string{"", filepath.Join("..", "..", "configs", "example.json")} {
		cfg, err := load(path, noEnv)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(cfg, Defaults()) {
			t.Fatalf("configuration from %q differs from defaults: %+v", path, cfg)
		}
	}
}

func TestConfigurationPrecedence(t *testing.T) {
	path := configFile(t, `{"http":{"address":"127.0.0.1:9000","idle_timeout":"30s"},"database":{"path":"from-file.db"},"log":{"level":"warn"}}`)
	cfg, err := load(path, fromEnv(map[string]string{
		"AUTO_ROUTER_HTTP_ADDRESS":          "127.0.0.1:9001",
		"AUTO_ROUTER_DATABASE_PATH":         "from-env.db",
		"AUTO_ROUTER_LOG_LEVEL":             "debug",
		"AUTO_ROUTER_READ_HEADER_TIMEOUT":   "3s",
		"AUTO_ROUTER_READINESS_TIMEOUT":     "150ms",
		"AUTO_ROUTER_SHUTDOWN_TIMEOUT":      "4s",
		"AUTO_ROUTER_DATABASE_BUSY_TIMEOUT": "250ms",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults()
	want.HTTP.Address = "127.0.0.1:9001"
	want.HTTP.IdleTimeout = Duration(30 * time.Second)
	want.HTTP.ReadHeaderTimeout = Duration(3 * time.Second)
	want.HTTP.ReadinessTimeout = Duration(150 * time.Millisecond)
	want.HTTP.ShutdownTimeout = Duration(4 * time.Second)
	want.Database.Path = "from-env.db"
	want.Database.BusyTimeout = Duration(250 * time.Millisecond)
	want.Log.Level = "debug"
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadRejectsInvalidFiles(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"null":             "null",
		"array":            "[]",
		"malformed":        "{",
		"trailing object":  "{} {}",
		"trailing garbage": "{} oops",
		"unknown field":    `{"unknown":true}`,
		"nested typo":      `{"http":{"adress":"127.0.0.1:8080"}}`,
		"numeric duration": `{"http":{"idle_timeout":1000}}`,
		"invalid duration": `{"http":{"idle_timeout":"forever"}}`,
		"null duration":    `{"http":{"idle_timeout":null}}`,
		"zero timeout":     `{"http":{"readiness_timeout":"0s"}}`,
		"negative timeout": `{"http":{"shutdown_timeout":"-1s"}}`,
		"empty path":       `{"database":{"path":"  "}}`,
		"NUL path":         `{"database":{"path":"bad\u0000path"}}`,
		"SQLite URI":       `{"database":{"path":"file:shared?mode=memory"}}`,
		"missing port":     `{"http":{"address":"localhost"}}`,
		"named port":       `{"http":{"address":"localhost:http"}}`,
		"negative port":    `{"http":{"address":"localhost:-1"}}`,
		"port too large":   `{"http":{"address":"localhost:65536"}}`,
		"invalid log":      `{"log":{"level":"verbose"}}`,
		"huge config":      "{}" + strings.Repeat(" ", 1<<20),
	}
	for name, contents := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := load(configFile(t, contents), noEnv); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
	if _, err := load(filepath.Join(t.TempDir(), "missing.json"), noEnv); err == nil {
		t.Fatal("missing explicit configuration should fail")
	}
}

func TestLoadRejectsInvalidEnvironment(t *testing.T) {
	cases := []struct{ key, value string }{
		{"AUTO_ROUTER_HTTP_ADDRESS", ""},
		{"AUTO_ROUTER_DATABASE_PATH", ""},
		{"AUTO_ROUTER_LOG_LEVEL", ""},
		{"AUTO_ROUTER_LOG_LEVEL", "trace"},
		{"AUTO_ROUTER_READ_HEADER_TIMEOUT", "0s"},
		{"AUTO_ROUTER_IDLE_TIMEOUT", "-1s"},
		{"AUTO_ROUTER_READINESS_TIMEOUT", ""},
		{"AUTO_ROUTER_SHUTDOWN_TIMEOUT", "not-a-duration"},
		{"AUTO_ROUTER_DATABASE_BUSY_TIMEOUT", "500us"},
		{"AUTO_ROUTER_DATABASE_BUSY_TIMEOUT", "1500us"},
		{"AUTO_ROUTER_DATABASE_BUSY_TIMEOUT", "2147483648ms"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			if _, err := load("", fromEnv(map[string]string{tc.key: tc.value})); err == nil {
				t.Fatal("expected environment validation error")
			}
		})
	}
}

func TestLoadAppliesEnvironment(t *testing.T) {
	t.Setenv("AUTO_ROUTER_HTTP_ADDRESS", "[::1]:0")
	t.Setenv("AUTO_ROUTER_IDLE_TIMEOUT", "45s")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Address != "[::1]:0" || time.Duration(cfg.HTTP.IdleTimeout) != 45*time.Second {
		t.Fatalf("environment not applied: %+v", cfg.HTTP)
	}
}

func TestDurationRoundTrip(t *testing.T) {
	want := Duration(1250 * time.Millisecond)
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Duration
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got != want || string(data) != `"1.25s"` {
		t.Fatalf("unexpected duration encoding: %s, %v", data, got)
	}
}
