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
// execution failure is a logged 500, never a silent fallback. templateName/
// dataRaw/contentType are read out of whichever shape produced them — the
// old single-jsonb-envelope's template/template_data/content_type (sso's
// callback, WriteRelHttpResponse's own non-full-control case), or a
// full-control route's envelope template/content_type paired with its
// second OUT column as Data (specs/new-routes.md ## Templates : "the other
// return type is then used as the Data").
func writeTemplateResponse(w http.ResponseWriter, r *http.Request, templates *TemplateSet, templateName string, dataRaw json.RawMessage, contentType string, status int) {
	rlog := logging.FromContext(r.Context()).With("module", "route")
	if templates == nil || templates.set == nil {
		rlog.Error("route: template set but no http.templates.path configured/found", "template", templateName)
		writePlainError(w, http.StatusInternalServerError, errcode.TemplateError, "template rendering unavailable (no http.templates.path configured)")
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

// templatesForConfig is a small convenience used by boot.BuildMux — kept
// here (not in boot) since TemplateSet is this package's own type.
func templatesForConfig(cfg *config.Config) *TemplateSet {
	return NewTemplateSet(cfg.Http.Templates.Path)
}
