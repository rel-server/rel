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

	"strings"

	"github.com/rel-server/rel/boot"
	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/reloadcmd"
	"github.com/rel-server/rel/route"
	"github.com/rel-server/rel/tsgen"
	"github.com/rel-server/rel/wellknown"
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

	// --typescript-out is a one-shot action, not the server : introspects
	// and writes database.ts, then exits — no reload.cmd, no route/
	// well-known registries, no mux, no listener. See runTypeScriptExport's
	// own doc comment for why reload.cmd is skipped here even though the
	// server always runs it before introspecting.
	if out, ok := typescriptOutFlag(os.Args[1:]); ok {
		if err := runTypeScriptExport(cfg, out); err != nil {
			logger.Error("typescript export failed", "error", err.Error())
			os.Exit(1)
		}
		return
	}

	// Introspection/reload.cmd use cfg.Pg's own primary login ; the serving pool
	// uses pg.query.* instead, when set — see config.PgQuery's own doc comment.
	primaryURI, queryURI, err := resolveConnectionURIs(cfg.Pg)
	if err != nil {
		logger.Error("resolving postgres connection", "error", err.Error())
		os.Exit(1)
	}

	// reload.cmd runs BEFORE introspection, always (specs/reload.md) ; a
	// failed run is logged, startup continues regardless — same as a
	// failure during a SIGUSR1 reload (boot/reload.go's Reload).
	if _, err := reloadcmd.Run(context.Background(), cfg, logger.With("module", "reloadcmd")); err != nil {
		logger.Error("reload.cmd failed, continuing with the schema as it was before this attempt", "error", err.Error())
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

	// specs/typescript.md ## Reloading `helper_path` : also written once at
	// startup, not only on every SIGUSR1 reload — after both registries
	// build, since Wellknowns generation needs wellKnownRegistry.
	boot.WriteTypeScriptHelperFile(db, cfg, wellKnownRegistry, logger.With("module", "boot"))

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
			logger.Info("received SIGUSR1, reloading")
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

// typescriptOutFlag scans for --typescript-out=<path>/--typescript-out
// <path>, same "=value or separate arg" shape config.parseFlags' own dotted
// flags accept — but handled here, directly, rather than as a dotted config
// key : it names a one-shot CLI action (where to write database.ts and
// exit), not a piece of the server's own runtime configuration, so it has
// no business living in config.Config or --help's Options table alongside
// pg.*/http.*/etc. path == "-" means stdout.
func typescriptOutFlag(args []string) (path string, ok bool) {
	for i, a := range args {
		if strings.HasPrefix(a, "--typescript-out=") {
			return a[len("--typescript-out="):], true
		}
		if a == "--typescript-out" {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
	}
	return "", false
}

// runTypeScriptExport is --typescript-out's entire body : introspect, build
// the well-known registry (Wellknowns generation needs it), generate,
// write, done — no reload.cmd, no /route registry, no mux, no HTTP
// listener. reload.cmd is deliberately skipped here, unlike the server's
// own startup sequence (specs/reload.md) : running an arbitrary migration
// command as a side effect of "print me the current types" would be a
// surprising thing for a read-only inspection command to do. The
// well-known registry, unlike reload.cmd/route, does its own read-only
// file I/O + validation against db — no side effects, so building it here
// doesn't carry the same objection.
func runTypeScriptExport(cfg *config.Config, out string) error {
	primaryURI, _, err := resolveConnectionURIs(cfg.Pg)
	if err != nil {
		return fmt.Errorf("resolving postgres connection: %w", err)
	}

	db, err := pg.NewInfos(primaryURI)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer db.Pool.Close()

	wkReg, err := wellknown.BuildRegistry(db, cfg)
	if err != nil {
		return fmt.Errorf("building well-known query registry: %w", err)
	}

	generated := tsgen.GenerateDatabaseTS(db, tsgen.Options{
		Schemas:   tsgen.ParseSchemaList(cfg.Http.TypeScript.Schemas),
		Blacklist: cfg.Blacklist,
	}, wkReg)

	if out == "-" {
		_, err := os.Stdout.WriteString(generated)
		return err
	}
	return os.WriteFile(out, []byte(generated), 0o644)
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
