// This file implements specs/http-content.md ## Templates : RelHttpResponse.
// template/template_data rendering via Jet.
package route

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/bytedance/sonic"

	jet "github.com/CloudyKit/jet/v6"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/websec"
)

// TemplateSet wraps a *jet.Set — nil is a valid, meaningful value (no
// http.templates.path configured/found, or config.Test()'s zero-value
// path), in which case a route response setting "template" is a 500,
// logged, same as a template that fails to load.
type TemplateSet struct {
	set *jet.Set
}

// NewTemplateSet builds a TemplateSet from cfg.Http.Templates.Path. Returns
// nil for an empty path. jet.InDevelopmentMode() is deliberately not used —
// SIGUSR1 reload is the one mechanism for picking up a changed .jet file,
// so every render is cached until the next reload, unconditionally, per
// specs/http-content.md ## Templates' own "Reload" paragraph.
func NewTemplateSet(path string) *TemplateSet {
	if path == "" {
		return nil
	}
	loader := jet.NewOSFileSystemLoader(path)
	return &TemplateSet{set: jet.NewSet(loader)}
}

// writeTemplateResponse implements ## Templates' steps 1-4 ; a load or
// execution failure is a logged 500, never a silent fallback.
func writeTemplateResponse(w http.ResponseWriter, r *http.Request, templates *TemplateSet, resp relHttpResponsePayload, status int) {
	rlog := logging.FromContext(r.Context()).With("module", "route")
	if templates == nil || templates.set == nil {
		rlog.Error("route: RelHttpResponse.template set but no http.templates.path configured/found", "template", resp.Template)
		writePlainError(w, http.StatusInternalServerError, errcode.TemplateError, "template rendering unavailable (no http.templates.path configured)")
		return
	}

	tmpl, err := templates.set.GetTemplate(resp.Template)
	if err != nil {
		rlog.Error("route: loading template", "template", resp.Template, "error", err.Error())
		writePlainError(w, http.StatusInternalServerError, errcode.TemplateError, "loading template")
		return
	}

	vars := make(jet.VarMap)
	vars.Set("Data", templateDataValue(resp.TemplateData))
	vars.Set("Req", decodeRequestForTemplate(r))
	vars.Set("Nonce", websec.NonceFromContext(r.Context()))

	// Buffered first, not streamed to w : a runtime error must still be a
	// clean 500, never a partially-written body.
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars, nil); err != nil {
		rlog.Error("route: executing template", "template", resp.Template, "error", err.Error())
		writePlainError(w, http.StatusInternalServerError, errcode.TemplateError, "executing template")
		return
	}

	if resp.ContentType != "" {
		w.Header().Set("Content-Type", resp.ContentType)
	}
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
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

// templatesForConfig is a small convenience used by boot.BuildMux — kept
// here (not in boot) since TemplateSet is this package's own type.
func templatesForConfig(cfg *config.Config) *TemplateSet {
	return NewTemplateSet(cfg.Http.Templates.Path)
}
