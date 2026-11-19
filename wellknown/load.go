package wellknown

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	goccyyaml "github.com/goccy/go-yaml"
	"github.com/huml-lang/go-huml"
	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/query"
	"github.com/samber/oops"
)

var log = logging.For("wellknown")

// candidate is one parsed and resolved definition, before the cross-file name collision check.
type candidate struct {
	def  query.RawWellKnownDefinition
	root *query.QueryNode
	path string
}

// BuildRegistry loads every well-known query file under pg.query.wellknown_path (colon-separated, a missing
// directory skipped). A failing file or definition is logged and left out ; nothing here is fatal.
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
				return oops.With("path", path).Wrapf(err, "walking %q", path)
			}
			if d.IsDir() || strings.HasPrefix(d.Name(), "_") {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(d.Name()))
			if ext != ".json" && ext != ".yml" && ext != ".yaml" && ext != ".huml" {
				return nil
			}
			loadFile(rctx, path, ext, byName)
			return nil
		})
		if err != nil {
			return nil, oops.Wrapf(err, "wellknown")
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

// loadFile parses (YAML/HUML or JSON) and resolves every definition in one file, appending each success to byName.
func loadFile(rctx *query.ResolveContext, path, ext string, byName map[string][]candidate) {
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Warn("reading well-known file failed, skipped", "path", path, "error", err.Error())
		return
	}

	jsonBytes := raw
	switch ext {
	case ".yml", ".yaml":
		jsonBytes, err = goccyyaml.YAMLToJSON(raw)
		if err != nil {
			log.Warn("invalid YAML, file skipped", "path", path, "error", err.Error())
			return
		}
	case ".huml":
		var v any
		if err = huml.Unmarshal(raw, &v); err != nil {
			log.Warn("invalid HUML, file skipped", "path", path, "error", err.Error())
			return
		}
		if jsonBytes, err = json.Marshal(v); err != nil {
			log.Warn("invalid HUML, file skipped", "path", path, "error", err.Error())
			return
		}
	}

	defs, err := query.ParseWellKnownFile(jsonBytes)
	if err != nil {
		log.Warn("invalid well-known definition, file skipped", "path", path, "error", err.Error())
		return
	}

	for _, def := range defs {
		root, err := rctx.Resolve(def.Query)
		if err != nil {
			log.Warn("well-known query failed to resolve, deactivated", "name", def.Name, "path", path, "error", err.Error())
			continue
		}
		byName[def.Name] = append(byName[def.Name], candidate{def: def, root: root, path: path})
	}
}

// compile compiles one resolved definition's read statement and checks its $params against Params both ways.
func compile(def query.RawWellKnownDefinition, root *query.QueryNode) (*Compiled, error) {
	params := make(map[string]ParamDef, len(def.Params))
	for name, p := range def.Params {
		if query.IsCBORTypeName(p.Type) && p.HasDefault && jsonKindOf(p.Default) != query.KindNull {
			return nil, oops.With("param", name).Code(errcode.WellKnownParamTypeMismatch).Errorf("a cbor param's default can only be null : a declaration has no CBOR bytes")
		}
		if p.HasDefault {
			if err := checkParamType(p.Type, jsonKindOf(p.Default)); err != nil {
				return nil, oops.With("param", name).Code(errcode.WellKnownParamTypeMismatch).Errorf("declared default: %w", err)
			}
		}
		params[name] = ParamDef{Type: p.Type, HasDefault: p.HasDefault, Default: p.Default}
	}

	sw, err := query.CompileSelect(root)
	if err != nil {
		return nil, oops.Code(errcode.Internal).Errorf("compiling read statement: %w", err)
	}

	swCBOR, err := query.CompileSelectFormat(root, query.FormatCBOR)
	if err != nil {
		return nil, oops.Code(errcode.Internal).Errorf("compiling CBOR read statement: %w", err)
	}

	referenced := sw.ParamNames()
	used := make(map[string]bool, len(referenced))
	for _, name := range referenced {
		used[name] = true
		if _, ok := params[name]; !ok {
			return nil, oops.With("param", name).Code(errcode.WellKnownUnknownParam).Errorf("query references undeclared param %q", name)
		}
	}
	for name, p := range params {
		// a cbor param is bound as bytes, which only a cbor cast reads, and only it is
		bytes, other := sw.ParamUses(name)
		if cbor := query.IsCBORTypeName(p.Type); (cbor && other > 0) || (!cbor && bytes > 0) {
			return nil, oops.With("param", name).Code(errcode.WellKnownParamTypeMismatch).Errorf("param %q : a param declared cbor is used with a cbor cast, and only it", name)
		}
		if !used[name] {
			return nil, oops.With("param", name).Code(errcode.WellKnownUnusedParam).Errorf("param %q is declared but never used", name)
		}
	}

	return &Compiled{Name: def.Name, Params: params, Root: root, Read: sw, ReadCBOR: swCBOR, QueryRaw: def.QueryRaw}, nil
}
