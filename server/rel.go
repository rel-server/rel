// Package server serves POST/GET /rel and the generated TypeScript client.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rel-server/rel/cborenc"
	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/httperr"
	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/query"
	"github.com/rel-server/rel/querystring"
	"github.com/rel-server/rel/relexec"
	"github.com/rel-server/rel/reqauth"
	"github.com/rel-server/rel/wellknown"
	"github.com/samber/oops"
)

// NewRelHandler serves POST/GET /rel (GET only for a bare relation or well-known query). Each request acquires one
// connection from db.Pool and runs as the user's role.
func NewRelHandler(db *pg.DbInfos, cfg *config.Config, wkReg *wellknown.Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET, POST")
			writeError(r.Context(), w, httperr.MethodNotAllowed(oops.Errorf("/rel only accepts GET or POST")), cfg.Dev)
			return
		}
		handleRel(w, r, db, cfg, wkReg)
	})
}

// relQueryBytes returns the request's query : the POST body verbatim, bounded by maxBodySize, or the GET query
// string decoded to JSON.
func relQueryBytes(w http.ResponseWriter, r *http.Request, maxBodySize int64) ([]byte, *httperr.Error) {
	if r.Method == http.MethodGet {
		b, err := decodeGETQuery(r.URL.RawQuery)
		if err != nil {
			return nil, httperr.BadRequest(errcode.MalformedBody, err)
		}
		return b, nil
	}
	// Content-Length is checked first so an announced oversize body is refused unread.
	if r.ContentLength > maxBodySize {
		return nil, bodyTooLarge()
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return nil, bodyTooLarge()
		}
		return nil, httperr.BadRequest(errcode.MalformedBody, oops.Wrapf(err, "reading request body"))
	}
	return body, nil
}

func bodyTooLarge() *httperr.Error {
	return &httperr.Error{Status: http.StatusRequestEntityTooLarge, Code: errcode.BodyTooLarge, Err: oops.Errorf("request body exceeds http.max_body_size")}
}

// isCBORRequest reports whether a POST body is CBOR, by its Content-Type ; anything else is JSON. The response
// format is chosen independently.
func isCBORRequest(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/cbor"
}

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

// decodeWellKnownGET builds {"wellknown", "params"?} JSON from a decoded GET query string ; rejects "data", since
// GET is read-only.
func decodeWellKnownGET(m map[string]any) ([]byte, error) {
	name, ok := m["wellknown"].(string)
	if !ok {
		return nil, oops.Errorf(`"wellknown" must be a plain value, not nested`)
	}
	if _, hasData := m["data"]; hasData {
		return nil, oops.Errorf(`/rel GET is read-only ; well-known writes need POST`)
	}
	out := map[string]any{"wellknown": name}
	if p, ok := m["params"]; ok {
		out["params"] = coerceQueryStringLeaves(p)
	}
	return json.Marshal(out)
}

// coerceQueryStringLeaves re-parses each string leaf of v as JSON, keeping the parsed value when valid.
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

	w.Header().Add("Vary", "Accept") // the representation depends on it, errors included
	wire := cborenc.Negotiate(r.Header.Get("Accept"))
	var sess *reqauth.Session
	var itemCount int
	var anyWrite bool
	var targets []string

	// Runs on every return path, early rejections included, with whatever is known by then.
	defer func() {
		role := ""
		if sess != nil {
			role = sess.Role
		}
		logging.AnnotateAccess(ctx, role, "item_count", itemCount, "write", anyWrite, "targets", targets)
	}()

	// Rejects (e.g. anonymous with no anonymous role) before reading the body or acquiring a connection.
	sess, err := reqauth.Authenticate(cfg, db, r)
	if err != nil {
		writeError(ctx, w, reqauth.HTTPError(err), cfg.Dev)
		return
	}

	body, bodyErr := relQueryBytes(w, r, int64(cfg.Http.MaxBodySize))
	if bodyErr != nil {
		writeError(ctx, w, bodyErr, cfg.Dev)
		return
	}

	pq, err := parseRelBody(r, body)
	if err != nil {
		code := errcode.QueryMalformedJSON
		if isCBORRequest(r) {
			code = errcode.QueryMalformedCBOR
		}
		writeError(ctx, w, httperr.BadRequest(code, err), cfg.Dev)
		return
	}

	if r.Method == http.MethodGet && pq.Sequence != nil {
		writeError(ctx, w, httperr.BadRequest(errcode.QueryMalformedJSON, oops.Errorf("/rel GET decodes to a single relation or well-known query, not a sequence")), cfg.Dev)
		return
	}
	// A ComplexQuery wrapper is never allowed on GET, with or without data.
	if r.Method == http.MethodGet && pq.Write != nil {
		writeError(ctx, w, httperr.BadRequest(errcode.QueryComplexNotAllowedOnGet, oops.Errorf("/rel GET accepts only a bare relation or well-known query, not a ComplexQuery wrapper")), cfg.Dev)
		return
	}

	items := pq.Sequence
	if items == nil {
		items = []query.ParsedQuery{pq}
	}
	itemCount = len(items)

	// Before acquiring a connection : a 400 here has nothing to roll back.
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
		writeError(ctx, w, httperr.Server(errcode.DBUnavailable, oops.Wrapf(err, "acquiring connection")), cfg.Dev)
		return
	}
	defer conn.Release()

	// Begin, claims and SET LOCAL ROLE go out in one round trip. The session is renewed only once the transaction
	// has begun, so a request that fails to start gets no fresh cookie.
	begin := sess.RelexecBegin()
	if err := prepared.Execute(ctx, conn, func(ctx context.Context, conn *pgxpool.Conn) error {
		if err := begin(ctx, conn); err != nil {
			return err
		}
		sess.Renew(w)
		return nil
	}, &bodyWriter{w: w, contentType: wire.MediaType()}); err != nil {
		var e *relexec.Error
		if errors.As(err, &e) && e.Started {
			// The status and part of the body are sent : truncated output is the only signal left.
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

// relexecRequestError maps a relexec failure to its response : a Postgres error is classified (never err.Error(),
// which can embed generated SQL), anything else is a 400 or a 500 by whose fault it is.
func relexecRequestError(err error) *httperr.Error {
	e, ok := errors.AsType[*relexec.Error](err)
	if !ok {
		return httperr.Server(errcode.Internal, err)
	}
	if e.Postgres {
		if he, ok := httperr.FromPostgres(e.Err); ok {
			return he
		}
	}
	if e.Fault == relexec.ClientFault {
		return httperr.BadRequest(e.Code, e.Err)
	}
	return httperr.Server(e.Code, e.Err)
}
