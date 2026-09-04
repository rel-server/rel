package wellknown

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/errcode"
	"github.com/ceymard/rel/logging"
	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/query"
	goccyyaml "github.com/goccy/go-yaml"
	"github.com/samber/oops"
)

var log = logging.For("wellknown")

// candidate is one file's parsed-and-resolved definition, before the
// cross-file collision check runs.
type candidate struct {
	def  query.RawWellKnownDefinition
	root *query.QueryNode
	path string
}

// BuildRegistry loads every well-known query file under
// pg.query.wellknown_path (## Configuration : a colon-separated directory
// list, consumed exactly like http.static.path — split at the point of
// use, a missing directory silently skipped, no cap on recursion depth or
// file count) and returns a *Registry of every one that parsed, resolved,
// and validated cleanly. A per-file or per-definition failure logs a
// warning and excludes just that definition (## Behaviour : "a warning is
// logged and the query is deactivated") — nothing here is fatal, since a
// broken well-known file is the deploying developer's own authoring
// mistake, not a reason to refuse to boot at all.
func BuildRegistry(db *pg.DbInfos, cfg *config.Config) (*Registry, error) {
	var dirs []string
	for _, d := range config.SplitPathList(cfg.Pg.Query.WellKnownDirs) {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			dirs = append(dirs, d)
		}
	}

	byName := map[string][]candidate{}
	rctx := &query.ResolveContext{Db: db, Config: cfg}

	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return fmt.Errorf("walking %q: %w", path, err)
			}
			if d.IsDir() || strings.HasPrefix(d.Name(), "_") {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(d.Name()))
			if ext != ".json" && ext != ".yml" && ext != ".yaml" {
				return nil
			}
			loadFile(rctx, path, ext, byName)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("wellknown: %w", err)
		}
	}

	reg := &Registry{byName: map[string]*Compiled{}}
	for name, cands := range byName {
		if len(cands) > 1 {
			paths := make([]string, len(cands))
			for i, c := range cands {
				paths[i] = c.path
			}
			log.Warn("duplicate well-known query name, every entry deactivated", "name", name, "files", paths)
			continue
		}
		c := cands[0]
		compiled, err := compile(c.def, c.root)
		if err != nil {
			log.Warn("well-known query failed validation, deactivated", "name", name, "path", c.path, "error", err.Error())
			continue
		}
		reg.byName[name] = compiled
	}

	return reg, nil
}

// loadFile reads, YAML-normalizes if needed, parses, and resolves every
// definition in one file, appending each success onto byName.
func loadFile(rctx *query.ResolveContext, path, ext string, byName map[string][]candidate) {
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Warn("reading well-known file failed, skipped", "path", path, "error", err.Error())
		return
	}

	jsonBytes := raw
	if ext == ".yml" || ext == ".yaml" {
		jsonBytes, err = goccyyaml.YAMLToJSON(raw)
		if err != nil {
			log.Warn("invalid YAML, file skipped", "path", path, "error", err.Error())
			return
		}
	}

	defs, err := query.ParseWellKnownFile(jsonBytes)
	if err != nil {
		log.Warn("invalid well-known definition, file skipped", "path", path, "error", err.Error())
		return
	}

	for _, def := range defs {
		root, err := rctx.ResolveQuery(def.Query)
		if err == nil {
			err = rctx.ResolveExpressions(root)
		}
		if err == nil {
			err = rctx.DeriveShapes(root)
		}
		if err != nil {
			log.Warn("well-known query failed to resolve, deactivated", "name", def.Name, "path", path, "error", err.Error())
			continue
		}
		byName[def.Name] = append(byName[def.Name], candidate{def: def, root: root, path: path})
	}
}

// compile finalizes one resolved definition : compiles its read statement,
// cross-checks every $param against declared Params both ways.
func compile(def query.RawWellKnownDefinition, root *query.QueryNode) (*Compiled, error) {
	params := make(map[string]ParamDef, len(def.Params))
	for name, p := range def.Params {
		if p.HasDefault {
			if err := checkParamType(p.Type, p.Default); err != nil {
				return nil, oops.With("param", name).Code(errcode.WellKnownParamTypeMismatch).Errorf("declared default: %w", err)
			}
		}
		params[name] = ParamDef{Type: p.Type, HasDefault: p.HasDefault, Default: p.Default}
	}

	sw, err := query.CompileSelect(root)
	if err != nil {
		return nil, oops.Code(errcode.Internal).Errorf("compiling read statement: %w", err)
	}

	referenced := sw.ParamNames()
	used := make(map[string]bool, len(referenced))
	for _, name := range referenced {
		used[name] = true
		if _, ok := params[name]; !ok {
			return nil, oops.With("param", name).Code(errcode.WellKnownUnknownParam).Errorf("query references undeclared param %q", name)
		}
	}
	for name := range params {
		if !used[name] {
			return nil, oops.With("param", name).Code(errcode.WellKnownUnusedParam).Errorf("param %q is declared but never used", name)
		}
	}

	return &Compiled{Name: def.Name, Params: params, Root: root, Read: sw, QueryRaw: def.QueryRaw}, nil
}
