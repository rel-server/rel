package boot

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/samber/oops"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/route"
	"github.com/rel-server/rel/tsgen"
	"github.com/rel-server/rel/wellknown"
)

// Snapshot is everything a running server serves from, built from one introspection of the
// database. Startup builds the first one ; a reload builds another and replaces the first as a
// whole, so a request never sees half of each.
type Snapshot struct {
	Db        *pg.DbInfos
	WellKnown *wellknown.Registry
	Handler   http.Handler
}

// Build builds a snapshot from db : the /route and well-known registries, then the mux over them.
// It has no effect outside the returned value (the TypeScript helper file included : see
// WriteTypeScriptHelper), so a failed build leaves whatever is being served untouched.
func Build(db *pg.DbInfos, cfg *config.Config, logger *slog.Logger) (*Snapshot, error) {
	reg, err := route.BuildRegistry(db, cfg)
	if err != nil {
		return nil, oops.Wrapf(err, "building /route registry")
	}
	wkReg, err := wellknown.BuildRegistry(db, cfg)
	if err != nil {
		return nil, oops.Wrapf(err, "building well-known query registry")
	}
	return &Snapshot{Db: db, WellKnown: wkReg, Handler: BuildMux(db, cfg, reg, wkReg, logger)}, nil
}

// WriteTypeScriptHelper is specs/typescript.md ## Reloading `helper_path` : a no-op when
// typescript.helper_path is unset. Called once the snapshot is the one being served (after startup's
// build, after a reload's swap), so a failed build leaves the file untouched, like the schema.
// A failure to write is logged, never fatal.
func (s *Snapshot) WriteTypeScriptHelper(cfg *config.Config, logger *slog.Logger) {
	if cfg.TypeScript.HelperPath == "" {
		return
	}
	opts, err := tsgen.Resolve(cfg, tsgen.Overrides{})
	if err != nil {
		logger.Error("writing typescript.helper_path", "path", cfg.TypeScript.HelperPath, "error", err.Error())
		return
	}
	out := tsgen.GenerateDatabaseTS(s.Db, opts, s.WellKnown)
	if err := os.WriteFile(cfg.TypeScript.HelperPath, []byte(out), 0o644); err != nil {
		logger.Error("writing typescript.helper_path", "path", cfg.TypeScript.HelperPath, "error", err.Error())
		return
	}
	logger.Info("wrote typescript.helper_path", "path", cfg.TypeScript.HelperPath)
}

// warnMissingAnonymousRole is the warning for a configured anonymous role the database does not have.
func warnMissingAnonymousRole(db *pg.DbInfos, cfg *config.Config, logger *slog.Logger) {
	if cfg.Pg.Query.AnonymousRole != "" && !db.AnonymousRoleExists {
		logger.Warn(fmt.Sprintf("configured anonymous role %q does not exist — all anonymous requests will be denied", cfg.Pg.Query.AnonymousRole))
	}
}
