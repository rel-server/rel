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

// runSelectScalar is runSelect's counterpart for a scalar-selected node
// (query-engine.md ## Reading Algorithm ### Scalar-selected nodes) : each
// row's single "json" column is a bare value (a string, a JSON array, ...),
// not an object, so it decodes into "any" rather than map[string]any.
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

// TestCompileSelect_ComputedColumnSelfAlias exercises specs/query-engine.md's
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

// TestCompileSelect_RootScalarSelect proves query-engine.md ## Reading
// Algorithm ### Scalar-selected nodes : a table-rooted node whose own
// select is a bare column (not own/full/an object literal) reads back as a
// flat JSON array of scalars, not an array of one-key objects — the
// "distinct shape" a scalar FUNCTION root already got (## Response Shape),
// now available for an ordinary table root too. TEXT is the important type
// to prove, not an integer : to_jsonb's quoting is what makes this safe at
// all — an unquoted raw string is not valid JSON on its own.
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
// isn't limited to a bare column — any non-shape-producing expression
// works, compiled and to_jsonb-cast the same way.
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
// through a to-one embed : movie's "director" key becomes a bare string
// (the director's name), not a nested {"name": ...} object.
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

// TestCompileSelect_ToManyEmbedScalarSelect proves the to-many side :
// director's "movies" key becomes a flat array of bare title strings, not
// an array of {"title": ...} objects.
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

// TestCompileSelect_ScalarHopThroughOutgoing proves query-engine.md's new
// "." hop through a to-one relation : ["own-and", {"director_name": [".",
// "director", "name"]}] pulls director.name straight into movie's own flat
// select, with no nested "director" object at all — the mechanism
// discussed this session as an alternative to always embedding the whole
// child.
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
		"select": ["own-and", {"director_name": [".", "director", "name"]}],
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

// TestCompileSelect_ScalarHopThroughOutgoing_NullTarget proves a scalar hop
// through a to-one relation that doesn't exist (director.studio_id is
// nullable) reads back as JSON null, not an error — the correlated
// subquery simply returns zero rows, and Postgres's own scalar-subquery
// rule ("no rows" -> NULL) does the rest.
func TestCompileSelect_ScalarHopThroughOutgoing_NullTarget(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('No Studio Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "director", "schema": "public",
		"select": ["own-and", {"studio_name": [".", "studio", "name"]}],
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
// chained through TWO to-one relations (movie -> director -> studio)
// compiles as nested scalar correlated subqueries, per compileScalarHop's
// own recursion through compileScalarHopWhere -> compileColumnPath.
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
		"select": ["own-and", {"studio_name": [".", [".", "director", "studio"], "name"]}],
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
// be BOTH fully embedded AND reached via a "." hop in the same select,
// without alias collision — compileScalarHop always allocates its own
// fresh alias and never consults c.alias[child], precisely so this can't
// pick up (or collide with) the full embed's own, separately-scoped alias.
// Redundant (the join executes twice), by design — see compileScalarHop's
// own doc comment on why deduplication isn't attempted here.
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
		"select": ["own-and", {"director": "director", "director_name": [".", "director", "name"]}],
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

// TestCompileSelect_ScalarHopThroughIncoming_Rejected proves the other
// half : resolution allows a "." hop into a to-many child (it has other
// uses — see resolveHopInto's own doc comment), but compiling it as a
// plain scalar select value is rejected at SQL-compile time, same
// late-compile-stage pattern "agg"'s own opposite restriction uses.
func TestCompileSelect_ScalarHopThroughIncoming_Rejected(t *testing.T) {
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": ["own-and", {"a_title": [".", "movies", "title"]}],
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

// TestCompileSelect_LateralSharedChild_ScalarSelect is
// TestCompileSelect_LateralSharedChild with "movies" itself scalar-selected
// — the highest-risk path this session's scalar-select work touched :
// compileLateralJoin's own json_agg(...) wrapping (the "arr" column shared
// LATERAL children materialize once for every consumer) must also switch
// between row_to_json and the bare "__scalar" column, or a LATERAL-shared
// scalar child would silently embed {"__scalar": ...} objects instead of
// bare values.
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

// TestCompileSelect_OrderByDescNullsLast proves query.ts's "asc and desc are
// nulls last by default" for a bare "desc" term specifically — Postgres's
// own native default for DESC is NULLS FIRST (only plain ASC defaults to
// NULLS LAST), verified directly against Postgres 16 ; compileOrderBy must
// emit "desc nulls last" explicitly rather than relying on Postgres's own
// default, or this promise is silently broken for every descending sort.
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
// "asc-nulls-first" tag (query.ts's own opt-out of ASC's usual nulls-last
// default) actually reorders nulls to the front — previously untested :
// nothing exercised this tag's SQL compilation at all before this test.
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
