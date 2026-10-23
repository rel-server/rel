package boot

import (
	"context"
	"log/slog"
	"net/url"
	"time"

	"github.com/samber/oops"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/reloadcmd"
)

// Start is the server's startup sequence (docs/content/configuration/reload.md ## When it runs) up
// to, but not including, listening : wait for Postgres, run the migration steps, introspect, build
// the first snapshot and wrap its handler. A failed migration step is logged and startup goes on
// with the schema as it was ; anything else is an error, and no pool is left open. The caller
// listens on Reloader.Wrapper, and closes Reloader.CurrentDbInfos().Pool at shutdown.
func Start(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*Reloader, error) {
	uri := cfg.Pg.URI
	target := redactedTarget(uri)

	// reload.cmd (often a migration tool) needs Postgres up too, so it waits first.
	if err := pg.WaitReady(ctx, uri, time.Duration(cfg.Pg.ConnectTimeout)*time.Second); err != nil {
		return nil, oops.With("target", target).Wrapf(err, "connecting to postgres")
	}

	// A failed run is logged and startup continues, as for a failure during a reload.
	if _, err := reloadcmd.Run(ctx, cfg, logger); err != nil {
		logger.Error("reload.cmd or dmut failed, continuing with the schema as it was before this attempt", reloadcmd.ErrorAttrs(err)...)
	}

	// Introspection, reload.cmd and the serving pool all use this same URI (config.PgQuery).
	db, err := pg.NewInfosAdminQuery(uri, uri, cfg.Pg.PoolSize, cfg.Pg.Query.AnonymousRole, pg.WithNoticeLevel(cfg.Pg.NoticesLevel))
	if err != nil {
		return nil, oops.With("target", target).Wrapf(err, "connecting to postgres")
	}
	logger.Info("connected to postgres", "target", target)
	warnMissingAnonymousRole(db, cfg, logger)

	bootLogger := logger.With("module", "boot")
	snap, err := Build(db, cfg, bootLogger)
	if err != nil {
		db.Pool.Close()
		return nil, err
	}
	snap.WriteTypeScriptHelper(cfg, bootLogger)

	return NewReloader(NewReloadableHandler(snap.Handler), db, uri, cfg, bootLogger), nil
}

// redactedTarget extracts "host:port/database" from a connection URI for logging, never the
// credentials ; unparsed input yields "" rather than an error.
func redactedTarget(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	return u.Host + u.Path
}
