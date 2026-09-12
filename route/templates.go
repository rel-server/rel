// This file implements specs/http-content.md ## Templates : RelHttpResponse.
// template/template_data rendering via Jet, and specs/templating-2.md's
// extension of that same jet.Set to static-served .jet files and rel().
package route

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"

	"github.com/bytedance/sonic"

	jet "github.com/CloudyKit/jet/v6"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/websec"
	"github.com/rel-server/rel/wellknown"
)

// TemplateSet wraps a *jet.Set — nil is a valid, meaningful value (no
// http.templates.path configured/found and no static directory either, or
// config.Test()'s zero-value path), in which case a route response setting
// "template" is a 500, logged, same as a template that fails to load.
// db/wkReg back rel() (rel_func.go) — nil-safe, since a TemplateSet built
// without them (e.g. sso's own, or a test fixture) simply can't run rel().
type TemplateSet struct {
	set   *jet.Set
	db    *pg.DbInfos
	wkReg *wellknown.Registry
}

// templateRoot is one real directory a multiRootLoader serves. A
// static-sourced root is namespaced under its own "/__staticN/" virtual
// prefix so it can never collide with a http.templates.path-rooted name in
// the shared jet.Set's own template cache (specs/templating-2.md) ; the
// templates.path root itself keeps its existing, unprefixed addressing so
// every absolute/relative {{include}}/{{extends}} already written against
// it keeps resolving exactly as before.
type templateRoot struct {
	prefix string // "" for the unprefixed templates.path root
	dir    string
}

// multiRootLoader implements jet.Loader across the templates.path root and
// every static search-list directory, refusing anything that resolves
// under uploadDir regardless of which root or resolution path found it
// (specs/templating-2.md's upload/jet exclusion, applied once here rather
// than at every call site).
type multiRootLoader struct {
	roots     []templateRoot
	uploadDir string // absolute path ; "" disables the exclusion check
}

// resolve turns a jet-computed virtual path into a real, existing file
// under one of the loader's roots, or ok=false. Bare (unprefixed) requests
// try the templates.path root only ; a "/__staticN/..." request tries only
// that one static root, by construction never falling back to another —
// each static directory's own template addressing stays exactly the search
// order static.Server.Resolve already picked.
func (l *multiRootLoader) resolve(templatePath string) (string, bool) {
	trimmed := strings.TrimPrefix(templatePath, "/")
	for _, root := range l.roots {
		rel, matches := trimmed, root.prefix == ""
		if root.prefix != "" {
			rel, matches = strings.CutPrefix(trimmed, root.prefix)
		}
		if !matches {
			continue
		}
		full, ok := resolveUnderDir(root.dir, rel)
		if !ok {
			continue
		}
		if l.uploadDir != "" && (full == l.uploadDir || strings.HasPrefix(full, l.uploadDir+string(os.PathSeparator))) {
			return "", false
		}
		if fi, err := os.Stat(full); err != nil || fi.IsDir() {
			continue
		}
		return full, true
	}
	return "", false
}

func (l *multiRootLoader) Exists(templatePath string) bool {
	_, ok := l.resolve(templatePath)
	return ok
}

func (l *multiRootLoader) Open(templatePath string) (io.ReadCloser, error) {
	full, ok := l.resolve(templatePath)
	if !ok {
		return nil, fmt.Errorf("template not found: %s", templatePath)
	}
	return os.Open(full)
}

// staticTemplateName is the virtual, "/__staticN/"-prefixed name a static
// .jet candidate found under staticDirs[dirIndex] is looked up under.
func staticTemplateName(dirIndex int, relPath string) string {
	return fmt.Sprintf("/__static%d/%s", dirIndex, relPath)
}

// NewTemplateSet builds a TemplateSet loading from cfg.Http.Templates.Path
// and, when non-empty, every entry of staticDirs (static.Server.Dirs) — the
// "shared jet loader/Set" specs/templating-2.md requires between database-
// function-return templates and static .jet rendering. uploadDir is the
// resolved absolute path jet execution is excluded from (empty disables the
// exclusion). Returns nil when there is nowhere at all to load from.
// jet.InDevelopmentMode() is deliberately not used — SIGUSR1 reload is the
// one mechanism for picking up a changed .jet file (static-sourced ones
// included), so every render is cached until the next reload,
// unconditionally, per specs/http-content.md ## Templates' own "Reload"
// paragraph.
func NewTemplateSet(cfg *config.Config, staticDirs []string, db *pg.DbInfos, wkReg *wellknown.Registry) *TemplateSet {
	// Prefixed static roots are tried before the bare templates.path root
	// (never the other way around) — the bare root's prefix-less match
	// would otherwise shadow a real static file if templates.path happened
	// to contain its own "__staticN" subdirectory.
	var roots []templateRoot
	for i, d := range staticDirs {
		roots = append(roots, templateRoot{prefix: fmt.Sprintf("__static%d/", i), dir: d})
	}
	if cfg.Http.Templates.Path != "" {
		roots = append(roots, templateRoot{dir: cfg.Http.Templates.Path})
	}
	if len(roots) == 0 {
		return nil
	}

	var uploadDir string
	if cfg.Http.Upload.Dir != "" && len(staticDirs) > 0 {
		if resolved, ok := resolveUnderDir(staticDirs[0], cfg.Http.Upload.Dir); ok {
			uploadDir = resolved
		}
	}

	set := jet.NewSet(&multiRootLoader{roots: roots, uploadDir: uploadDir})
	ts := &TemplateSet{set: set, db: db, wkReg: wkReg}
	set.AddGlobalFunc("rel", ts.relJetFunc)
	return ts
}

// writeTemplateResponse implements ## Templates' steps 1-4 ; a load or
// execution failure is a logged 500, never a silent fallback. templateName/
// dataRaw/contentType are read out of whichever shape produced them — the
// old single-jsonb-envelope's template/template_data/content_type (sso's
// callback, WriteRelHttpResponse's own non-full-control case), or a
// full-control route's envelope template/content_type paired with its
// second OUT column as Data (specs/new-routes.md ## Templates : "the other
// return type is then used as the Data").
func writeTemplateResponse(w http.ResponseWriter, r *http.Request, cfg *config.Config, templates *TemplateSet, templateName string, dataRaw json.RawMessage, contentType string, status int) {
	rlog := logging.FromContext(r.Context()).With("module", "route")
	if templates == nil || templates.set == nil {
		rlog.Error("route: no jet.Set available (http.templates.path unset and no static directory either)", "template", templateName)
		writePlainError(w, http.StatusInternalServerError, errcode.TemplateError, "template rendering unavailable")
		return
	}

	tmpl, err := templates.set.GetTemplate(templateName)
	if err != nil {
		rlog.Error("route: loading template", "template", templateName, "error", err.Error())
		writePlainError(w, http.StatusInternalServerError, errcode.TemplateError, "loading template")
		return
	}

	vars := make(jet.VarMap)
	vars.Set("Data", templateDataValue(dataRaw))
	vars.Set("Req", decodeRequestForTemplate(r))
	vars.Set("Nonce", websec.NonceFromContext(r.Context()))
	templates.bindRelContext(vars, r, cfg)

	// Buffered first, not streamed to w : a runtime error must still be a
	// clean 500, never a partially-written body.
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars, nil); err != nil {
		rlog.Error("route: executing template", "template", templateName, "error", err.Error())
		writePlainError(w, http.StatusInternalServerError, errcode.TemplateError, "executing template")
		return
	}

	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// bindRelContext stashes this request's rel() context into vars, resolving
// claims the same way GateMiddleware does (inline jwtpkg.VerifyRequest, not
// through jwtpkg.Middleware) — a no-op (rel() then reports unavailable) when
// this TemplateSet has no db/wkReg, e.g. sso's own template set.
func (ts *TemplateSet) bindRelContext(vars jet.VarMap, r *http.Request, cfg *config.Config) {
	if ts == nil || ts.db == nil {
		return
	}
	claims, verified := jwtpkg.VerifyRequest(cfg.Jwt, r)
	vars.Set(relCtxVarName, &relRuntimeCtx{
		ctx: r.Context(), db: ts.db, cfg: cfg, wkReg: ts.wkReg,
		claims: claims, verified: verified,
	})
}

// relCtxVarName is the hidden per-execution variable relJetFunc resolves
// off the current jet.Runtime — never meant to be read from a template
// directly.
const relCtxVarName = "__relctx"

// relRuntimeCtx is one render's worth of context rel() needs, built once
// per request/render (bindRelContext) rather than per call within it.
type relRuntimeCtx struct {
	ctx      context.Context
	db       *pg.DbInfos
	cfg      *config.Config
	wkReg    *wellknown.Registry
	claims   jwtpkg.Claims
	verified bool
}

// relRuntimeCtxFrom type-asserts rc (a *relRuntimeCtx resolved off the
// current jet.Runtime) back to its concrete type — false when rel() is
// called outside any render that bound one (relCtxVarName never set).
func relRuntimeCtxFrom(rc reflect.Value) (*relRuntimeCtx, bool) {
	if !rc.IsValid() || !rc.CanInterface() {
		return nil, false
	}
	v, ok := rc.Interface().(*relRuntimeCtx)
	if !ok || v == nil {
		return nil, false
	}
	return v, true
}

// relJetFunc is rel()'s jet.Func body, registered once as a Set-global
// (NewTemplateSet) so it's reachable from both static and database-
// function-return templates alike — the per-request pieces (role, wkReg's
// registry, the request's own context) come from relCtxVarName, resolved
// off the currently executing jet.Runtime, not from a closure over any one
// request.
func (ts *TemplateSet) relJetFunc(a jet.Arguments) reflect.Value {
	a.RequireNumOfArguments("rel", 1, 1)
	rcVal := a.Runtime().Resolve(relCtxVarName)
	rc, ok := relRuntimeCtxFrom(rcVal)
	if !ok {
		a.Panicf("rel(): not available in this context")
	}
	queryJSON, err := sonic.Marshal(a.Get(0).Interface())
	if err != nil {
		a.Panicf("rel(): encoding query: %s", err.Error())
	}
	raw, err := executeRelRead(rc.ctx, rc.db, rc.cfg, rc.wkReg, rc.claims, rc.verified, queryJSON)
	if err != nil {
		a.Panicf("rel(): %s", err.Error())
	}
	return reflect.ValueOf(jsonRawToAny(raw))
}

// templateDataValue decodes resp.template_data into a plain any (nil for
// JSON null), matching Jet's own dot-access expectations.
func templateDataValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	_ = sonic.Unmarshal(raw, &v)
	return v
}

// templateDataFromSingleReturn wraps a single-return route's raw Postgres
// bytes into the json.RawMessage templateDataValue expects. A jsonb/json
// return (contentType "application/json") is already valid JSON and passes
// through unchanged ; every other non-binary shape classifySingleReturnType
// produces — text/plain, or a mimetype domain over a text underlying, e.g.
// "text/csv" — is raw text (e.g. hello, not "hello"), which
// templateDataValue would otherwise silently fail to unmarshal into nil.
// classifyShape already rejects template+binary, so binary never reaches
// here.
func templateDataFromSingleReturn(raw []byte, contentType string) json.RawMessage {
	if contentType == "application/json" {
		return json.RawMessage(raw)
	}
	encoded, err := sonic.Marshal(string(raw))
	if err != nil {
		return nil
	}
	return json.RawMessage(encoded)
}

// decodeRequestForTemplate decodes the exact request JSON the route
// function received (stashed by handleRoute) rather than re-deriving it.
func decodeRequestForTemplate(r *http.Request) any {
	raw, _ := requestJSONFromContext(r.Context())
	if len(raw) == 0 {
		return nil
	}
	var v any
	_ = sonic.Unmarshal(raw, &v)
	return v
}
