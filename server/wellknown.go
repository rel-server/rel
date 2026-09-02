// POST/GET /wellknown : specs/well-known-queries.md ## Querying — "/rel
// with precompiled queries." Reuses the exact same infrastructure handleRel
// already built : the auth pipeline (jwt.Middleware's Verify, then
// check_session/Renew/SET LOCAL ROLE via applyRole), the manual streaming
// response shape, and the RelErrorResponse error envelope. What's
// different : there is no query tree in the request body at all, only a
// name — the tree itself (wellknown.Compiled.Root) and, for the read path,
// its already-compiled SQL text (wellknown.Compiled.Read) were both fixed
// at load time (wellknown.BuildRegistry), not parsed per request.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"errors"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"
	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/errcode"
	jwtpkg "github.com/ceymard/rel/jwt"
	"github.com/ceymard/rel/logging"
	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/query"
	"github.com/ceymard/rel/querystring"
	"github.com/ceymard/rel/wellknown"
	"github.com/ceymard/rel/writer"
)

// wellKnownRequest is one decoded {name, params?, data?} invocation — see
// specs/well-known-queries.md ## Querying's WellknownQuery type. hasData
// distinguishes "data omitted" (a read) from "data": null (a write with a
// null payload — denormalize's own null/absent handling takes it from
// there, same as a plain /rel write already does) ; a plain []byte nil
// check can't tell those apart, same reasoning as RawWellKnownParam's own
// presence-aware Default.
type wellKnownRequest struct {
	Name    string
	Params  []byte
	Data    []byte
	HasData bool
}

// NewWellKnownHandler serves POST/GET /wellknown. reg is rebuilt alongside
// db/cfg on every SIGUSR1 reload (boot/reload.go), exactly like rpc.Registry
// — see boot.BuildMux's own doc comment for why registry-building itself
// stays its caller's responsibility rather than happening here.
func NewWellKnownHandler(db *pg.DbInfos, cfg *config.Config, reg *wellknown.Registry) http.Handler {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET, POST")
			writeError(w, methodNotAllowed(fmt.Errorf("/wellknown only accepts GET or POST")), cfg.Dev)
			return
		}
		handleWellKnown(w, r, db, cfg, reg)
	})
	return jwtpkg.Middleware(cfg.Jwt)(inner)
}

func handleWellKnown(w http.ResponseWriter, r *http.Request, db *pg.DbInfos, cfg *config.Config, reg *wellknown.Registry) {
	ctx := r.Context()

	if _, verified := jwtpkg.FromContext(r.Context()); !verified && !db.AnonymousRoleExists {
		writeError(w, unauthorized(errcode.AnonymousDisabled, fmt.Errorf("%s", errcode.AnonymousDisabledMessage)), cfg.Dev)
		return
	}

	req, err := decodeWellKnownRequest(r)
	if err != nil {
		writeError(w, badRequest(errcode.MalformedBody, err), cfg.Dev)
		return
	}
	if r.Method == http.MethodGet && req.HasData {
		writeError(w, badRequest(errcode.MalformedBody, fmt.Errorf("/wellknown GET is read-only ; use POST to supply \"data\"")), cfg.Dev)
		return
	}

	wk, ok := reg.Lookup(req.Name)
	if !ok {
		writeError(w, badRequest(errcode.WellKnownUnknownQuery, fmt.Errorf("well-known query %q is not registered", req.Name)), cfg.Dev)
		return
	}

	paramValues, err := wk.ResolveParams(req.Params)
	if err != nil {
		writeError(w, badRequest(codeOrUnclassified(err), err), cfg.Dev)
		return
	}

	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		writeError(w, serverError(errcode.DBUnavailable, fmt.Errorf("acquiring connection: %w", err)), cfg.Dev)
		return
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, query.DataTableDDL); err != nil {
		writeError(w, serverError(errcode.TransactionError, fmt.Errorf("preparing _data: %w", err)), cfg.Dev)
		return
	}
	if _, err := conn.Exec(ctx, "grant all on _data to public"); err != nil {
		writeError(w, serverError(errcode.TransactionError, fmt.Errorf("granting _data: %w", err)), cfg.Dev)
		return
	}
	if _, err := conn.Exec(ctx, "truncate _data"); err != nil {
		writeError(w, serverError(errcode.TransactionError, fmt.Errorf("clearing _data: %w", err)), cfg.Dev)
		return
	}
	defer func() { _, _ = conn.Exec(context.Background(), "truncate _data") }()

	if _, err := conn.Exec(ctx, "begin"); err != nil {
		writeError(w, serverError(errcode.TransactionError, fmt.Errorf("begin: %w", err)), cfg.Dev)
		return
	}

	if err := applyRole(ctx, w, r, conn, cfg); err != nil {
		_, _ = conn.Exec(ctx, "rollback")
		writeError(w, err, cfg.Dev)
		return
	}

	var sw *writer.SQLWriter
	if req.HasData {
		result, err := query.ExecuteWriteStateParams(ctx, conn, wk.Root, req.Data, &query.WriteState{}, paramValues)
		if err != nil {
			_, _ = conn.Exec(ctx, "rollback")
			writeError(w, classifyWriteError(err, 0), cfg.Dev)
			return
		}
		compiled, cerr := query.CompileSelectForDataNode(wk.Root, result.NodeIDs[wk.Root])
		if cerr != nil {
			_, _ = conn.Exec(ctx, "rollback")
			writeError(w, serverError(errcode.Internal, fmt.Errorf("compiling response: %w", cerr)), cfg.Dev)
			return
		}
		sw = compiled
	} else {
		sw = wk.Read
	}

	args, err := sw.ResolveArgs(paramValues)
	if err != nil {
		_, _ = conn.Exec(ctx, "rollback")
		writeError(w, serverError(errcode.Internal, fmt.Errorf("resolving params: %w", err)), cfg.Dev)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := streamItem(ctx, w, conn, wk.Root, sw, args, true); err != nil {
		_, _ = conn.Exec(ctx, "rollback")
		if cse, ok := errors.AsType[*cleanStreamError](err); ok {
			writeError(w, classifyReadError(cse.Unwrap(), 0), cfg.Dev)
		}
		return
	}

	if _, err := conn.Exec(ctx, "commit"); err != nil {
		logging.FromContext(ctx).With("module", "server").Error("commit failed after streaming had already started", "error", err.Error())
	}
}

// decodeWellKnownRequest reads {name, params?, data?} off the request — the
// POST body verbatim, or GET's own flat query-string convention (name=...,
// params.<key>=<value> pairs, dot-path nested per querystring.DecodeQueryField).
func decodeWellKnownRequest(r *http.Request) (wellKnownRequest, error) {
	if r.Method == http.MethodGet {
		return decodeWellKnownGET(r)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return wellKnownRequest{}, fmt.Errorf("reading request body: %w", err)
	}
	root, perr := ast.NewParser(string(body)).Parse()
	if perr != 0 {
		return wellKnownRequest{}, fmt.Errorf("invalid JSON: %w", perr)
	}
	nameNode := root.Get("name")
	if !nameNode.Exists() {
		return wellKnownRequest{}, fmt.Errorf(`missing "name"`)
	}
	name, err := nameNode.StrictString()
	if err != nil {
		return wellKnownRequest{}, fmt.Errorf(`"name" must be a string: %w`, err)
	}
	req := wellKnownRequest{Name: name}
	if p := root.Get("params"); p.Exists() {
		if raw, err := p.Raw(); err == nil {
			req.Params = []byte(raw)
		}
	}
	if d := root.Get("data"); d.Exists() {
		raw, err := d.Raw()
		if err != nil {
			return wellKnownRequest{}, fmt.Errorf(`"data": %w`, err)
		}
		req.Data = []byte(raw)
		req.HasData = true
	}
	return req, nil
}

// decodeWellKnownGET decodes ?name=...&params.<key>=<value>&... — dot-path
// structural decoding (querystring.DecodeQueryField, the same generic
// layer /rpc's own free-form `query` field already uses) gives the
// name/params nesting for free ; params' own leaf values (always decoded
// as plain strings by that layer) are then opportunistically re-parsed as
// JSON scalars, so "?params.limit=5" produces the number 5 rather than the
// string "5" — falling back to the literal string when it isn't valid
// JSON (an ordinary bare word like "bob").
func decodeWellKnownGET(r *http.Request) (wellKnownRequest, error) {
	tree, err := querystring.DecodeQueryField(r.URL.RawQuery)
	if err != nil {
		return wellKnownRequest{}, err
	}
	m, ok := tree.(map[string]any)
	if !ok {
		return wellKnownRequest{}, fmt.Errorf(`/wellknown GET requires a "name" key`)
	}
	nameRaw, ok := m["name"]
	if !ok {
		return wellKnownRequest{}, fmt.Errorf(`/wellknown GET requires a "name" key`)
	}
	name, ok := nameRaw.(string)
	if !ok {
		return wellKnownRequest{}, fmt.Errorf(`"name" must be a plain value, not nested`)
	}
	req := wellKnownRequest{Name: name}
	if _, hasData := m["data"]; hasData {
		req.HasData = true
	}
	if p, ok := m["params"]; ok {
		raw, err := sonic.Marshal(coerceQueryStringLeaves(p))
		if err != nil {
			return wellKnownRequest{}, fmt.Errorf("params: %w", err)
		}
		req.Params = raw
	}
	return req, nil
}

// coerceQueryStringLeaves re-parses every string leaf of v (a
// querystring.DecodeQueryField tree) as JSON, keeping the parsed value
// whenever it IS valid JSON — a bare word like "bob" isn't, and stays the
// string "bob" ; "5" parses to the number 5 ; a literal, quoted "\"bob\""
// (URL-encoded %22bob%22) parses BACK to the plain string "bob", the
// escape hatch for a text-typed param whose value would otherwise coerce
// to a number or boolean (params.code=12345 -> number 12345,
// params.code=%2212345%22 -> the string "12345").
func coerceQueryStringLeaves(v any) any {
	switch t := v.(type) {
	case string:
		var parsed any
		if json.Unmarshal([]byte(t), &parsed) == nil {
			return parsed
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = coerceQueryStringLeaves(vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = coerceQueryStringLeaves(vv)
		}
		return out
	default:
		return v
	}
}
