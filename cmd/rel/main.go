// Command rel is the process entrypoint : loads configuration
// (specs/01-configuration.md), builds the process-wide logger
// (specs/01-logging.md ## Configuration/## Logger construction), connects
// to Postgres, and serves POST /rel (server.NewRelHandler) and
// /rpc/{schema}/{function} (rpc.NewHandler) until an interrupt/terminate
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

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/logging"
	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/rpc"
	"github.com/ceymard/rel/server"
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
	db, err := pg.NewInfosAdminQuery(primaryURI, queryURI, cfg.Pg.PoolSize)
	if err != nil {
		logger.Error("connecting to postgres", "target", redactedTarget(primaryURI), "error", err.Error())
		os.Exit(1)
	}
	logger.Info("connected to postgres", "target", redactedTarget(primaryURI))

	rpcRegistry, err := rpc.BuildRegistry(db, cfg)
	if err != nil {
		logger.Error("building /rpc route registry", "error", err.Error())
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/rel", server.NewRelHandler(db, cfg))
	// rpc.NewHandler's own internal mux already registers the full
	// "/rpc/{schema}/{function}" pattern and expects the unmodified request
	// path — mounting it here under "/rpc/" does no prefix-stripping (that's
	// only http.StripPrefix's job, not plain ServeMux.Handle), so this
	// composition is correct as-is.
	mux.Handle("/rpc/", rpc.NewHandler(db, cfg, rpcRegistry))

	addr := fmt.Sprintf("%s:%d", cfg.Http.Host, cfg.Http.Port)
	srv := &http.Server{
		Addr:     addr,
		Handler:  mux,
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", addr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("serving", "error", err.Error())
			db.Pool.Close()
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

	db.Pool.Close()
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
