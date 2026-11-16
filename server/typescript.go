// GET /rel/database.ts and /rel/database.json.
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

// NewTypeScriptHandler serves GET /rel/database.ts. It does not check http.typescript.enable : the caller only
// mounts it when set.
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

// NewDatabaseJSONHandler serves GET /rel/database.json, with the same schema filtering as /rel/database.ts. It
// does not check http.typescript.enable either.
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
