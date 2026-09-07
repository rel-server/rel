// GET /rel/database.ts — docs/content/typescript-client.md ## Fetching it / specs/typescript.md ## database.ts :
// the introspected, schema-whitelisted database exported as a single,
// self-sufficient TypeScript file (github.com/rel-server/rel/tsgen does the
// actual generation ; this file is only the HTTP wiring).
package server

import (
	"fmt"
	"net/http"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/tsgen"
	"github.com/rel-server/rel/wellknown"
)

// NewTypeScriptHandler serves GET /rel/database.ts. Mounting is conditional
// on http.typescript.enable (boot/mux.go) — this handler assumes it's
// already gated, and doesn't re-check the flag itself.
func NewTypeScriptHandler(db *pg.DbInfos, cfg *config.Config, wkReg *wellknown.Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeError(w, methodNotAllowed(fmt.Errorf("/rel/database.ts only accepts GET")), cfg.Dev)
			return
		}

		schemas, err := resolveTypeScriptSchemas(cfg.Http.TypeScript.Schemas, r.URL.Query().Get("schemas"))
		if err != nil {
			writeError(w, badRequest(errcode.Unclassified, err), cfg.Dev)
			return
		}

		out := tsgen.GenerateDatabaseTS(db, tsgen.Options{Schemas: schemas, Blacklist: cfg.Blacklist}, wkReg)

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(out))
	})
}

// NewDatabaseJSONHandler serves GET /rel/database.json — specs/database-
// json.md's own Endpoint section : the same schemas-param/whitelist/
// blacklist gating as /rel/database.ts, just a different generator. Mounting
// is conditional on http.typescript.enable too (boot/mux.go) — this handler
// assumes it's already gated, and doesn't re-check the flag itself.
func NewDatabaseJSONHandler(db *pg.DbInfos, cfg *config.Config, wkReg *wellknown.Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeError(w, methodNotAllowed(fmt.Errorf("/rel/database.json only accepts GET")), cfg.Dev)
			return
		}

		schemas, err := resolveTypeScriptSchemas(cfg.Http.TypeScript.Schemas, r.URL.Query().Get("schemas"))
		if err != nil {
			writeError(w, badRequest(errcode.Unclassified, err), cfg.Dev)
			return
		}

		out, err := tsgen.GenerateDatabaseJSON(db, tsgen.Options{Schemas: schemas, Blacklist: cfg.Blacklist}, wkReg)
		if err != nil {
			writeError(w, serverError(errcode.Internal, err), cfg.Dev)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(out)
	})
}

// resolveTypeScriptSchemas is docs/content/typescript-client.md ## Fetching it's `schemas`
// query param, intersected with http.typescript.schemas' own whitelist —
// the redactor's own answer, during this feature's implementation, to how
// the two combine : the param never widens past the configured whitelist ;
// a name outside it is a 400, not a silent drop, so a typo/misconfiguration
// surfaces instead of quietly exporting less than asked.
func resolveTypeScriptSchemas(configured, param string) ([]string, error) {
	cfgList := tsgen.ParseSchemaList(configured)
	paramList := tsgen.ParseSchemaList(param)

	if len(cfgList) == 0 {
		return paramList, nil // "" whitelist means "every schema" ; param alone decides
	}
	if len(paramList) == 0 {
		return cfgList, nil // no param : the whole configured whitelist
	}

	allowed := make(map[string]bool, len(cfgList))
	for _, s := range cfgList {
		allowed[s] = true
	}
	out := make([]string, 0, len(paramList))
	for _, s := range paramList {
		if !allowed[s] {
			return nil, fmt.Errorf("schemas=%q is not in the configured http.typescript.schemas whitelist", s)
		}
		out = append(out, s)
	}
	return out, nil
}
