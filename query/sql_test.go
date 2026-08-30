package query

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func mustCompileSelect(t *testing.T, node *QueryNode) (string, []any) {
	t.Helper()
	w, err := CompileSelect(node)
	if err != nil {
		t.Fatalf("CompileSelect: %v", err)
	}
	return w.String(), w.Args()
}

// runSelect executes the compiled SQL against testDb.Pool and decodes the
// single "json" column of each returned row.
func runSelect(t *testing.T, sql string, args []any) []map[string]any {
	t.Helper()
	rows, err := testDb.Pool.Query(context.Background(), sql, args...)
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

func TestCompileSelect_BareOwn(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Denis Villeneuve') returning id`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": ["own"], "where": ["=", "name", ["Denis Villeneuve"]]}`)
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	if rows[0]["name"] != "Denis Villeneuve" {
		t.Errorf("expected name=Denis Villeneuve, got %#v", rows[0])
	}
}

// TestCompileSelect_ComputedColumnSelfAlias exercises specs/querying.md's
// "## Scoping" computed-column paragraph : a row-type-taking function
// (director_display_name(d director), pg/testdata/schema.sql) called with
// the node's own declared alias as its bare argument — "d" resolves to the
// *QueryNode itself (query/scope.go's LookupInScope self case), which used
// to hit compileResolvedField's "not yet supported" error unconditionally.
// Regression test for that fix : query/sql_expr.go now recognizes a
// self-reference (the resolved *QueryNode IS the node currently being
// compiled) and emits its own already-known SQL alias.
func TestCompileSelect_ComputedColumnSelfAlias(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Self Alias Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public", "alias": "d",
		"select": {
			"name": "name",
			"display": ["call", {"schema": "public", "name": "director_display_name"}, "d"]
		},
		"where": ["=", "name", ["Self Alias Director"]]
	}`)
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	if rows[0]["display"] != "Self Alias Director (director)" {
		t.Errorf("expected display=%q, got %#v", "Self Alias Director (director)", rows[0])
	}
}

// TestCompileSelect_EmbeddedChildAliasStillUnsupported confirms the fix is
// scoped to self-references only : a DIFFERENT node's alias (a joined
// child's) embedded as a bare value must still be rejected, not silently
// emit some other node's alias — the harder cross-subquery-scope case the
// fix deliberately doesn't attempt.
func TestCompileSelect_EmbeddedChildAliasStillUnsupported(t *testing.T) {
	// Selecting "director" directly as a top-level select entry is a
	// perfectly normal embed (compileSelectField's own job, a completely
	// different path from compileResolvedField) — NOT the case this test is
	// after. The still-unsupported case is a child alias reaching
	// compileResolvedField NESTED inside another expression, e.g. as a
	// coalesce() argument — resolution succeeds (LookupInScope's alias
	// case), but pass 3 (codegen) must still refuse to emit a bare value
	// for it.
	node := mustResolveQuery(t, `{
		"relation": "movie", "schema": "public", "alias": "m",
		"select": {"x": ["coalesce", "director", null]},
		"join": {"director": {"relation": "director", "schema": "public", "on": {"id": "director_id"}, "select": ["own"]}}
	}`)
	if _, err := CompileSelect(node); err == nil {
		t.Fatalf("expected embedding a child alias as a bare value to still fail to compile")
	}
}

func TestCompileSelect_Cast(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Cast Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// ["::", "id", "text"] : the type name (an unquoted identifier, "text")
	// must NOT be scope-resolved as a column of the current relation — it's
	// data, not a name. Also covers ["ilike"] not-needed here; the real
	// point is that pass 2 leaves BinaryCast's Right alone (see
	// expression_resolve.go's BinaryExpr case and sql_expr.go's
	// castTypeName).
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"id_text": ["::", "id", "text"]},
		"where": ["=", "name", ["Cast Director"]]
	}`)
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	if _, ok := rows[0]["id_text"].(string); !ok {
		t.Errorf("expected id_text to decode as a JSON string (text cast), got %#v (sql: %s)", rows[0]["id_text"], sql)
	}

	// castTypeName's other branch : a quoted type name arrives as a
	// StringLiteral, not a bare Identifier — e.g. an array type where the
	// caller writes it as a JSON string rather than a bare identifier.
	node2 := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"tags": ["::", ["arr", ["a"], ["b"]], ["text[]"]]},
		"where": ["=", "name", ["Cast Director"]]
	}`)
	sql2, args2 := mustCompileSelect(t, node2)
	rows2 := runSelect(t, sql2, args2)
	if len(rows2) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows2), sql2)
	}
	tags, ok := rows2[0]["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Errorf("expected tags=[a b], got %#v (sql: %s)", rows2[0]["tags"], sql2)
	}
}

func TestCompileSelect_Cast_MultiWordTypeName(t *testing.T) {
	// Multi-word standard SQL type names ("character varying") must still
	// compile — validCastTypeName's whole point is to distinguish these
	// from injected SQL, not to reject them.
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"n": ["::", "name", "character varying"]}
	}`)
	sql, args := mustCompileSelect(t, node)
	if !strings.Contains(sql, "::character varying") {
		t.Fatalf("expected ::character varying in generated SQL, got: %s", sql)
	}
	rows := runSelect(t, sql, args)
	_ = rows
}

func TestCompileSelect_Cast_RejectsInjectedTypeName(t *testing.T) {
	// castTypeName's Right is never scope-resolved (BinaryCast is special-
	// cased in pass 2 precisely so a legitimate type name doesn't error as
	// an unresolvable column) — that unresolved string then gets written
	// straight into the generated SQL after "::", so it must be validated
	// here or it's a raw injection point. Assert on the compile-time error,
	// not execution : the point is the fragment never reaches the database.
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"x": ["::", "id", "text) or (1=1"]}
	}`)
	_, err := CompileSelect(node)
	if err == nil {
		t.Fatalf("expected CompileSelect to reject an invalid cast type name, got success")
	}
}

func TestCompileSelect_ToManyEmbed(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('Toshiro Embed Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	if _, err := testDb.Pool.Exec(ctx, `insert into movie (director_id, title) values ($1, 'Movie A'), ($1, 'Movie B')`, directorID); err != nil {
		t.Fatalf("insert movies: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "director", "schema": "public",
		"select": {"id": "id", "name": "name", "movies": "movies"},
		"where": ["=", "id", %d],
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": {"title": "title"}}}
	}`, directorID))

	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	movies, ok := rows[0]["movies"].([]any)
	if !ok || len(movies) != 2 {
		t.Fatalf("expected 2 movies embedded, got %#v", rows[0]["movies"])
	}
}

func TestCompileSelect_ToOneEmbed(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('ToOne Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	var movieID int
	if err := testDb.Pool.QueryRow(ctx, `insert into movie (director_id, title) values ($1, 'ToOne Movie') returning id`, directorID).Scan(&movieID); err != nil {
		t.Fatalf("insert movie: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "movie", "schema": "public",
		"select": {"id": "id", "title": "title", "director": "director"},
		"where": ["=", "id", %d],
		"join": {"director": {"relation": "director", "schema": "public", "on": {"id": "director_id"}, "select": {"name": "name"}}}
	}`, movieID))
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	director, ok := rows[0]["director"].(map[string]any)
	if !ok || director["name"] != "ToOne Director" {
		t.Fatalf("expected embedded director object, got %#v", rows[0]["director"])
	}
}

func TestCompileSelect_CompositePath(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into venue (name, home) values ('Venue Composite', row('Main St', 'Springfield')::addr_t)`); err != nil {
		t.Fatalf("insert venue: %v", err)
	}

	node := mustResolveQuery(t, `{
		"relation": "venue", "schema": "public",
		"select": {"id": "id", "city": [".", "home", "city"]},
		"where": ["=", "name", ["Venue Composite"]]
	}`)
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	if rows[0]["city"] != "Springfield" {
		t.Errorf("expected city=Springfield, got %#v (sql: %s)", rows[0]["city"], sql)
	}
}

func TestCompileSelect_AggSingleConsumer_NoLateral(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('Agg Only Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	if _, err := testDb.Pool.Exec(ctx, `insert into movie (director_id, title) values ($1, 'A'), ($1, 'B'), ($1, 'C')`, directorID); err != nil {
		t.Fatalf("insert movies: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "director", "schema": "public",
		"select": ["own-and", {"movie_count": ["agg", {"schema": "pg_catalog", "name": "count"}, ["movies"]]}],
		"where": ["=", "id", %d],
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}}}
	}`, directorID))
	sql, args := mustCompileSelect(t, node)
	if containsLateral(sql) {
		t.Errorf("expected no LATERAL join for a single agg consumer, got:\n%s", sql)
	}
	rows := runSelect(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	count, ok := rows[0]["movie_count"].(float64)
	if !ok || count != 3 {
		t.Errorf("expected movie_count=3, got %#v (sql: %s)", rows[0]["movie_count"], sql)
	}
}

func TestCompileSelect_LateralSharedChild(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('Lateral Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	if _, err := testDb.Pool.Exec(ctx, `insert into movie (director_id, title) values ($1, 'X'), ($1, 'Y')`, directorID); err != nil {
		t.Fatalf("insert movies: %v", err)
	}

	// "movies" is consumed twice : embedded as its own array, AND counted
	// via "agg" — exactly the dual-consumption case that forces LATERAL
	// (Reading Algorithm step 5).
	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "director", "schema": "public",
		"select": {"id": "id", "movies": "movies", "movie_count": ["agg", {"schema": "pg_catalog", "name": "count"}, ["movies"]]},
		"where": ["=", "id", %d],
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": {"title": "title"}}}
	}`, directorID))
	sql, args := mustCompileSelect(t, node)
	if !containsLateral(sql) {
		t.Fatalf("expected a LATERAL join for a dual-consumed child, got:\n%s", sql)
	}
	rows := runSelect(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	movies, ok := rows[0]["movies"].([]any)
	if !ok || len(movies) != 2 {
		t.Fatalf("expected 2 movies embedded, got %#v", rows[0]["movies"])
	}
	count, ok := rows[0]["movie_count"].(float64)
	if !ok || count != 2 {
		t.Errorf("expected movie_count=2, got %#v (sql: %s)", rows[0]["movie_count"], sql)
	}
}

func TestCompileSelect_FunctionRoot(t *testing.T) {
	node := mustResolveQuery(t, `{"function": "fn_plain_add", "schema": "public", "arguments": [2, 3]}`)
	sql, args := mustCompileSelect(t, node)

	var got int
	if err := testDb.Pool.QueryRow(context.Background(), sql, args...).Scan(&got); err != nil {
		t.Fatalf("query scalar: %v\nsql: %s", err, sql)
	}
	if got != 5 {
		t.Errorf("expected fn_plain_add(2,3)=5, got %d", got)
	}
}

func TestCompileSelect_TableValuedFunctionRoot(t *testing.T) {
	if _, err := testDb.Pool.Exec(context.Background(), `insert into director (name) values ('TVF Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	node := mustResolveQuery(t, `{"function": "fn_directors", "schema": "public"}`)
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)
	if len(rows) == 0 {
		t.Fatalf("expected at least 1 row from fn_directors(), got 0 : %s", sql)
	}
}

// TestCompileSelect_RecordRelationFunctionRoot exercises pg.Function.
// RecordRelation : movie_counts_by_director() is RETURNS TABLE(...), an
// anonymous record with no backing composite type (unlike fn_directors'
// SETOF director above), so its own column list only ever resolves via its
// OUT-mode Arguments, not ReturnType.Relation. Both explicit-select and
// "own" shorthand are checked, since "own" walks node.Relation.Columns
// directly.
func TestCompileSelect_RecordRelationFunctionRoot(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('RecordRelation Director') returning id`); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `select id from director where name = 'RecordRelation Director'`).Scan(&directorID); err != nil {
		t.Fatalf("select director id: %v", err)
	}
	if _, err := testDb.Pool.Exec(ctx, `insert into movie (director_id, title) values ($1, 'M1'), ($1, 'M2')`, directorID); err != nil {
		t.Fatalf("insert movies: %v", err)
	}

	node := mustResolveQuery(t, `{
		"function": "movie_counts_by_director", "schema": "public",
		"select": {"director_id": "director_id", "count": "movie_count"},
		"where": ["=", "director_id", `+fmt.Sprint(directorID)+`]
	}`)
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	if got := rows[0]["count"]; fmt.Sprint(got) != "2" {
		t.Errorf("expected count=2, got %#v", got)
	}

	ownNode := mustResolveQuery(t, `{"function": "movie_counts_by_director", "schema": "public", "select": ["own"], "where": ["=", "director_id", `+fmt.Sprint(directorID)+`]}`)
	ownSQL, ownArgs := mustCompileSelect(t, ownNode)
	ownRows := runSelect(t, ownSQL, ownArgs)
	if len(ownRows) != 1 || fmt.Sprint(ownRows[0]["movie_count"]) != "2" {
		t.Errorf("expected own-shorthand movie_count=2, got %#v : %s", ownRows, ownSQL)
	}
}

// TestCompileSelect_RecordRelationCannotBeJoinChild confirms the structural
// limit explained to the user : a RETURNS TABLE function's output can never
// be indexed by Postgres, so it can never be the CHILD/joined-into side of
// any relationship, even now that its own columns resolve. This must fail
// at RESOLUTION time (ResolveJoin, called from resolveNode), not later.
func TestCompileSelect_RecordRelationCannotBeJoinChild(t *testing.T) {
	err := resolveQueryExpectError(t, `{
		"relation": "director", "schema": "public",
		"select": ["own"],
		"join": {"counts": {"function": "movie_counts_by_director", "schema": "public", "on": {"director_id": "id"}, "select": ["own"]}}
	}`)
	if err == nil {
		t.Fatalf("expected joining INTO a RETURNS TABLE function to fail (unindexable child side)")
	}
}

// TestCompileSelect_RecordRelationAsOutgoingJoinParent confirms the
// direction that DOES work : the record-relation function as the
// PARENT/outer side of an outgoing join out to a real, indexed relation
// (director.id, its primary key) — no index is required on the local
// (record-relation) side for an outgoing join, only on the joined side.
func TestCompileSelect_RecordRelationAsOutgoingJoinParent(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('Outgoing Parent Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	if _, err := testDb.Pool.Exec(ctx, `insert into movie (director_id, title) values ($1, 'M3')`, directorID); err != nil {
		t.Fatalf("insert movie: %v", err)
	}

	node := mustResolveQuery(t, `{
		"function": "movie_counts_by_director", "schema": "public",
		"select": {"count": "movie_count", "d": "d"},
		"where": ["=", "director_id", `+fmt.Sprint(directorID)+`],
		"join": {"d": {"relation": "director", "schema": "public", "on": {"id": "director_id"}, "select": ["own"]}}
	}`)
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	embedded, ok := rows[0]["d"].(map[string]any)
	if !ok {
		t.Fatalf("expected d to embed as an object, got %#v", rows[0]["d"])
	}
	if embedded["name"] != "Outgoing Parent Director" {
		t.Errorf("expected embedded director name=%q, got %#v", "Outgoing Parent Director", embedded)
	}
}

// TestExecuteWrite_RecordRelationRootIsCleanlyRejected confirms a write
// attempt against a RETURNS TABLE function root is rejected cleanly
// (findUnwritableNode, before any DML is generated) rather than reaching
// Postgres as broken SQL trying to INSERT into a function call — the
// safety this session traced back to PrimaryKey/every constraint lookup
// being correctly left nil on RecordRelation, not a separate guard.
func TestExecuteWrite_RecordRelationRootIsCleanlyRejected(t *testing.T) {
	conn := acquireWriteConn(t)
	node := mustResolveQuery(t, `{"function": "movie_counts_by_director", "schema": "public", "select": ["own"]}`)
	_, err := ExecuteWrite(context.Background(), conn, node, []byte(`[{"director_id": 1, "movie_count": 5}]`))
	if err == nil {
		t.Fatalf("expected a write against a RETURNS TABLE function root to be rejected")
	}
}

func containsLateral(sql string) bool {
	return strings.Contains(sql, "left join lateral")
}

func TestCompileSelect_Between(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('Between Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "director", "schema": "public",
		"select": ["own-except", []],
		"where": ["between", %d, "id", %d]
	}`, directorID, directorID))
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
}

func TestCompileSelect_InLiteralAndExpr(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('In Test Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": ["own-except", []],
		"where": ["in", "name", "In Test Director", "Someone Else"]
	}`)
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (literal \"in\" candidates), got %d : %s", len(rows), sql)
	}
}

func TestCompileSelect_AnyAll(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('AnyAll Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "director", "schema": "public",
		"select": ["own-except", []],
		"where": ["any", "=", "id", ["arr", %d, -1]]
	}`, directorID))
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
}

func TestCompileSelect_JsonbArrow(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into venue (name, metadata) values ('Jsonb Venue', '{"rating": 5}'::jsonb)`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, `{
		"relation": "venue", "schema": "public",
		"select": {"id": "id", "rating": ["->>", "metadata", ["rating"]]},
		"where": ["=", "name", ["Jsonb Venue"]]
	}`)
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	if rows[0]["rating"] != "5" {
		t.Errorf("expected rating=\"5\" (->> yields text), got %#v (sql: %s)", rows[0]["rating"], sql)
	}
}

func TestCompileSelect_CoalesceOperators(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Coalesce Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"n": ["??", "name", ["fallback"]], "c": ["||?", "name", "name"]},
		"where": ["=", "name", ["Coalesce Director"]]
	}`)
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	if rows[0]["n"] != "Coalesce Director" {
		t.Errorf("expected n=Coalesce Director, got %#v", rows[0]["n"])
	}
	if rows[0]["c"] != "Coalesce DirectorCoalesce Director" {
		t.Errorf("expected c=<name doubled>, got %#v (sql: %s)", rows[0]["c"], sql)
	}
}

func TestCompileSelect_NestedObjectLiteral(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Nested Object Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// A plain JSON object nested as a select value compiles to
	// jsonb_build_object — the same untyped-$1-parameter inference failure
	// that ARRAY[$1,...] had (subjectPgType's fix) applies to its bound
	// keys too; this must run against real Postgres to prove the key bind
	// is typed correctly, not just that it compiles.
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"info": {"n": "name"}},
		"where": ["=", "name", ["Nested Object Director"]]
	}`)
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	info, ok := rows[0]["info"].(map[string]any)
	if !ok || info["n"] != "Nested Object Director" {
		t.Fatalf("expected info.n=Nested Object Director, got %#v (sql: %s)", rows[0]["info"], sql)
	}
}

func TestCompileSelect_NestedShapeAsValue(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Nested Shape Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// own-except reached as a nested VALUE (not the node's own top-level
	// select) exercises compileShapeAsJsonObject — a separate codepath
	// from compileNode's plain-column select list, and one that also
	// binds its keys via jsonb_build_object.
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"id": "id", "basic": ["own-except", ["id"]]},
		"where": ["=", "name", ["Nested Shape Director"]]
	}`)
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	basic, ok := rows[0]["basic"].(map[string]any)
	if !ok || basic["name"] != "Nested Shape Director" {
		t.Fatalf("expected basic.name=Nested Shape Director, got %#v (sql: %s)", rows[0]["basic"], sql)
	}
	if _, hasID := basic["id"]; hasID {
		t.Errorf("expected id excluded from basic, got %#v", basic)
	}
}

func TestCompileSelect_UnaryOperators(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Unary Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// "not" (prefix) and "is-not-null" (postfix) exercise both templates
	// in compileUnary's per-operator table — untested until now.
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": ["own-except", []],
		"where": ["and", ["is-not-null", "name"], ["not", ["=", "name", ["Someone Else"]]]]
	}`)
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)
	found := false
	for _, r := range rows {
		if r["name"] == "Unary Director" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected Unary Director among rows, got %#v (sql: %s)", rows, sql)
	}
}

func TestCompileSelect_NotInAndNotBetween(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('NotIn Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Negate is set (not-in / not-between) but never exercised — a wrong
	// "not " placement here is a syntax error at execution, not a compile
	// error, so this needs to run against real Postgres.
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": ["own-except", []],
		"where": ["and", ["not-in", "name", "Someone Else", "Nobody"], ["not-between", "id", -1, 0]]
	}`)
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)
	found := false
	for _, r := range rows {
		if r["name"] == "NotIn Director" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected NotIn Director among rows (id=%d), got %#v (sql: %s)", directorID, rows, sql)
	}
}

func TestCompileSelect_EmbeddedOrderByLimitOffset(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('OrderLimit Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := testDb.Pool.Exec(ctx, `insert into movie (director_id, title) values ($1, 'Z'), ($1, 'A'), ($1, 'M')`, directorID); err != nil {
		t.Fatalf("insert movies: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "director", "schema": "public",
		"select": {"movies": "movies"},
		"where": ["=", "id", %d],
		"join": {"movies": {
			"relation": "movie", "schema": "public", "on": {"director_id": "id"},
			"select": {"title": "title"},
			"order_by": ["title"],
			"limit": 2
		}}
	}`, directorID))
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	movies, ok := rows[0]["movies"].([]any)
	if !ok || len(movies) != 2 {
		t.Fatalf("expected 2 movies (limit 2 of 3), got %#v (sql: %s)", rows[0]["movies"], sql)
	}
	first := movies[0].(map[string]any)
	if first["title"] != "A" {
		t.Errorf("expected first movie (order by title asc) to be \"A\", got %#v", first["title"])
	}
}

func TestCompileSelect_DistinctOn(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('Distinct Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := testDb.Pool.Exec(ctx, `insert into movie (director_id, title) values ($1, 'Dup'), ($1, 'Dup'), ($1, 'Unique')`, directorID); err != nil {
		t.Fatalf("insert movies: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "movie", "schema": "public",
		"select": {"title": "title"},
		"where": ["=", "director_id", %d],
		"distinct_on": ["title"],
		"order_by": ["title"]
	}`, directorID))
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)
	if len(rows) != 2 {
		t.Fatalf("expected 2 distinct titles, got %d : %#v (sql: %s)", len(rows), rows, sql)
	}
}
