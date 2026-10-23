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

	// A bad typescript.codec is a startup error, not a 400 on every request.
	if _, err := tsgen.ParseCodec(cfg.TypeScript.Codec); err != nil {
		logger.Error("invalid typescript.codec", "error", err.Error())
		os.Exit(1)
	}

	// --typescript-out is a one-shot action, not the server : introspects
	// and writes database.ts, then exits — no reload.cmd, no route/
	// well-known registries, no mux, no listener. See runTypeScriptExport's
	// own doc comment for why reload.cmd is skipped here even though the
	// server always runs it before introspecting.
	if out, ok := typescriptOutFlag(os.Args[1:]); ok {
		codec, _ := oneShotFlag(os.Args[1:], "--typescript-codec")
		if err := runTypeScriptExport(cfg, out, codec); err != nil {
			logger.Error("typescript export failed", "error", err.Error())
			os.Exit(1)
		}
		return
	}

	// http.Server.Handler is reloader.Wrapper, permanently ; only its own
	// atomic.Pointer is swapped, by the reload sequence (specs/migrations.md ## Reloading).
	reloader, err := boot.Start(context.Background(), cfg, logger)
	if err != nil {
		logger.Error("starting", logging.Error(err)...)
		os.Exit(1)
	}

	addr := fmt.Sprintf("%s:%d", cfg.Http.Host, cfg.Http.Port)
	srv := &http.Server{
		Addr:     addr,
		Handler:  reloader.Wrapper,
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
	return oneShotFlag(args, "--typescript-out")
}

// oneShotFlag returns the value of the --name=value / --name value flag, and whether it is present.
func oneShotFlag(args []string, name string) (value string, ok bool) {
	for i, a := range args {
		if strings.HasPrefix(a, name+"=") {
			return a[len(name)+1:], true
		}
		if a == name {
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
// own startup sequence (docs/content/configuration/reload.md) : running an arbitrary migration
// command as a side effect of "print me the current types" would be a
// surprising thing for a read-only inspection command to do. The
// well-known registry, unlike reload.cmd/route, does its own read-only
// file I/O + validation against db — no side effects, so building it here
// doesn't carry the same objection.
func runTypeScriptExport(cfg *config.Config, out, codecOverride string) error {
	primaryURI := cfg.Pg.URI

	if err := pg.WaitReady(context.Background(), primaryURI, time.Duration(cfg.Pg.ConnectTimeout)*time.Second); err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
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

	// codecOverride is --typescript-codec : it replaces typescript.codec for this export.
	codec, err := tsgen.ResolveCodec(cfg.TypeScript.Codec, codecOverride)
	if err != nil {
		return err
	}

	generated := tsgen.GenerateDatabaseTS(db, tsgen.Options{
		Schemas:   tsgen.ParseSchemaList(cfg.TypeScript.Schemas),
		Blacklist: cfg.Blacklist,
		Codec:     codec,
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
