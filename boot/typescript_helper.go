package boot

import (
	"log/slog"
	"os"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/tsgen"
)

// WriteTypeScriptHelperFile is specs/typescript.md ## Reloading
// `helper_path` : a no-op when typescript.helper_path is unset. Called at
// startup (cmd/rel/main.go) and from Reload right after step 4
// (reintrospection) — using the SAME freshly reintrospected db step 5's
// registry rebuild goes on to consume — never on a dmut/reintrospection
// failure, since that step is never reached then (the file is left
// untouched, matching the spec's own "left untouched" wording for the
// schema/registry themselves).
func WriteTypeScriptHelperFile(db *pg.DbInfos, cfg *config.Config, logger *slog.Logger) {
	if cfg.TypeScript.HelperPath == "" {
		return
	}
	out := tsgen.GenerateDatabaseTS(db, tsgen.Options{
		Schemas:   tsgen.ParseSchemaList(cfg.Http.TypeScript.Schemas),
		Blacklist: cfg.Blacklist,
	})
	if err := os.WriteFile(cfg.TypeScript.HelperPath, []byte(out), 0o644); err != nil {
		logger.Error("writing typescript.helper_path", "path", cfg.TypeScript.HelperPath, "error", err.Error())
	}
}
