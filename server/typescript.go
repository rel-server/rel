// GET /rel/database.ts — docs/content/typescript-client.md ## Fetching it / specs/typescript.md ## database.ts :
// the introspected, schema-whitelisted database exported as a single,
// self-sufficient TypeScript file (github.com/rel-server/rel/tsgen does the
// actual generation ; this file is only the HTTP wiring).
package server

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/httperr"
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
			writeError(r.Context(), w, httperr.MethodNotAllowed(fmt.Errorf("/rel/database.ts only accepts GET")), cfg.Dev)
			return
		}

		opts, herr := typeScriptOptions(cfg, r, true)
		if herr != nil {
			writeError(r.Context(), w, herr, cfg.Dev)
			return
		}

		out := tsgen.GenerateDatabaseTS(db, opts, wkReg)

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
			writeError(r.Context(), w, httperr.MethodNotAllowed(fmt.Errorf("/rel/database.json only accepts GET")), cfg.Dev)
			return
		}

		opts, herr := typeScriptOptions(cfg, r, false)
		if herr != nil {
			writeError(r.Context(), w, herr, cfg.Dev)
			return
		}

		out, err := tsgen.GenerateDatabaseJSON(db, opts, wkReg)
		if err != nil {
			writeError(r.Context(), w, httperr.Server(errcode.Internal, err), cfg.Dev)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(out)
	})
}

// typeScriptOptions is tsgen.Resolve for a request's `schemas` and `codec` params : a bad param is a
// 400, anything else (a bad configured value) a 500.
func typeScriptOptions(cfg *config.Config, r *http.Request, withCodec bool) (tsgen.Options, *httperr.Error) {
	q := r.URL.Query()
	ov := tsgen.Overrides{Schemas: q.Get("schemas")}
	if withCodec {
		ov.Codec = q.Get("codec")
	}
	opts, err := tsgen.Resolve(cfg, ov)
	if err == nil {
		return opts, nil
	}
	if _, ok := errors.AsType[*tsgen.BadOverrideError](err); ok {
		return opts, httperr.BadRequest(errcode.Unclassified, err)
	}
	return opts, httperr.Server(errcode.Internal, err)
}
