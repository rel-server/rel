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

	uri := postgresURI(cfg.Pg)
	db, err := pg.NewInfos(uri)
	if err != nil {
		logger.Error("connecting to postgres", "host", cfg.Pg.Host, "port", cfg.Pg.Port, "database", cfg.Pg.Database, "error", err.Error())
		os.Exit(1)
	}
	logger.Info("connected to postgres", "host", cfg.Pg.Host, "port", cfg.Pg.Port, "database", cfg.Pg.Database)

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
