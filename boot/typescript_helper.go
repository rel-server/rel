package boot

import (
	"log/slog"
	"os"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/tsgen"
	"github.com/rel-server/rel/wellknown"
)

// WriteTypeScriptHelperFile is specs/typescript.md ## Reloading
// `helper_path` : a no-op when typescript.helper_path is unset. Called at
// startup (cmd/rel/main.go) and from Reload right after the route/
// well-known registry rebuild — using the SAME db (freshly reintrospected,
// or the previous one when reload.cmd/reintrospection didn't succeed —
// specs/reload.md) AND the freshly rebuilt *wellknown.Registry Wellknowns
// generation needs. Never reached on a /route or well-known registry build
// failure specifically, since Reload returns before this point then (the
// file is left untouched, matching the spec's own "left untouched" wording
// for the schema/registry themselves).
func WriteTypeScriptHelperFile(db *pg.DbInfos, cfg *config.Config, wkReg *wellknown.Registry, logger *slog.Logger) {
	if cfg.TypeScript.HelperPath == "" {
		return
	}
	out := tsgen.GenerateDatabaseTS(db, tsgen.Options{
		Schemas:   tsgen.ParseSchemaList(cfg.Http.TypeScript.Schemas),
		Blacklist: cfg.Blacklist,
	}, wkReg)
	if err := os.WriteFile(cfg.TypeScript.HelperPath, []byte(out), 0o644); err != nil {
		logger.Error("writing typescript.helper_path", "path", cfg.TypeScript.HelperPath, "error", err.Error())
		return
	}
	logger.Info("wrote typescript.helper_path", "path", cfg.TypeScript.HelperPath)
}
