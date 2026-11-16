// Command rel loads the configuration and serves until SIGINT/SIGTERM ; SIGUSR1 reloads.
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
	"github.com/samber/oops"
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
		slog.Default().Error("building logger", "error", err.Error())
		os.Exit(1)
	}

	// A bad typescript.* value is a startup error, not a failure on every request.
	if _, err := tsgen.Resolve(cfg, tsgen.Overrides{}); err != nil {
		logger.Error("invalid typescript configuration", "error", err.Error())
		os.Exit(1)
	}

	// --typescript-out writes database.ts and exits, without serving.
	if out, ok := typescriptOutFlag(os.Args[1:]); ok {
		codec, _ := oneShotFlag(os.Args[1:], "--typescript-codec")
		if err := runTypeScriptExport(cfg, out, codec); err != nil {
			logger.Error("typescript export failed", "error", err.Error())
			os.Exit(1)
		}
		return
	}

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
	// Waits for an in-progress Reload to return before the pool is closed.
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

	reloader.CurrentDbInfos().Pool.Close()
	logger.Info("stopped")
}

// typescriptOutFlag reads --typescript-out, a one-shot action kept out of config.Config. "-" means stdout.
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

// runTypeScriptExport introspects and writes database.ts. Unlike startup it never runs reload.cmd : a read-only
// export must not migrate.
func runTypeScriptExport(cfg *config.Config, out, codecOverride string) error {
	primaryURI := cfg.Pg.URI

	if err := pg.WaitReady(context.Background(), primaryURI, time.Duration(cfg.Pg.ConnectTimeout)*time.Second); err != nil {
		return oops.Wrapf(err, "connecting to postgres")
	}

	db, err := pg.NewInfos(primaryURI)
	if err != nil {
		return oops.Wrapf(err, "connecting to postgres")
	}
	defer db.Pool.Close()

	wkReg, err := wellknown.BuildRegistry(db, cfg)
	if err != nil {
		return oops.Wrapf(err, "building well-known query registry")
	}

	opts, err := tsgen.Resolve(cfg, tsgen.Overrides{Codec: codecOverride})
	if err != nil {
		return err
	}
	generated := tsgen.GenerateDatabaseTS(db, opts, wkReg)

	if out == "-" {
		_, err := os.Stdout.WriteString(generated)
		return err
	}
	return os.WriteFile(out, []byte(generated), 0o644)
}

// wantsHelp scans for "--help"/"-h", before config.Load, which expects a value after every flag.
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return false
}
