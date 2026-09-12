// This file implements specs/templating-2.md's static-serving half : the
// root-level static fallback mount (boot.BuildMux) and masking's
// static_file (route/encode.go) both need the same Resolve-then-maybe-
// render behavior, so it lives here once rather than twice.
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
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/static"
	"github.com/rel-server/rel/websec"
)

// NewStaticHandler is the root-level static-fallback mount — Resolve picks
// a plain file (served exactly as static.Server.Handler already would) or a
// .jet candidate, rendered here. Replaces staticSrv.Handler(db, cfg) at
// boot.BuildMux's mux.NotFound, which has no rendering capability of its
// own (static/static.go's own package doc comment explains why that stays
// true there).
func NewStaticHandler(db *pg.DbInfos, cfg *config.Config, staticSrv *static.Server, templates *TemplateSet) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := strings.TrimPrefix(r.URL.Path, "/")
		serveResolvedStatic(w, r, cfg, staticSrv, templates, upath)
	})
}

// serveStaticFileOrJet is masking's static_file (specs/new-routes.md
// ## Static path masking) — the jet-aware twin of static.Server.ServeFile,
// used instead of it wherever rendering matters (route/encode.go). Returns
// false when relPath resolves to nothing at all, leaving 404 rendering to
// the caller, exactly like static.Server.ServeFile's own contract.
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

// serveResolvedStatic is NewStaticHandler's body, factored out so it can be
// unit-driven without going through a full http.Handler wrap.
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

// renderStaticJet renders jc — specs/templating-2.md's jet-in-static
// rendering : nonce/CSP/JWT verification run only here, on the jet-
// execution branch, never mounted unconditionally on /static (see
// websec.NonceMiddleware's own doc comment for why /rel and /static don't
// get one today ; this keeps that true for the common plain-file case,
// adding it back only where a render actually happens). An excluded
// candidate (falls under http.upload.dir) 404s, never renders, regardless
// of how it was reached (top-level request or {{include}}, enforced once
// in the shared jet.Set's own loader — see templates.go's multiRootLoader).
func renderStaticJet(w http.ResponseWriter, r *http.Request, cfg *config.Config, templates *TemplateSet, jc *static.JetCandidate) {
	rlog := logging.FromContext(r.Context()).With("module", "route")
	if jc.Excluded {
		http.NotFound(w, r)
		return
	}
	if templates == nil || templates.set == nil {
		rlog.Error("route: static .jet candidate but no jet.Set available", "path", jc.AbsPath)
		writePlainError(w, http.StatusInternalServerError, errcode.TemplateError, "template rendering unavailable")
		return
	}

	name := staticTemplateName(jc.DirIndex, jc.RelPath)
	tmpl, err := templates.set.GetTemplate(name)
	if err != nil {
		rlog.Error("route: loading static template", "template", name, "error", err.Error())
		writePlainError(w, http.StatusInternalServerError, errcode.TemplateError, "loading template")
		return
	}

	claims, verified := jwtpkg.VerifyRequest(cfg.Jwt, r)
	nonce, nerr := websec.NewNonce()
	if nerr != nil {
		rlog.Error("route: generating nonce", "error", nerr.Error())
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "internal error")
		return
	}
	ctx := websec.WithNonce(r.Context(), nonce)
	r = r.WithContext(ctx)

	// request.static isn't meaningful for a request that IS the static
	// render itself, unlike /route or /rel where it describes a DIFFERENT,
	// possibly-masked path.
	reqPayload, rerr := buildRelHttpRequest(r, json.RawMessage("null"), verified, claims, nil, nil, nil, nil, "")
	if rerr != nil {
		rlog.Error("route: building Req", "error", rerr.Error())
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "internal error")
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
		writePlainError(w, http.StatusInternalServerError, errcode.TemplateError, "executing template")
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
