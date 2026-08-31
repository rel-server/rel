// Benchmarks for the READ path (query.CompileSelect + execution) — the
// companion to query/write_bench_test.go's write-path benchmarks, but
// against the hotel/booking fixture (test/dmut, test/seed/seed — see
// test/README.md) rather than the flat movie/director schema, since the
// whole point of the hotel fixture is to be a realistic volume/shape for
// exactly this kind of measurement (test/README.md's own "Why a hotel/
// booking domain"). Run with e.g.:
//
//	go test ./query_bench/... -run=^$ -bench=. -benchtime=1x
//
// ## What each benchmark measures, and the two-part split
//
// Every non-trivial query below gets a plain execute-and-decode benchmark
// (resolve + compile once, then loop pool.Query + json.Unmarshal b.N times)
// AND, for a few of them, a paired "_Compile" benchmark that isolates
// CompileSelect itself — resolve once, then loop CompileSelect b.N times,
// no DB round trip at all. These catch two DIFFERENT kinds of regression :
//
//   - A slowdown in CompileSelect's own SQL-generation logic (string
//     building, alias bookkeeping, LATERAL-sharing analysis, ...) shows up
//     in the "_Compile" benchmark even if the resulting SQL text is
//     unchanged and Postgres's own execution time is identical.
//   - A slowdown from Postgres itself (more rows, a worse plan, a missing
//     index) shows up in the execute-and-decode benchmark but NOT in
//     "_Compile", since the SQL text — and hence the compiler's own work —
//     hasn't changed at all.
//
// Resolution (ParseQuery -> ResolveQuery -> ResolveExpressions ->
// DeriveShapes) happens ONCE per benchmark, outside the timed loop, same as
// CompileSelect for the execute-and-decode variants : none of these queries
// have any per-iteration data dependency (unlike write_bench_test.go's
// inserts, reads have nothing that must be "fresh" each call), so timing
// resolve/compile repeatedly would only measure redundant, identical work.
//
// ## Join eligibility shaped which "realistic" queries were even possible
//
// specs/query-engine.md's "### Join eligibility" requires the CHILD side of
// every join to be covered by an index on its own `on` columns — Postgres
// does not auto-index the referencing side of a foreign key, and this
// schema (deliberately realistic, not hand-tuned for rel) mostly doesn't
// either. Concretely : rooms.property_id, room_types.property_id,
// rate_plans.property_id, property_amenities.property_id, and
// booking_guests.booking_id are all indexed (each is the LEADING column of
// a real unique constraint) — but reviews.property_id/guest_id/booking_id,
// payments.booking_id, bookings.guest_id/room_id, payment_methods.guest_id,
// and staff.property_id/manager_id (the INCOMING direction) are not indexed
// at all. So "guests embedding bookings", "bookings embedding payments",
// "properties embedding reviews", and "staff embedding direct reports" are
// all queries rel correctly REFUSES to compile against this schema as-is —
// not a gap in these benchmarks, a real property of the fixture worth
// knowing before reaching for one of those shapes elsewhere. Every
// to-many/aggregate benchmark below instead uses one of the actually-
// indexed relationships (rooms, room_types, rate_plans, property_amenities,
// booking_guests) ; every to-one embed is unaffected by this at all, since
// the "one" side of an outgoing join is always a primary key and therefore
// always indexed automatically.
//
// ## RETURNS TABLE functions
//
// hotel.booking_stats (RETURNS TABLE(...)) used to be unselectable — its
// output columns are Postgres proargmode 't' pseudo-columns, sharing the
// one generic pg_catalog.record prorettype every RETURNS TABLE function
// has, so GetRelationByType had nothing to map "record" to and every
// column reference failed with "unresolvable identifier". Fixed :
// pg.Function.RecordRelation (pg/info_function.go) is now built directly
// from the function's own OUT/TABLE-mode arguments at introspection time,
// independent of ReturnType, and query/node_resolve.go falls back to it —
// see BenchmarkSelect_RecordFunction below. This does NOT make it usable
// as the CHILD/joined-into side of a relationship — Postgres can never
// index a function's computed output, and ### Join eligibility requires
// exactly that on the child side — only as a query root or as the
// parent/outer side of an outgoing join out to a real, indexed relation.
// hotel.search_properties (SETOF hotel.properties) never had this problem
// in the first place — it reuses a real composite type (properties' own),
// not an anonymous record — covered below (BenchmarkSelect_FullTextSearch).
package query_bench

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ceymard/rel/query"
)

// mustResolveQuery runs the full pass-1/pass-2 pipeline (ParseQuery ->
// ResolveQuery -> ResolveExpressions -> DeriveShapes), exactly mirroring
// query/expression_resolve_test.go's own mustResolveQuery — duplicated
// rather than imported because it's hard-wired to *testing.T there and this
// file needs the *testing.B form throughout; query_bench is also a
// different package (it needs its OWN container/schema/seed, see
// main_test.go), so it can't reuse the query package's unexported test
// helpers regardless.
func mustResolveQuery(b *testing.B, src string) *query.QueryNode {
	b.Helper()
	pq, err := query.ParseQuery([]byte(src))
	if err != nil {
		b.Fatalf("ParseQuery(%s): %v", src, err)
	}
	if pq.Relation == nil {
		b.Fatalf("ParseQuery(%s) did not produce a bare Relation", src)
	}
	ctx := &query.ResolveContext{Db: testDb, Config: testCfg}
	node, err := ctx.ResolveQuery(pq.Relation)
	if err != nil {
		b.Fatalf("ResolveQuery: %v", err)
	}
	if err := ctx.ResolveExpressions(node); err != nil {
		b.Fatalf("ResolveExpressions: %v", err)
	}
	if err := ctx.DeriveShapes(node); err != nil {
		b.Fatalf("DeriveShapes: %v", err)
	}
	return node
}

func mustCompileSelect(b *testing.B, node *query.QueryNode) (string, []any) {
	b.Helper()
	w, err := query.CompileSelect(node)
	if err != nil {
		b.Fatalf("CompileSelect: %v", err)
	}
	return w.String(), w.Args()
}

// runAndDecode executes sql/args and decodes every row's single "json"
// column, exactly like query/sql_test.go's own runSelect — returns the row
// count so callers can b.ReportMetric it (rows/call, matching
// write_bench_test.go's rows/call convention for its own batch-size case).
func runAndDecode(b *testing.B, sql string, args []any) int {
	b.Helper()
	rows, err := testDb.Pool.Query(context.Background(), sql, args...)
	if err != nil {
		b.Fatalf("query: %v\nsql: %s", err, sql)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			b.Fatalf("scan: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			b.Fatalf("unmarshal %s: %v", raw, err)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		b.Fatalf("rows: %v", err)
	}
	return n
}

// ---- 1. Flat list : filter + pagination -------------------------------
//
// The simplest realistic case : one table, an equality filter on an enum
// column, ordered, a real page size (20 rows), offset into the middle of
// the ~500 seeded bookings.

func BenchmarkSelect_FlatFilterPagination(b *testing.B) {
	node := mustResolveQuery(b, `{
		"relation": "bookings", "schema": "hotel",
		"select": ["own"],
		"where": ["=", "status", ["confirmed"]],
		"order_by": [["desc", "created_at"]],
		"limit": 20,
		"offset": 40
	}`)
	sql, args := mustCompileSelect(b, node)

	b.ResetTimer()
	var rows int
	for i := 0; i < b.N; i++ {
		rows = runAndDecode(b, sql, args)
	}
	b.ReportMetric(float64(rows), "rows/call")
}

func BenchmarkSelect_FlatFilterPagination_Compile(b *testing.B) {
	node := mustResolveQuery(b, `{
		"relation": "bookings", "schema": "hotel",
		"select": ["own"],
		"where": ["=", "status", ["confirmed"]],
		"order_by": [["desc", "created_at"]],
		"limit": 20,
		"offset": 40
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := query.CompileSelect(node); err != nil {
			b.Fatalf("CompileSelect: %v", err)
		}
	}
}

// ---- 2. One-hop join : bookings embedding their guest and room --------
//
// Both are OUTGOING (to-one) : bookings.guest_id -> guests.id and
// bookings.room_id -> rooms.id, each landing on the target's own primary
// key, so both are eligible regardless of any index on the FK column
// itself (### Join eligibility : the unique/"one" side never needs a
// separate index check).

const oneHopJoinQuery = `{
	"relation": "bookings", "schema": "hotel",
	"select": {"id": "id", "status": "status", "guest": "guest", "room": "room"},
	"join": {
		"guest": {"relation": "guests", "schema": "hotel", "on": {"id": "guest_id"},
			"select": {"id": "id", "first_name": "first_name", "last_name": "last_name", "email": "email"}},
		"room": {"relation": "rooms", "schema": "hotel", "on": {"id": "room_id"},
			"select": {"id": "id", "room_number": "room_number", "floor": "floor"}}
	},
	"order_by": [["desc", "created_at"]],
	"limit": 20
}`

func BenchmarkSelect_OneHopJoin(b *testing.B) {
	node := mustResolveQuery(b, oneHopJoinQuery)
	sql, args := mustCompileSelect(b, node)

	b.ResetTimer()
	var rows int
	for i := 0; i < b.N; i++ {
		rows = runAndDecode(b, sql, args)
	}
	b.ReportMetric(float64(rows), "rows/call")
}

func BenchmarkSelect_OneHopJoin_Compile(b *testing.B) {
	node := mustResolveQuery(b, oneHopJoinQuery)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := query.CompileSelect(node); err != nil {
			b.Fatalf("CompileSelect: %v", err)
		}
	}
}

// ---- 3. Embedded to-many : a property embedding its rooms -------------
//
// rooms.property_id IS indexed — the leading column of unique(property_id,
// room_number) — unlike most other incoming relations in this schema (see
// the package doc comment), which is exactly why this is the realistic
// to-many choice here rather than e.g. reviews.

func BenchmarkSelect_EmbeddedToMany(b *testing.B) {
	node := mustResolveQuery(b, `{
		"relation": "properties", "schema": "hotel",
		"select": {"id": "id", "name": "name", "rooms": "rooms"},
		"join": {"rooms": {"relation": "rooms", "schema": "hotel", "on": {"property_id": "id"}, "select": ["own"]}}
	}`)
	sql, args := mustCompileSelect(b, node)

	b.ResetTimer()
	var rows int
	for i := 0; i < b.N; i++ {
		rows = runAndDecode(b, sql, args)
	}
	b.ReportMetric(float64(rows), "rows/call")
}

// ---- 4. Deeper nested embed : booking -> room -> room_type/property ---
//
// Three levels, all outgoing (to-one) : bookings.room_id -> rooms.id,
// rooms.room_type_id -> room_types.id, rooms.property_id -> properties.id.
// All eligible regardless of indexing on the FK columns themselves, since
// every landing side is a primary key.

func BenchmarkSelect_DeepNestedEmbed(b *testing.B) {
	node := mustResolveQuery(b, `{
		"relation": "bookings", "schema": "hotel",
		"select": {"id": "id", "status": "status", "room": "room"},
		"join": {"room": {
			"relation": "rooms", "schema": "hotel", "on": {"id": "room_id"},
			"select": {"id": "id", "room_number": "room_number", "room_type": "room_type", "property": "property"},
			"join": {
				"room_type": {"relation": "room_types", "schema": "hotel", "on": {"id": "room_type_id"}, "select": ["own"]},
				"property": {"relation": "properties", "schema": "hotel", "on": {"id": "property_id"},
					"select": {"id": "id", "name": "name"}}
			}
		}},
		"limit": 20
	}`)
	sql, args := mustCompileSelect(b, node)

	b.ResetTimer()
	var rows int
	for i := 0; i < b.N; i++ {
		rows = runAndDecode(b, sql, args)
	}
	b.ReportMetric(float64(rows), "rows/call")
}

// ---- 5. Self-join : staff walking the manager_id hierarchy two levels -
//
// The fixture's own stated reason for existing (test/README.md : "the
// fixture's instance of query-engine.md's own self-join example"). Each
// "manager" hop is outgoing (staff.manager_id -> staff.id, the PK), so
// eligible regardless of the incoming direction (direct reports) being
// unindexed and therefore un-embeddable.

func BenchmarkSelect_SelfJoin(b *testing.B) {
	node := mustResolveQuery(b, `{
		"relation": "staff", "schema": "hotel",
		"select": {"id": "id", "name": "name", "role": "role", "manager": "manager"},
		"join": {"manager": {
			"relation": "staff", "schema": "hotel", "on": {"id": "manager_id"},
			"select": {"id": "id", "name": "name", "role": "role", "manager": "manager"},
			"join": {"manager": {"relation": "staff", "schema": "hotel", "on": {"id": "manager_id"},
				"select": {"id": "id", "name": "name", "role": "role"}}}
		}}
	}`)
	sql, args := mustCompileSelect(b, node)

	b.ResetTimer()
	var rows int
	for i := 0; i < b.N; i++ {
		rows = runAndDecode(b, sql, args)
	}
	b.ReportMetric(float64(rows), "rows/call")
}

// ---- 6. Aggregate-heavy : a property's rooms, embedded AND counted ----
//
// Deliberately both embeds "rooms" as an array AND aggregates over the
// exact same child (room_count via count(*)) — the dual-consumption shape
// that forces LATERAL sharing (see query/sql_test.go's own comment on the
// equivalent movie/director case) : the child is materialized once, read
// twice, rather than joined in twice. Read-path cost distinct from a plain
// join, per the task's own framing.

func BenchmarkSelect_AggregateHeavy(b *testing.B) {
	node := mustResolveQuery(b, `{
		"relation": "properties", "schema": "hotel",
		"select": {
			"id": "id", "name": "name", "rooms": "rooms",
			"room_count": ["agg", {"schema": "pg_catalog", "name": "count"}, ["rooms"]]
		},
		"join": {"rooms": {"relation": "rooms", "schema": "hotel", "on": {"property_id": "id"}, "select": ["own"]}}
	}`)
	sql, args := mustCompileSelect(b, node)

	b.ResetTimer()
	var rows int
	for i := 0; i < b.N; i++ {
		rows = runAndDecode(b, sql, args)
	}
	b.ReportMetric(float64(rows), "rows/call")
}

func BenchmarkSelect_AggregateHeavy_Compile(b *testing.B) {
	node := mustResolveQuery(b, `{
		"relation": "properties", "schema": "hotel",
		"select": {
			"id": "id", "name": "name", "rooms": "rooms",
			"room_count": ["agg", {"schema": "pg_catalog", "name": "count"}, ["rooms"]]
		},
		"join": {"rooms": {"relation": "rooms", "schema": "hotel", "on": {"property_id": "id"}, "select": ["own"]}}
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := query.CompileSelect(node); err != nil {
			b.Fatalf("CompileSelect: %v", err)
		}
	}
}

// ---- 7. Full text search : search_properties(query) --------------------
//
// A function-relation (SETOF hotel.properties), a genuinely different
// query shape from a plain table/join — hotel.search_properties runs
// websearch_to_tsquery against properties.description_search internally.
// "Located" is guaranteed to match : test/seed/seed's seeder appends
// "Located in <city>." to every generated property description.

func BenchmarkSelect_FullTextSearch(b *testing.B) {
	node := mustResolveQuery(b, `{
		"function": "search_properties", "schema": "hotel",
		"arguments": [["Located"]],
		"select": ["own"]
	}`)
	sql, args := mustCompileSelect(b, node)

	b.ResetTimer()
	var rows int
	for i := 0; i < b.N; i++ {
		rows = runAndDecode(b, sql, args)
	}
	b.ReportMetric(float64(rows), "rows/call")
}

// ---- 7b. RETURNS TABLE function root : hotel.booking_stats -------------
//
// A genuinely different resolution path from every other benchmark here
// (pg.Function.RecordRelation, not Type.Relation) — see the package doc
// comment above. property_id=1 is guaranteed to exist : test/seed/seed
// always seeds at least one property per chain, in insertion order
// starting at id 1.

func BenchmarkSelect_RecordFunction(b *testing.B) {
	node := mustResolveQuery(b, `{
		"function": "booking_stats", "schema": "hotel",
		"arguments": [1],
		"select": ["own"]
	}`)
	sql, args := mustCompileSelect(b, node)

	b.ResetTimer()
	var rows int
	for i := 0; i < b.N; i++ {
		rows = runAndDecode(b, sql, args)
	}
	b.ReportMetric(float64(rows), "rows/call")
}

func BenchmarkSelect_RecordFunction_Compile(b *testing.B) {
	node := mustResolveQuery(b, `{
		"function": "booking_stats", "schema": "hotel",
		"arguments": [1],
		"select": ["own"]
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := query.CompileSelect(node); err != nil {
			b.Fatalf("CompileSelect: %v", err)
		}
	}
}

// ---- 8. Filter-heavy : several combined where conditions --------------
//
// Stresses the filter EXPRESSION GRAMMAR's compilation specifically (not
// just execution) : an "in", a "between", and a tstzrange overlap ("&&",
// the exclusion-constraint operator itself), all "and"-ed together. The
// literal range is built from time.Now() (not hard-coded) so it always
// overlaps a meaningful slice of the seeded data regardless of when the
// benchmark runs — test/seed/seed's own bookings span roughly "now minus 6
// months" through "now plus a few days" (its horizon/offsetDays/nights
// arithmetic), so a 4-month window centered on "now minus 3 months" reliably
// intersects a real portion of it.

func filterHeavyQuery() string {
	now := time.Now().UTC()
	from := now.AddDate(0, -5, 0).Format("2006-01-02")
	to := now.AddDate(0, -1, 0).Format("2006-01-02")
	return fmt.Sprintf(`{
		"relation": "bookings", "schema": "hotel",
		"select": ["own"],
		"where": ["and",
			["in", "status", ["confirmed"], ["checked_out"]],
			["between", 1, "guest_id", 200],
			["&&", "stay", ["::", ["[%s,%s)"], "tstzrange"]]
		],
		"limit": 50
	}`, from, to)
}

func BenchmarkSelect_FilterHeavy(b *testing.B) {
	node := mustResolveQuery(b, filterHeavyQuery())
	sql, args := mustCompileSelect(b, node)

	b.ResetTimer()
	var rows int
	for i := 0; i < b.N; i++ {
		rows = runAndDecode(b, sql, args)
	}
	b.ReportMetric(float64(rows), "rows/call")
}

func BenchmarkSelect_FilterHeavy_Compile(b *testing.B) {
	node := mustResolveQuery(b, filterHeavyQuery())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := query.CompileSelect(node); err != nil {
			b.Fatalf("CompileSelect: %v", err)
		}
	}
}

// ---- 9. Wide/unscoped : every booking, embedding guest and room -------
//
// The deliberate "what does this cost at real volume, unbounded" stress
// case the task calls for explicitly : same shape as #2 (one-hop join),
// but with NO limit — all ~500 seeded bookings, each embedding its guest
// and room, in one response.

const wideUnscopedQuery = `{
	"relation": "bookings", "schema": "hotel",
	"select": {"id": "id", "status": "status", "guest": "guest", "room": "room"},
	"join": {
		"guest": {"relation": "guests", "schema": "hotel", "on": {"id": "guest_id"},
			"select": {"id": "id", "first_name": "first_name", "last_name": "last_name", "email": "email"}},
		"room": {"relation": "rooms", "schema": "hotel", "on": {"id": "room_id"},
			"select": {"id": "id", "room_number": "room_number", "floor": "floor"}}
	}
}`

func BenchmarkSelect_WideUnscoped(b *testing.B) {
	node := mustResolveQuery(b, wideUnscopedQuery)
	sql, args := mustCompileSelect(b, node)

	b.ResetTimer()
	var rows int
	for i := 0; i < b.N; i++ {
		rows = runAndDecode(b, sql, args)
	}
	b.ReportMetric(float64(rows), "rows/call")
}

func BenchmarkSelect_WideUnscoped_Compile(b *testing.B) {
	node := mustResolveQuery(b, wideUnscopedQuery)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := query.CompileSelect(node); err != nil {
			b.Fatalf("CompileSelect: %v", err)
		}
	}
}
