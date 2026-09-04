// Benchmarks for ExecuteWrite's write path (outgoing relationships). Each
// iteration truncates "_data" first — __row_id is a PRIMARY KEY, so replays collide.
package query

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// acquireWriteConnB is acquireWriteConn's *testing.B twin; see its doc
// comment in write_test.go for why a pinned connection is required.
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

// truncateDataB clears "_data" with the timer stopped, so only ExecuteWrite's
// own cost is measured (see the package comment for why truncation resets).
func truncateDataB(b *testing.B, conn *pgxpool.Conn) {
	b.Helper()
	b.StopTimer()
	if _, err := conn.Exec(context.Background(), `truncate _data`); err != nil {
		b.Fatalf("truncate _data: %v", err)
	}
	b.StartTimer()
}

// bMustResolveQuery is mustResolveQuery's *testing.B twin (not generalized
// to testing.TB; mustResolveQuery is hard-wired to *testing.T).
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
// director.studio_id is a nullable FK added for this case.

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

// ---- 4. Wide outgoing fan-out : order_t -> customer (x2, sibling FKs) ----
// customer_id and billing_customer_id are two distinct FKs to the same relation.

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
// Each b.Run measures one ExecuteWrite call ; b.N is the repeat count, orthogonal to payload row count N.

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

// buildBatchPayload builds n movie rows named by (iter, row) so every call
// across every b.N iteration writes genuinely new rows.
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
// director (root) with an outgoing "studio" child and incoming "movies" at once.

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
