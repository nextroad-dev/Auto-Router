package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/config"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestRunHelpAndInvalidArguments(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), []string{"-help"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "-config") {
		t.Fatal("help output should describe configuration")
	}
	for _, args := range [][]string{{"unexpected"}, {"-unknown"}, {"-config", "does-not-exist.json"}} {
		if err := run(t.Context(), args, io.Discard); err == nil {
			t.Fatalf("expected error for arguments %v", args)
		}
	}
}

func TestServerTimeoutsDoNotCutOffStreams(t *testing.T) {
	cfg := config.Defaults().HTTP
	server := newHTTPServer(cfg, nil, testLogger())
	if server.ReadTimeout != 0 || server.WriteTimeout != 0 {
		t.Fatal("global request/response timeouts must not truncate future streams")
	}
	if server.ReadHeaderTimeout != time.Duration(cfg.ReadHeaderTimeout) || server.IdleTimeout != time.Duration(cfg.IdleTimeout) {
		t.Fatal("header and keep-alive protections must remain configured")
	}
}

func startServer(t *testing.T, server *http.Server, timeout time.Duration) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	t.Cleanup(func() { server.Close() })
	result := make(chan error, 1)
	go func() { result <- serve(ctx, server, listener, timeout, testLogger()) }()
	return "http://" + listener.Addr().String(), cancel, result
}

func waitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for lifecycle event")
	}
}

func waitResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server/request result")
		return nil
	}
}

func makeRequest(t *testing.T, url string) <-chan error {
	t.Helper()
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
	result := make(chan error, 1)
	go func() {
		response, err := client.Get(url)
		if err != nil {
			result <- err
			return
		}
		defer response.Body.Close()
		_, err = io.Copy(io.Discard, response.Body)
		if response.StatusCode != http.StatusOK && err == nil {
			err = errors.New("unexpected HTTP status")
		}
		result <- err
	}()
	return result
}

func TestServeGracefullyDrainsInFlightRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	shutdownStarted := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			_, _ = io.WriteString(w, "finished")
		case <-r.Context().Done():
			t.Error("graceful shutdown canceled an in-flight request")
		}
	})}
	server.RegisterOnShutdown(func() { close(shutdownStarted) })
	url, cancel, result := startServer(t, server, 2*time.Second)
	request := makeRequest(t, url)
	waitSignal(t, started)
	cancel()
	waitSignal(t, shutdownStarted)
	select {
	case err := <-result:
		t.Fatalf("server stopped before its in-flight request completed: %v", err)
	default:
	}
	unblock()
	if err := waitResult(t, request); err != nil {
		t.Fatal(err)
	}
	if err := waitResult(t, result); err != nil {
		t.Fatal(err)
	}
}

func TestServeForcesCloseAfterShutdownDeadline(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(canceled)
	})}
	url, cancel, result := startServer(t, server, 30*time.Millisecond)
	request := makeRequest(t, url)
	waitSignal(t, started)
	cancel()
	if err := waitResult(t, result); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected shutdown deadline error, got %v", err)
	}
	waitSignal(t, canceled)
	if err := waitResult(t, request); err == nil {
		t.Fatal("forced close should interrupt the unfinished response")
	}
}

func TestServeReportsListenerFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	if err := serve(t.Context(), &http.Server{}, listener, time.Second, testLogger()); err == nil {
		t.Fatal("closed listener must not be reported as a normal shutdown")
	}
}
