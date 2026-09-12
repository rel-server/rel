// This file implements specs/templating-2.md ## query functions : rel(), a
// read-only-only query entrypoint bound into the shared jet.Set (see
// templates.go), reusing beginRoleScopedTx (tx.go) for its own per-call,
// role-scoped transaction rather than /rel's write/streaming machinery
// (server/rel.go), which is entangled with concerns rel() deliberately
// doesn't have (writes, sequences of mixed reads/writes, the "_data" table).
package route

import (
	"context"
	"net/http"

	"github.com/bytedance/sonic"
	"github.com/jackc/pgx/v5"
	"github.com/samber/oops"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/query"
	"github.com/rel-server/rel/wellknown"
)

// discardResponseWriter satisfies http.ResponseWriter without ever touching
// a real response — beginRoleScopedTx writes an error response to w on
// failure, which would corrupt the real HTTP response if rel() is called
// mid-template-render (the real response is still buffered at that point,
// see writeTemplateResponse). Errors are reported to rel()'s own caller via
// the returned error instead ; whatever beginRoleScopedTx wrote here is
// discarded.
type discardResponseWriter struct{ header http.Header }

func (d *discardResponseWriter) Header() http.Header         { return d.header }
func (d *discardResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardResponseWriter) WriteHeader(int)             {}

// executeRelRead runs one rel() call : parseQuery, reject any write shape,
// resolve/compile each item, execute in its own short-lived role-scoped
// transaction, and return the results as JSON — a single item's row array,
// or an array of row arrays for a Sequence.
func executeRelRead(ctx context.Context, db *pg.DbInfos, cfg *config.Config, wkReg *wellknown.Registry, claims jwtpkg.Claims, verified bool, queryJSON []byte) ([]byte, error) {
	pq, err := query.ParseQuery(queryJSON)
	if err != nil {
		return nil, oops.Code(errcode.QueryMalformedJSON).Wrapf(err, "rel(): invalid query JSON")
	}

	items := pq.Sequence
	single := items == nil
	if single {
		items = []query.ParsedQuery{pq}
	}

	type compiledItem struct {
		sql         string
		args        []any
		paramValues map[string]any
	}
	rctx := &query.ResolveContext{Db: db, Config: cfg}
	compiled := make([]compiledItem, len(items))
	for i, item := range items {
		if err := rejectRelWrite(item); err != nil {
			return nil, oops.With("item", i).Wrap(err)
		}

		var sw interface{ String() string }
		var argsSrc interface{ ResolveArgs(map[string]any) ([]any, error) }
		var paramValues map[string]any

		switch {
		case item.WellKnown != nil:
			wk, ok := wkReg.Lookup(item.WellKnown.WellKnown)
			if !ok {
				return nil, oops.Code(errcode.WellKnownUnknownQuery).Errorf("rel(): well-known query %q is not registered", item.WellKnown.WellKnown)
			}
			pv, perr := wk.ResolveParams(item.WellKnown.Params)
			if perr != nil {
				return nil, oops.With("item", i).Wrap(perr)
			}
			paramValues = pv
			sw = wk.Read
			argsSrc = wk.Read
		case item.Relation != nil:
			root, rerr := rctx.ResolveQuery(item.Relation)
			if rerr != nil {
				return nil, oops.With("item", i).Wrap(rerr)
			}
			if err := rctx.ResolveExpressions(root); err != nil {
				return nil, oops.With("item", i).Wrap(err)
			}
			if err := rctx.DeriveShapes(root); err != nil {
				return nil, oops.With("item", i).Wrap(err)
			}
			w, cerr := query.CompileSelect(root)
			if cerr != nil {
				return nil, oops.With("item", i).Wrap(cerr)
			}
			sw, argsSrc = w, w
		default:
			return nil, oops.Code(errcode.QueryMalformedJSON).Errorf("rel(): item %d: empty query", i)
		}

		args, aerr := argsSrc.ResolveArgs(paramValues)
		if aerr != nil {
			return nil, oops.With("item", i).Wrap(aerr)
		}
		compiled[i] = compiledItem{sql: sw.String(), args: args, paramValues: paramValues}
	}

	dw := &discardResponseWriter{header: http.Header{}}
	tx, release, _, ok := beginRoleScopedTx(ctx, dw, db, cfg, claims, verified)
	if !ok {
		return nil, oops.Code(errcode.NoRoleConfigured).Errorf("rel(): could not open a role-scoped transaction")
	}
	defer release()

	results := make([][]byte, len(compiled))
	for i, c := range compiled {
		rows, err := tx.Query(ctx, c.sql, c.args...)
		if err != nil {
			_ = tx.Rollback(ctx)
			return nil, oops.With("item", i).Wrapf(err, "rel(): executing query")
		}
		raw, serr := scanRowsToJSONArray(rows)
		if serr != nil {
			_ = tx.Rollback(ctx)
			return nil, oops.With("item", i).Wrapf(serr, "rel(): reading rows")
		}
		results[i] = raw
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, oops.Wrapf(err, "rel(): committing")
	}

	if single {
		return results[0], nil
	}
	out := append([]byte("["), []byte{}...)
	for i, r := range results {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, r...)
	}
	out = append(out, ']')
	return out, nil
}

// rejectRelWrite enforces specs/templating-2.md ## query functions' "rel()
// only executes reads" rule — a ComplexQuery/write wrapper (item.Write) is
// out of scope regardless of whether its own Data is set, mirroring
// server/rel.go's own isWrite derivation (checked at the parse level, since
// QueryNode.WriteMode defaults to a write mode even for a plain read once
// resolved, and so isn't a reliable signal here).
func rejectRelWrite(item query.ParsedQuery) error {
	if item.Write != nil {
		return oops.Code(errcode.RelFuncWriteNotAllowed).Errorf("rel(): write queries are not allowed from templates")
	}
	return nil
}

// scanRowsToJSONArray reads rows (each a single jsonb/json column, per
// query.CompileSelect's own row shape) into a JSON array, the same
// manually-streamed shape server's streamRows produces for /rel — rel() has
// no partial-write/streaming concern of its own (everything here still sits
// in an in-memory buffer, unlike an HTTP response), so this stays a plain
// byte-slice builder rather than reusing that io.Writer-oriented helper.
func scanRowsToJSONArray(rows pgx.Rows) ([]byte, error) {
	defer rows.Close()
	out := []byte("[")
	first := true
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if !first {
			out = append(out, ',')
		}
		first = false
		out = append(out, raw...)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out = append(out, ']')
	return out, nil
}

// jsonRawToAny decodes raw into a plain any (nil for empty/JSON null),
// matching jet's own dot-access expectations — the same shape
// templateDataValue produces for Data.
func jsonRawToAny(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	_ = sonic.Unmarshal(raw, &v)
	return v
}
