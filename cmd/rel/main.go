// Command rel is the process entrypoint : loads configuration
// (specs/configuration.md), builds the process-wide logger
// (specs/logging.md ## Configuration/## Logger construction), connects
// to Postgres, and serves POST /rel (server.NewRelHandler) and
// /route/{schema}/{function} (route.NewHandler) until an interrupt/terminate
// signal requests a graceful shutdown.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ceymard/rel/boot"
	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/dmut"
	"github.com/ceymard/rel/logging"
	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/route"
	"github.com/ceymard/rel/wellknown"
)

func main() {
	if wantsHelp(os.Args[1:]) {
		fmt.Print(config.Help())
		return
	}

	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		slog.Default().Error("loading configuration", "error", err.Error())
		os.Exit(1)
	}

	logger, err := logging.Install(cfg.Logging)
	if err != nil {
		// logging.Install failed, so the real logger doesn't exist — fall
		// back to slog.Default(), same rule config.Load's own error paths
		// use before the real logger is built.
		slog.Default().Error("building logger", "error", err.Error())
		os.Exit(1)
	}

	// Introspection and dmut migrations always use cfg.Pg's own primary
	// login (or pg.uri) ; the pool that actually serves requests (and
	// whose connections get their role SET per-request) uses pg.query.*
	// instead, WHEN SET — see config.PgQuery's own doc comment for why
	// that's optional, not required. See pg.NewInfosAdminQuery's own doc
	// comment for why these stay two distinct connections rather than one
	// shared pool whenever they do differ.
	primaryURI, queryURI, err := resolveConnectionURIs(cfg.Pg)
	if err != nil {
		logger.Error("resolving postgres connection", "error", err.Error())
		os.Exit(1)
	}

	// specs/migrations.md ## Execution : dmut runs BEFORE introspection,
	// always — rel's introspected schema cache must reflect whatever dmut
	// leaves the database in, never whatever it looked like before. A
	// failed run is logged and startup continues regardless (dmut's own
	// transaction discipline means a failed run is fully rolled back, so
	// there's nothing half-migrated for introspection to see).
	if _, err := dmut.Run(context.Background(), primaryURI, cfg.Dmut, logger.With("module", "dmut")); err != nil {
		logger.Error("dmut run failed, continuing with the schema as it was before this attempt", "error", err.Error())
	}

	db, err := pg.NewInfosAdminQuery(primaryURI, queryURI, cfg.Pg.PoolSize, cfg.Pg.Query.AnonymousRole)
	if err != nil {
		logger.Error("connecting to postgres", "target", redactedTarget(primaryURI), "error", err.Error())
		os.Exit(1)
	}
	logger.Info("connected to postgres", "target", redactedTarget(primaryURI))
	if cfg.Pg.Query.AnonymousRole != "" && !db.AnonymousRoleExists {
		logger.Warn(fmt.Sprintf("configured anonymous role %q does not exist — all anonymous requests will be denied", cfg.Pg.Query.AnonymousRole))
	}

	routeRegistry, err := route.BuildRegistry(db, cfg)
	if err != nil {
		logger.Error("building /route registry", "error", err.Error())
		os.Exit(1)
	}
	wellKnownRegistry, err := wellknown.BuildRegistry(db, cfg)
	if err != nil {
		logger.Error("building well-known query registry", "error", err.Error())
		os.Exit(1)
	}

	// boot.BuildMux is the one shared assembler of the full inner handler
	// (/rel, /route/, /wellknown, /static/, uniformly wrapped in CORS/CSP
	// middleware) — boot/reload.go's Reload calls the exact same function
	// on every SIGUSR1, so the two call sites can't drift on what the mux
	// contains.
	mux, err := boot.BuildMux(db, cfg, routeRegistry, wellKnownRegistry, logger.With("module", "boot"))
	if err != nil {
		logger.Error("building mux", "error", err.Error())
		os.Exit(1)
	}

	// specs/migrations.md ## Reloading : http.Server.Handler is, permanently,
	// this small reload-aware wrapper — written to srv.Handler exactly
	// once, below, and never touched again. Only the wrapper's own
	// atomic.Pointer is ever swapped, by the reload sequence.
	wrapper := boot.NewReloadableHandler(mux)
	reloader := boot.NewReloader(wrapper, db, primaryURI, cfg, logger.With("module", "boot"))

	addr := fmt.Sprintf("%s:%d", cfg.Http.Host, cfg.Http.Port)
	srv := &http.Server{
		Addr:     addr,
		Handler:  wrapper,
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// SIGUSR1 : a persistent, repeatable reload trigger — unlike the
	// one-shot signal.NotifyContext above for SIGINT/SIGTERM, this keeps
	// listening for as long as the process runs (signal.Notify, not
	// NotifyContext). A buffered channel of size 1 coalesces a burst of
	// signals arriving faster than reloads can run into a single pending
	// reload, and the for-range loop's own sequential processing means
	// overlapping reloads never run concurrently with each other.
	sigusr1 := make(chan os.Signal, 1)
	signal.Notify(sigusr1, syscall.SIGUSR1)
	reloadDone := make(chan struct{})
	go func() {
		defer close(reloadDone)
		for range sigusr1 {
			logger.Info("received SIGUSR1, reloading dmut mutations")
			reloader.Reload(context.Background())
		}
	}()
	// stopReloader stops new SIGUSR1s from being delivered and waits for
	// the reload goroutine to actually exit — meaning any Reload() already
	// in progress has returned — before the caller closes the pool below.
	// Without this, a reload racing shutdown could still be mid-Reload
	// (route.BuildRegistry querying reloader.CurrentDbInfos().Pool directly)
	// at the exact moment Pool.Close() runs, on the SAME pool : spurious
	// reload-failure logging at best, Pool.Close() blocking on a checked-
	// out connection past this function's own control at worst.
	stopReloader := func() {
		signal.Stop(sigusr1)
		close(sigusr1)
		<-reloadDone
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", addr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("serving", "error", err.Error())
			stopReloader()
			reloader.CurrentDbInfos().Pool.Close()
			os.Exit(1)
		}
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown", "error", err.Error())
		}
	}

	stopReloader()

	// reloader.CurrentDbInfos, not the original db : a reload since startup
	// may have reintrospected into a fresh *pg.DbInfos (same underlying
	// Pool either way — pg.ReIntrospect reuses it — but this is the
	// correct owner to ask, not the possibly-stale local variable from
	// before any reload ever happened).
	reloader.CurrentDbInfos().Pool.Close()
	logger.Info("stopped")
}

// wantsHelp scans for a bare "--help"/"-h" token — checked BEFORE
// config.Load, since parseFlags treats every other "--xxx" as a dotted
// config key expecting a value ("--help" with nothing after it would
// otherwise fail as "flag --help has no value", not print anything useful).
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return false
}
