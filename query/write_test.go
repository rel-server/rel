package query

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// acquireWriteConn returns one pinned connection with a fresh "_data" temp
// table — writes need one physical connection throughout (temp tables are
// connection-scoped, and phase 1's later nodes must see earlier nodes'
// committed "keys"), unlike sql_test.go's reads, which can run each query
// against whatever connection the pool happens to hand back.
func acquireWriteConn(t *testing.T) *pgxpool.Conn {
	t.Helper()
	conn, err := testDb.Pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Cleanup(conn.Release)
	// The pool can hand back a connection a previous test already created
	// "_data" on (temp tables outlive a Release, they're connection-scoped,
	// not request-scoped) — DROP first so this is idempotent regardless of
	// which physical connection the pool happens to reuse.
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

	// director (root, insert) with an incoming "movies" child (default
	// write_mode for an incoming subquery is merge) : movie.director_id
	// must be resolved from the just-inserted director's own key, not from
	// the payload (which doesn't supply it at all).
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

	// movie (root, insert) with an outgoing "director" child (default
	// write_mode for an outgoing subquery is upsert) : movie.director_id
	// must be resolved from the director child's own just-recovered key —
	// the reverse direction from TestExecuteWrite_IncomingChildFK, and the
	// one case that needs the extra "left join _data ocN" in the resolved
	// CTE (outgoingKeySource), not the plain "par" join.
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

// TestExecuteWrite_UnwritableSelect_Rejected covers specs/query-engine.md
// ## Configuration : a relation is writable only when its identity target's
// columns are present, unique, and untransformed in the select output. A
// select omitting the identity column (here, "id") must be rejected up
// front, not silently write a phantom/mismatched identity — see write.go's
// findUnwritableNode doc comment for why this matters (an update whose keys
// are wrong can match zero rows, or the wrong row, and still return 200).
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

// TestExecuteWrite_FunctionRootUnwritable_EvenWithRealTableRelation covers
// specs/query-engine.md ## Reading Algorithm ### Function-rooted nodes : a
// function-rooted node is NEVER writable, even when its return type
// resolves to a real, otherwise-writable table via a real primary key
// (fn_directors() returns setof director — its own Relation IS director's
// real Relation, PK included). Without the query/shape.go IsFunction()
// guard, this would look identical to writing through "director" directly
// and silently succeed, bypassing whatever filtering the function's own SQL
// body does.
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

	// default write_mode for an incoming subquery is "merge" : rows not in
	// the payload get deleted (phase 2), rows present get upserted (phase
	// 1). "Keep Me" is present (by id, so it upserts in place), "Drop Me"
	// is absent, "New One" is new.
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
	// The existing movie is present (by id) but with a DIFFERENT title —
	// merge-new must NOT update it (only insert-new/delete-absent), yet
	// still recover its key so it's not treated as deleted.
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
	// supplied in the payload — this is the same path
	// TestExecuteWrite_PlainInsert already exercises, named explicitly here
	// per the plan's coverage list to make the default-application case a
	// first-class, intentionally-named test rather than incidental.
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

	// insert_columns restricts which payload columns are actually written —
	// "name" is deliberately excluded, so the column should end up
	// null-violating (director.name is NOT NULL, no default) if the filter
	// is respected, proving the payload's "name" value was genuinely
	// dropped rather than merely unused.
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

// TestExecuteWrite_CompositeSubFieldInsert proves specs/query-engine.md ##
// Writability's composite sub-field writability is actually implemented,
// not just derived : inserting through a bare "." chain writes ONLY the
// named sub-field, leaving the composite's other field NULL — Postgres's
// own behavior for a dotted INSERT target against a NULL/absent composite
// base (verified directly against Postgres 16 : "insert into t (col.field)
// ..." populates just that field, the rest of the composite reading back
// NULL), which is exactly what write_dml.go's writeTargetPath relies on
// rather than trying to synthesize a full ROW(...) itself.
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
// chain touches ONLY the named sub-field — the composite's OTHER field,
// already set from before this write, must survive untouched (Postgres's
// own partial-composite-update semantics, verified directly).
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

// TestExecuteWrite_CompositeSubFieldUpsert covers the ON CONFLICT DO UPDATE
// SET path specifically : the composite sub-field target on the conflict
// side must read back off "excluded" using Postgres's row-value
// parenthesization ("(excluded.home).city", not "excluded.home.city",
// which is a syntax error — verified directly against Postgres 16 ; see
// WriteQualifiedPath, resolved_field.go).
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
	// name is required : venue.name is NOT NULL with no default, and an
	// UPSERT's own INSERT side is still fully constructed (and its
	// constraints checked) even when the row is known to already exist —
	// this is a pre-existing requirement of every upsert, unrelated to the
	// composite sub-field this test is actually about.
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

// TestExecuteWrite_CompositeWholeAndSubFieldTogether_Rejected proves the
// one combination genuinely left unhandled : selecting BOTH a composite
// column whole (e.g. via own/full) AND one of its own sub-fields
// independently in the same write is rejected — by Postgres itself
// ("column specified more than once" / "multiple assignments to same
// column", verified directly), not a check this package duplicates.
// query-engine.md ## Writability already treats "home" and "home.city" as
// independent write targets by design ; this is the one case where that
// independence can't actually both apply at the SQL level.
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

	// deleteonly with an empty payload and a "where" that only matches one
	// of the two rows : without compiling node.Where into the delete, "not
	// in (select ... from _data where __node_id=X)" is true for every row
	// once _data is empty, deleting the whole table — query.ts's own
	// write_mode doc comment requires the where condition to also scope
	// deleteonly/merge deletes.
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

func TestKeysColumns_IncludesChildTargetedColumnBeyondOnConflict(t *testing.T) {
	// profile's on_conflict target (user_email) isn't its primary key (id).
	// A child correlating back to the parent via the PK, rather than via
	// the on_conflict column, needs "id" recovered into keys too — or
	// incomingKeySource's "par.keys->>'id'" reads NULL at DML time. This is
	// a direct unit test of keysColumns (no real FK in the fixture schema
	// conveniently targets a non-PK on_conflict column, so a synthetic
	// child node is simpler and more precise than routing through a full
	// ExecuteWrite).
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

	// profile's on_conflict target (user_email) isn't its primary key
	// (id). The payload supplies only user_email (never id), so a
	// conflicting row's "resolved.id" would be a phantom nextval() value,
	// not the real row's id — recoverKeys must be the sole source of keys
	// for merge-new, not a blanket write off "resolved". profile_note (an
	// incoming child correlating via id, not user_email) is what forces id
	// into keysColumns at all — without a child, this bug can't manifest,
	// since nothing downstream ever reads the phantom value.
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

	// merge-new's primary purpose : insert a row that ISN'T already there.
	// A genuinely-new row must (a) get real keys recovered (not left null)
	// and (b) survive phase 2's delete — a row with null keys looks
	// indistinguishable from "not in this request" to runDelete's own
	// "keys is not null" filter, so a bug here manifests as the row being
	// inserted and then immediately deleted again in the same request.
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
	// Symmetric case to TestKeysColumns_IncludesChildTargetedColumnBeyondOnConflict :
	// when node is itself an outgoing child, its parent reads
	// "par.keys->>'<jc.Local.Name>'" off node's own keys — jc.Local must be
	// in node's own keysColumns even when it isn't node's primary key
	// (query.ts's "on" doc : a to-one join may target any unique column).
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

// runSelectOn is sql_test.go's runSelect, but against a specific pinned
// connection instead of testDb.Pool — the write-then-reread tests below
// need "_data" on the SAME connection ExecuteWrite just used.
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
// mechanism applies to the write-then-reread path too, not just an
// ordinary read : CompileSelectForDataNode shares compileNodeCorrelated
// with CompileSelect, so this is really confirming that sharing holds, not
// testing a separately-implemented case. The scalar select lives on a
// READONLY embedded child, not the root : a write's own root/writable
// node always needs its identity columns present in select (##
// Configuration), which a bare scalar select can never satisfy on its
// own — a readonly child has no such requirement, and its own reread goes
// through the exact same shared compileNodeCorrelated path regardless.
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
