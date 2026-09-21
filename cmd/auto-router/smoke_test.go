package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

// TestProcessSmokeBuildsAndRunsTheRealBinary exercises the shipped artifact, not
// the in-process run() function: sync against a local source, serve, probe
// /readyz, shut down, then reopen the database with the storage layer.
func TestProcessSmokeBuildsAndRunsTheRealBinary(t *testing.T) {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain is not available")
	}
	binary := filepath.Join(t.TempDir(), "auto-router")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command(goBinary, "build", "-o", binary, ".")
	build.Env = os.Environ()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v\n%s", err, output)
	}

	payload := modelsDevFixture(t)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer source.Close()

	cfg := config.Defaults()
	cfg.Database.Path = filepath.Join(t.TempDir(), "data", "router.db")
	cfg.Registry.Sync.URL = source.URL
	cfg.Registry.Sync.Timeout = config.Duration(5 * time.Second)
	cfg.Registry.Sync.Include = []string{"openai/gpt-4o-mini"}
	cfg.Registry.Providers = []config.RegistryProvider{{
		Key:      "openai",
		BaseURL:  "https://api.openai.com/v1",
		APIKey:   "sk-smoke-secret-0123456789",
		Enabled:  true,
		Priority: 1,
	}}
	configPath := writeRuntimeConfig(t, cfg)

	// 1. Explicit synchronization mode exits by itself and never listens.
	syncCommand := exec.Command(binary, "-config", configPath, "-sync-models")
	syncOutput, err := syncCommand.CombinedOutput()
	if err != nil {
		t.Fatalf("sync process failed: %v\n%s", err, syncOutput)
	}
	if !bytes.Contains(syncOutput, []byte("model registry synchronized")) {
		t.Fatalf("sync process did not report success:\n%s", syncOutput)
	}

	// 2. Reopen the persisted database with a separate connection. The sync
	// alone leaves the pair unroutable until the provider is configured.
	db, err := storage.Open(t.Context(), storage.Options{Path: cfg.Database.Path, BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	synced, err := storage.LoadCatalog(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	provider, ok := synced.Provider("openai")
	if !ok || provider.Enabled || provider.BaseURL != "" || provider.APIKey != "" {
		t.Fatalf("a synced provider must stay inert until configured locally: %+v", provider)
	}
	if pairs := synced.PairsForModel("gpt-4o-mini"); len(pairs) != 0 {
		t.Fatalf("a disabled provider must not be routable: %+v", pairs)
	}

	// 3. Serve, probe readiness, then stop the real process.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	cfg.HTTP.Address = address
	configPath = writeRuntimeConfig(t, cfg)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	serveCommand := exec.CommandContext(ctx, binary, "-config", configPath)
	var serveOutput bytes.Buffer
	serveCommand.Stdout = &serveOutput
	serveCommand.Stderr = &serveOutput
	if err := serveCommand.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = serveCommand.Process.Kill()
		_, _ = serveCommand.Process.Wait()
	})
	deadline := time.Now().Add(15 * time.Second)
	ready := false
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		response, err := client.Get("http://" + address + "/readyz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("real process never became ready:\n%s", serveOutput.String())
	}
	if strings.Contains(serveOutput.String(), "sk-smoke-secret-0123456789") {
		t.Fatalf("real process logs leaked the API key:\n%s", serveOutput.String())
	}

	if runtime.GOOS == "windows" {
		// The Windows Go runtime cannot deliver os.Interrupt to another process;
		// graceful shutdown is covered by the in-process serve tests.
		if err := serveCommand.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		if err := serveCommand.Wait(); err == nil {
			t.Fatal("a killed process should not report a clean exit")
		}
	} else {
		if err := serveCommand.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		if err := serveCommand.Wait(); err != nil {
			t.Fatalf("real process did not stop cleanly: %v\n%s", err, serveOutput.String())
		}
	}

	// 4. The database survives process shutdown, the local provider override is
	// persisted and the synced pair became routable.
	reopened, err := storage.Open(t.Context(), storage.Options{Path: cfg.Database.Path, BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := storage.Migrate(t.Context(), reopened); err != nil {
		t.Fatal(err)
	}
	after, err := storage.LoadCatalog(t.Context(), reopened)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Providers) != 1 || len(after.Pairs) != 1 {
		t.Fatalf("registry did not survive the process: %+v", after)
	}
	if pairs := after.PairsForModel("gpt-4o-mini"); len(pairs) != 1 {
		t.Fatalf("synced pair is not routable after local provider configuration: %+v", pairs)
	}
	configured, _ := after.Provider("openai")
	if configured.Source != models.SourceLocal || configured.APIKey == "" {
		t.Fatalf("local provider override missing: %+v", configured)
	}
}
