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

func runCount(t *testing.T, sql string, args []any) int {
	t.Helper()
	var n int
	if err := testDb.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("query: %v\nsql: %s", err, sql)
	}
	return n
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

// runSelectScalar is runSelect's counterpart for a scalar-selected node
// (## Reading Algorithm ### Scalar-selected nodes) : decodes into "any", not map[string]any.
func runSelectScalar(t *testing.T, sql string, args []any) []any {
	t.Helper()
	rows, err := testDb.Pool.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("query: %v\nsql: %s", err, sql)
	}
	defer rows.Close()

	var out []any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan: %v", err)
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		out = append(out, v)
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

// TestCompileSelect_ComputedColumnSelfAlias proves a row-type-taking
// function called with the node's own alias (## Scoping) resolves via a self-reference, not "not yet supported".
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

// TestCompileSelect_EmbeddedChildAlias_ToOneChild proves a to-one child's
// alias nested inside another expression (e.g. coalesce()) compiles via compileChildRowValue, not compileSelectField's top-level path.
func TestCompileSelect_EmbeddedChildAlias_ToOneChild(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('Embedded Alias Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	var movieID int
	if err := testDb.Pool.QueryRow(ctx, `insert into movie (director_id, title) values ($1, 'Embedded Alias Movie') returning id`, directorID).Scan(&movieID); err != nil {
		t.Fatalf("insert movie: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "movie", "schema": "public", "alias": "m",
		"select": {"x": ["coalesce", "director", null]},
		"where": ["=", "id", %d],
		"join": {"director": {"relation": "director", "schema": "public", "on": {"id": "director_id"}}}
	}`, movieID))
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	obj, ok := rows[0]["x"].(map[string]any)
	if !ok {
		t.Fatalf("expected x to decode as a JSON object (the director row's composite type), got %#v (sql: %s)", rows[0]["x"], sql)
	}
	if obj["name"] != "Embedded Alias Director" {
		t.Errorf("expected x.name=\"Embedded Alias Director\", got %#v", obj)
	}
	if fmt.Sprintf("%v", obj["id"]) != fmt.Sprintf("%d", directorID) {
		t.Errorf("expected x.id=%d, got %#v", directorID, obj["id"])
	}
}

// TestCompileSelect_EmbeddedChildAlias_NullTarget proves a nullable to-one
// target reads back as JSON null, not an error (Postgres's own "no rows -> NULL" rule).
func TestCompileSelect_EmbeddedChildAlias_NullTarget(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('No Studio Embedded Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "director", "schema": "public",
		"select": {"x": ["coalesce", "studio", null]},
		"where": ["=", "id", %d],
		"join": {"studio": {"relation": "studio", "schema": "public", "on": {"id": "studio_id"}}}
	}`, directorID))
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	if rows[0]["x"] != nil {
		t.Errorf("expected x=nil (no studio set), got %#v", rows[0]["x"])
	}
}

// TestCompileSelect_EmbeddedChildAlias_ToManyChild_Rejected proves a
// to-many child's bare alias is still rejected — no single row to name.
func TestCompileSelect_EmbeddedChildAlias_ToManyChild_Rejected(t *testing.T) {
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"x": ["coalesce", "movies", null]},
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}}}
	}`)
	_, err := CompileSelect(node)
	if err == nil {
		t.Fatal("expected embedding a to-many child's alias as a bare value to fail to compile")
	}
	if !strings.Contains(err.Error(), "to-many") {
		t.Errorf("expected the error to explain the to-many rejection, got: %v", err)
	}
}

func TestCompileSelect_Cast(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Cast Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// ["::", "id", "text"] : the type name must NOT be scope-resolved as a
	// column — pass 2 leaves BinaryCast's Right alone (sql_expr.go's castTypeName).
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
	// StringLiteral, not a bare Identifier.
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

// TestParseExpression_BigIntLiteral_RejectsMalformed proves ["bigint"/"numeric", v]'s
// v is validated at parse time, before it ever reaches SQL text (was a SQL-injection path).
func TestParseExpression_BigIntLiteral_RejectsMalformed(t *testing.T) {
	_, err := ParseExpression([]byte(`["bigint", "1) OR (1=1) --"]`))
	if err == nil {
		t.Fatal("expected a parse error for a non-integer bigint literal")
	}
	if !strings.Contains(err.Error(), "not a valid") {
		t.Errorf("expected the error to explain the format rejection, got: %v", err)
	}
}

func TestParseExpression_NumericLiteral_RejectsMalformed(t *testing.T) {
	_, err := ParseExpression([]byte(`["numeric", "1); drop table director; --"]`))
	if err == nil {
		t.Fatal("expected a parse error for a non-numeric numeric literal")
	}
	if !strings.Contains(err.Error(), "not a valid") {
		t.Errorf("expected the error to explain the format rejection, got: %v", err)
	}
}

// TestCompileSelect_BigIntLiteral_BoundAsParam proves a well-formed
// ["bigint", v] compiles to a bound $n parameter, never inlined into the SQL text.
func TestCompileSelect_BigIntLiteral_BoundAsParam(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('BigInt Literal Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"n": ["::", ["bigint", "123456789012345"], "text"]},
		"where": ["=", "name", ["BigInt Literal Director"]]
	}`)
	sql, args := mustCompileSelect(t, node)
	if !strings.Contains(sql, "::bigint") {
		t.Errorf("expected a ::bigint cast in the compiled SQL, got: %s", sql)
	}
	if strings.Contains(sql, "123456789012345") {
		t.Errorf("expected the literal value NOT to appear inlined in the SQL text, got: %s", sql)
	}
	found := false
	for _, a := range args {
		if a == "123456789012345" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected \"123456789012345\" among the bound args, got %#v", args)
	}
	rows := runSelect(t, sql, args)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if got := fmt.Sprintf("%v", rows[0]["n"]); got != "123456789012345" {
		t.Errorf("expected n=123456789012345, got %v", rows[0]["n"])
	}
}

func TestCompileSelect_NumericLiteral_BoundAsParam(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Numeric Literal Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"n": ["numeric", "42.5"]},
		"where": ["=", "name", ["Numeric Literal Director"]]
	}`)
	sql, args := mustCompileSelect(t, node)
	if !strings.Contains(sql, "::numeric") {
		t.Errorf("expected a ::numeric cast in the compiled SQL, got: %s", sql)
	}
	if strings.Contains(sql, "42.5") {
		t.Errorf("expected the literal value NOT to appear inlined in the SQL text, got: %s", sql)
	}
	rows := runSelect(t, sql, args)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if got := fmt.Sprintf("%v", rows[0]["n"]); got != "42.5" {
		t.Errorf("expected n=42.5, got %v", rows[0]["n"])
	}
}

func TestCompileSelect_Cast_MultiWordTypeName(t *testing.T) {
	// Multi-word type names ("character varying") must still compile —
	// validCastTypeName distinguishes these from injected SQL, not rejects them.
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
	// castTypeName's Right is never scope-resolved and is written straight
	// after "::" — must be validated here, an unvalidated string is a raw injection point.
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"x": ["::", "id", "text) or (1=1"]}
	}`)
	_, err := CompileSelect(node)
	if err == nil {
		t.Fatalf("expected CompileSelect to reject an invalid cast type name, got success")
	}
}

// TestCompileCount_IgnoresLimit proves count reports the total matching
// row count, not the paginated page size (specs/complex-query.md ## count).
func TestCompileCount_IgnoresLimit(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Count Director A'), ('Count Director B'), ('Count Director C')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": ["own"], "where": ["like", "name", ["Count Director%"]], "limit": 1}`)

	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)
	if len(rows) != 1 {
		t.Fatalf("expected the paginated select to return 1 row, got %d", len(rows))
	}

	cw, err := CompileCount(node)
	if err != nil {
		t.Fatalf("CompileCount: %v", err)
	}
	n := runCount(t, cw.String(), cw.Args())
	if n != 3 {
		t.Fatalf("expected count 3 (ignoring limit 1), got %d : %s", n, cw.String())
	}
}

// TestCompileCount_WhereReferencingJoin proves count's WHERE compiles
// correctly when it references a joined relation (an agg, correlated
// subquery — see query/sql_expr.go's compileAgg) even though count never
// compiles root's own select list/lateral joins.
func TestCompileCount_WhereReferencingJoin(t *testing.T) {
	ctx := context.Background()
	var dirWithMovies, dirWithout int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('Count Join Director With') returning id`).Scan(&dirWithMovies); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('Count Join Director Without') returning id`).Scan(&dirWithout); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	if _, err := testDb.Pool.Exec(ctx, `insert into movie (director_id, title) values ($1, 'Count Join Movie')`, dirWithMovies); err != nil {
		t.Fatalf("insert movie: %v", err)
	}

	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": ["own"],
		"where": ["and", ["like", "name", ["Count Join Director%"]], [">", ["agg", {"schema": "pg_catalog", "name": "count"}, ["movies"]], 0]],
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}}}
	}`)

	cw, err := CompileCount(node)
	if err != nil {
		t.Fatalf("CompileCount: %v", err)
	}
	n := runCount(t, cw.String(), cw.Args())
	if n != 1 {
		t.Fatalf("expected count 1 (only the director with a movie), got %d : %s", n, cw.String())
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

// TestCompileSelect_RootScalarSelect proves ## Scalar-selected nodes for a
// table root : a bare-column select reads back as a flat array of scalars, not one-key objects. TEXT matters since to_jsonb's quoting is the point.
func TestCompileSelect_RootScalarSelect(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Scalar Root Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": "name", "where": ["=", "name", ["Scalar Root Director"]]}`)
	sql, args := mustCompileSelect(t, node)
	rows := runSelectScalar(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	if rows[0] != "Scalar Root Director" {
		t.Errorf("expected bare scalar \"Scalar Root Director\", got %#v (sql: %s)", rows[0], sql)
	}
}

// TestCompileSelect_RootScalarSelect_Expression proves the scalar branch
// isn't limited to a bare column — any non-shape-producing expression works.
func TestCompileSelect_RootScalarSelect_Expression(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Expr Root Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": ["format", "%s!", "name"], "where": ["=", "name", ["Expr Root Director"]]}`)
	sql, args := mustCompileSelect(t, node)
	rows := runSelectScalar(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	if rows[0] != "Expr Root Director!" {
		t.Errorf("expected \"Expr Root Director!\", got %#v (sql: %s)", rows[0], sql)
	}
}

// TestCompileSelect_ToOneEmbedScalarSelect proves the same mechanism
// through a to-one embed : a bare string, not a nested {"name": ...} object.
func TestCompileSelect_ToOneEmbedScalarSelect(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('ToOne Scalar Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	var movieID int
	if err := testDb.Pool.QueryRow(ctx, `insert into movie (director_id, title) values ($1, 'ToOne Scalar Movie') returning id`, directorID).Scan(&movieID); err != nil {
		t.Fatalf("insert movie: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "movie", "schema": "public",
		"select": {"title": "title", "director": "director"},
		"where": ["=", "id", %d],
		"join": {"director": {"relation": "director", "schema": "public", "on": {"id": "director_id"}, "select": "name"}}
	}`, movieID))
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	if rows[0]["director"] != "ToOne Scalar Director" {
		t.Errorf("expected director to be a bare string \"ToOne Scalar Director\", got %#v (sql: %s)", rows[0]["director"], sql)
	}
}

// TestCompileSelect_ToManyEmbedScalarSelect proves the to-many side : a
// flat array of bare strings, not an array of {"title": ...} objects.
func TestCompileSelect_ToManyEmbedScalarSelect(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('ToMany Scalar Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	if _, err := testDb.Pool.Exec(ctx, `insert into movie (director_id, title) values ($1, 'Alpha'), ($1, 'Beta')`, directorID); err != nil {
		t.Fatalf("insert movies: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "director", "schema": "public",
		"select": {"name": "name", "movies": "movies"},
		"where": ["=", "id", %d],
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": "title", "order_by": ["title"]}}
	}`, directorID))
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	movies, ok := rows[0]["movies"].([]any)
	if !ok || len(movies) != 2 {
		t.Fatalf("expected a flat 2-element array, got %#v (sql: %s)", rows[0]["movies"], sql)
	}
	if movies[0] != "Alpha" || movies[1] != "Beta" {
		t.Errorf("expected [\"Alpha\", \"Beta\"], got %#v", movies)
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

// TestCompileSelect_ScalarHopThroughOutgoing proves a "." hop through a
// to-one relation pulls one field into the parent's flat select, no nested object at all.
func TestCompileSelect_ScalarHopThroughOutgoing(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('Scalar Hop Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	var movieID int
	if err := testDb.Pool.QueryRow(ctx, `insert into movie (director_id, title) values ($1, 'Scalar Hop Movie') returning id`, directorID).Scan(&movieID); err != nil {
		t.Fatalf("insert movie: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "movie", "schema": "public",
		"select": ["own_and", {"director_name": [".", "director", "name"]}],
		"where": ["=", "id", %d],
		"join": {"director": {"relation": "director", "schema": "public", "on": {"id": "director_id"}}}
	}`, movieID))
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	if _, isObject := rows[0]["director"]; isObject {
		t.Errorf("expected no nested \"director\" object at all, got %#v", rows[0])
	}
	if rows[0]["director_name"] != "Scalar Hop Director" {
		t.Errorf("expected director_name=\"Scalar Hop Director\", got %#v (sql: %s)", rows[0]["director_name"], sql)
	}
	if rows[0]["title"] != "Scalar Hop Movie" {
		t.Errorf("expected own's title to still be present, got %#v", rows[0])
	}
}

// TestCompileSelect_ScalarHopThroughOutgoing_NullTarget proves a hop
// through a nullable to-one relation reads back as JSON null, not an error.
func TestCompileSelect_ScalarHopThroughOutgoing_NullTarget(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('No Studio Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "director", "schema": "public",
		"select": ["own_and", {"studio_name": [".", "studio", "name"]}],
		"where": ["=", "id", %d],
		"join": {"studio": {"relation": "studio", "schema": "public", "on": {"id": "studio_id"}}}
	}`, directorID))
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	if rows[0]["studio_name"] != nil {
		t.Errorf("expected studio_name=nil (no studio set), got %#v", rows[0]["studio_name"])
	}
}

// TestCompileSelect_ScalarHopThroughTwoOutgoingLevels proves a "." hop
// chained through two to-one relations compiles as nested correlated subqueries.
func TestCompileSelect_ScalarHopThroughTwoOutgoingLevels(t *testing.T) {
	ctx := context.Background()
	var studioID int
	if err := testDb.Pool.QueryRow(ctx, `insert into studio (name) values ('Two-Level Studio') returning id`).Scan(&studioID); err != nil {
		t.Fatalf("insert studio: %v", err)
	}
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name, studio_id) values ('Two-Level Director', $1) returning id`, studioID).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	var movieID int
	if err := testDb.Pool.QueryRow(ctx, `insert into movie (director_id, title) values ($1, 'Two-Level Movie') returning id`, directorID).Scan(&movieID); err != nil {
		t.Fatalf("insert movie: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "movie", "schema": "public",
		"select": ["own_and", {"studio_name": [".", [".", "director", "studio"], "name"]}],
		"where": ["=", "id", %d],
		"join": {"director": {"relation": "director", "schema": "public", "on": {"id": "director_id"},
			"join": {"studio": {"relation": "studio", "schema": "public", "on": {"id": "studio_id"}}}
		}}
	}`, movieID))
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	if rows[0]["studio_name"] != "Two-Level Studio" {
		t.Errorf("expected studio_name=\"Two-Level Studio\", got %#v (sql: %s)", rows[0]["studio_name"], sql)
	}
}

// TestCompileSelect_ScalarHopAlongsideFullEmbed proves the same child can
// be both fully embedded and "." hopped without alias collision — compileScalarHop always allocates its own fresh alias, by design (see its own doc comment).
func TestCompileSelect_ScalarHopAlongsideFullEmbed(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('Alongside Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	var movieID int
	if err := testDb.Pool.QueryRow(ctx, `insert into movie (director_id, title) values ($1, 'Alongside Movie') returning id`, directorID).Scan(&movieID); err != nil {
		t.Fatalf("insert movie: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "movie", "schema": "public",
		"select": ["own_and", {"director": "director", "director_name": [".", "director", "name"]}],
		"where": ["=", "id", %d],
		"join": {"director": {"relation": "director", "schema": "public", "on": {"id": "director_id"}, "select": ["own"]}}
	}`, movieID))
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	director, ok := rows[0]["director"].(map[string]any)
	if !ok || director["name"] != "Alongside Director" {
		t.Fatalf("expected embedded director object, got %#v", rows[0]["director"])
	}
	if rows[0]["director_name"] != "Alongside Director" {
		t.Errorf("expected director_name=\"Alongside Director\" alongside the full embed, got %#v (sql: %s)", rows[0]["director_name"], sql)
	}
}

// TestCompileSelect_ScalarHopThroughIncoming_Rejected proves a "." hop
// into a to-many child resolves fine but is rejected as a scalar select value at SQL-compile time.
func TestCompileSelect_ScalarHopThroughIncoming_Rejected(t *testing.T) {
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": ["own_and", {"a_title": [".", "movies", "title"]}],
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}}}
	}`)
	_, err := CompileSelect(node)
	if err == nil {
		t.Fatal("expected a compile error for a scalar hop through a to-many relation")
	}
	if !strings.Contains(err.Error(), "to-many") {
		t.Errorf("expected the error to explain the to-many rejection, got: %v", err)
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
		"select": ["own_and", {"movie_count": ["agg", {"schema": "pg_catalog", "name": "count"}, ["movies"]]}],
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

	// "movies" is consumed twice (embedded array + "agg") — the dual-
	// consumption case that forces LATERAL (## Reading Algorithm step 5).
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

// TestCompileSelect_LateralSharedChild_ScalarSelect proves
// compileLateralJoin also switches to the bare "__scalar" column for a LATERAL-shared scalar-selected child.
func TestCompileSelect_LateralSharedChild_ScalarSelect(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('Lateral Scalar Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	if _, err := testDb.Pool.Exec(ctx, `insert into movie (director_id, title) values ($1, 'X'), ($1, 'Y')`, directorID); err != nil {
		t.Fatalf("insert movies: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "director", "schema": "public",
		"select": {"id": "id", "movies": "movies", "movie_count": ["agg", {"schema": "pg_catalog", "name": "count"}, ["movies"]]},
		"where": ["=", "id", %d],
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": "title", "order_by": ["title"]}}
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
		t.Fatalf("expected a flat 2-element array, got %#v (sql: %s)", rows[0]["movies"], sql)
	}
	if movies[0] != "X" || movies[1] != "Y" {
		t.Errorf("expected [\"X\", \"Y\"] (bare scalars, not objects), got %#v", movies)
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

// TestCompileSelect_SingleRowCompositeFunctionRoot proves a single-row
// composite function (Relation != nil, ReturnsSet false) gets ordinary row_to_json wrapping, not the bare-scalar shortcut.
func TestCompileSelect_SingleRowCompositeFunctionRoot(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Single Row Fn Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `select id from director where name = 'Single Row Fn Director'`).Scan(&directorID); err != nil {
		t.Fatalf("select id: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{"function": "fn_one_director", "schema": "public", "arguments": [%d]}`, directorID))
	sql, args := mustCompileSelect(t, node)
	if strings.Contains(sql, "row_to_json") == false {
		t.Fatalf("expected row_to_json wrapping (this is a composite return, not a bare scalar), got: %s", sql)
	}
	rows := runSelect(t, sql, args)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	if rows[0]["name"] != "Single Row Fn Director" {
		t.Errorf("expected name=Single Row Fn Director, got %#v", rows[0])
	}
}

// TestCompileSelect_SingleRowCompositeFunctionRoot_Join proves a single-row
// composite function root's own `join` entries actually compile and return rows.
func TestCompileSelect_SingleRowCompositeFunctionRoot_Join(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Single Row Fn Join Director')`); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `select id from director where name = 'Single Row Fn Join Director'`).Scan(&directorID); err != nil {
		t.Fatalf("select id: %v", err)
	}
	if _, err := testDb.Pool.Exec(ctx, `insert into movie (title, director_id) values ('Single Row Fn Join Movie', $1)`, directorID); err != nil {
		t.Fatalf("insert movie: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"function": "fn_one_director", "schema": "public", "arguments": [%d],
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}}}
	}`, directorID))
	sql, args := mustCompileSelect(t, node)
	if !strings.Contains(sql, `"public"."movie"`) {
		t.Fatalf("expected compiled SQL to reference the joined movie table, got: %s", sql)
	}
	rows := runSelect(t, sql, args)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	movies, ok := rows[0]["movies"].([]any)
	if !ok || len(movies) != 1 {
		t.Fatalf("expected 1 embedded movie, got %#v", rows[0]["movies"])
	}
	movieObj, ok := movies[0].(map[string]any)
	if !ok || movieObj["title"] != "Single Row Fn Join Movie" {
		t.Errorf("expected embedded movie titled 'Single Row Fn Join Movie', got %#v", movies[0])
	}
}

// TestCompileSelect_RecordRelationFunctionRoot exercises pg.Function.
// RecordRelation : a RETURNS TABLE(...) function's columns resolve via its OUT-mode Arguments, not ReturnType.Relation.
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

// TestCompileSelect_RecordRelationCannotBeJoinChild proves a RETURNS TABLE
// function can never be the joined-into side (unindexable) — fails at resolution, not later.
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

// TestCompileSelect_RecordRelationAsOutgoingJoinParent proves the working
// direction : record-relation as the outgoing join's parent side, no local index required.
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

// TestExecuteWrite_RecordRelationRootIsCleanlyRejected proves a write
// against a RETURNS TABLE root is rejected before any DML is generated.
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
		"select": ["own_except", []],
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
		"select": ["own_except", []],
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
		"select": ["own_except", []],
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

	// jsonb_build_object's bound keys need the same untyped-$1 inference
	// fix ARRAY[$1,...] needed (subjectPgType) — must run against real Postgres.
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

	// own_except as a nested VALUE exercises compileShapeAsJsonObject, a
	// separate codepath from compileNode's plain-column select list.
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"id": "id", "basic": ["own_except", ["id"]]},
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

	// "not" (prefix) and "is_not_null" (postfix) exercise both templates
	// in compileUnary's per-operator table — untested until now.
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": ["own_except", []],
		"where": ["and", ["is_not_null", "name"], ["not", ["=", "name", ["Someone Else"]]]]
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

	// Negate (not_in/not_between) : a wrong "not " placement is a syntax
	// error at execution, not compile time — needs real Postgres.
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": ["own_except", []],
		"where": ["and", ["not_in", "name", "Someone Else", "Nobody"], ["not_between", "id", -1, 0]]
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

// TestCompileSelect_OrderByDescNullsLast proves a bare "desc" term emits
// "desc nulls last" explicitly — Postgres's own DESC default is NULLS FIRST.
func TestCompileSelect_OrderByDescNullsLast(t *testing.T) {
	ctx := context.Background()
	var withStudioID, withoutStudioID int
	var studioID int
	if err := testDb.Pool.QueryRow(ctx, `insert into studio (name) values ('DescNullsLast Studio') returning id`).Scan(&studioID); err != nil {
		t.Fatalf("insert studio: %v", err)
	}
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name, studio_id) values ('DescNullsLast With', $1) returning id`, studioID).Scan(&withStudioID); err != nil {
		t.Fatalf("insert director with studio: %v", err)
	}
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name, studio_id) values ('DescNullsLast Without', null) returning id`).Scan(&withoutStudioID); err != nil {
		t.Fatalf("insert director without studio: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "director", "schema": "public",
		"select": {"id": "id", "studio_id": "studio_id"},
		"where": ["in", "id", %d, %d],
		"order_by": [["desc", "studio_id"]]
	}`, withStudioID, withoutStudioID))
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)

	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d : %s", len(rows), sql)
	}
	if rows[0]["studio_id"] == nil {
		t.Fatalf("expected the row WITH a studio_id first (desc, nulls last), got nil first : %#v (sql: %s)", rows, sql)
	}
	if rows[1]["studio_id"] != nil {
		t.Errorf("expected the row WITHOUT a studio_id (null) last, got %#v (sql: %s)", rows, sql)
	}
}

// TestCompileSelect_OrderByAscNullsFirst proves the explicit
// "asc-nulls-first" tag actually reorders nulls to the front.
func TestCompileSelect_OrderByAscNullsFirst(t *testing.T) {
	ctx := context.Background()
	var withStudioID, withoutStudioID int
	var studioID int
	if err := testDb.Pool.QueryRow(ctx, `insert into studio (name) values ('AscNullsFirst Studio') returning id`).Scan(&studioID); err != nil {
		t.Fatalf("insert studio: %v", err)
	}
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name, studio_id) values ('AscNullsFirst With', $1) returning id`, studioID).Scan(&withStudioID); err != nil {
		t.Fatalf("insert director with studio: %v", err)
	}
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name, studio_id) values ('AscNullsFirst Without', null) returning id`).Scan(&withoutStudioID); err != nil {
		t.Fatalf("insert director without studio: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "director", "schema": "public",
		"select": {"id": "id", "studio_id": "studio_id"},
		"where": ["in", "id", %d, %d],
		"order_by": [["asc-nulls-first", "studio_id"]]
	}`, withStudioID, withoutStudioID))
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)

	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d : %s", len(rows), sql)
	}
	if rows[0]["studio_id"] != nil {
		t.Fatalf("expected the row WITHOUT a studio_id first (asc-nulls-first), got %#v (sql: %s)", rows, sql)
	}
	if rows[1]["studio_id"] == nil {
		t.Errorf("expected the row WITH a studio_id last, got %#v (sql: %s)", rows, sql)
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
