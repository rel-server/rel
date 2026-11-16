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

// Snapshot is everything a running server serves from ; a reload replaces it as a whole.
type Snapshot struct {
	Db        *pg.DbInfos
	WellKnown *wellknown.Registry
	Handler   http.Handler
}

// Build builds a snapshot from db. It has no side effect (not even the TypeScript helper file), so a failed build
// leaves what is served untouched.
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

// WriteTypeScriptHelper writes typescript.helper_path if set. Call it only once the snapshot is served. Write
// failures are logged, never fatal.
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

func warnMissingAnonymousRole(db *pg.DbInfos, cfg *config.Config, logger *slog.Logger) {
	if cfg.Pg.Query.AnonymousRole != "" && !db.AnonymousRoleExists {
		logger.Warn(fmt.Sprintf("configured anonymous role %q does not exist — all anonymous requests will be denied", cfg.Pg.Query.AnonymousRole))
	}
}
