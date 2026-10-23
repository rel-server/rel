// Package server implements POST/GET /rel — query-engine.md's Reading/
// Writing algorithms wired into an HTTP request, including a
// ParsedQuery.Sequence and a well-known query in either position query.ts's
// Query union allows (bare read, or wrapped in WriteQuery.query for a
// write), composable alongside a plain Relation in the same Sequence. See
// the role switch below, and jwt/middleware.go's own doc comment, for how the
// JWT Lifecycle splits across the two packages.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rel-server/rel/cborenc"
	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/dbauth"
	"github.com/rel-server/rel/errcode"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/pgerr"
	"github.com/rel-server/rel/query"
	"github.com/rel-server/rel/querystring"
	"github.com/rel-server/rel/relexec"
	"github.com/rel-server/rel/wellknown"
)

// NewRelHandler serves POST/GET /rel per specs/query-engine.md's
// ## Configuration ("all of them MUST be POST") and ## Response Shape.
// db.Pool is acquired from once per request ; wkReg resolves a well-known
// item by name, rebuilt alongside db/cfg on every SIGUSR1 reload
// (boot/reload.go). Wrapped in jwt.Middleware (Lifecycle step 2/Verify
// only) — resolveClaimsAndRole (called once handleRel has a connection) does steps
// 3 (Check), 4 (Renew), and 5 (Apply role), reading the claims
// jwt.FromContext left behind.
func NewRelHandler(db *pg.DbInfos, cfg *config.Config, wkReg *wellknown.Registry) http.Handler {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET, POST")
			writeError(r.Context(), w, methodNotAllowed(fmt.Errorf("/rel only accepts GET or POST")), cfg.Dev)
			return
		}
		handleRel(w, r, db, cfg, wkReg)
	})
	return jwtpkg.Middleware(cfg.Jwt)(inner)
}

// relQueryBytes returns the query.ts Query JSON the request describes :
// the POST body verbatim, or GET's own query-json.md decoding below.
func relQueryBytes(r *http.Request) ([]byte, error) {
	if r.Method == http.MethodGet {
		return decodeGETQuery(r.URL.RawQuery)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("reading request body: %w", err)
	}
	return body, nil
}

// isCBORRequest reports whether a POST body is CBOR (RFC 8949), by its Content-Type ; anything
// else is JSON. A response is JSON either way : the two formats are chosen independently.
func isCBORRequest(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/cbor"
}

// parseRelBody decodes the request body into a query, from CBOR or JSON per its Content-Type.
func parseRelBody(r *http.Request, body []byte) (query.ParsedQuery, error) {
	if isCBORRequest(r) {
		n, err := query.NewCBORNode(body)
		if err != nil {
			return query.ParsedQuery{}, err
		}
		return query.ParseQueryNode(n)
	}
	return query.ParseQuery(body)
}

// decodeGETQuery picks the wellknown vs relation/function GET grammar by
// peeking at the "wellknown" key via the generic structural decoder first.
func decodeGETQuery(raw string) ([]byte, error) {
	tree, err := querystring.DecodeQueryField(raw)
	if err != nil {
		return nil, err
	}
	if m, ok := tree.(map[string]any); ok {
		if _, hasWellKnown := m["wellknown"]; hasWellKnown {
			return decodeWellKnownGET(m)
		}
	}
	return querystring.DecodeRelation(raw)
}

// decodeWellKnownGET builds {"wellknown", "params"?} JSON from a decoded
// GET query string ; rejects "data" (query-json.md's read-only rule).
func decodeWellKnownGET(m map[string]any) ([]byte, error) {
	name, ok := m["wellknown"].(string)
	if !ok {
		return nil, fmt.Errorf(`"wellknown" must be a plain value, not nested`)
	}
	if _, hasData := m["data"]; hasData {
		return nil, fmt.Errorf(`/rel GET is read-only ; well-known writes need POST`)
	}
	out := map[string]any{"wellknown": name}
	if p, ok := m["params"]; ok {
		out["params"] = coerceQueryStringLeaves(p)
	}
	return json.Marshal(out)
}

// coerceQueryStringLeaves re-parses each string leaf of v as JSON, keeping
// the parsed value when valid — query-json.md's %22-quoted escape hatch.
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

func handleRel(w http.ResponseWriter, r *http.Request, db *pg.DbInfos, cfg *config.Config, wkReg *wellknown.Registry) {
	ctx := r.Context()
	start := time.Now()

	// Wraps w for the rest of this function so every downstream response
	// helper keeps calling the ordinary http.ResponseWriter methods
	// unchanged, while specs/logging.md ## Access logging's status/size
	// fields get captured for free.
	rec := logging.NewResponseRecorder(w)
	w = rec
	w.Header().Add("Vary", "Accept") // the representation depends on it, errors included
	wire := cborenc.Negotiate(r.Header.Get("Accept"))
	var role string
	var itemCount int
	var anyWrite bool
	var targets []string

	// specs/logging.md ## Access logging : fires exactly once, from every
	// return path (including an early rejection), since role/itemCount/
	// anyWrite/targets not yet known at that point are simply the zero value.
	defer func() {
		claims, verified := jwtpkg.FromContext(ctx)
		logging.FromContext(ctx).Info("request",
			"method", r.Method, "path", r.URL.Path,
			"status", rec.Status, "response_size", rec.Size, "duration", time.Since(start),
			"verified", verified, "role", role, "claims", claims,
			"item_count", itemCount, "write", anyWrite, "targets", targets)
	}()

	// authentication.md "## Roles ## Anonymous role existence" : reject
	// before reading the body or acquiring a connection when disabled.
	if _, verified := jwtpkg.FromContext(r.Context()); !verified && !db.AnonymousRoleExists {
		writeError(ctx, w, unauthorized(errcode.AnonymousDisabled, fmt.Errorf("%s", errcode.AnonymousDisabledMessage)), cfg.Dev)
		return
	}

	body, err := relQueryBytes(r)
	if err != nil {
		writeError(ctx, w, badRequest(errcode.MalformedBody, err), cfg.Dev)
		return
	}

	pq, err := parseRelBody(r, body)
	if err != nil {
		code := errcode.QueryMalformedJSON
		if isCBORRequest(r) {
			code = errcode.QueryMalformedCBOR
		}
		writeError(ctx, w, badRequest(code, err), cfg.Dev)
		return
	}

	// query-json.md ## Scope / ## Well-known queries on GET /rel : GET
	// never decodes to a Sequence ; asserted explicitly, not just assumed.
	if r.Method == http.MethodGet && pq.Sequence != nil {
		writeError(ctx, w, badRequest(errcode.QueryMalformedJSON, fmt.Errorf("/rel GET decodes to a single relation or well-known query, not a sequence")), cfg.Dev)
		return
	}
	// specs/complex-query.md ## rollback's GET rule : a ComplexQuery
	// wrapper (data present or not) is GET-ineligible, only a bare
	// RelationQuery/WellKnownQuery is convenience-served there.
	if r.Method == http.MethodGet && pq.Write != nil {
		writeError(ctx, w, badRequest(errcode.QueryComplexNotAllowedOnGet, fmt.Errorf("/rel GET accepts only a bare relation or well-known query, not a ComplexQuery wrapper")), cfg.Dev)
		return
	}

	items := pq.Sequence
	if items == nil {
		items = []query.ParsedQuery{pq}
	}
	itemCount = len(items)

	// Resolving before touching a connection : a 400 here means nothing has been opened yet,
	// nothing to roll back.
	prepared, err := relexec.Prepare(&query.ResolveContext{Db: db, Config: cfg}, wkReg, items, relexec.Options{
		Format: wire,
		Policy: relexecPolicy(cfg),
		Buffer: cfg.Http.ResponseBuffer,
	})
	if err != nil {
		writeError(ctx, w, relexecRequestError(err), cfg.Dev)
		return
	}
	anyWrite, targets = prepared.AnyWrite(), prepared.Targets()

	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		writeError(ctx, w, serverError(errcode.DBUnavailable, fmt.Errorf("acquiring connection: %w", err)), cfg.Dev)
		return
	}
	defer conn.Release()

	// Lifecycle steps 3-5 : the role is decided first (no database involved), then
	// begin, claims and SET LOCAL ROLE go out in one round trip ; both revert at the
	// single commit/rollback relexec ends the transaction with (## Transactions).
	begin := func(ctx context.Context, conn *pgxpool.Conn) error {
		claims, resolved, err := resolveClaimsAndRole(w, r, cfg)
		if err != nil {
			return err
		}
		role = resolved
		if err := dbauth.BeginAs(ctx, conn, claims, role); err != nil {
			if step, ok := errors.AsType[*dbauth.StepError](err); ok && step.Step == "begin" {
				return relexec.NewError(relexec.ServerFault, errcode.TransactionError, err)
			} else if ok && step.Step == "claims" {
				return relexec.NewError(relexec.ServerFault, errcode.Internal, fmt.Errorf("setting jwt claims: %w", err))
			}
			return relexec.NewError(relexec.ServerFault, errcode.Internal, fmt.Errorf("applying role: %w", err))
		}
		return nil
	}

	if err := prepared.Execute(ctx, conn, begin, &bodyWriter{w: w, contentType: wire.MediaType()}); err != nil {
		var e *relexec.Error
		if errors.As(err, &e) && e.Started {
			// The status and part of the body are gone already : truncated output is the only
			// signal left (writeError's own doc comment covers this).
			logging.FromContext(ctx).Error("response cut short", append([]any{"code", e.Code}, logging.Error(err)...)...)
			return
		}
		writeError(ctx, w, relexecRequestError(err), cfg.Dev)
	}
}

// bodyWriter sets the response's Content-Type just before its first byte, so a failure before
// that is answered with the error envelope's own.
type bodyWriter struct {
	w           http.ResponseWriter
	contentType string
	started     bool
}

func (b *bodyWriter) Write(p []byte) (int, error) {
	if !b.started {
		b.started = true
		b.w.Header().Set("Content-Type", b.contentType)
	}
	return b.w.Write(p)
}

// relexecPolicy is what cfg lets a ComplexQuery ask for.
func relexecPolicy(cfg *config.Config) relexec.Policy {
	q := cfg.Pg.Query
	return relexec.Policy{Count: q.AllowCount, Stats: q.AllowStats, QueryPlan: q.AllowQueryPlan, SQL: q.AllowSql, Rollback: q.AllowRollback}
}

// relexecRequestError maps a relexec failure to the response it deserves : a Postgres error is
// classified (PG_*/RSxxx, never err.Error(), which can embed generated SQL), anything else is a 400
// or a 500 by whose fault it is.
func relexecRequestError(err error) *requestError {
	e, ok := errors.AsType[*relexec.Error](err)
	if !ok {
		return serverError(errcode.Internal, err)
	}
	if e.Postgres {
		if status, code, tier, detail, ok := pgerr.Classify(e.Err); ok {
			return pgClassified(status, code, tier, detail, e.Err)
		}
	}
	if e.Fault == relexec.ClientFault {
		return badRequest(e.Code, e.Err)
	}
	return serverError(e.Code, e.Err)
}

// resolveClaimsAndRole runs Lifecycle steps 3-4 (Check/Renew) and decides the
// role of step 5, without touching the database : the caller applies both inside
// its transaction (dbauth.BeginAs).
func resolveClaimsAndRole(w http.ResponseWriter, r *http.Request, cfg *config.Config) (jwtpkg.Claims, string, error) {
	claims, verified := jwtpkg.FromContext(r.Context())
	// The claims exposed to SQL are the request's own, as received ; a renewal
	// only changes the token sent back, and the role resolved from it.
	received := claims
	if verified {
		claims = jwtpkg.RenewIfDue(cfg.Jwt, w, claims)
	}
	role := jwtpkg.ResolveRole(cfg.Pg.Query.AnonymousRole, claims, verified)
	if role == "" {
		return received, "", relexec.NewError(relexec.ServerFault, errcode.NoRoleConfigured, fmt.Errorf("%s", dbauth.NoRoleConfiguredMessage))
	}
	return received, role, nil
}
