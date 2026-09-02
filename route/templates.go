// This file implements specs/http-content.md ## Templates :
// RelHttpResponse.template/template_data rendering via Jet. The Jet *Set
// is rebuilt as part of the same SIGUSR1 reload sequence that rebuilds
// everything else derived from config/schema (## Templates' own
// "Reload" — falls out of boot.BuildMux being called fresh on every
// reload, same as the rest of this package's dependencies).
package route

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/bytedance/sonic"

	jet "github.com/CloudyKit/jet/v6"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/errcode"
	"github.com/ceymard/rel/logging"
	"github.com/ceymard/rel/websec"
)

// TemplateSet wraps a *jet.Set — nil is a valid, meaningful value (no
// http.templates.path configured/found, or config.Test()'s zero-value
// path), in which case a route response setting "template" is a 500,
// logged, same as a template that fails to load.
type TemplateSet struct {
	set *jet.Set
}

// NewTemplateSet builds a TemplateSet from cfg.Http.Templates.Path — no
// existence check on the directory itself : jet.NewOSFileSystemLoader
// doesn't require the directory to exist up front (a route simply never
// sets "template" if none is configured), matching ## Templates' own
// silence on a missing-directory case (unlike ## Static files' explicit
// "missing directories silently skipped" rule, templates has no multi-
// directory search list to skip entries from — a single directory, always
// present as *jet.Set once configured).
//
// jet.InDevelopmentMode() is deliberately NOT used, per ## Templates'
// own explicit "Reload" paragraph : reload (SIGUSR1) is already the one
// mechanism for picking up a changed .jet file without a full restart, so
// a second, independent one (per-render disk reads / no caching) would
// just be two ways to do the same thing. This means every render is
// cached until the next reload, unconditionally — not gated on
// logging.level=debug (an earlier draft's step 1 wording implied a
// debug-mode bypass, which contradicts "InDevelopmentMode() is NOT used"
// categorically ; the Reload paragraph is the more specific, rationale-
// backed rule and is what's implemented here — flagged in this session's
// own report as the judgment call it is).
func NewTemplateSet(path string) *TemplateSet {
	if path == "" {
		return nil
	}
	loader := jet.NewOSFileSystemLoader(path)
	return &TemplateSet{set: jet.NewSet(loader)}
}

// writeTemplateResponse implements ## Templates' numbered steps 1-4 :
// load (and cache) the named template, execute with the three named
// VarMap variables (Data/Req/Nonce), the rendered output becomes the
// response body. A load or runtime execution failure is a 500, logged,
// never a silent fallback to resp.content.
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

	// Rendered into a buffer FIRST, not straight to w : step 4's "a runtime
	// error during execution is also a 500... never a partially-written
	// body" requires knowing whether execution succeeded before any status
	// line/body byte is written — Jet has no lower-level "start writing,
	// bail cleanly" primitive, so buffering the (typically modest — HTML
	// pages, not multi-GB media) output is what makes that guarantee true.
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

// templateDataValue decodes resp.template_data (json.RawMessage, "null"
// when unset) into a plain any — nil for JSON null, otherwise whatever
// encoding/json's default decode produces (map[string]any / []any /
// scalar), matching Jet's own dot-access expectations for a Go value.
func templateDataValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	_ = sonic.Unmarshal(raw, &v)
	return v
}

// decodeRequestForTemplate re-decodes the exact RelHttpRequest JSON the
// route function itself received into a plain Go map/slice tree, per
// ## Templates step 2's "Req — the exact same RelHttpRequest JSON value
// the route function itself received." r.relHttpRequestJSON is stashed by
// handleRoute right after buildRelHttpRequest, specifically so this doesn't
// need to independently re-derive it (which would risk drifting from what
// was actually sent, e.g. a renewed JWT changing req.jwt after the fact).
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
