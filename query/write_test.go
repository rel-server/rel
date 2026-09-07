package query

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// acquireWriteConn returns one pinned connection with a fresh "_data" temp
// table — writes need one physical connection throughout (temp tables are connection-scoped).
func acquireWriteConn(t *testing.T) *pgxpool.Conn {
	t.Helper()
	conn, err := testDb.Pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Cleanup(conn.Release)
	// The pool can hand back a connection with a leftover "_data" from a
	// previous test (temp tables outlive Release) — DROP first for idempotency.
	if _, err := conn.Exec(context.Background(), `drop table if exists _data`); err != nil {
		t.Fatalf("drop _data: %v", err)
	}
	if _, err := conn.Exec(context.Background(), DataTableDDL); err != nil {
		t.Fatalf("create _data: %v", err)
	}
	return conn
}

func dataKeysFor(t *testing.T, conn *pgxpool.Conn, nodeID int) []map[string]any {
	t.Helper()
	rows, err := conn.Query(context.Background(), `select keys from _data where __node_id = $1 order by __row_id`, nodeID)
	if err != nil {
		t.Fatalf("query _data: %v", err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan: %v", err)
		}
		var m map[string]any
		if raw != nil {
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("unmarshal keys %s: %v", raw, err)
			}
		}
		out = append(out, m)
	}
	return out
}

func TestExecuteWrite_PlainInsert(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": ["own"], "write_mode": "insert"}`)

	result, err := ExecuteWrite(ctx, conn, node, []byte(`[{"name": "Insert Director"}]`))
	if err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}
	if result.RowCount != 1 {
		t.Fatalf("expected 1 row, got %d", result.RowCount)
	}

	keys := dataKeysFor(t, conn, result.NodeIDs[node])
	if len(keys) != 1 {
		t.Fatalf("expected 1 keys row, got %d : %#v", len(keys), keys)
	}
	if _, ok := keys[0]["id"]; !ok {
		t.Fatalf("expected recovered id in keys, got %#v", keys[0])
	}

	var name string
	if err := conn.QueryRow(ctx, `select name from director where id = ($1::jsonb->>'id')::int`, mustJSON(t, keys[0])).Scan(&name); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if name != "Insert Director" {
		t.Errorf("expected name=Insert Director, got %q", name)
	}
}

func TestExecuteWrite_IncomingChildFK(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	// Incoming "movies" child (default write_mode merge) : movie.director_id
	// must resolve from the just-inserted director's own key, not the payload.
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"id": "id", "name": "name", "movies": "movies"},
		"write_mode": "insert",
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": ["own"]}}
	}`)
	movieNode := node.IncomingNodes[0]

	payload := []byte(`[{"name": "FK Director", "movies": [{"title": "Movie One"}, {"title": "Movie Two"}]}]`)
	result, err := ExecuteWrite(ctx, conn, node, payload)
	if err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}

	directorKeys := dataKeysFor(t, conn, result.NodeIDs[node])
	if len(directorKeys) != 1 {
		t.Fatalf("expected 1 director keys row, got %d", len(directorKeys))
	}
	movieKeys := dataKeysFor(t, conn, result.NodeIDs[movieNode])
	if len(movieKeys) != 2 {
		t.Fatalf("expected 2 movie keys rows, got %d : %#v", len(movieKeys), movieKeys)
	}

	var count int
	if err := conn.QueryRow(ctx, `
		select count(*) from movie m
		join director d on d.id = m.director_id
		where d.name = 'FK Director'
	`).Scan(&count); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 movies linked to FK Director, got %d", count)
	}
}

func TestExecuteWrite_OutgoingChildFK(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	// Outgoing "director" child (default write_mode upsert) : movie.director_id
	// resolves from the child's own recovered key via outgoingKeySource's extra left join, not the plain "par" join.
	node := mustResolveQuery(t, `{
		"relation": "movie", "schema": "public",
		"select": {"id": "id", "title": "title", "director": "director"},
		"write_mode": "insert",
		"join": {"director": {"relation": "director", "schema": "public", "on": {"id": "director_id"}, "select": ["own"]}}
	}`)
	directorNode := node.OutgoingNodes[0]

	payload := []byte(`[{"title": "Outgoing Movie", "director": {"name": "Outgoing Director"}}]`)
	result, err := ExecuteWrite(ctx, conn, node, payload)
	if err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}

	movieKeys := dataKeysFor(t, conn, result.NodeIDs[node])
	directorKeys := dataKeysFor(t, conn, result.NodeIDs[directorNode])
	if len(movieKeys) != 1 || len(directorKeys) != 1 {
		t.Fatalf("expected 1 movie + 1 director keys row, got %d/%d", len(movieKeys), len(directorKeys))
	}

	var count int
	if err := conn.QueryRow(ctx, `
		select count(*) from movie m
		join director d on d.id = m.director_id
		where m.title = 'Outgoing Movie' and d.name = 'Outgoing Director'
	`).Scan(&count); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected movie correctly linked to its new director, got count=%d", count)
	}
}

func TestExecuteWrite_Update(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	var directorID int
	if err := conn.QueryRow(ctx, `insert into director (name) values ('Before Update') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": ["own"], "write_mode": "update"}`)
	payload := []byte(`[{"id": ` + itoa(directorID) + `, "name": "After Update"}]`)
	if _, err := ExecuteWrite(ctx, conn, node, payload); err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}

	var name string
	if err := conn.QueryRow(ctx, `select name from director where id = $1`, directorID).Scan(&name); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if name != "After Update" {
		t.Errorf("expected name=After Update, got %q", name)
	}
}

// TestExecuteWrite_UnwritableSelect_Rejected proves a select omitting the
// identity column ("id") is rejected up front (## Configuration), not silently writing a phantom identity — see write.go's findUnwritableNode.
func TestExecuteWrite_UnwritableSelect_Rejected(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	var directorID int
	if err := conn.QueryRow(ctx, `insert into director (name) values ('Before Unwritable Update') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": {"name": "name"}, "write_mode": "update"}`)
	payload := []byte(`[{"name": "Renamed"}]`)
	if _, err := ExecuteWrite(ctx, conn, node, payload); err == nil {
		t.Fatalf("expected ExecuteWrite to reject a write whose select omits the identity column")
	}

	var name string
	if err := conn.QueryRow(ctx, `select name from director where id = $1`, directorID).Scan(&name); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if name != "Before Unwritable Update" {
		t.Errorf("expected the row untouched, got name=%q", name)
	}
}

// TestExecuteWrite_FunctionRootUnwritable_EvenWithRealTableRelation proves
// a function root is never writable even with a real Relation (### Function-rooted nodes) — shape.go's IsFunction() guard.
func TestExecuteWrite_FunctionRootUnwritable_EvenWithRealTableRelation(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	node := mustResolveQuery(t, `{"function": "fn_directors", "schema": "public", "select": ["own"], "write_mode": "insert"}`)
	payload := []byte(`[{"name": "Via Function Root"}]`)
	if _, err := ExecuteWrite(ctx, conn, node, payload); err == nil {
		t.Fatalf("expected ExecuteWrite to reject a write through a function-rooted node")
	}

	var count int
	if err := conn.QueryRow(ctx, `select count(*) from director where name = $1`, "Via Function Root").Scan(&count); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if count != 0 {
		t.Errorf("expected no row written through the function-rooted node, found %d", count)
	}
}

func TestExecuteWrite_Upsert(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	var directorID int
	if err := conn.QueryRow(ctx, `insert into director (name) values ('Before Upsert') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": ["own"], "write_mode": "upsert"}`)

	// Two rows in one payload : one conflicts (existing id, updates in
	// place), one is fresh (no id, gets inserted).
	payload := []byte(`[
		{"id": ` + itoa(directorID) + `, "name": "After Upsert"},
		{"name": "Fresh Upsert Director"}
	]`)
	result, err := ExecuteWrite(ctx, conn, node, payload)
	if err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}

	keys := dataKeysFor(t, conn, result.NodeIDs[node])
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys rows, got %d : %#v", len(keys), keys)
	}
	for _, k := range keys {
		if _, ok := k["id"]; !ok {
			t.Errorf("expected recovered id in every keys row, got %#v", k)
		}
	}

	var name string
	if err := conn.QueryRow(ctx, `select name from director where id = $1`, directorID).Scan(&name); err != nil {
		t.Fatalf("select back conflicting row: %v", err)
	}
	if name != "After Upsert" {
		t.Errorf("expected conflicting row updated to After Upsert, got %q", name)
	}

	var count int
	if err := conn.QueryRow(ctx, `select count(*) from director where name = 'Fresh Upsert Director'`).Scan(&count); err != nil {
		t.Fatalf("select back fresh row: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected fresh row inserted, got count=%d", count)
	}
}

// TestExecuteWriteStateParamsOpts_Sql proves `sql` collects each DML
// statement's compiled text without needing execution to have been wrapped
// in anything extra (specs/complex-query.md ## sql : "adds zero DB round trips").
func TestExecuteWriteStateParamsOpts_Sql(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": ["own"], "write_mode": "insert"}`)
	payload := []byte(`[{"name": "Sql Insert Director"}]`)

	result, err := ExecuteWriteStateParamsOpts(ctx, conn, node, payload, &WriteState{}, nil, WriteOptions{Sql: true})
	if err != nil {
		t.Fatalf("ExecuteWriteStateParamsOpts: %v", err)
	}
	if len(result.Sql) != 1 {
		t.Fatalf("expected 1 SqlResult, got %d", len(result.Sql))
	}
	if result.Sql[0].Insert == "" {
		t.Fatalf("expected Insert to carry the compiled statement text, got empty")
	}
	if result.Sql[0].Update != "" || result.Sql[0].Delete != "" || result.Sql[0].Upsert != "" {
		t.Errorf("expected only Insert set, got %#v", result.Sql[0])
	}
}

// TestExecuteWriteStateParamsOpts_QueryPlan proves `query_plan` runs the
// DML statement for real (EXPLAIN ANALYZE), reporting a plan and leaving
// the actual write in place — specs/complex-query.md ## query_plan.
func TestExecuteWriteStateParamsOpts_QueryPlan(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": ["own"], "write_mode": "insert"}`)
	payload := []byte(`[{"name": "Query Plan Insert Director"}]`)

	result, err := ExecuteWriteStateParamsOpts(ctx, conn, node, payload, &WriteState{}, nil, WriteOptions{QueryPlan: true})
	if err != nil {
		t.Fatalf("ExecuteWriteStateParamsOpts: %v", err)
	}
	if len(result.QueryPlan) != 1 {
		t.Fatalf("expected 1 PlanResult, got %d", len(result.QueryPlan))
	}
	if len(result.QueryPlan[0].Insert) == 0 {
		t.Fatalf("expected Insert to carry a plan, got empty")
	}
	var plan []map[string]any
	if err := json.Unmarshal(result.QueryPlan[0].Insert, &plan); err != nil {
		t.Fatalf("expected valid EXPLAIN JSON, got %s: %v", result.QueryPlan[0].Insert, err)
	}
	if len(plan) == 0 || plan[0]["Plan"] == nil {
		t.Fatalf("expected a \"Plan\" key in the EXPLAIN output, got %s", result.QueryPlan[0].Insert)
	}

	// The statement still ran for real — its side effect persists.
	var count int
	if err := conn.QueryRow(ctx, `select count(*) from director where name = 'Query Plan Insert Director'`).Scan(&count); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected the row actually inserted despite EXPLAIN ANALYZE wrapping, got count=%d", count)
	}
}

// TestExecuteWriteStateParamsStats_Insert proves stats reports the insert
// count, and that collectStats=false leaves WriteResult.Stats nil
// (specs/complex-query.md ## stats).
func TestExecuteWriteStateParamsStats_Insert(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": ["own"], "write_mode": "insert"}`)
	payload := []byte(`[{"name": "Stats Insert A"}, {"name": "Stats Insert B"}]`)

	result, err := ExecuteWriteStateParamsStats(ctx, conn, node, payload, &WriteState{}, nil, true)
	if err != nil {
		t.Fatalf("ExecuteWriteStateParamsStats: %v", err)
	}
	if len(result.Stats) != 1 {
		t.Fatalf("expected 1 Stat, got %d : %#v", len(result.Stats), result.Stats)
	}
	s := result.Stats[0]
	if len(s.Path) != 0 {
		t.Errorf("expected root path [], got %#v", s.Path)
	}
	if s.Table != "public.director" {
		t.Errorf("expected table public.director, got %q", s.Table)
	}
	if s.Submitted != 2 || s.Inserted != 2 || s.Updated != 0 || s.Deleted != 0 {
		t.Errorf("expected submitted=2 inserted=2 updated=0 deleted=0, got %#v", s)
	}

	// collectStats=false is the default (ExecuteWrite/ExecuteWriteState/
	// ExecuteWriteStateParams all route through it) — no stats collected.
	conn2 := acquireWriteConn(t)
	node2 := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": ["own"], "write_mode": "insert"}`)
	result2, err := ExecuteWrite(ctx, conn2, node2, []byte(`[{"name": "No Stats Director"}]`))
	if err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}
	if result2.Stats != nil {
		t.Errorf("expected nil Stats when collectStats wasn't requested, got %#v", result2.Stats)
	}
}

// TestExecuteWriteStateParamsStats_Update proves stats reports the actual
// updated count, not the submitted count, when a row's identity doesn't match.
func TestExecuteWriteStateParamsStats_Update(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	var directorID int
	if err := conn.QueryRow(ctx, `insert into director (name) values ('Stats Update Before') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": ["own"], "write_mode": "update"}`)
	// One row matches an existing id (updates), one doesn't (affects 0 rows).
	payload := []byte(`[{"id": ` + itoa(directorID) + `, "name": "Stats Update After"}, {"id": 999999999, "name": "No Match"}]`)

	result, err := ExecuteWriteStateParamsStats(ctx, conn, node, payload, &WriteState{}, nil, true)
	if err != nil {
		t.Fatalf("ExecuteWriteStateParamsStats: %v", err)
	}
	if len(result.Stats) != 1 {
		t.Fatalf("expected 1 Stat, got %d : %#v", len(result.Stats), result.Stats)
	}
	s := result.Stats[0]
	if s.Submitted != 2 {
		t.Errorf("expected submitted=2, got %d", s.Submitted)
	}
	if s.Updated != 1 {
		t.Errorf("expected updated=1 (only the matching row), got %d", s.Updated)
	}

	var name string
	if err := conn.QueryRow(ctx, `select name from director where id = $1`, directorID).Scan(&name); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if name != "Stats Update After" {
		t.Errorf("expected name=Stats Update After, got %q", name)
	}
}

// TestExecuteWriteStateParamsStats_Upsert proves the xmax-based split
// distinguishes inserted from updated rows within one upsert statement.
func TestExecuteWriteStateParamsStats_Upsert(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	var directorID int
	if err := conn.QueryRow(ctx, `insert into director (name) values ('Stats Upsert Before') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": ["own"], "write_mode": "upsert"}`)
	payload := []byte(`[
		{"id": ` + itoa(directorID) + `, "name": "Stats Upsert After"},
		{"name": "Stats Upsert Fresh"}
	]`)

	result, err := ExecuteWriteStateParamsStats(ctx, conn, node, payload, &WriteState{}, nil, true)
	if err != nil {
		t.Fatalf("ExecuteWriteStateParamsStats: %v", err)
	}
	if len(result.Stats) != 1 {
		t.Fatalf("expected 1 Stat, got %d : %#v", len(result.Stats), result.Stats)
	}
	s := result.Stats[0]
	if s.Submitted != 2 {
		t.Errorf("expected submitted=2, got %d", s.Submitted)
	}
	if s.Inserted != 1 || s.Updated != 1 {
		t.Errorf("expected inserted=1 updated=1, got %#v", s)
	}

	keys := dataKeysFor(t, conn, result.NodeIDs[node])
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys rows (upsert side effects unaffected by stats collection), got %d", len(keys))
	}
}

// TestExecuteWriteStateParamsStats_Delete proves a delete-bearing mode
// reports its Deleted count.
func TestExecuteWriteStateParamsStats_Delete(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	var directorID int
	if err := conn.QueryRow(ctx, `insert into director (name) values ('Stats Delete Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	if _, err := conn.Exec(ctx, `insert into movie (director_id, title) values ($1, 'Stats Delete Movie A'), ($1, 'Stats Delete Movie B')`, directorID); err != nil {
		t.Fatalf("insert movies: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "director", "schema": "public",
		"select": ["own"],
		"where": ["=", "id", %d],
		"write_mode": "update",
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "write_mode": "deleteonly"}}
	}`, directorID))
	// Payload carries no movies at all — deleteonly removes every existing one.
	payload := []byte(`[{"id": ` + itoa(directorID) + `, "name": "Stats Delete Director"}]`)

	result, err := ExecuteWriteStateParamsStats(ctx, conn, node, payload, &WriteState{}, nil, true)
	if err != nil {
		t.Fatalf("ExecuteWriteStateParamsStats: %v", err)
	}

	var moviesStat *Stat
	for i := range result.Stats {
		if result.Stats[i].Table == "public.movie" {
			moviesStat = &result.Stats[i]
		}
	}
	if moviesStat == nil {
		t.Fatalf("expected a Stat for public.movie, got %#v", result.Stats)
	}
	if len(moviesStat.Path) != 1 || moviesStat.Path[0] != "movies" {
		t.Errorf(`expected path ["movies"], got %#v`, moviesStat.Path)
	}
	if moviesStat.Submitted != 0 {
		t.Errorf("expected submitted=0 (unpopulated, deleted via parent's population), got %d", moviesStat.Submitted)
	}
	if moviesStat.Deleted != 2 {
		t.Errorf("expected deleted=2, got %d", moviesStat.Deleted)
	}
}

func TestExecuteWrite_MergeDeletesAbsentRows(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	var directorID int
	if err := conn.QueryRow(ctx, `insert into director (name) values ('Merge Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	var keepID, dropID int
	if err := conn.QueryRow(ctx, `insert into movie (director_id, title) values ($1, 'Keep Me') returning id`, directorID).Scan(&keepID); err != nil {
		t.Fatalf("insert movie: %v", err)
	}
	if err := conn.QueryRow(ctx, `insert into movie (director_id, title) values ($1, 'Drop Me') returning id`, directorID).Scan(&dropID); err != nil {
		t.Fatalf("insert movie: %v", err)
	}

	// Default incoming write_mode "merge" : "Keep Me" upserts in place,
	// "Drop Me" (absent) gets deleted, "New One" is a fresh insert.
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"id": "id", "movies": "movies"},
		"write_mode": "update",
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": ["own"]}}
	}`)
	payload := fmt.Appendf(nil, `[{"id": %d, "movies": [{"id": %d, "title": "Keep Me Renamed"}, {"title": "New One"}]}]`, directorID, keepID)

	if _, err := ExecuteWrite(ctx, conn, node, payload); err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}

	var titles []string
	rows, err := conn.Query(ctx, `select title from movie where director_id = $1 order by title`, directorID)
	if err != nil {
		t.Fatalf("select back: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			t.Fatalf("scan: %v", err)
		}
		titles = append(titles, title)
	}
	if len(titles) != 2 || titles[0] != "Keep Me Renamed" || titles[1] != "New One" {
		t.Fatalf("expected [Keep Me Renamed, New One], got %v", titles)
	}
}

// TestExecuteWrite_MergeAbsentIncomingKeyDeletesAllChildren proves an
// incoming child entirely OMITTED from the payload still gets every row deleted — phase 2 gates on the PARENT's population, not the child's own.
func TestExecuteWrite_MergeAbsentIncomingKeyDeletesAllChildren(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	var directorID int
	if err := conn.QueryRow(ctx, `insert into director (name) values ('Absent Movies Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	if _, err := conn.Exec(ctx, `insert into movie (director_id, title) values ($1, 'Should Be Deleted')`, directorID); err != nil {
		t.Fatalf("insert movie: %v", err)
	}

	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"id": "id", "movies": "movies"},
		"write_mode": "update",
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": ["own"]}}
	}`)
	// "movies" key entirely absent — not even "[]".
	payload := fmt.Appendf(nil, `[{"id": %d}]`, directorID)

	if _, err := ExecuteWrite(ctx, conn, node, payload); err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}

	var count int
	if err := conn.QueryRow(ctx, `select count(*) from movie where director_id = $1`, directorID).Scan(&count); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected every movie deleted (payload supplied no movies at all), got %d left", count)
	}
}

// countingQuerier wraps a Querier, counting Exec/Query calls, to prove the
// noop-skip optimization actually skips statements. CopyFrom passes through unwrapped — always needed regardless of which nodes end up populated.
type countingQuerier struct {
	Querier
	execs   int
	queries int
}

func (c *countingQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	c.execs++
	return c.Querier.Exec(ctx, sql, args...)
}

func (c *countingQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.queries++
	return c.Querier.Query(ctx, sql, args...)
}

// TestExecuteWrite_NullOutgoingSkipsChildStatements proves the skip itself
// fires (not just that it's behavior-preserving) : supplying null for the nullable outgoing "studio" must skip its phase1 statement(s) entirely.
func TestExecuteWrite_NullOutgoingSkipsChildStatements(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"id": "id", "name": "name", "studio": "studio"},
		"write_mode": "insert",
		"join": {"studio": {"relation": "studio", "schema": "public", "on": {"id": "studio_id"}, "select": ["own"]}}
	}`)
	studioNode := node.OutgoingNodes[0]

	counting := &countingQuerier{Querier: conn}
	payload := []byte(`[{"name": "No Studio Director", "studio": null}]`)
	result, err := ExecuteWrite(ctx, counting, node, payload)
	if err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}

	directorKeys := dataKeysFor(t, conn, result.NodeIDs[node])
	if len(directorKeys) != 1 {
		t.Fatalf("expected 1 director keys row, got %d", len(directorKeys))
	}
	studioKeys := dataKeysFor(t, conn, result.NodeIDs[studioNode])
	if len(studioKeys) != 0 {
		t.Fatalf("expected 0 studio keys rows (studio was null), got %d : %#v", len(studioKeys), studioKeys)
	}

	var studioID *int
	if err := conn.QueryRow(ctx, `select studio_id from director where name = 'No Studio Director'`).Scan(&studioID); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if studioID != nil {
		t.Fatalf("expected studio_id null, got %v", *studioID)
	}

	// Baseline is director's own insert (1 exec) — if studio's phase1
	// statement also ran, this would be at least 2.
	if counting.execs != 1 {
		t.Fatalf("expected exactly 1 Exec (director's own insert, studio's skipped), got %d", counting.execs)
	}
}

func TestExecuteWrite_MergeNewLeavesExistingUntouched(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	var directorID int
	if err := conn.QueryRow(ctx, `insert into director (name) values ('MergeNew Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	var movieID int
	if err := conn.QueryRow(ctx, `insert into movie (director_id, title) values ($1, 'Original Title') returning id`, directorID).Scan(&movieID); err != nil {
		t.Fatalf("insert movie: %v", err)
	}

	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"id": "id", "movies": "movies"},
		"write_mode": "update",
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": ["own"], "write_mode": "merge-new"}}
	}`)
	movieNode := node.IncomingNodes[0]
	// The existing movie has a DIFFERENT title — merge-new must NOT update
	// it (insert-new/delete-absent only), yet still recover its key.
	payload := fmt.Appendf(nil, `[{"id": %d, "movies": [{"id": %d, "title": "Attempted Rename"}]}]`, directorID, movieID)

	result, err := ExecuteWrite(ctx, conn, node, payload)
	if err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}

	keys := dataKeysFor(t, conn, result.NodeIDs[movieNode])
	if len(keys) != 1 || keys[0]["id"] == nil {
		t.Fatalf("expected the existing movie's key recovered despite do-nothing, got %#v", keys)
	}

	var title string
	if err := conn.QueryRow(ctx, `select title from movie where id = $1`, movieID).Scan(&title); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if title != "Original Title" {
		t.Errorf("expected merge-new to leave the existing row untouched, got title=%q", title)
	}
}

func TestExecuteWrite_DefaultValueOmitted(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	// director.id is serial (DefaultExpression = nextval(...)) and never
	// supplied in the payload — the default-application case, named explicitly rather than incidental.
	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": ["own"], "write_mode": "insert"}`)
	result, err := ExecuteWrite(ctx, conn, node, []byte(`[{"name": "Default Value Director"}]`))
	if err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}
	keys := dataKeysFor(t, conn, result.NodeIDs[node])
	if len(keys) != 1 || keys[0]["id"] == nil {
		t.Fatalf("expected id filled from the column's default, got %#v", keys)
	}
}

func TestExecuteWrite_InsertColumnsFiltering(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	// insert_columns excludes "name" deliberately — should null-violate
	// (director.name NOT NULL, no default) if the filter is actually respected.
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": ["own"], "write_mode": "insert",
		"insert_columns": ["id"]
	}`)
	_, err := ExecuteWrite(ctx, conn, node, []byte(`[{"name": "Filtered Out"}]`))
	if err == nil {
		t.Fatalf("expected a NOT NULL violation on name (filtered out by insert_columns), got success")
	}
}

// TestExecuteWrite_CompositeSubFieldInsert proves a "." chain writes ONLY
// the named sub-field via a dotted INSERT target, leaving the composite's other field NULL (Postgres's own behavior ; write_dml.go's writeTargetPath relies on it).
func TestExecuteWrite_CompositeSubFieldInsert(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	node := mustResolveQuery(t, `{
		"relation": "venue", "schema": "public",
		"select": {"id": "id", "name": "name", "city": [".", "home", "city"]},
		"write_mode": "insert"
	}`)
	result, err := ExecuteWrite(ctx, conn, node, []byte(`[{"name": "Composite Insert Venue", "city": "Springfield"}]`))
	if err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}
	keys := dataKeysFor(t, conn, result.NodeIDs[node])
	if len(keys) != 1 {
		t.Fatalf("expected 1 keys row, got %d : %#v", len(keys), keys)
	}

	var street *string
	var city string
	if err := conn.QueryRow(ctx, `select (home).street, (home).city from venue where id = ($1::jsonb->>'id')::int`, mustJSON(t, keys[0])).Scan(&street, &city); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if street != nil {
		t.Errorf("expected home.street to stay NULL (never in the payload), got %q", *street)
	}
	if city != "Springfield" {
		t.Errorf("expected home.city=Springfield, got %q", city)
	}
}

// TestExecuteWrite_CompositeSubFieldUpdate proves an UPDATE through a "."
// chain touches ONLY the named sub-field — the other field survives untouched.
func TestExecuteWrite_CompositeSubFieldUpdate(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	var venueID int
	if err := conn.QueryRow(ctx, `insert into venue (name, home) values ('Composite Update Venue', row('Main St', 'Old City')) returning id`).Scan(&venueID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, `{
		"relation": "venue", "schema": "public",
		"select": {"id": "id", "city": [".", "home", "city"]},
		"write_mode": "update"
	}`)
	payload := []byte(`[{"id": ` + itoa(venueID) + `, "city": "New City"}]`)
	if _, err := ExecuteWrite(ctx, conn, node, payload); err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}

	var street, city string
	if err := conn.QueryRow(ctx, `select (home).street, (home).city from venue where id = $1`, venueID).Scan(&street, &city); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if street != "Main St" {
		t.Errorf("expected home.street to survive untouched (\"Main St\"), got %q", street)
	}
	if city != "New City" {
		t.Errorf("expected home.city updated to \"New City\", got %q", city)
	}
}

// TestExecuteWrite_CompositeSubFieldUpsert proves ON CONFLICT DO UPDATE
// reads a composite sub-field via row-value parenthesization, "(excluded.home).city" not "excluded.home.city" (a syntax error).
func TestExecuteWrite_CompositeSubFieldUpsert(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	var venueID int
	if err := conn.QueryRow(ctx, `insert into venue (name, home) values ('Composite Upsert Venue', row('Main St', 'Old City')) returning id`).Scan(&venueID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, `{
		"relation": "venue", "schema": "public",
		"select": {"id": "id", "name": "name", "city": [".", "home", "city"]},
		"write_mode": "upsert"
	}`)
	// name is required : an UPSERT's INSERT side is still fully constructed
	// and constraint-checked even when the row is known to already exist.
	payload := []byte(`[{"id": ` + itoa(venueID) + `, "name": "Composite Upsert Venue", "city": "Upserted City"}]`)
	if _, err := ExecuteWrite(ctx, conn, node, payload); err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}

	var street, city string
	if err := conn.QueryRow(ctx, `select (home).street, (home).city from venue where id = $1`, venueID).Scan(&street, &city); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if street != "Main St" {
		t.Errorf("expected home.street to survive the upsert's conflict path untouched, got %q", street)
	}
	if city != "Upserted City" {
		t.Errorf("expected home.city=\"Upserted City\", got %q", city)
	}
}

// TestExecuteWrite_CompositeWholeAndSubFieldTogether_Rejected proves a
// composite column whole plus one of its own sub-fields is rejected by Postgres itself, not a check this package duplicates.
func TestExecuteWrite_CompositeWholeAndSubFieldTogether_Rejected(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	node := mustResolveQuery(t, `{
		"relation": "venue", "schema": "public",
		"select": ["own_and", {"city_again": [".", "home", "city"]}],
		"write_mode": "insert"
	}`)
	_, err := ExecuteWrite(ctx, conn, node, []byte(`[{"name": "Both Venue", "home": {"street": "S", "city": "C"}, "city_again": "C2"}]`))
	if err == nil {
		t.Fatalf("expected Postgres to reject writing both the whole composite column and one of its own sub-fields")
	}
}

func TestExecuteWrite_DeleteOnlyScopedByWhere(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	var directorID int
	if err := conn.QueryRow(ctx, `insert into director (name) values ('DeleteOnly Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	if _, err := conn.Exec(ctx, `insert into movie (director_id, title) values ($1, 'Only This'), ($1, 'Not This')`, directorID); err != nil {
		t.Fatalf("insert movies: %v", err)
	}

	// deleteonly + empty payload + a "where" matching only one row : without
	// compiling node.Where into the delete, an empty _data deletes the whole table instead of just the matched row.
	node := mustResolveQuery(t, `{
		"relation": "movie", "schema": "public",
		"select": ["own"], "write_mode": "deleteonly",
		"where": ["=", "title", ["Only This"]]
	}`)
	if _, err := ExecuteWrite(ctx, conn, node, []byte(`[]`)); err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}

	var count int
	if err := conn.QueryRow(ctx, `select count(*) from movie where director_id = $1`, directorID).Scan(&count); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 movie left (the where-excluded one), got %d", count)
	}
	var remaining string
	if err := conn.QueryRow(ctx, `select title from movie where director_id = $1`, directorID).Scan(&remaining); err != nil {
		t.Fatalf("select remaining: %v", err)
	}
	if remaining != "Not This" {
		t.Fatalf("expected the where-excluded row (Not This) to survive, got %q", remaining)
	}
}

// TestExecuteWrite_ParamScopesDeleteOnlyWhere pins $param support on the
// write side (dmlCompiler.paramValues, threaded through ExecuteWriteStateParams).
func TestExecuteWrite_ParamScopesDeleteOnlyWhere(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	var directorID int
	if err := conn.QueryRow(ctx, `insert into director (name) values ('Param DeleteOnly Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	if _, err := conn.Exec(ctx, `insert into movie (director_id, title) values ($1, 'Only This'), ($1, 'Not This')`, directorID); err != nil {
		t.Fatalf("insert movies: %v", err)
	}

	node := mustResolveQuery(t, `{
		"relation": "movie", "schema": "public",
		"select": ["own"], "write_mode": "deleteonly",
		"where": ["=", "title", ["$param", "title", "text"]]
	}`)
	params := map[string]any{"title": "Only This"}
	if _, err := ExecuteWriteStateParams(ctx, conn, node, []byte(`[]`), &WriteState{}, params); err != nil {
		t.Fatalf("ExecuteWriteStateParams: %v", err)
	}

	var count int
	if err := conn.QueryRow(ctx, `select count(*) from movie where director_id = $1`, directorID).Scan(&count); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 movie left (the param-excluded one), got %d", count)
	}
	var remaining string
	if err := conn.QueryRow(ctx, `select title from movie where director_id = $1`, directorID).Scan(&remaining); err != nil {
		t.Fatalf("select remaining: %v", err)
	}
	if remaining != "Not This" {
		t.Fatalf("expected the param-excluded row (Not This) to survive, got %q", remaining)
	}
}

func TestKeysColumns_IncludesChildTargetedColumnBeyondOnConflict(t *testing.T) {
	// profile's on_conflict target (user_email) isn't its PK (id) — a child
	// correlating via the PK needs "id" recovered into keys too, or incomingKeySource reads NULL. A synthetic node, since no fixture FK targets a non-PK on_conflict column.
	rel := testDb.ResolveRelation("public", "profile")
	if rel == nil {
		t.Fatal("profile relation not found")
	}
	idCol := rel.ColumnsMap["id"]
	emailCol := rel.ColumnsMap["user_email"]

	parent := &QueryNode{Relation: rel, OnConflictColumns: []string{"user_email"}}
	child := &QueryNode{
		Parent:      parent,
		JoinColumns: []QueryJoinColumn{{Local: idCol, Distant: idCol}}, // Local col irrelevant here ; Distant is what matters
	}
	parent.IncomingNodes = []*QueryNode{child}

	cols := keysColumns(parent)
	hasEmail, hasID := false, false
	for _, c := range cols {
		if c == emailCol {
			hasEmail = true
		}
		if c == idCol {
			hasID = true
		}
	}
	if !hasEmail {
		t.Errorf("expected keysColumns to still include the on_conflict column (user_email), got %v", cols)
	}
	if !hasID {
		t.Errorf("expected keysColumns to additionally include id (a child's JoinColumns target), got %v", cols)
	}
}

func TestExecuteWrite_MergeNewNonPKOnConflictRecoversRealKey(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	// profile's on_conflict target isn't its PK ; a conflicting row's
	// "resolved.id" would be a phantom nextval() value, so recoverKeys must be the sole source of keys for merge-new, not "resolved" itself.
	var existingID int
	if err := conn.QueryRow(ctx, `insert into profile (user_email) values ('phantom@example.com') returning id`).Scan(&existingID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, `{
		"relation": "profile", "schema": "public",
		"select": {"id": "id", "user_email": "user_email", "notes": "notes"},
		"write_mode": "merge-new",
		"on_conflict": ["user_email"],
		"join": {"notes": {"relation": "profile_note", "schema": "public", "on": {"profile_id": "id"}, "select": ["own"]}}
	}`)
	result, err := ExecuteWrite(ctx, conn, node, []byte(`[{"user_email": "phantom@example.com", "notes": []}]`))
	if err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}

	keys := dataKeysFor(t, conn, result.NodeIDs[node])
	if len(keys) != 1 {
		t.Fatalf("expected 1 keys row, got %d", len(keys))
	}
	gotID, ok := keys[0]["id"]
	if !ok {
		t.Fatalf("expected recovered id in keys, got %#v", keys[0])
	}
	if int(gotID.(float64)) != existingID {
		t.Errorf("expected recovered id=%d (the real, pre-existing row), got %v (a phantom nextval)", existingID, gotID)
	}

	var count int
	if err := conn.QueryRow(ctx, `select count(*) from profile where user_email = 'phantom@example.com'`).Scan(&count); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 profile row (no duplicate inserted), got %d", count)
	}
}

func TestExecuteWrite_MergeNewInsertsGenuinelyNewRow(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	// A genuinely-new row must get real keys recovered AND survive phase
	// 2's delete — null keys look like "not in this request" to runDelete's own filter, inserting then immediately deleting it.
	var directorID int
	if err := conn.QueryRow(ctx, `insert into director (name) values ('MergeNew New Row Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}

	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"id": "id", "movies": "movies"},
		"write_mode": "update",
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": ["own"], "write_mode": "merge-new"}}
	}`)
	movieNode := node.IncomingNodes[0]
	payload := fmt.Appendf(nil, `[{"id": %d, "movies": [{"title": "Brand New Movie"}]}]`, directorID)

	result, err := ExecuteWrite(ctx, conn, node, payload)
	if err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}

	keys := dataKeysFor(t, conn, result.NodeIDs[movieNode])
	if len(keys) != 1 || keys[0]["id"] == nil {
		t.Fatalf("expected the new row's real id recovered, got %#v", keys)
	}

	var count int
	if err := conn.QueryRow(ctx, `select count(*) from movie where director_id = $1 and title = 'Brand New Movie'`, directorID).Scan(&count); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected the new movie to still exist after phase 2, got count=%d (deleted by its own insert's request)", count)
	}
}

func TestKeysColumns_IncludesOutgoingChildsOwnJoinColumn(t *testing.T) {
	// Symmetric to TestKeysColumns_IncludesChildTargetedColumnBeyondOnConflict :
	// jc.Local must be in node's own keysColumns even when it isn't the PK.
	rel := testDb.ResolveRelation("public", "director")
	if rel == nil {
		t.Fatal("director relation not found")
	}
	idCol := rel.ColumnsMap["id"]
	nameCol := rel.ColumnsMap["name"]

	parent := &QueryNode{}
	child := &QueryNode{
		Parent:            parent,
		Relation:          rel,
		OnConflictColumns: []string{"id"}, // identityColumns(child) won't include nameCol
		JoinColumns:       []QueryJoinColumn{{Local: nameCol, Distant: idCol}},
	}
	parent.OutgoingNodes = []*QueryNode{child}

	cols := keysColumns(child)
	if !slices.Contains(cols, nameCol) {
		t.Errorf("expected keysColumns(child) to include nameCol (the outgoing join's own Local column), got %v", cols)
	}
}

// runSelectOn is runSelect against a specific pinned connection — the
// write-then-reread tests below need "_data" on the SAME connection ExecuteWrite just used.
func runSelectOn(t *testing.T, conn *pgxpool.Conn, sql string, args []any) []map[string]any {
	t.Helper()
	rows, err := conn.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("query: %v\nsql: %s", err, sql)
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
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func TestCompileSelectForDataNode_ScopesToWrittenRows(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	// A pre-existing, untouched director must NOT appear in the reread —
	// only rows this specific request wrote should.
	if _, err := conn.Exec(ctx, `insert into director (name) values ('Untouched Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": ["own"], "write_mode": "insert"}`)
	payload := []byte(`[{"name": "Reread Director A"}, {"name": "Reread Director B"}]`)
	result, err := ExecuteWrite(ctx, conn, node, payload)
	if err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}

	w, err := CompileSelectForDataNode(node, result.NodeIDs[node])
	if err != nil {
		t.Fatalf("CompileSelectForDataNode: %v", err)
	}
	rows := runSelectOn(t, conn, w.String(), w.Args())
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows (only the ones just written), got %d : %#v (sql: %s)", len(rows), rows, w.String())
	}
	if rows[0]["name"] != "Reread Director A" || rows[1]["name"] != "Reread Director B" {
		t.Errorf("expected payload order [A, B], got %#v", rows)
	}
}

// TestCompileSelectForDataNode_ScalarSelect proves the scalar-select
// mechanism applies via the compileNodeCorrelated path shared with CompileSelect — on a READONLY child, since the root needs identity columns in select.
func TestCompileSelectForDataNode_ScalarSelect(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"id": "id", "name": "name", "movies": "movies"},
		"write_mode": "insert",
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": "title", "write_mode": "readonly", "order_by": ["title"]}}
	}`)
	payload := []byte(`[{"name": "Scalar Reread Director", "movies": []}]`)
	result, err := ExecuteWrite(ctx, conn, node, payload)
	if err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}
	var directorID int
	if err := conn.QueryRow(ctx, `select id from director where name = 'Scalar Reread Director'`).Scan(&directorID); err != nil {
		t.Fatalf("select id: %v", err)
	}
	if _, err := conn.Exec(ctx, `insert into movie (director_id, title) values ($1, 'Alpha'), ($1, 'Beta')`, directorID); err != nil {
		t.Fatalf("insert movies: %v", err)
	}

	w, err := CompileSelectForDataNode(node, result.NodeIDs[node])
	if err != nil {
		t.Fatalf("CompileSelectForDataNode: %v", err)
	}
	rows := runSelectOn(t, conn, w.String(), w.Args())
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), w.String())
	}
	movies, ok := rows[0]["movies"].([]any)
	if !ok || len(movies) != 2 {
		t.Fatalf("expected a flat 2-element array, got %#v (sql: %s)", rows[0]["movies"], w.String())
	}
	if movies[0] != "Alpha" || movies[1] != "Beta" {
		t.Errorf("expected [\"Alpha\", \"Beta\"] (bare scalars), got %#v", movies)
	}
}

func TestCompileSelectForDataNode_WithEmbeddedChild(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()

	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"id": "id", "name": "name", "movies": "movies"},
		"write_mode": "insert",
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": ["own"]}}
	}`)
	payload := []byte(`[{"name": "Reread With Movies", "movies": [{"title": "Movie X"}, {"title": "Movie Y"}]}]`)
	result, err := ExecuteWrite(ctx, conn, node, payload)
	if err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}

	w, err := CompileSelectForDataNode(node, result.NodeIDs[node])
	if err != nil {
		t.Fatalf("CompileSelectForDataNode: %v", err)
	}
	rows := runSelectOn(t, conn, w.String(), w.Args())
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), w.String())
	}
	movies, ok := rows[0]["movies"].([]any)
	if !ok || len(movies) != 2 {
		t.Fatalf("expected 2 embedded movies, got %#v (sql: %s)", rows[0]["movies"], w.String())
	}
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
