// Jet rendering of a response's template, shared with static .jet files and rel().
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
	"github.com/samber/oops"

	jet "github.com/CloudyKit/jet/v6"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/httperr"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/reqauth"
	"github.com/rel-server/rel/websec"
	"github.com/rel-server/rel/wellknown"
)

// TemplateSet wraps a *jet.Set. A nil TemplateSet (nowhere to load templates from) makes any template response a
// logged 500. Without db/wkReg, rel() is unavailable.
type TemplateSet struct {
	set   *jet.Set
	db    *pg.DbInfos
	wkReg *wellknown.Registry
}

// templateRoot is one directory a multiRootLoader serves. A static root is under its own "/__staticN/" prefix so
// its names never collide with templates.path ones in the shared cache ; templates.path stays unprefixed.
type templateRoot struct {
	prefix string // "" for the unprefixed templates.path root
	dir    string
}

// multiRootLoader loads from the templates.path root and every static directory, refusing anything under uploadDir.
type multiRootLoader struct {
	roots     []templateRoot
	uploadDir string // absolute path ; "" disables the exclusion check
}

// resolve turns a template path into an existing file : an unprefixed path tries only templates.path, a
// "/__staticN/" one only that static root, never falling back to another.
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
		return nil, oops.With("template", templatePath).Errorf("template not found: %s", templatePath)
	}
	return os.Open(full)
}

// staticTemplateName is the template name of a static .jet file found under staticDirs[dirIndex].
func staticTemplateName(dirIndex int, relPath string) string {
	return fmt.Sprintf("/__static%d/%s", dirIndex, relPath)
}

// NewTemplateSet builds a TemplateSet loading from cfg.Http.Templates.Path and every staticDirs entry ; nil when
// there is nowhere to load from. Templates are cached until the next reload, even in dev.
func NewTemplateSet(cfg *config.Config, staticDirs []string, db *pg.DbInfos, wkReg *wellknown.Registry) *TemplateSet {
	// Static roots go first, or a "__staticN" subdirectory of templates.path would shadow them.
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

// writeTemplateResponse renders templateName with dataRaw as Data ; a load or execution failure is a logged 500.
func writeTemplateResponse(w http.ResponseWriter, r *http.Request, cfg *config.Config, templates *TemplateSet, templateName string, dataRaw json.RawMessage, contentType string, status int) {
	rlog := logging.FromContext(r.Context()).With("module", "route")
	if templates == nil || templates.set == nil {
		rlog.Error("route: no jet.Set available (http.templates.path unset and no static directory either)", "template", templateName)
		httperr.WritePlain(w, http.StatusInternalServerError, errcode.TemplateError, "template rendering unavailable")
		return
	}

	tmpl, err := templates.set.GetTemplate(templateName)
	if err != nil {
		rlog.Error("route: loading template", "template", templateName, "error", err.Error())
		httperr.WritePlain(w, http.StatusInternalServerError, errcode.TemplateError, "loading template")
		return
	}

	vars := make(jet.VarMap)
	vars.Set("Data", templateDataValue(dataRaw))
	vars.Set("Req", decodeRequestForTemplate(r))
	vars.Set("Nonce", websec.NonceFromContext(r.Context()))
	templates.bindRelContext(vars, r, cfg)

	// Buffered, so a runtime error is still a clean 500.
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars, nil); err != nil {
		rlog.Error("route: executing template", "template", templateName, "error", err.Error())
		httperr.WritePlain(w, http.StatusInternalServerError, errcode.TemplateError, "executing template")
		return
	}

	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// bindRelContext stashes this request's rel() context into vars ; the session is only verified, rel() has no
// anonymous check of its own. No-op without db.
func (ts *TemplateSet) bindRelContext(vars jet.VarMap, r *http.Request, cfg *config.Config) {
	if ts == nil || ts.db == nil {
		return
	}
	claims, verified := jwtpkg.VerifyRequest(cfg.Jwt, r)
	vars.Set(relCtxVarName, &relRuntimeCtx{
		ctx: r.Context(), db: ts.db, cfg: cfg, wkReg: ts.wkReg,
		sess: reqauth.NewSession(cfg, claims, verified),
	})
}

// relCtxVarName is the hidden per-render variable holding rel()'s context.
const relCtxVarName = "__relctx"

// relRuntimeCtx is the context rel() needs, built once per render.
type relRuntimeCtx struct {
	ctx   context.Context
	db    *pg.DbInfos
	cfg   *config.Config
	wkReg *wellknown.Registry
	sess  *reqauth.Session
}

// relRuntimeCtxFrom is false when the render bound no rel() context.
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

// relJetFunc is rel(), a global of the set ; the per-request context comes from relCtxVarName.
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
	raw, err := executeRelRead(rc.ctx, rc.db, rc.cfg, rc.wkReg, rc.sess, queryJSON)
	if err != nil {
		a.Panicf("rel(): %s", err.Error())
	}
	return reflect.ValueOf(jsonRawToAny(raw))
}

// templateDataValue decodes template data into a plain any (nil for JSON null).
func templateDataValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	_ = sonic.Unmarshal(raw, &v)
	return v
}

// templateDataFromSingleReturn turns a single-return route's value into JSON : json/jsonb passes through, any text
// return is encoded as a JSON string.
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

// decodeRequestForTemplate decodes the exact request JSON the route function received.
func decodeRequestForTemplate(r *http.Request) any {
	raw, _ := requestJSONFromContext(r.Context())
	if len(raw) == 0 {
		return nil
	}
	var v any
	_ = sonic.Unmarshal(raw, &v)
	return v
}
