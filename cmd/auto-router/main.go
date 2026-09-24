package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/api"
	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/logging"
	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/modelsdev"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
	"github.com/nextroad-dev/Auto-Router/internal/providers/direct"
	"github.com/nextroad-dev/Auto-Router/internal/proxy"
	"github.com/nextroad-dev/Auto-Router/internal/router/analyzer"
	"github.com/nextroad-dev/Auto-Router/internal/router/auto"
	"github.com/nextroad-dev/Auto-Router/internal/router/jev"
	"github.com/nextroad-dev/Auto-Router/internal/router/policy"
	"github.com/nextroad-dev/Auto-Router/internal/settings"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

// processStart is when this process began running. It is the health report's
// uptime origin, and it is a package variable rather than a parameter so every
// assembly path reports the same instant.
var processStart = time.Now()

// globalExecutorRegistry is rebuilt together with every published catalog. A
// request resolves its provider executor once, so a catalog edit cannot change
// the endpoint or credential of an in-flight request.
var globalExecutorRegistry = direct.NewRegistry()

// buildVersion identifies the binary that is running. It is set at build time with
// -ldflags "-X main.buildVersion=..." and is "dev" for a plain `go build`.
//
// It exists because "which build is this?" is a question that is otherwise
// unanswerable from the outside, and it has a specific failure mode: on Windows a
// shell resolves ./bin/auto-router through PATHEXT, so a freshly built extensionless
// file is silently ignored in favour of a stale bin/auto-router.exe. The reported
// version, combined with the configuration path in a decode error, is what turns
// "unknown field session_ttl" from a mystery into a diagnosis.
var buildVersion = "dev"

func main() {
	ctx, stop := notifyContext(context.Background())
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
		logger.Error("service stopped with an error", "error", err)
		// A policy check reports a routing refusal through a distinct exit code so a
		// maintenance run can gate a deployment. Every other failure is a usage,
		// configuration or I/O error.
		os.Exit(exitCode(err))
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
	listenAddress := flags.String("listen", "", "override the default HTTP listen address")
	configPath := flags.String("config", "", "deprecated; ignored")
	syncModels := flags.Bool("sync-models", false, "deprecated; use the WebUI to synchronize models.dev")
	recoverAdmin := flags.Bool("recover-admin", false, "offline recovery: rotate administrator credentials while holding the fixed listener port")
	jevCheck := flags.Bool("jev-check", false, "send one real TypeSafe Jev request using the configured candidates, then exit")
	jevCheckPrompt := flags.String("jev-check-prompt", "", "optional text to send to TypeSafe instead of the built-in check sentence")
	var analyzeCheck analyzeCheckFlags
	registerAnalyzeCheckFlags(flags, &analyzeCheck)
	var policyCheck policyCheckFlags
	registerPolicyCheckFlags(flags, &policyCheck)
	var logsCheck logsCheckFlags
	registerLogsCheckFlags(flags, &logsCheck)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments; use -help for usage")
	}
	if *jevCheckPrompt != "" && !*jevCheck {
		return errors.New("-jev-check-prompt requires -jev-check")
	}
	// The maintenance modes each take over the process and then exit, so exactly one
	// of them may be requested. The check is exhaustive rather than a loop over a
	// list, so adding a mode cannot silently create a second usable combination.
	modes := []struct {
		name    string
		enabled bool
	}{
		{"-sync-models", *syncModels},
		{"-recover-admin", *recoverAdmin},
		{"-jev-check", *jevCheck},
		{"-analyze-check", analyzeCheck.enabled},
		{"-policy-check", policyCheck.enabled},
		{"-logs-check", logsCheck.enabled},
	}
	for i := 0; i < len(modes); i++ {
		for j := i + 1; j < len(modes); j++ {
			if modes[i].enabled && modes[j].enabled {
				return fmt.Errorf("%s and %s are separate maintenance modes; run them one at a time", modes[i].name, modes[j].name)
			}
		}
	}
	if err := analyzeCheck.validate(flagsSet(flags)); err != nil {
		return err
	}
	if err := policyCheck.validate(flagsSet(flags)); err != nil {
		return err
	}
	if err := logsCheck.validate(flagsSet(flags)); err != nil {
		return err
	}
	if *configPath != "" {
		fmt.Fprintln(output, "warning: -config is deprecated and ignored; configuration files and AUTO_ROUTER_* variables no longer affect runtime behavior")
	}
	cfg := config.Defaults()
	if *listenAddress != "" {
		cfg.HTTP.Address = *listenAddress
	}
	sources := config.Sources{}
	if *syncModels {
		return errors.New("model synchronization is managed from the WebUI (Admin → Models → Sync); -sync-models did not run")
	}
	if *recoverAdmin {
		return runAdminRecovery(ctx, cfg, output)
	}
	if analyzeCheck.enabled || policyCheck.enabled || logsCheck.enabled {
		var loadErr error
		cfg, loadErr = loadStoredSettingsIfPresent(ctx, cfg)
		if loadErr != nil {
			return loadErr
		}
		if analyzeCheck.enabled {
			return runAnalyzeCheck(cfg, analyzeCheck, output)
		}
		if policyCheck.enabled {
			return runPolicyCheck(cfg, policyCheck, output)
		}
		return runLogsCheck(ctx, cfg, logsCheck, loggerFor(cfg, output), output)
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
	if err := storage.RefuseLegacyDatabase(ctx, db, 8); err != nil {
		return err
	}
	if err := storage.Migrate(ctx, db); err != nil {
		return err
	}
	// Migrations are the first write transaction, so the WAL sidecars (which
	// hold recent registry writes, including credentials) appear here and need
	// the same owner-only permissions as the database file.
	if err := storage.HardenDatabaseFiles(cfg.Database.Path); err != nil {
		return err
	}
	for _, warning := range cfg.Warnings() {
		logger.Warn("configuration warning", "detail", warning)
	}
	credentials, err := storage.LoadInboundCredentials(ctx, db)
	if err != nil {
		return err
	}
	authenticator, err := api.NewDatabaseAuthenticator(toAPICredentials(credentials), logger)
	if err != nil {
		return fmt.Errorf("load inbound credentials: %w", err)
	}
	store, err := loadRegistryFromDB(ctx, db, logger)
	if err != nil {
		return err
	}
	groups, err := storage.LoadModelGroups(ctx, db)
	if err != nil {
		return err
	}
	var groupStore atomic.Pointer[storage.ModelGroups]
	groupStore.Store(&groups)
	// The runtime settings store publishes its first snapshot before the listener
	// exists, so a stored overlay that has become unusable fails startup rather than
	// the first request. The overlay is applied to the values the assembly owns
	// directly, and it is published so the request path reads it per request.
	settingsStore, err := settings.New(ctx, settings.Options{
		Base:    cfg,
		Sources: sources,
		DB:      db,
		Logger:  logger,
		Prepare: func(candidate config.Config) (any, error) { return newJevClient(candidate, logger) },
	})
	if err != nil {
		return err
	}
	cfg = settingsStore.Config()
	if *jevCheck {
		// The check reads the same snapshot the serving process would use and
		// never starts the HTTP server. Failure is reported with an actionable
		// classification instead of a raw error string.
		catalog := store.Load()
		if err := runJevCheck(ctx, cfg, catalog, *jevCheckPrompt, output); err != nil {
			return errors.New(renderJevCheckError(err))
		}
		return nil
	}
	// Build the direct provider executor set from the catalog already loaded from
	// SQLite. Providers without a base_url remain routable in the catalog but
	// return provider_not_configured when selected.
	globalExecutorRegistry.Build(store.Load())
	executor := &providerRouter{registry: globalExecutorRegistry}
	defer func() {
		// Swap an empty snapshot to close idle pools during shutdown. Requests that
		// already hold an old executor retain it until their response completes.
		globalExecutorRegistry.Build(&models.Catalog{})
	}()
	// The routing log's writer is assembled before the listener exists and closed
	// after the server stopped draining, so a request that is still in flight
	// during shutdown still gets its event queued and flushed.
	recorder, err := newRoutingLogWriter(cfg, db, settingsStore, logger)
	if err != nil {
		return err
	}
	if recorder != nil {
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.HTTP.ShutdownTimeout))
			defer cancel()
			if err := recorder.Close(shutdownCtx); err != nil {
				logger.Warn("the routing log did not flush cleanly", "error", err.Error())
			}
		}()
	}
	jevClient, _ := settingsStore.Current().Runtime.(*jev.Client)
	if jevClient != nil {
		defer jevClient.CloseIdleConnections()
	}
	sessions := api.NewSessionStore(time.Duration(cfg.Admin.SessionTTL), nil)
	var previousJev *jev.Client
	settingsStore.Observe(func(snapshot settings.Snapshot) {
		sessions.SetTTL(time.Duration(snapshot.Config.Admin.SessionTTL))
		current, _ := snapshot.Runtime.(*jev.Client)
		if previousJev != nil && previousJev != current {
			previousJev.CloseIdleConnections()
		}
		previousJev = current
	})
	modelMetadataResolver, err := modelsdev.NewMetadataResolver(cfg.Registry.Sync.URL, time.Duration(cfg.Registry.Sync.Timeout), 15*time.Minute)
	if err != nil {
		return err
	}
	syncGate := newRegistrySyncGate()
	server := newHTTPServer(serverAssembly{
		Config: cfg, Store: store, DB: db, Checker: db, Executor: executor, Jev: jevClient,
		Recorder: recorder, Settings: settingsStore, Authenticator: authenticator, Sessions: sessions,
		Groups: &groupStore, ResolveModelMetadata: modelMetadataResolver.Resolve,
		SyncRegistry: func(syncCtx context.Context) (any, error) {
			return syncGate.Do(syncCtx, func(syncCtx context.Context) (any, error) {
				current := settingsStore.Config()
				if len(current.Registry.Sync.Include) == 0 {
					return nil, api.ErrSyncAllowlistEmpty
				}
				if err := syncRegistry(syncCtx, current, db, logger); err != nil {
					return nil, err
				}
				if err := reloadCatalog(syncCtx, db, logger, store); err != nil {
					return nil, err
				}
				state, found, err := storage.LoadSyncState(syncCtx, db, models.SourceModelsDev)
				if err != nil {
					return nil, err
				}
				if !found {
					return map[string]any{"synchronized": false}, nil
				}
				return map[string]any{"synchronized": true, "url": state.URL, "fetched_at": state.FetchedAt, "imported_pairs": state.ImportedPairs, "skipped_pairs": state.SkippedPairs, "warnings": state.Warnings}, nil
			})
		},
	}, logger)
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	return serve(ctx, server, listener, time.Duration(cfg.HTTP.ShutdownTimeout), logger)
}

// providerRouter selects the direct executor for the provider chosen by the
// catalog/router. It does not execute requests itself; the registry lookup and
// the executor call are deliberately one small boundary.
type providerRouter struct {
	registry *direct.Registry
}

var _ providers.Executor = (*providerRouter)(nil)

func (r *providerRouter) Do(ctx context.Context, request *providers.Request) (*providers.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("%w: request is nil", providers.ErrProviderNotConfigured)
	}
	if r == nil || r.registry == nil {
		return nil, fmt.Errorf("%w: no direct provider registry", providers.ErrProviderNotConfigured)
	}
	executor, ok := r.registry.Get(request.ProviderKey)
	if !ok {
		return nil, fmt.Errorf("%w: provider %q has no configured executor", providers.ErrProviderNotConfigured, request.ProviderKey)
	}
	return executor.Do(ctx, request)
}

// newRoutingLogWriter assembles the durable routing log. A disabled section
// returns a nil recorder rather than a no-op writer: the forwarding path then takes
// exactly the stage-7 code path, with no observer, no goroutine and no queue.
//
// A writer is returned even when the database is unusable: recording a request is
// never a reason to refuse to serve one, and the writer reports its own failures.
func newRoutingLogWriter(cfg config.Config, db *sql.DB, settingsStore *settings.Store, logger *slog.Logger) (*logging.Writer, error) {
	if !cfg.Routing.Log.Enabled {
		if mounted, _ := cfg.AdminMounted(); !mounted {
			logger.Info("the durable routing log is disabled; routing decisions are recorded in the process log only")
			return nil, nil
		}
		// The one documented exception to "a disabled log builds nothing": the Admin
		// API has to be able to turn recording on without a restart, so a writer
		// exists, drops every event at the door and installs no usage observer.
		logger.Info("the durable routing log is disabled, but the Admin API is mounted so it can be enabled without a restart")
	}
	store := storage.NewRoutingLog(db)
	store.Logger = logger
	writer := logging.New(store, logging.Config{
		ClientIP:         cfg.Routing.Log.StoreClientIP,
		RetentionDays:    cfg.Routing.Log.RetentionDays,
		JevRetentionDays: cfg.Routing.Log.JevTrace.RetentionDays,
	}, logger)
	// The configured state is applied immediately after construction, so the process
	// starts in exactly the state the configuration describes; the switches exist so
	// that state can change afterwards. An observer registration reports the current
	// snapshot as well, which is why the configured state is applied exactly once.
	if settingsStore != nil {
		settingsStore.Observe(func(snapshot settings.Snapshot) {
			applyRoutingLogSettings(writer, snapshot.Config, logger)
		})
	} else {
		applyRoutingLogSettings(writer, cfg, logger)
	}
	return writer, nil
}

// applyRoutingLogSettings mirrors the routing log settings onto the writer's atomic
// switches. It runs at startup and after every accepted settings change.
func applyRoutingLogSettings(writer *logging.Writer, cfg config.Config, logger *slog.Logger) {
	if writer == nil {
		return
	}
	writer.SetEnabled(cfg.Routing.Log.Enabled)
	writer.SetStoreClientIP(cfg.Routing.Log.StoreClientIP)
	writer.SetJevTraces(cfg.Routing.Log.JevTrace.Enabled)
	writer.SetRetention(cfg.Routing.Log.RetentionDays, cfg.Routing.Log.JevTrace.RetentionDays)
	if logger == nil {
		return
	}
	logger.Info("durable routing log state",
		"enabled", writer.Enabled(),
		"store_client_ip", cfg.Routing.Log.StoreClientIP,
		"retention_days", cfg.Routing.Log.RetentionDays,
		"jev_trace", cfg.Routing.Log.JevTrace.Enabled,
		"jev_retention_days", cfg.Routing.Log.JevTrace.RetentionDays,
	)
}

// reloadCatalog rebuilds the registry snapshot from the database and publishes it.
// It is the one reload every registry write shares: the Admin API calls it after a
// row change, so the next request and the next /v1/models listing see the change.
func reloadCatalog(ctx context.Context, db *sql.DB, logger *slog.Logger, store *models.Store) error {
	catalog, err := storage.LoadCatalog(ctx, db)
	if err != nil {
		// A failed reload leaves the previous snapshot in place: an unreadable
		// registry is a reason to keep serving what is already validated, not a reason
		// to publish an empty one.
		return err
	}
	published := store.Swap(catalog)
	globalExecutorRegistry.Build(published)
	logCatalogSnapshot(logger, published, true)
	return nil
}

// flagsSet records which flags the operator actually wrote, so a flag whose
// zero value is legitimate can still be refused when it is given alone.
func flagsSet(flags *flag.FlagSet) map[string]bool {
	set := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

// newAutoRouter assembles the automatic routing stack. It always returns a
// router: a missing engine is a state the router itself reports as 503
// routing_unavailable, which keeps the failure visible on the request path
// instead of turning into a nil dereference during startup.
//
// A disabled Jev client is passed through as nil, which the router reports as
// jev_status=disabled: "Jev is off" and "Jev failed" are different facts, and a
// routing log has to be able to tell them apart.
func newAutoRouter(cfg config.Config, store *models.Store, engine *policy.Engine, jevClient *jev.Client, settingsStore *settings.Store, groups *atomic.Pointer[storage.ModelGroups], logger *slog.Logger) *auto.Router {
	var caller auto.JevCaller
	if jevClient != nil {
		caller = jevClient
	}
	options := auto.Options{
		Catalog:           store,
		Analyzer:          analyzer.New(),
		Jev:               caller,
		Engine:            engine,
		DefaultPreference: cfg.Routing.DefaultPreference,
		InputMode:         cfg.Jev.DomainInputMode(),
		FailoverEnabled:   cfg.Routing.Auto.Failover.Enabled,
		GroupConfig:       groupConfigFromStorage(groups),
	}
	if settingsStore != nil {
		// The live source replaces every automatic-routing value that can change,
		// including the compiled policy engine, so an accepted settings change applies
		// to the next decision rather than to the next restart. The engine is read per
		// decision from the store, which is why no callback has to reach into the router.
		options.Engine = settingsStore.Current().Engine
		options.Settings = settingsValues{store: settingsStore, groups: groups}
	}
	router := auto.New(options)
	if options.Engine == nil {
		logger.Error("automatic routing is unavailable: the routing policy could not be compiled")
	}
	return router
}

// settingsValues adapts the settings store to the live-value contracts the request
// path declares. It is the only place that knows both sides, which is why no
// request-path package depends on internal/settings.
type settingsValues struct {
	store  *settings.Store
	groups *atomic.Pointer[storage.ModelGroups]
}

func groupConfigFromStorage(groups *atomic.Pointer[storage.ModelGroups]) auto.GroupConfig {
	if groups == nil || groups.Load() == nil {
		return auto.GroupConfig{}
	}
	stored := groups.Load()
	convert := func(items []storage.ModelGroupMember) []auto.GroupMember {
		out := make([]auto.GroupMember, 0, len(items))
		for _, item := range items {
			out = append(out, auto.GroupMember{ProviderKey: item.Provider, ModelID: item.Model})
		}
		return out
	}
	return auto.GroupConfig{Simple: convert(stored.Simple), Medium: convert(stored.Medium), Complex: convert(stored.Complex)}
}

func autoRuntimeSnapshot(snapshot *settings.Snapshot, groups *atomic.Pointer[storage.ModelGroups]) auto.RuntimeSettings {
	var client auto.JevCaller
	if prepared, ok := snapshot.Runtime.(*jev.Client); ok && prepared != nil {
		client = prepared
	}
	return auto.RuntimeSettings{Engine: snapshot.Engine, JevEnabled: snapshot.Config.Jev.Enabled, Jev: client, InputMode: snapshot.Config.Jev.DomainInputMode(), FailoverEnabled: snapshot.Config.Routing.Auto.Failover.Enabled, DefaultPreference: snapshot.Config.Routing.DefaultPreference, GroupConfig: groupConfigFromStorage(groups)}
}
func (v settingsValues) RuntimeSnapshot() auto.RuntimeSettings {
	return autoRuntimeSnapshot(v.store.Current(), v.groups)
}

// liveProxyValues adapts the settings store to the forwarding boundary's own live
// contract.
type liveProxyValues struct {
	store  *settings.Store
	groups *atomic.Pointer[storage.ModelGroups]
}

func (v liveProxyValues) Snapshot() proxy.LiveSnapshot {
	if v.store == nil {
		return proxy.LiveSnapshot{DefaultPreference: analyzer.PreferenceBalanced, RoutingLogEnabled: true}
	}
	snapshot := v.store.Current()
	cfg := snapshot.Config
	return proxy.LiveSnapshot{AllowProviderOverride: cfg.Routing.AllowProviderOverride, DefaultPreference: cfg.Routing.DefaultPreference, RoutingLogEnabled: cfg.Routing.Log.Enabled, StoreClientIP: cfg.Routing.Log.StoreClientIP, JevTraceEnabled: cfg.Routing.Log.JevTrace.Enabled, AutoSettings: autoRuntimeSnapshot(snapshot, v.groups), HasAutoSettings: true}
}

// liveDebugValues adapts the settings store to the two debug endpoints' live
// contract: they read the default preference and the compiled policy engine.
type liveDebugValues struct{ store *settings.Store }

func (v liveDebugValues) DefaultPreference() analyzer.Preference {
	if v.store == nil {
		return analyzer.PreferenceBalanced
	}
	return v.store.Config().Routing.DefaultPreference
}

func (v liveDebugValues) Engine() *policy.Engine {
	if v.store == nil {
		return nil
	}
	return v.store.Current().Engine
}

func (v liveDebugValues) AnalyzerEnabled() bool {
	return v.store != nil && v.store.Config().Routing.AnalyzerDebugEndpoint
}
func (v liveDebugValues) PolicyEnabled() bool {
	return v.store != nil && v.store.Config().Routing.PolicyDebugEndpoint
}

// serverAssembly is everything the listener is built from. It is a struct rather
// than a parameter list because the request path now has four optional seams (the
// executor, the Jev client, the routing log writer and the settings store) and a
// positional call with four optional arguments is a call nobody can read.
type serverAssembly struct {
	Config               config.Config
	Store                *models.Store
	DB                   *sql.DB
	Checker              api.ReadinessChecker
	Executor             providers.Executor
	Jev                  *jev.Client
	Recorder             *logging.Writer
	Settings             *settings.Store
	Authenticator        *api.Authenticator
	Sessions             *api.SessionStore
	SyncRegistry         func(context.Context) (any, error)
	ResolveModelMetadata func(context.Context, string, string) (modelsdev.MetadataMatch, bool, error)
	Groups               *atomic.Pointer[storage.ModelGroups]
}

// newHTTPServer builds the HTTP handler. The debug endpoints are mounted only when
// their fixed/runtime settings enable them and remain loopback-gated. After
// setup, the authenticated Admin API is always mounted.
func newHTTPServer(assembly serverAssembly, logger *slog.Logger) *http.Server {
	cfg := assembly.Config
	store := assembly.Store
	db := assembly.DB
	checker := assembly.Checker
	if checker == nil {
		checker = db
	}
	executor := assembly.Executor
	jevClient := assembly.Jev
	recorder := assembly.Recorder
	settingsStore := assembly.Settings
	debugHandler := gateDebugHandler(api.NewDebugAnalyzer(api.DebugOptions{
		Analyzer: analyzer.New(), DefaultPreference: cfg.Routing.DefaultPreference,
		MaxRequestBytes: cfg.HTTP.MaxRequestBytes, Live: liveDebugValues{store: settingsStore},
	}), settingsStore, true)
	engine, err := cfg.PolicyEngine()
	if err != nil {
		// A configuration that reached this point has already been validated, so a
		// compile failure here means the two validators disagree: a programming
		// error rather than an operator mistake. It is reported loudly and the
		// automatic path stays unassembled (503) instead of routing on a policy
		// nobody compiled.
		logger.Error("the routing policy could not be compiled", "error", err)
		engine = nil
	}
	routeDebugHandler := gateDebugHandler(api.NewDebugRoute(api.RouteDebugOptions{
		Analyzer: analyzer.New(), DefaultPreference: cfg.Routing.DefaultPreference,
		MaxRequestBytes: cfg.HTTP.MaxRequestBytes, Live: liveDebugValues{store: settingsStore},
	}), settingsStore, false)
	authenticator := assembly.Authenticator
	if authenticator == nil {
		authenticator, _ = api.NewDatabaseAuthenticator(nil, logger)
	}
	handler := proxy.New(proxy.Options{
		Catalog:               store,
		Executor:              executor,
		MaxRequestBytes:       cfg.HTTP.MaxRequestBytes,
		Logger:                logger,
		AllowProviderOverride: cfg.Routing.AllowProviderOverride,
		// Provider readiness is decided by providerRouter and classified as
		// provider_not_configured per request.
		ProviderConfigured: true,
		DefaultPreference:  cfg.Routing.DefaultPreference,
		AutoRouter:         newAutoRouter(cfg, store, engine, jevClient, settingsStore, assembly.Groups, logger),
		Recorder:           recorder,
		Live:               liveProxyValues{store: settingsStore, groups: assembly.Groups},
		OverrideAuthorizer: api.OverrideAuthorizer(authenticator),
	})
	var adminHandler http.Handler
	// sessionStore is created only when the management surface is mounted, and it is
	// shared with the HTTP middleware so a cookie the login endpoint issues is
	// accepted by the same process that issued it.
	var sessionStore *api.SessionStore
	if mounted, reason := cfg.AdminMounted(); mounted {
		sessions := assembly.Sessions
		if sessions == nil {
			sessions = api.NewSessionStore(time.Duration(cfg.Admin.SessionTTL), nil)
		}
		adminHandler = api.NewAdmin(api.AdminOptions{
			DB:            db,
			Catalog:       store,
			Settings:      settingsStore,
			Readiness:     checker,
			Recorder:      recorder,
			PageSize:      cfg.Admin.PageSize,
			MaxPageSize:   cfg.Admin.MaxPageSize,
			Authenticator: authenticator,
			Sessions:      sessions,
			StartedAt:     processStart,
			Logger:        logger,
			ReloadCatalog: func(ctx context.Context) error { return reloadCatalog(ctx, db, logger, store) },
			ReloadGroups: func(_ context.Context, groups storage.ModelGroups) error {
				if assembly.Groups != nil {
					assembly.Groups.Store(&groups)
				}
				return nil
			},
			ReloadCredentials: func(ctx context.Context) error {
				credentials, err := storage.LoadInboundCredentials(ctx, db)
				if err != nil {
					_ = authenticator.ReplaceDatabaseCredentials(nil)
					return err
				}
				if err := authenticator.ReplaceDatabaseCredentials(toAPICredentials(credentials)); err != nil {
					_ = authenticator.ReplaceDatabaseCredentials(nil)
					return err
				}
				return nil
			},
			SyncRegistry:         assembly.SyncRegistry,
			ResolveModelMetadata: assembly.ResolveModelMetadata,
		})
		sessionStore = sessions
		logger.Info("the Admin API is mounted",
			"base_path", "/admin/v1",
			"authentication", "required",
			"dashboard", "/admin/",
			"session_ttl", time.Duration(cfg.Admin.SessionTTL).String(),
		)
	} else if reason != "" {
		logger.Warn("the Admin API is not mounted", "reason", reason)
	}
	return &http.Server{
		Addr: cfg.HTTP.Address,
		Handler: api.NewHandler(api.Options{
			Readiness:        checker,
			ReadinessTimeout: time.Duration(cfg.HTTP.ReadinessTimeout),
			Catalog:          store,
			Proxy:            handler,
			Debug:            debugHandler,
			RouteDebug:       routeDebugHandler,
			Admin:            adminHandler,
			Auth:             authenticator,
			Sessions:         sessionStore,
		}),
		ReadHeaderTimeout: time.Duration(cfg.HTTP.ReadHeaderTimeout),
		IdleTimeout:       time.Duration(cfg.HTTP.IdleTimeout),
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		// Deliberately no global ReadTimeout/WriteTimeout: model streaming
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
	logger.Info("service started", "address", listener.Addr().String(), "version", buildVersion)

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
