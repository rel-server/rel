// Jet rendering for static serving, shared by the static fallback and a response's static_file.
package route

import (
	"bytes"
	"encoding/json"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/bytedance/sonic"

	jet "github.com/CloudyKit/jet/v6"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/httperr"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/static"
	"github.com/rel-server/rel/websec"
)

// NewStaticHandler is the static fallback : serves a plain file, or renders a .jet candidate.
func NewStaticHandler(db *pg.DbInfos, cfg *config.Config, staticSrv *static.Server, templates *TemplateSet) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := strings.TrimPrefix(r.URL.Path, "/")
		serveResolvedStatic(w, r, cfg, staticSrv, templates, upath)
	})
}

// serveStaticFileOrJet serves a response's static_file, rendering .jet. Returns false when relPath resolves to
// nothing, leaving the 404 to the caller.
func serveStaticFileOrJet(w http.ResponseWriter, r *http.Request, cfg *config.Config, staticSrv *static.Server, templates *TemplateSet, relPath string) bool {
	if staticSrv == nil {
		return false
	}
	servable, jc, ok := staticSrv.Resolve(relPath)
	if !ok {
		return false
	}
	if jc == nil {
		http.ServeFile(w, r, servable)
		return true
	}
	renderStaticJet(w, r, cfg, templates, jc)
	return true
}

// serveResolvedStatic is NewStaticHandler's body.
func serveResolvedStatic(w http.ResponseWriter, r *http.Request, cfg *config.Config, staticSrv *static.Server, templates *TemplateSet, upath string) {
	if staticSrv.NeedsTrailingSlash(upath) {
		to := path.Base(r.URL.Path) + "/"
		if q := r.URL.RawQuery; q != "" {
			to += "?" + q
		}
		http.Redirect(w, r, to, http.StatusMovedPermanently)
		return
	}
	servable, jc, ok := staticSrv.Resolve(upath)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if jc == nil {
		http.ServeFile(w, r, servable)
		return
	}
	renderStaticJet(w, r, cfg, templates, jc)
}

// renderStaticJet renders jc. Nonce, CSP and JWT verification happen only here, not for plain static files.
// A candidate under http.upload.dir 404s.
func renderStaticJet(w http.ResponseWriter, r *http.Request, cfg *config.Config, templates *TemplateSet, jc *static.JetCandidate) {
	rlog := logging.FromContext(r.Context()).With("module", "route")
	if jc.Excluded {
		http.NotFound(w, r)
		return
	}
	if templates == nil || templates.set == nil {
		rlog.Error("route: static .jet candidate but no jet.Set available", "path", jc.AbsPath)
		httperr.WritePlain(w, http.StatusInternalServerError, errcode.TemplateError, "template rendering unavailable")
		return
	}

	name := staticTemplateName(jc.DirIndex, jc.RelPath)
	tmpl, err := templates.set.GetTemplate(name)
	if err != nil {
		rlog.Error("route: loading static template", "template", name, "error", err.Error())
		httperr.WritePlain(w, http.StatusInternalServerError, errcode.TemplateError, "loading template")
		return
	}

	claims, verified := jwtpkg.VerifyRequest(cfg.Jwt, r)
	nonce, nerr := websec.NewNonce()
	if nerr != nil {
		rlog.Error("route: generating nonce", "error", nerr.Error())
		httperr.WritePlain(w, http.StatusInternalServerError, errcode.Internal, "internal error")
		return
	}
	ctx := websec.WithNonce(r.Context(), nonce)
	r = r.WithContext(ctx)

	// No request.static : this request is the static file itself.
	reqPayload, rerr := buildRelHttpRequest(r, json.RawMessage("null"), verified, claims, nil, nil, nil, nil, "")
	if rerr != nil {
		rlog.Error("route: building Req", "error", rerr.Error())
		httperr.WritePlain(w, http.StatusInternalServerError, errcode.Internal, "internal error")
		return
	}
	var reqAny any
	_ = sonic.Unmarshal(reqPayload, &reqAny)

	vars := make(jet.VarMap)
	vars.Set("Req", reqAny)
	vars.Set("Nonce", nonce)
	templates.bindRelContext(vars, r, cfg)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars, nil); err != nil {
		rlog.Error("route: executing static template", "template", name, "error", err.Error())
		httperr.WritePlain(w, http.StatusInternalServerError, errcode.TemplateError, "executing template")
		return
	}

	contentType := mime.TypeByExtension("." + jc.Ext)
	if contentType == "" {
		contentType = "text/plain"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", websec.Policy(cfg.Http.Csp, "", nonce))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}
