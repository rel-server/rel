// ComplexQuery flags, their validation, and the envelope response.
package relexec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/query"
	"github.com/samber/oops"
)

// validateComplexFlags checks the flag combinations a ComplexQuery may use, whatever the server grants.
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

// checkRollbackGrant refuses an ungranted rollback instead of degrading it, or the user would believe effects undone.
func checkRollbackGrant(ri *resolvedItem, policy Policy) error {
	if ri.rollback && !policy.Rollback {
		return oops.Code(errcode.QueryRollbackNotGranted).Errorf(`query: "rollback" is not enabled on this server`)
	}
	return nil
}

// usesEnvelope reports whether ri is answered with an envelope, from the flags as requested, granted or not.
func (ri *resolvedItem) usesEnvelope() bool {
	return ri.returns == "none" || ri.count || ri.stats || ri.queryPlan || ri.sql
}

// emptyPath marshals as "[]", not "null".
var emptyPath = []string{}

// bufferReadback runs a rolled-back write's read-back before the rollback erases the rows it reads. A write is
// never function-rooted, so there is no scalar case.
func bufferReadback(ctx context.Context, conn *pgxpool.Conn, root *query.QueryNode, nodeID int, paramValues map[string]any, format query.OutputFormat) ([][]byte, error) {
	sw, err := query.CompileSelectForDataNodeFormat(root, nodeID, format)
	if err != nil {
		return nil, fmt.Errorf("compiling read-back: %w", err)
	}
	args, err := sw.ResolveArgs(paramValues)
	if err != nil {
		return nil, fmt.Errorf("resolving read-back params: %w", err)
	}
	if format == query.FormatCBOR {
		args = withBinaryResults(args)
	}
	rows, err := conn.Query(ctx, sw.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("running read-back: %w\nsql: %s", err, sw.String())
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		if format == query.FormatCBOR { // binary records, read later by the result encoder
			out = append(out, copyRaw(rows))
			continue
		}
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

// runCount returns the total matching row count, ignoring offset/limit.
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

// explainFormatJSON runs "explain (format json) <sql>", without ANALYZE.
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

// writeKey writes ,"key":<json of val>, without the comma if first. The envelope is written by hand so "result"
// can stream instead of being buffered.
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

// envelopeData holds the envelope's keys other than "result", computed before any byte is written. nil means not
// requested (key absent) ; an empty slice means requested but not granted (key present as []).
type envelopeData struct {
	count         *int
	offset, limit *int
	stats         []query.Stat
	queryPlan     []query.PlanResult
	sql           []query.SqlResult
}

// writeEnvelope writes the envelope's precomputed keys, then "result" through streamResult unless returns=="none".
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
