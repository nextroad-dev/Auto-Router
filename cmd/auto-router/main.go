package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/api"
	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

func main() {
	ctx, stop := notifyContext(context.Background())
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
		logger.Error("service stopped with an error", "error", err)
		os.Exit(1)
	}
}

// notifyContext cancels on the signals a service manager uses to ask for a
// graceful stop. It is separate from main so its wiring can be tested.
func notifyContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("auto-router", flag.ContinueOnError)
	flags.SetOutput(output)
	configPath := flags.String("config", "", "optional JSON configuration file")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments; use -help for usage")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.Log.Level)); err != nil {
		return fmt.Errorf("parse log level: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level}))
	db, err := storage.Open(ctx, storage.Options{
		Path:        cfg.Database.Path,
		BusyTimeout: time.Duration(cfg.Database.BusyTimeout),
	})
	if err != nil {
		return err
	}
	defer db.Close()
	if err := storage.Migrate(ctx, db); err != nil {
		return err
	}

	server := newHTTPServer(cfg.HTTP, db, logger)
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	return serve(ctx, server, listener, time.Duration(cfg.HTTP.ShutdownTimeout), logger)
}

func newHTTPServer(cfg config.HTTPConfig, checker api.ReadinessChecker, logger *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              cfg.Address,
		Handler:           api.NewHandler(checker, time.Duration(cfg.ReadinessTimeout)),
		ReadHeaderTimeout: time.Duration(cfg.ReadHeaderTimeout),
		IdleTimeout:       time.Duration(cfg.IdleTimeout),
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		// Deliberately no global ReadTimeout/WriteTimeout: future streaming
		// handlers enforce body/upstream timeouts without truncating long SSE.
	}
}

// serve drains in-flight requests on cancellation, with a bounded forced-close
// path. It does not bind request contexts to the signal context, since doing so
// would cancel every in-flight request before graceful draining could begin.
func serve(ctx context.Context, server *http.Server, listener net.Listener, shutdownTimeout time.Duration, logger *slog.Logger) error {
	defer server.Close()
	result := make(chan error, 1)
	go func() {
		result <- server.Serve(listener)
	}()
	logger.Info("service started", "address", listener.Addr().String())

	select {
	case err := <-result:
		return serveError(err)
	case <-ctx.Done():
		logger.Info("service shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			closeErr := server.Close()
			return errors.Join(fmt.Errorf("graceful shutdown: %w", err), closeErr, serveError(<-result))
		}
		if err := serveError(<-result); err != nil {
			return err
		}
		logger.Info("service stopped")
		return nil
	}
}

func serveError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve HTTP: %w", err)
}
