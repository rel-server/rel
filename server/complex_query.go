// ComplexQuery support (specs/complex-query.md) : per-item response-shaping
// flags, their validation/availability gating, and the envelope response
// writer. Split out of rel.go since handleRel's own flow already carries
// plenty of state ; kept in the same package since every piece here is
// wired directly into handleRel's per-item loops.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/query"
	"github.com/samber/oops"
)

// validateComplexFlags checks the structural rules a ComplexQuery's flags
// must satisfy regardless of ## Availability's per-flag grants — errors
// here are about the request's own shape, not what this server allows.
func validateComplexFlags(ri *resolvedItem) error {
	if ri.count && ri.isWrite {
		return oops.Code(errcode.QueryCountIsReadOnly).Errorf("query: \"count\" is valid only on a read, not alongside \"data\"")
	}
	if ri.stats && !ri.isWrite {
		return oops.Code(errcode.QueryStatsIsWriteOnly).Errorf("query: \"stats\" is valid only on a write")
	}
	if ri.stats && ri.queryPlan {
		return oops.Code(errcode.QueryStatsQueryPlanConflict).Errorf("query: \"stats\" and \"query_plan\" cannot both be requested on the same write")
	}
	if ri.returns == "none" && !ri.count && !ri.stats && !ri.queryPlan && !ri.sql {
		return oops.Code(errcode.QueryUnsupportedReturns).Errorf(`query: "returns": "none" with every other flag false/absent has nothing to put in an envelope`)
	}
	return nil
}

// checkRollbackGrant is separate from validateComplexFlags : an ungranted
// rollback is a hard error (## Availability), unlike count/stats/query_plan/
// sql's silent degrade, since silently ignoring it would let effects persist
// the caller believed were undone.
func checkRollbackGrant(ri *resolvedItem, cfg *config.Config) error {
	if ri.rollback && !cfg.AllowRollback {
		return oops.Code(errcode.QueryRollbackNotGranted).Errorf(`query: "rollback" is not enabled on this server`)
	}
	return nil
}

// usesEnvelope reports whether ri's response is the envelope shape or the
// bare selection (## Response shape) — decided by the flags as requested,
// independent of ## Availability's grants (a client asking for an ungranted
// flag still gets an envelope, just with that flag's content degraded).
func (ri *resolvedItem) usesEnvelope() bool {
	return ri.returns == "none" || ri.count || ri.stats || ri.queryPlan || ri.sql
}

// emptyPath is JSON-marshaled as "[]", never "null" — every Path field
// below is built from this, not a nil slice.
var emptyPath = []string{}

// bufferReadback compiles and runs a write's read-back SELECT right away,
// capturing every row's raw JSON — used only to give a rolled-back write's
// "result" something to stream from, since by the time the normal per-item
// streaming loop runs, the rollback has already erased both the real rows
// and this item's own "_data" rows the read-back would otherwise join
// against. A write is never function-rooted (findUnwritableNode rejects
// it), so root.IsFunction()'s scalar special case never applies here.
func bufferReadback(ctx context.Context, conn *pgxpool.Conn, root *query.QueryNode, nodeID int, paramValues map[string]any) ([][]byte, error) {
	sw, err := query.CompileSelectForDataNode(root, nodeID)
	if err != nil {
		return nil, fmt.Errorf("compiling read-back: %w", err)
	}
	args, err := sw.ResolveArgs(paramValues)
	if err != nil {
		return nil, fmt.Errorf("resolving read-back params: %w", err)
	}
	rows, err := conn.Query(ctx, sw.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("running read-back: %w\nsql: %s", err, sw.String())
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scanning read-back row: %w", err)
		}
		out = append(out, raw)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("running read-back: %w", err)
	}
	return out, nil
}

// runCount compiles and runs CompileCount(root), returning the total
// matching row count (specs/complex-query.md ## count).
func runCount(ctx context.Context, conn *pgxpool.Conn, root *query.QueryNode, paramValues map[string]any) (int, error) {
	cw, err := query.CompileCount(root)
	if err != nil {
		return 0, fmt.Errorf("compiling count: %w", err)
	}
	args, err := cw.ResolveArgs(paramValues)
	if err != nil {
		return 0, fmt.Errorf("resolving count params: %w", err)
	}
	var n int
	if err := conn.QueryRow(ctx, cw.String(), args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("running count: %w\nsql: %s", err, cw.String())
	}
	return n, nil
}

// explainFormatJSON runs "explain (format json) <sql>" — never executed for
// real beyond what EXPLAIN itself runs (specs/complex-query.md ## query_plan
// "On a read"), used both for a plain read and for a write's read-back.
func explainFormatJSON(ctx context.Context, conn *pgxpool.Conn, sql string, args []any) (json.RawMessage, error) {
	rows, err := conn.Query(ctx, "explain (format json)\n"+sql, args...)
	if err != nil {
		return nil, fmt.Errorf("explain: %w", err)
	}
	defer rows.Close()
	var raw []byte
	if rows.Next() {
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("explain: scanning plan: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("explain: %w", err)
	}
	return json.RawMessage(raw), nil
}

// writeKey writes ,"key":<json of val> (no leading comma if first) ; used
// only by writeEnvelope's own hand-rolled JSON, since "result" must stream
// row-by-row alongside these already-known keys, never buffered whole
// (specs/complex-query.md ## Response shape).
func writeKey(w io.Writer, first *bool, key string, val any) error {
	prefix := ","
	if *first {
		prefix = ""
		*first = false
	}
	vb, err := json.Marshal(val)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s%q:%s", prefix, key, vb)
	return err
}

// envelopeData is every non-"result" envelope key's already-resolved value,
// computed and error-checked before any response byte is written (## sql/##
// query_plan/## count : "knowable before read-back streaming begins").
// A nil slice/pointer field means "not requested" (key absent) ; a
// non-nil-but-empty slice means "requested, degraded by ## Availability"
// (key present as [], per that section's own rule).
type envelopeData struct {
	count         *int
	offset, limit *int
	stats         []query.Stat
	queryPlan     []query.PlanResult
	sql           []query.SqlResult
}

// writeEnvelope writes the ComplexResult envelope : precomputed keys first,
// then "result" (streamResult does the actual streaming, called with the
// writer positioned right after "result":) unless returns=="none".
func writeEnvelope(w io.Writer, ri *resolvedItem, data envelopeData, streamResult func(io.Writer) error) error {
	if _, err := io.WriteString(w, "{"); err != nil {
		return err
	}
	first := true
	if data.count != nil {
		if err := writeKey(w, &first, "count", *data.count); err != nil {
			return err
		}
		if err := writeKey(w, &first, "offset", zeroIfNil(data.offset)); err != nil {
			return err
		}
		if data.limit != nil {
			if err := writeKey(w, &first, "limit", *data.limit); err != nil {
				return err
			}
		}
	}
	if data.stats != nil {
		if err := writeKey(w, &first, "stats", data.stats); err != nil {
			return err
		}
	}
	if data.queryPlan != nil {
		if err := writeKey(w, &first, "query_plan", data.queryPlan); err != nil {
			return err
		}
	}
	if data.sql != nil {
		if err := writeKey(w, &first, "sql", data.sql); err != nil {
			return err
		}
	}
	if ri.returns != "none" {
		prefix := ","
		if first {
			prefix = ""
			first = false
		}
		if _, err := fmt.Fprintf(w, `%s"result":`, prefix); err != nil {
			return err
		}
		if err := streamResult(w); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "}")
	return err
}

func zeroIfNil(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
