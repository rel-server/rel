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
		// The real logger doesn't exist yet ; fall back to slog.Default(),
		// same as config.Load's own error paths above.
		slog.Default().Error("building logger", "error", err.Error())
		os.Exit(1)
	}

	// Introspection/dmut use cfg.Pg's own primary login ; the serving pool
	// uses pg.query.* instead, when set — see config.PgQuery's own doc comment.
	primaryURI, queryURI, err := resolveConnectionURIs(cfg.Pg)
	if err != nil {
		logger.Error("resolving postgres connection", "error", err.Error())
		os.Exit(1)
	}

	// dmut runs BEFORE introspection, always (specs/migrations.md ##
	// Execution) ; a failed run is logged, startup continues regardless.
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

	// boot/reload.go's Reload calls this same function on every SIGUSR1,
	// so the two call sites can't drift on what the mux contains.
	mux, err := boot.BuildMux(db, cfg, routeRegistry, wellKnownRegistry, logger.With("module", "boot"))
	if err != nil {
		logger.Error("building mux", "error", err.Error())
		os.Exit(1)
	}

	// http.Server.Handler is this wrapper, permanently ; only its own
	// atomic.Pointer is swapped, by the reload sequence (specs/migrations.md ## Reloading).
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

	// Unlike NotifyContext above, SIGUSR1 keeps listening for the process
	// lifetime ; the for-range loop keeps reloads from overlapping.
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
	// Waits for any in-progress Reload() to return before the pool closes
	// below — otherwise shutdown could race a reload using the same pool.
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
	// may have reintrospected into a fresh *pg.DbInfos.
	reloader.CurrentDbInfos().Pool.Close()
	logger.Info("stopped")
}

// wantsHelp scans for a bare "--help"/"-h" token, checked before
// config.Load since parseFlags otherwise expects a value after every flag.
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return false
}
