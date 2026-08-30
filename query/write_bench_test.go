// Benchmarks for the write path (ExecuteWrite / ExecuteWriteState), focused
// per the driving request on OUTGOING relationships specifically : a child
// the root's own foreign key points AT (director under movie, studio under
// director — see pg/testdata/schema.sql's studio/director.studio_id, added
// for this file since the fixture previously had no outgoing chain deeper
// than one level). Run with e.g.:
//
//	go test ./query/... -run=^$ -bench=. -benchtime=1x
//
// ## What "one iteration" measures
//
// Every benchmark below calls ExecuteWrite once per b.N iteration, against a
// FRESH payload each time (a running counter embedded in a name/title
// field) — this is deliberate, not incidental :
//
//   - The root's default write_mode is INSERT (see node_resolve_test.go's
//     TestResolveQuery_SimpleRelation), and no column touched by these
//     benchmarks carries a unique constraint (movie.title, director.name,
//     studio.name are all plain text) — so even a byte-for-byte IDENTICAL
//     payload replayed b.N times would still insert b.N genuinely distinct
//     rows (new serial pk each time), never hit an update path.
//   - An OUTGOING child's default write_mode is UPSERT (on_conflict on its
//     own primary key). The payload never supplies that child's id, so the
//     upsert's own "on conflict" clause never actually fires — it exercises
//     the upsert SQL shape (### Insertion/Updates' "upsert" mechanism) on
//     its unconditional-insert path, not its update-on-conflict path. This
//     is called out explicitly because "upsert" sounds like it should mean
//     something else — what's measured here is the cost of the upsert
//     MECHANISM (extra RETURNING + correlation join vs. plain insert),
//     not conflict-resolution cost.
//   - The counter is embedded anyway (not strictly required for the flat
//     insert case, but IS required to be a meaningfully "fresh" object for
//     the batch-scaling case, where b.N stays 1 test-run over per case and
//     the row count comes from the payload's own array length instead) so
//     every benchmark's payload-construction code looks the same and the
//     "why a counter" reasoning doesn't have to be re-derived per case.
//
// Each benchmark truncates "_data" between iterations (b.StopTimer'd, so the
// truncate itself isn't measured) — this mirrors the real per-request
// lifecycle, not an arbitrary simplification : specs/querying.md's
// ## Response Shape is explicit that "_data" truncation "happens once, at
// the very end of the whole request, after the response has been fully
// sent", i.e. each request starts against an empty "_data" and a fresh
// WriteState (see ExecuteWrite's own doc comment — __row_id/__node_id both
// restart at 0 per call). That restart is also load-bearing for
// correctness here, not just realism : "_data".__row_id is a PRIMARY KEY
// (see DataTableDDL), so replaying ExecuteWrite b.N times against the SAME
// un-truncated "_data" collides on __row_id the moment b.N > 1 — every
// benchmark below would work at -benchtime=1x (b.N==1, no second call to
// collide with) and then fail outright under a real `go test -bench=.` run,
// which is exactly the gap a 1x-only verification pass can't surface.
// Threading one shared WriteState across iterations instead (continuing the
// row-id sequence rather than truncating) would also avoid the collision,
// but would let "_data" grow unboundedly across b.N iterations and make
// later iterations scan an ever-larger table — not the steady-state,
// per-request condition being measured here.
package query

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// acquireWriteConnB is acquireWriteConn's *testing.B twin — see its doc
// comment in write_test.go for why a single pinned connection (not the
// pool) is required for writes at all.
func acquireWriteConnB(b *testing.B) *pgxpool.Conn {
	b.Helper()
	conn, err := testDb.Pool.Acquire(context.Background())
	if err != nil {
		b.Fatalf("acquire: %v", err)
	}
	b.Cleanup(conn.Release)
	if _, err := conn.Exec(context.Background(), `drop table if exists _data`); err != nil {
		b.Fatalf("drop _data: %v", err)
	}
	if _, err := conn.Exec(context.Background(), DataTableDDL); err != nil {
		b.Fatalf("create _data: %v", err)
	}
	return conn
}

// truncateDataB clears "_data" between iterations, timer stopped around the
// truncate itself so only ExecuteWrite's own cost is measured — see the
// package doc comment above for why this (not a shared WriteState) is the
// right per-iteration reset.
func truncateDataB(b *testing.B, conn *pgxpool.Conn) {
	b.Helper()
	b.StopTimer()
	if _, err := conn.Exec(context.Background(), `truncate _data`); err != nil {
		b.Fatalf("truncate _data: %v", err)
	}
	b.StartTimer()
}

// bMustResolveQuery is mustResolveQuery's *testing.B twin (mustResolveQuery
// itself is hard-wired to *testing.T via mustParseRelation, not worth
// generalizing to testing.TB for one file's sake).
func bMustResolveQuery(b *testing.B, src string) *QueryNode {
	b.Helper()
	pq, err := ParseQuery([]byte(src))
	if err != nil {
		b.Fatalf("ParseQuery(%s): %v", src, err)
	}
	if pq.Relation == nil {
		b.Fatalf("ParseQuery(%s) did not produce a bare Relation: %#v", src, pq)
	}
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
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

// ---- 1. Baseline : flat single-relation insert, no relationships at all ----

func BenchmarkExecuteWrite_FlatInsert(b *testing.B) {
	conn := acquireWriteConnB(b)
	ctx := context.Background()
	node := bMustResolveQuery(b, `{"relation": "director", "schema": "public", "select": ["own"], "write_mode": "insert"}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		payload := fmt.Appendf(nil, `[{"name": "Bench Director %d"}]`, i)
		truncateDataB(b, conn)
		if _, err := ExecuteWrite(ctx, conn, node, payload); err != nil {
			b.Fatalf("ExecuteWrite: %v", err)
		}
	}
}

// ---- 2. One outgoing child : movie -> director (mirrors TestExecuteWrite_OutgoingChildFK) ----

func BenchmarkExecuteWrite_OneOutgoingChild(b *testing.B) {
	conn := acquireWriteConnB(b)
	ctx := context.Background()
	node := bMustResolveQuery(b, `{
		"relation": "movie", "schema": "public",
		"select": {"id": "id", "title": "title", "director": "director"},
		"write_mode": "insert",
		"join": {"director": {"relation": "director", "schema": "public", "on": {"id": "director_id"}, "select": ["own"]}}
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		payload := fmt.Appendf(nil, `[{"title": "Bench Movie %d", "director": {"name": "Bench Director %d"}}]`, i, i)
		truncateDataB(b, conn)
		if _, err := ExecuteWrite(ctx, conn, node, payload); err != nil {
			b.Fatalf("ExecuteWrite: %v", err)
		}
	}
}

// ---- 3. Deep outgoing chain : movie -> director -> studio (3 levels) ----
//
// director.studio_id is a new nullable FK added to pg/testdata/schema.sql
// specifically for this case — the existing fixture had no outgoing
// relation chained two levels deep (director itself had no outgoing FK of
// its own before this).

func BenchmarkExecuteWrite_DeepOutgoingChain(b *testing.B) {
	conn := acquireWriteConnB(b)
	ctx := context.Background()
	node := bMustResolveQuery(b, `{
		"relation": "movie", "schema": "public",
		"select": {"id": "id", "title": "title", "director": "director"},
		"write_mode": "insert",
		"join": {"director": {
			"relation": "director", "schema": "public", "on": {"id": "director_id"},
			"select": {"id": "id", "name": "name", "studio": "studio"},
			"join": {"studio": {"relation": "studio", "schema": "public", "on": {"id": "studio_id"}, "select": ["own"]}}
		}}
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		payload := fmt.Appendf(nil, `[{"title": "Bench Chain Movie %d", "director": {"name": "Bench Chain Director %d", "studio": {"name": "Bench Studio %d"}}}]`, i, i, i)
		truncateDataB(b, conn)
		if _, err := ExecuteWrite(ctx, conn, node, payload); err != nil {
			b.Fatalf("ExecuteWrite: %v", err)
		}
	}
}

// ---- 4. Wide outgoing fan-out : order_t -> customer (x2, sibling FKs to the same relation) ----
//
// order_t.customer_id and order_t.billing_customer_id are two DISTINCT
// outgoing FKs to the same target relation (customer) — already present in
// the fixture (pg/testdata/schema.sql, added for pg package composite-FK
// tests), so no schema change was needed for this one.

func BenchmarkExecuteWrite_WideOutgoingFanout(b *testing.B) {
	conn := acquireWriteConnB(b)
	ctx := context.Background()
	node := bMustResolveQuery(b, `{
		"relation": "order_t", "schema": "public",
		"select": {"id": "id", "customer": "customer", "billing_customer": "billing_customer"},
		"write_mode": "insert",
		"join": {
			"customer": {"relation": "customer", "schema": "public", "on": {"id": "customer_id"}, "select": ["own"]},
			"billing_customer": {"relation": "customer", "schema": "public", "on": {"id": "billing_customer_id"}, "select": ["own"]}
		}
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		payload := []byte(`[{"customer": {}, "billing_customer": {}}]`)
		truncateDataB(b, conn)
		if _, err := ExecuteWrite(ctx, conn, node, payload); err != nil {
			b.Fatalf("ExecuteWrite: %v", err)
		}
	}
}

// ---- 5. Batch size scaling : one outgoing child, N rows per payload ----
//
// Each b.Run measures ONE ExecuteWrite call whose payload's root array holds
// N rows, repeated b.N times (b.N here being go test's own iteration count
// for statistical stability, orthogonal to the payload's row count N) — use
// b.ReportMetric to see a per-row-write figure alongside the per-call one,
// and compare across N to see whether cost stays linear or degrades.

func BenchmarkExecuteWrite_BatchSize(b *testing.B) {
	for _, n := range []int{1, 10, 100, 1000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			conn := acquireWriteConnB(b)
			ctx := context.Background()
			node := bMustResolveQuery(b, `{
				"relation": "movie", "schema": "public",
				"select": {"id": "id", "title": "title", "director": "director"},
				"write_mode": "insert",
				"join": {"director": {"relation": "director", "schema": "public", "on": {"id": "director_id"}, "select": ["own"]}}
			}`)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				payload := buildBatchPayload(n, i)
				if _, err := conn.Exec(ctx, `truncate _data`); err != nil {
					b.Fatalf("truncate _data: %v", err)
				}
				b.StartTimer()
				if _, err := ExecuteWrite(ctx, conn, node, payload); err != nil {
					b.Fatalf("ExecuteWrite: %v", err)
				}
			}
			b.ReportMetric(float64(n), "rows/call")
		})
	}
}

// buildBatchPayload builds n root-level movie rows, each with its own
// outgoing director, uniquely named via (iter, row-within-batch) so every
// call across every b.N iteration writes genuinely new rows.
func buildBatchPayload(n, iter int) []byte {
	buf := []byte{'['}
	for i := 0; i < n; i++ {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = fmt.Appendf(buf, `{"title": "Batch Movie %d-%d", "director": {"name": "Batch Director %d-%d"}}`, iter, i, iter, i)
	}
	buf = append(buf, ']')
	return buf
}

// ---- 6. Mixed outgoing + incoming in the same payload ----
//
// director (root) with an outgoing "studio" child (director.studio_id) AND
// an incoming "movies" child (movie.director_id) at the same time — the
// realistic "nested write touching both directions at once" shape, per
// query.ts director/movie/studio all coexisting in one tree.

func BenchmarkExecuteWrite_MixedOutgoingIncoming(b *testing.B) {
	conn := acquireWriteConnB(b)
	ctx := context.Background()
	node := bMustResolveQuery(b, `{
		"relation": "director", "schema": "public",
		"select": {"id": "id", "name": "name", "studio": "studio", "movies": "movies"},
		"write_mode": "insert",
		"join": {
			"studio": {"relation": "studio", "schema": "public", "on": {"id": "studio_id"}, "select": ["own"]},
			"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": ["own"]}
		}
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		payload := fmt.Appendf(nil, `[{
			"name": "Bench Mixed Director %d",
			"studio": {"name": "Bench Mixed Studio %d"},
			"movies": [{"title": "Bench Mixed Movie A %d"}, {"title": "Bench Mixed Movie B %d"}]
		}]`, i, i, i, i)
		truncateDataB(b, conn)
		if _, err := ExecuteWrite(ctx, conn, node, payload); err != nil {
			b.Fatalf("ExecuteWrite: %v", err)
		}
	}
}
