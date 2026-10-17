package query

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// runBothWays runs the same write twice, each time on the same starting state
// (a savepoint is rolled back in between) : once through the general "_data"
// path and its read-back, once through the fast path. It returns what each answered
// and the table's rows afterwards.
func runBothWays(t *testing.T, setup []string, node *QueryNode, payload string, table string) (general, fast []map[string]any, generalTable, fastTable []map[string]any) {
	t.Helper()
	conn := acquireWriteConn(t)
	ctx := context.Background()
	for _, s := range append([]string{"begin"}, setup...) {
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), "rollback") })
	snapshot := func() []map[string]any {
		rows, err := conn.Query(ctx, `select to_jsonb(t) from `+table+` t order by id`)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		defer rows.Close()
		var out []map[string]any
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				t.Fatalf("scan: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			out = append(out, m)
		}
		return out
	}

	if _, err := conn.Exec(ctx, "savepoint s"); err != nil {
		t.Fatal(err)
	}
	res, err := ExecuteWriteStateParamsOpts(ctx, conn, node, []byte(payload), &WriteState{}, nil, WriteOptions{NoFastPath: true})
	if err != nil {
		t.Fatalf("general path: %v", err)
	}
	if res.Rows != nil {
		t.Fatalf("NoFastPath still produced fast rows")
	}
	sw, err := CompileSelectForDataNode(node, res.NodeIDs[node])
	if err != nil {
		t.Fatal(err)
	}
	general = runSelectOn(t, conn, sw.String(), mustArgs(t, sw))
	generalTable = snapshot()
	if _, err := conn.Exec(ctx, "rollback to savepoint s"); err != nil {
		t.Fatal(err)
	}

	res, err = ExecuteWriteStateParamsOpts(ctx, conn, node, []byte(payload), &WriteState{}, nil, WriteOptions{})
	if err != nil {
		t.Fatalf("fast path: %v", err)
	}
	if res.Rows == nil {
		t.Fatalf("expected the fast path to build the response")
	}
	for _, raw := range res.Rows {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		fast = append(fast, m)
	}
	fastTable = snapshot()
	return
}

func mustArgs(t *testing.T, sw interface{ Args() []any }) []any {
	t.Helper()
	return sw.Args()
}

// TestFastWrite_MatchesTheGeneralPath proves the fast path answers exactly what the
// general path's read-back does, and leaves the table in the same state.
func TestFastWrite_MatchesTheGeneralPath(t *testing.T) {
	cases := map[string]struct {
		setup   []string
		query   string
		payload string
		table   string
		rows    int
	}{
		"insert, several rows in payload order": {
			query:   `{"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"}`,
			payload: `[{"id": 9002, "name": "B"}, {"id": 9001, "name": "A"}, {"id": 9003, "name": "C"}]`,
			table:   "director", rows: 3,
		},
		"insert, response filtered by a where": {
			query:   `{"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert", "where": ["=", ["col", "name"], "keep"]}`,
			payload: `[{"id": 9010, "name": "keep"}, {"id": 9011, "name": "drop"}]`,
			table:   "director", rows: 1,
		},
		"insert, selecting a few columns, ordered and limited": {
			query:   `{"relation": "director", "schema": "public", "select": {"id": ["col", "id"], "name": ["col", "name"]}, "write_mode": "insert", "order_by": [["desc", ["col", "id"]]], "limit": 2}`,
			payload: `[{"id": 9021, "name": "x"}, {"id": 9022, "name": "y"}, {"id": 9023, "name": "z"}]`,
			table:   "director", rows: 2,
		},
		"upsert, one existing row and one new": {
			setup:   []string{`insert into director (id, name) values (9031, 'old')`},
			query:   `{"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "upsert"}`,
			payload: `[{"id": 9031, "name": "new"}, {"id": 9032, "name": "fresh"}]`,
			table:   "director", rows: 2,
		},
		"update-existing, answered in payload order": {
			setup:   []string{`insert into director (id, name) values (9041, 'a'), (9042, 'b')`},
			query:   `{"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "update-existing"}`,
			payload: `[{"id": 9042, "name": "b2"}, {"id": 9041, "name": "a2"}]`,
			table:   "director", rows: 2,
		},
		"update-existing with only the identity selected": {
			setup:   []string{`insert into director (id, name) values (9051, 'kept')`},
			query:   `{"relation": "director", "schema": "public", "select": {"id": ["col", "id"]}, "write_mode": "update-existing"}`,
			payload: `[{"id": 9051}]`,
			table:   "director", rows: 1,
		},
		"update where, a patch on several rows": {
			setup:   []string{`insert into director (id, name) values (9061, 'p'), (9062, 'p'), (9063, 'untouched')`},
			query:   `{"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "update", "where": ["in", ["col", "id"], 9061, 9062]}`,
			payload: `{"name": "patched"}`,
			table:   "director", rows: 2,
		},
		"update where, a row that stops matching is written but not answered": {
			setup:   []string{`insert into director (id, name) values (9071, 'before')`},
			query:   `{"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "update", "where": ["=", ["col", "name"], "before"]}`,
			payload: `{"name": "after"}`,
			table:   "director", rows: 0,
		},
		"numeric and bigint columns arrive as text": {
			setup:   []string{`insert into agg_parent (id) values (9081)`},
			query:   `{"relation": "agg_child", "schema": "public", "select": ["*~"], "write_mode": "insert"}`,
			payload: `[{"id": 9082, "parent_id": 9081, "amount": "12.50", "big": 9007199254740993}]`,
			table:   "agg_child", rows: 1,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			node := mustResolveQuery(t, tc.query)
			general, fast, generalTable, fastTable := runBothWays(t, tc.setup, node, tc.payload, tc.table)
			if len(fast) != tc.rows {
				t.Errorf("expected %d response rows, got %d : %v", tc.rows, len(fast), fast)
			}
			if !reflect.DeepEqual(general, fast) {
				t.Errorf("responses differ\n general: %v\n    fast: %v", general, fast)
			}
			if !reflect.DeepEqual(generalTable, fastTable) {
				t.Errorf("tables differ\n general: %v\n    fast: %v", generalTable, fastTable)
			}
		})
	}
}

// TestFastWrite_Eligibility proves what falls back to the general path.
func TestFastWrite_Eligibility(t *testing.T) {
	for name, tc := range map[string]struct {
		query string
		opts  WriteOptions
		fast  bool
	}{
		"a flat insert":             {`{"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"}`, WriteOptions{}, true},
		"forced off":                {`{"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"}`, WriteOptions{NoFastPath: true}, false},
		"stats asked":               {`{"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"}`, WriteOptions{Stats: true}, false},
		"sql asked":                 {`{"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"}`, WriteOptions{Sql: true}, false},
		"query_plan asked":          {`{"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"}`, WriteOptions{QueryPlan: true}, false},
		"a nested write":            {`{"relation": "director", "schema": "public", "select": {"id": ["col", "id"], "movies": [".", "movies"]}, "write_mode": "insert", "join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": ["*~"]}}}`, WriteOptions{}, false},
		"a read-only join to embed": {`{"relation": "director", "schema": "public", "select": {"id": ["col", "id"], "movies": [".", "movies"]}, "write_mode": "insert", "join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "write_mode": "readonly"}}}`, WriteOptions{}, false},
		"a scalar select":           {`{"relation": "director", "schema": "public", "select": ["col", "name"], "write_mode": "insert"}`, WriteOptions{}, false},
		"a where on a joined value": {`{"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert", "where": [">", ["agg", {"schema": "pg_catalog", "name": "count"}, [[".", "movies"]]], 0], "join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "write_mode": "readonly"}}}`, WriteOptions{}, false},
	} {
		t.Run(name, func(t *testing.T) {
			node := mustResolveQuery(t, tc.query)
			if got := fastWriteEligible(node, tc.opts); got != tc.fast {
				t.Errorf("fastWriteEligible = %v, want %v", got, tc.fast)
			}
		})
	}
}

var _ = (*pgxpool.Conn)(nil)
