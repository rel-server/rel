package query

import (
	"fmt"
	"testing"

	"github.com/ceymard/rel/pg"
	"github.com/samber/oops"
)

// ParseAndResolve parses a bare Expression JSON (not a full query) and
// resolves it against a throwaway "director" relation node — for testing
// resolveExpr's handling of wrapper node types (between/in/any/all/
// concat_ws/arr/lst/slice) directly, without needing a full query to wrap
// each one in.
func ParseAndResolve(t *testing.T, exprJSON string) (Expression, error) {
	t.Helper()
	return parseAndResolveOn(t, "director", exprJSON)
}

func parseAndResolveOn(t *testing.T, relation, exprJSON string) (Expression, error) {
	t.Helper()
	node := mustResolveQuery(t, fmt.Sprintf(`{"relation": %q, "schema": "public"}`, relation))
	expr, err := ParseExpression([]byte(exprJSON))
	if err != nil {
		t.Fatalf("ParseExpression: %v", err)
	}
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	return ctx.resolveExpr(expr, node, oops.With("test", relation))
}

func mustResolveQuery(t *testing.T, src string) *QueryNode {
	t.Helper()
	raw := mustParseRelation(t, src)
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	node, err := ctx.ResolveQuery(raw)
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if err := ctx.ResolveExpressions(node); err != nil {
		t.Fatalf("ResolveExpressions: %v", err)
	}
	if err := ctx.DeriveShapes(node); err != nil {
		t.Fatalf("DeriveShapes: %v", err)
	}
	return node
}

func resolveQueryExpectError(t *testing.T, src string) error {
	t.Helper()
	raw := mustParseRelation(t, src)
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	node, err := ctx.ResolveQuery(raw)
	if err != nil {
		return err
	}
	if err := ctx.ResolveExpressions(node); err != nil {
		return err
	}
	return ctx.DeriveShapes(node)
}

func TestResolveExpressions_BareColumn(t *testing.T) {
	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "where": "name"}`)
	id, ok := node.Where.(*Identifier)
	if !ok {
		t.Fatalf("expected *Identifier, got %#v", node.Where)
	}
	cp, ok := id.Resolved.(ColumnPath)
	if !ok || len(cp.Path) != 1 || cp.Path[0].Name != "name" {
		t.Fatalf("expected ColumnPath{name}, got %#v", id.Resolved)
	}
}

func TestResolveExpressions_SelfAlias(t *testing.T) {
	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "alias": "d", "where": [".", "d", "name"]}`)
	folded, ok := node.Where.(FoldedExpr)
	if !ok || folded.Op != FoldDot {
		t.Fatalf("expected FoldedExpr(.), got %#v", node.Where)
	}
	id, ok := folded.Right.(*Identifier)
	if !ok {
		t.Fatalf("expected *Identifier right, got %#v", folded.Right)
	}
	cp, ok := id.Resolved.(ColumnPath)
	if !ok || cp.Path[0].Name != "name" {
		t.Fatalf("expected self-reference to land on ColumnPath{name}, got %#v", id.Resolved)
	}
}

func TestResolveExpressions_ChildAliasHop(t *testing.T) {
	node := mustResolveQuery(t, `{
		"relation": "director",
		"schema": "public",
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}}},
		"where": [".", "movies", "title"]
	}`)
	folded := node.Where.(FoldedExpr)
	id := folded.Right.(*Identifier)
	cp, ok := id.Resolved.(ColumnPath)
	if !ok || cp.Path[0].Name != "title" || cp.Node != node.IncomingNodes[0] {
		t.Fatalf("expected hop into movies to land on movie.title, got %#v", id.Resolved)
	}
}

func TestResolveExpressions_CompositeChain(t *testing.T) {
	node := mustResolveQuery(t, `{"relation": "venue", "schema": "public", "where": [".", "home", "city"]}`)
	folded := node.Where.(FoldedExpr)
	id := folded.Right.(*Identifier)
	cp, ok := id.Resolved.(ColumnPath)
	if !ok || len(cp.Path) != 2 || cp.Path[0].Name != "home" || cp.Path[1].Name != "city" {
		t.Fatalf("expected ColumnPath{home,city}, got %#v", id.Resolved)
	}
}

func TestResolveExpressions_CompositeCollision(t *testing.T) {
	node := mustResolveQuery(t, `{
		"relation": "venue",
		"schema": "public",
		"select": {"a": [".", "home", "city"], "b": [".", "work", "city"], "c": "home"}
	}`)
	obj := node.Select.(ObjectExpr)
	homeCity := obj.Fields["a"].(FoldedExpr).Right.(*Identifier).Resolved.(ColumnPath)
	workCity := obj.Fields["b"].(FoldedExpr).Right.(*Identifier).Resolved.(ColumnPath)
	homeAlone := obj.Fields["c"].(*Identifier).Resolved.(ColumnPath)

	if homeCity.Key() == workCity.Key() {
		t.Errorf("home.city and work.city must not collide, both keyed %q", homeCity.Key())
	}
	if homeCity.Key() == homeAlone.Key() {
		t.Errorf("home.city and home (alone) must not collide, both keyed %q", homeCity.Key())
	}
	// terminal *pg.Column pointer IS shared (same composite type), which is
	// exactly why Key() must not rely on it alone
	if homeCity.Path[1] != workCity.Path[1] {
		t.Fatalf("test premise broken : expected home.city and work.city to share the same *pg.Column (addr_t.city)")
	}
}

func TestResolveExpressions_ChainPastScalar(t *testing.T) {
	err := resolveQueryExpectError(t, `{"relation": "director", "schema": "public", "where": [".", "name", "x"]}`)
	if err == nil {
		t.Fatalf("expected chaining past a non-composite column to be rejected")
	}
}

func TestResolveExpressions_UnresolvableIdentifier(t *testing.T) {
	err := resolveQueryExpectError(t, `{"relation": "director", "schema": "public", "where": "no_such_column"}`)
	if err == nil {
		t.Fatalf("expected an unresolvable identifier to be rejected")
	}
}

func TestResolveExpressions_NameCollision(t *testing.T) {
	// director has a column "name" ; alias the movies join also "name" ->
	// collision, must be a hard error, not silently resolved by precedence.
	err := resolveQueryExpectError(t, `{
		"relation": "director",
		"schema": "public",
		"join": {"name": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}}},
		"where": "name"
	}`)
	if err == nil {
		t.Fatalf("expected a column/alias name collision to be rejected")
	}
}

func TestResolveExpressions_JsonArrowNotScopeResolved(t *testing.T) {
	// "nonexistent_key" is not a column of venue, but -> never scope-resolves
	// its right side, so this must succeed.
	node := mustResolveQuery(t, `{"relation": "venue", "schema": "public", "where": ["->", "metadata", ["nonexistent_key"]]}`)
	folded := node.Where.(FoldedExpr)
	if folded.Op != FoldJsonGet {
		t.Fatalf("expected FoldJsonGet, got %v", folded.Op)
	}
	left := folded.Left.(*Identifier)
	if _, ok := left.Resolved.(ColumnPath); !ok {
		t.Fatalf("expected Left (metadata) to resolve normally, got %#v", left.Resolved)
	}
	// Right must be untouched : still a StringLiteral, never scope-resolved
	if _, ok := folded.Right.(StringLiteral); !ok {
		t.Fatalf("expected Right to stay a StringLiteral (untouched), got %#v", folded.Right)
	}
}

func TestResolveExpressions_CallAndAgg(t *testing.T) {
	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": {"x": ["call", {"schema": "public", "name": "fn_overload"}, "id"]}}`)
	obj := node.Select.(ObjectExpr)
	call, ok := obj.Fields["x"].(*CallExpr)
	if !ok || call.ResolvedFunction == nil || call.ResolvedFunction.PgNargs != 1 {
		t.Fatalf("expected CallExpr resolved to the 1-arg fn_overload, got %#v", obj.Fields["x"])
	}
}

func TestResolveExpressions_CallBlacklisted(t *testing.T) {
	err := resolveQueryExpectError(t, `{"relation": "director", "schema": "public", "select": {"x": ["call", {"schema": "pg_catalog", "name": "pg_sleep"}, 1]}}`)
	if err == nil {
		t.Fatalf("expected a blacklisted call to be rejected")
	}
}

func TestResolveExpressions_CallAmbiguous(t *testing.T) {
	err := resolveQueryExpectError(t, `{"relation": "director", "schema": "public", "select": {"x": ["call", {"schema": "public", "name": "fn_ambig"}, 1]}}`)
	if err == nil {
		t.Fatalf("expected an ambiguous call (fn_ambig(int)/fn_ambig(text)) to be rejected")
	}
}

func TestResolveExpressions_AggRequiresAggregateKind(t *testing.T) {
	// fn_overload is a plain function, not an aggregate ; "agg" must reject it.
	err := resolveQueryExpectError(t, `{"relation": "director", "schema": "public", "select": {"x": ["agg", {"schema": "public", "name": "fn_overload"}, ["id"]]}}`)
	if err == nil {
		t.Fatalf("expected \"agg\" to reject a non-aggregate function")
	}
}

func TestResolveExpressions_GetSetColumn(t *testing.T) {
	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": {"x": ["get-set", "name"]}}`)
	obj := node.Select.(ObjectExpr)
	gs, ok := obj.Fields["x"].(*GetSetExpr)
	if !ok || gs.ResolvedColumn == nil || gs.ResolvedColumn.Name != "name" {
		t.Fatalf("expected GetSetExpr resolved to column name, got %#v", obj.Fields["x"])
	}
}

func TestResolveExpressions_GetSetUnknownColumn(t *testing.T) {
	err := resolveQueryExpectError(t, `{"relation": "director", "schema": "public", "select": {"x": ["set", "no_such_column"]}}`)
	if err == nil {
		t.Fatalf("expected an unknown set column to be rejected")
	}
}

func TestResolveExpressions_ExceptValidation(t *testing.T) {
	err := resolveQueryExpectError(t, `{"relation": "director", "schema": "public", "select": ["full-except", ["no_such_column"]]}`)
	if err == nil {
		t.Fatalf("expected an unknown except column to be rejected")
	}
}

func TestDeriveShapes_Writability(t *testing.T) {
	// director.id (single, bare) : writable. director.name referenced twice
	// (once bare, once via coalesce) : the bare one is fine on its own but
	// duplicated -> not writable.
	node := mustResolveQuery(t, `{
		"relation": "director",
		"schema": "public",
		"write_mode": "update",
		"select": {"id": "id", "n1": "name", "n2": ["coalesce", "name", ["x"]]}
	}`)
	idKey := (ColumnPath{Node: node, Path: []*pg.Column{node.Relation.ColumnsMap["id"]}}).Key()
	nameKey := (ColumnPath{Node: node, Path: []*pg.Column{node.Relation.ColumnsMap["name"]}}).Key()

	foundID, foundName := false, false
	for _, ex := range node.Shape.Extractors {
		if ex.Path.Key() == idKey {
			foundID = true
		}
		if ex.Path.Key() == nameKey {
			foundName = true
		}
	}
	if !foundID {
		t.Errorf("expected director.id (single, bare) to be a writable extractor")
	}
	if foundName {
		t.Errorf("expected director.name (referenced twice) to NOT be a writable extractor")
	}
}

func TestDeriveShapes_CoalesceWrappedIsWritable(t *testing.T) {
	node := mustResolveQuery(t, `{
		"relation": "director",
		"schema": "public",
		"write_mode": "update",
		"select": {"name": ["coalesce", "name", ["unknown"]]}
	}`)
	nameKey := (ColumnPath{Node: node, Path: []*pg.Column{node.Relation.ColumnsMap["name"]}}).Key()
	found := false
	for _, ex := range node.Shape.Extractors {
		if ex.Path.Key() == nameKey {
			found = true
			if len(ex.JsonPath) != 1 || ex.JsonPath[0] != "name" {
				t.Errorf("expected JsonPath [name], got %v", ex.JsonPath)
			}
		}
	}
	if !found {
		t.Errorf("expected a coalesce-wrapped single reference to be writable")
	}
}

func TestDeriveShapes_OtherwiseWrappedIsNotWritable(t *testing.T) {
	node := mustResolveQuery(t, `{
		"relation": "director",
		"schema": "public",
		"write_mode": "update",
		"select": {"n": ["format", "%s!", "name"]}
	}`)
	nameKey := (ColumnPath{Node: node, Path: []*pg.Column{node.Relation.ColumnsMap["name"]}}).Key()
	for _, ex := range node.Shape.Extractors {
		if ex.Path.Key() == nameKey {
			t.Errorf("expected a format()-wrapped reference to NOT be writable")
		}
	}
}

func TestDeriveShapes_SetSharesOccurrenceBucketWithBare(t *testing.T) {
	// "name" referenced once via a bare column AND once via set : that's two
	// occurrences of the same column, so it must NOT be writable — set does
	// not get its own separate allowance.
	node := mustResolveQuery(t, `{
		"relation": "director",
		"schema": "public",
		"write_mode": "update",
		"select": {"n1": "name", "n2": ["set", "name"]}
	}`)
	nameKey := (ColumnPath{Node: node, Path: []*pg.Column{node.Relation.ColumnsMap["name"]}}).Key()
	for _, ex := range node.Shape.Extractors {
		if ex.Path.Key() == nameKey {
			t.Errorf("expected \"name\" referenced via both bare and set to NOT be writable (shared bucket)")
		}
	}
}

func TestDeriveShapes_GetExcludedFromWritability(t *testing.T) {
	// "name" referenced via get (excluded) plus a real bare reference
	// elsewhere would still be writable if get truly doesn't count — but
	// simplest direct check : get alone contributes no extractor at all.
	node := mustResolveQuery(t, `{
		"relation": "director",
		"schema": "public",
		"write_mode": "update",
		"select": {"n": ["get", "name"]}
	}`)
	nameKey := (ColumnPath{Node: node, Path: []*pg.Column{node.Relation.ColumnsMap["name"]}}).Key()
	for _, ex := range node.Shape.Extractors {
		if ex.Path.Key() == nameKey {
			t.Errorf("expected get's column to never contribute an extractor")
		}
	}
}

func TestDeriveShapes_DefaultFullSelectWritable(t *testing.T) {
	// No "select" at all -> defaults to FullExpr{} (pass 1) -> every own
	// column should come out as a clean, single occurrence.
	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "write_mode": "update"}`)
	idKey := (ColumnPath{Node: node, Path: []*pg.Column{node.Relation.ColumnsMap["id"]}}).Key()
	found := false
	for _, ex := range node.Shape.Extractors {
		if ex.Path.Key() == idKey {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the default full select to contribute a clean occurrence for id")
	}
	if !node.Shape.Writable {
		t.Errorf("expected the default full select to make the relation's identity (PK) writable")
	}
}

func TestDeriveShapes_RelationRequiresIdentityWritable(t *testing.T) {
	// id excluded from select -> the PK is never referenced -> not writable,
	// even though other columns are fine.
	node := mustResolveQuery(t, `{
		"relation": "director",
		"schema": "public",
		"write_mode": "update",
		"select": ["own-except", ["id"]]
	}`)
	if node.Shape.Writable {
		t.Errorf("expected the relation to be unwritable when its PK is excluded from select")
	}
}

func TestDeriveShapes_ReadOnlySkipsWritability(t *testing.T) {
	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "write_mode": "readonly"}`)
	if node.Shape.Extractors != nil {
		t.Errorf("expected no extractors to be computed for a readonly node")
	}
	if !node.Shape.Writable {
		t.Errorf("expected a readonly node's own Writable to be trivially true (nothing to check)")
	}
}

func TestDeriveShapes_TreeLevelReadOnlyPropagation(t *testing.T) {
	// child excludes its own PK from select (unwritable), and is not itself
	// explicitly readonly -> must force the parent unwritable too.
	node := mustResolveQuery(t, `{
		"relation": "director",
		"schema": "public",
		"write_mode": "update",
		"join": {
			"movies": {
				"relation": "movie", "schema": "public", "on": {"director_id": "id"},
				"write_mode": "update",
				"select": ["own-except", ["id"]]
			}
		}
	}`)
	if node.IncomingNodes[0].Shape.Writable {
		t.Fatalf("test premise broken : expected the child itself to be unwritable")
	}
	if node.Shape.Writable {
		t.Errorf("expected an unwritable, non-readonly child to force the parent unwritable")
	}
}

func TestDeriveShapes_ExplicitReadonlyChildDoesNotPropagate(t *testing.T) {
	node := mustResolveQuery(t, `{
		"relation": "director",
		"schema": "public",
		"write_mode": "update",
		"join": {
			"movies": {
				"relation": "movie", "schema": "public", "on": {"director_id": "id"},
				"write_mode": "readonly",
				"select": ["own-except", ["id"]]
			}
		}
	}`)
	if !node.Shape.Writable {
		t.Errorf("expected an explicitly readonly child to NOT force the parent unwritable")
	}
}

func TestResolveExpressions_DomainWrappedComposite(t *testing.T) {
	// depot.location is nested_t{label, addr}, and addr is addr_domain (a
	// domain over addr_t) — regression for the domain-unwrap fix : composite
	// navigation through a domain-typed composite FIELD (not a top-level
	// column, which information_schema.columns already auto-unwraps) must
	// still work.
	node := mustResolveQuery(t, `{"relation": "depot", "schema": "public", "where": [".", [".", "location", "addr"], "city"]}`)
	outer := node.Where.(FoldedExpr)
	id := outer.Right.(*Identifier)
	cp, ok := id.Resolved.(ColumnPath)
	if !ok || len(cp.Path) != 3 || cp.Path[0].Name != "location" || cp.Path[1].Name != "addr" || cp.Path[2].Name != "city" {
		t.Fatalf("expected ColumnPath{location,addr,city}, got %#v", id.Resolved)
	}
}

func TestResolveExpressions_ArrayIndexThenDot(t *testing.T) {
	// ["index", "addresses", 1] lands with ElementType set (the array's
	// element type, addr_t) ; the subsequent ".city" hop then appends "city"
	// to Path using that element type for its composite check — the FINAL
	// landing (city's own) is Path=[addresses, city], no longer needing the
	// override since city's own Type is what a further hop would check.
	node := mustResolveQuery(t, `{"relation": "warehouse", "schema": "public", "where": [".", ["index", "addresses", 1], "city"]}`)
	outer := node.Where.(FoldedExpr)
	id := outer.Right.(*Identifier)
	cp, ok := id.Resolved.(ColumnPath)
	if !ok || len(cp.Path) != 2 || cp.Path[0].Name != "addresses" || cp.Path[1].Name != "city" {
		t.Fatalf("expected ColumnPath{addresses,city}, got %#v", id.Resolved)
	}
}

func TestResolveExpressions_IndexIntoNonArray_Error(t *testing.T) {
	err := resolveQueryExpectError(t, `{"relation": "director", "schema": "public", "where": [".", ["index", "name", 1], "x"]}`)
	if err == nil {
		t.Fatalf("expected indexing a non-array column to be rejected")
	}
}

func TestResolveExpressions_ComputedKeyHop(t *testing.T) {
	node := mustResolveQuery(t, `{
		"relation": "director",
		"schema": "public",
		"join": {"movies": {
			"relation": "movie", "schema": "public", "on": {"director_id": "id"},
			"select": ["own-and", {"t": "title"}]
		}},
		"where": [".", "movies", "t"]
	}`)
	folded := node.Where.(FoldedExpr)
	id := folded.Right.(*Identifier)
	cp, ok := id.Resolved.(ColumnPath)
	if !ok || cp.Path[0].Name != "title" {
		t.Fatalf("expected the computed key \"t\" to land on movie.title, got %#v", id.Resolved)
	}
}

func TestResolveExpressions_NestedObjectLiteralHop(t *testing.T) {
	node := mustResolveQuery(t, `{
		"relation": "director",
		"schema": "public",
		"join": {"movies": {
			"relation": "movie", "schema": "public", "on": {"director_id": "id"},
			"select": {"info": {"t": "title"}}
		}},
		"where": [".", [".", "movies", "info"], "t"]
	}`)
	outer := node.Where.(FoldedExpr)
	id := outer.Right.(*Identifier)
	cp, ok := id.Resolved.(ColumnPath)
	if !ok || cp.Path[0].Name != "title" {
		t.Fatalf("expected the nested literal's \"t\" key to land on movie.title, got %#v", id.Resolved)
	}
}

func TestResolveExpressions_OwnAndNestedInsideObjectLiteral(t *testing.T) {
	node := mustResolveQuery(t, `{
		"relation": "director",
		"schema": "public",
		"join": {"movies": {
			"relation": "movie", "schema": "public", "on": {"director_id": "id"},
			"select": {"info": ["own-and", {"t": "title"}]}
		}},
		"where": [".", [".", "movies", "info"], "t"]
	}`)
	outer := node.Where.(FoldedExpr)
	id := outer.Right.(*Identifier)
	cp, ok := id.Resolved.(ColumnPath)
	if !ok || cp.Path[0].Name != "title" {
		t.Fatalf("expected own-and's computed key \"t\", nested inside an object literal, to land on movie.title, got %#v", id.Resolved)
	}
}

func TestResolveExpressions_OwnAndNestedInsideObjectLiteral_BaseColumn(t *testing.T) {
	node := mustResolveQuery(t, `{
		"relation": "director",
		"schema": "public",
		"join": {"movies": {
			"relation": "movie", "schema": "public", "on": {"director_id": "id"},
			"select": {"info": ["own-and", {"t": "title"}]}
		}},
		"where": [".", [".", "movies", "info"], "id"]
	}`)
	outer := node.Where.(FoldedExpr)
	id := outer.Right.(*Identifier)
	cp, ok := id.Resolved.(ColumnPath)
	if !ok || cp.Path[0].Name != "id" {
		t.Fatalf("expected own-and's base column \"id\", nested inside an object literal, to be reachable, got %#v", id.Resolved)
	}
}

func TestResolveExpressions_ExceptAndKeyOverridesOmittedColumn(t *testing.T) {
	// query.ts's own-except-and/full-except-and comment : "and" MAY specify a
	// key that was omitted via "except" — that's a legal override, not a
	// build-time collision, since the base no longer has that column once
	// omitted. (A parent hopping externally into "movies.title" afterwards
	// is a separate question, and genuinely ambiguous — Scope always sees
	// the real "title" column regardless of what select exports it as, so
	// that name simultaneously means two different things from outside ;
	// covered by TestResolveExpressions_AndKeyImplicitlyShadowsColumn_Error's
	// sibling case below, not asserted successful here.)
	node := mustResolveQuery(t, `{
		"relation": "director",
		"schema": "public",
		"join": {"movies": {
			"relation": "movie", "schema": "public", "on": {"director_id": "id"},
			"select": ["own-except-and", ["title"], {"title": "id"}]
		}}
	}`)
	movies := node.IncomingNodes[0]
	field, ok := movies.Shape.Fields["title"]
	cp, isCol := field.(ColumnPath)
	if !ok || !isCol || cp.Path[0].Name != "id" {
		t.Fatalf("expected the overridden \"title\" export key to land on movie.id, got %#v", field)
	}
}

func TestResolveExpressions_AndKeyImplicitlyShadowsColumn_Error(t *testing.T) {
	// query.ts's comment : "merge_with cannot shadow keys implicitly ; this
	// is an error" — a plain own-and colliding with a real, non-omitted
	// column must be rejected, not silently overridden.
	err := resolveQueryExpectError(t, `{
		"relation": "director",
		"schema": "public",
		"join": {"movies": {
			"relation": "movie", "schema": "public", "on": {"director_id": "id"},
			"select": ["own-except-and", ["director_id"], {"title": "id"}]
		}},
		"where": [".", "movies", "title"]
	}`)
	if err == nil {
		t.Fatalf("expected \"title\" (not omitted) colliding with the and key to be rejected as an implicit shadow")
	}
}

func TestResolveExpressions_SelfHopInsideOwnSelect_Error(t *testing.T) {
	// A node's own select referencing one of its own COMPUTED keys through a
	// "." chain into its own alias must not see its own Shape (same "no
	// forward-reference within one select object, no sibling access" rule
	// that already applies elsewhere) — must be a clean hard error, not
	// unbounded recursion (selectShape -> resolveChain -> self *QueryNode
	// landing -> resolveExternalHop -> selectShape again). A self-hop into a
	// plain physical column (as opposed to a computed key) is NOT this case
	// — that's ordinary Scope access, same as any other self-reference, and
	// resolves fine regardless of select's own state ; see
	// TestResolveExpressions_SelfAlias.
	err := resolveQueryExpectError(t, `{
		"relation": "director",
		"schema": "public",
		"alias": "d",
		"select": ["own-and", {"x": "name", "y": [".", "d", "x"]}]
	}`)
	if err == nil {
		t.Fatalf("expected a select hopping into one of its own node's computed keys via a self-alias to be rejected, not silently resolved or to hang")
	}
}

func TestResolveExpressions_SelfHopFromWhereIntoOwnShape_Error(t *testing.T) {
	// Same rule as TestResolveExpressions_SelfHopInsideOwnSelect_Error, but
	// reached from "where" instead of "select" : where resolves BEFORE
	// select (ResolveExpressions), so without ctx.resolvingOwn this hop
	// would reach selectShape while node.Select is still fully unresolved
	// scope-only (LookupInScope finds nothing for a name that only exists
	// as an own-and computed key), silently returning the computed value
	// instead of being rejected — a node's own where must not see its own
	// select's computed keys, regardless of resolution order between them.
	err := resolveQueryExpectError(t, `{
		"relation": "director",
		"schema": "public",
		"alias": "d",
		"select": ["own-and", {"x": "name"}],
		"where": [".", "d", "x"]
	}`)
	if err == nil {
		t.Fatalf("expected where hopping into its own node's computed select key via a self-alias to be rejected")
	}
}

func TestResolveExpressions_ExceptAndOverrideAmbiguousFromOutside_Error(t *testing.T) {
	// The except-and override in TestResolveExpressions_ExceptAndKeyOverridesOmittedColumn
	// is legal to BUILD (the base no longer has "title" once omitted, so no
	// collision), but that key is still unreferenceable from a PARENT's
	// where/order_by under that same name : Scope always sees the real
	// "title" column regardless of what select exports it as, so an
	// external hop by "title" is genuinely ambiguous between the real
	// column and the overridden export value — must stay a hard error via
	// resolveExternalHop's scope/shape disagreement check.
	err := resolveQueryExpectError(t, `{
		"relation": "director",
		"schema": "public",
		"join": {"movies": {
			"relation": "movie", "schema": "public", "on": {"director_id": "id"},
			"select": ["own-except-and", ["title"], {"title": "id"}]
		}},
		"where": [".", "movies", "title"]
	}`)
	if err == nil {
		t.Fatalf("expected hopping into \"movies.title\" (real column vs. overridden export key) to be rejected as ambiguous")
	}
}

func TestResolveExpressions_ComputedKeyCollidesWithColumn_Error(t *testing.T) {
	err := resolveQueryExpectError(t, `{
		"relation": "director",
		"schema": "public",
		"join": {"movies": {
			"relation": "movie", "schema": "public", "on": {"director_id": "id"},
			"select": ["own-and", {"title": "id"}]
		}},
		"where": [".", "movies", "title"]
	}`)
	if err == nil {
		t.Fatalf("expected a computed key colliding with a real column name to be rejected as ambiguous")
	}
}

func TestResolveExpressions_UnknownComputedKey_Error(t *testing.T) {
	err := resolveQueryExpectError(t, `{
		"relation": "director",
		"schema": "public",
		"join": {"movies": {
			"relation": "movie", "schema": "public", "on": {"director_id": "id"},
			"select": ["own-and", {"t": "title"}]
		}},
		"where": [".", "movies", "no_such_key"]
	}`)
	if err == nil {
		t.Fatalf("expected an unknown key (neither a column, alias, nor computed key) to be rejected")
	}
}

func TestResolveExpressions_ChainPastJsonOpaque_Error(t *testing.T) {
	// "->" lands opaque (jsonb navigation, not "." semantics) : chaining a
	// "." hop off of it must be rejected, not silently treated as chaining
	// off whatever "metadata" itself was.
	err := resolveQueryExpectError(t, `{"relation": "venue", "schema": "public", "where": [".", ["->", "metadata", ["key"]], "x"]}`)
	if err == nil {
		t.Fatalf("expected chaining \".\" off a ->-opaque value to be rejected")
	}
}

func TestResolveExpressions_IndexOpaqueExpression_NoError(t *testing.T) {
	// Indexing something that isn't a known ColumnPath (here, an inline "arr"
	// literal) must resolve successfully (its Items still get resolved) and
	// simply produce an opaque (unchainable) landing — not an error. Only
	// indexing a real, non-array COLUMN is an error (IndexIntoNonArray_Error).
	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "where": ["index", ["arr", "id", "id"], 1]}`)
	idx, ok := node.Where.(IndexExpr)
	if !ok {
		t.Fatalf("expected IndexExpr, got %#v", node.Where)
	}
	arr, ok := idx.Array.(ArrExpr)
	if !ok || len(arr.Items) != 2 {
		t.Fatalf("expected the inline array's Items to still be resolved, got %#v", idx.Array)
	}
	for i, item := range arr.Items {
		if _, ok := item.(*Identifier).Resolved.(ColumnPath); !ok {
			t.Errorf("expected arr.Items[%d] to still resolve to a ColumnPath, got %#v", i, item)
		}
	}
}

func TestResolveExpressions_FullAndKeyCollidesWithChildAlias_Error(t *testing.T) {
	// buildShape's collision check must catch an "and" key colliding with a
	// CHILD ALIAS (only present in "full"'s base, not "own"'s), not just a
	// physical column — ownFullBase folds aliases into the same base map a
	// plain column would occupy, so the same collision rule must apply.
	err := resolveQueryExpectError(t, `{
		"relation": "director",
		"schema": "public",
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}}},
		"select": ["full-and", {"movies": "name"}]
	}`)
	if err == nil {
		t.Fatalf("expected an \"and\" key colliding with a child join alias (via full's base) to be rejected")
	}
}

func TestResolveExpressions_BetweenSubExpressionsResolve(t *testing.T) {
	// Min/Exp/Max are three distinct fields ; use three distinct queries,
	// each with a bad identifier in exactly one position, to prove all three
	// (not just Min, the first one, easy to typo-copy the other two from) are
	// actually threaded through resolution.
	if _, err := ParseAndResolve(t, `["between", "no_such_column", "id", "id"]`); err == nil {
		t.Fatalf("expected an unresolvable Min to be rejected")
	}
	if _, err := ParseAndResolve(t, `["between", "id", "no_such_column", "id"]`); err == nil {
		t.Fatalf("expected an unresolvable Exp to be rejected")
	}
	if _, err := ParseAndResolve(t, `["between", "id", "id", "no_such_column"]`); err == nil {
		t.Fatalf("expected an unresolvable Max to be rejected")
	}
	expr, err := ParseAndResolve(t, `["between", "id", "name", "id"]`)
	if err != nil {
		t.Fatalf("ParseAndResolve: %v", err)
	}
	b := expr.(BetweenExpr)
	if _, ok := b.Min.(*Identifier).Resolved.(ColumnPath); !ok {
		t.Errorf("expected Min resolved, got %#v", b.Min)
	}
	if _, ok := b.Exp.(*Identifier).Resolved.(ColumnPath); !ok {
		t.Errorf("expected Exp resolved, got %#v", b.Exp)
	}
	if _, ok := b.Max.(*Identifier).Resolved.(ColumnPath); !ok {
		t.Errorf("expected Max resolved, got %#v", b.Max)
	}
}

func TestResolveExpressions_InCandidatesResolve(t *testing.T) {
	// A bare JSON string candidate is ALWAYS a literal (query.ts's explicit
	// carve-out) and must stay untouched, never scope-resolved even if it
	// happens to spell a real column name ; a non-literal candidate (here,
	// wrapped in a single-arg coalesce so it isn't a bare string) DOES
	// resolve, and an unresolvable one is rejected.
	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "where": ["in", "id", "name", ["coalesce", "id"]]}`)
	in := node.Where.(InExpr)
	if !in.Candidates[0].IsLiteral || in.Candidates[0].Literal != "name" {
		t.Fatalf("expected candidate 0 to stay a literal \"name\", got %#v", in.Candidates[0])
	}
	coal, ok := in.Candidates[1].Expr.(CoalesceExpr)
	if !ok {
		t.Fatalf("expected candidate 1 to be a CoalesceExpr, got %#v", in.Candidates[1].Expr)
	}
	if _, ok := coal.Args[0].(*Identifier).Resolved.(ColumnPath); !ok {
		t.Errorf("expected candidate 1's inner identifier resolved, got %#v", coal.Args[0])
	}

	if _, err := ParseAndResolve(t, `["in", "id", ["coalesce", "no_such_column"]]`); err == nil {
		t.Fatalf("expected an unresolvable non-literal candidate to be rejected")
	}
}

func TestResolveExpressions_AnyAllSubExpressionsResolve(t *testing.T) {
	if _, err := ParseAndResolve(t, `["any", "=", "no_such_column", "id"]`); err == nil {
		t.Fatalf("expected an unresolvable Subject to be rejected")
	}
	if _, err := ParseAndResolve(t, `["any", "=", "id", "no_such_column"]`); err == nil {
		t.Fatalf("expected an unresolvable Array to be rejected")
	}
	expr, err := ParseAndResolve(t, `["all", "=", "id", "name"]`)
	if err != nil {
		t.Fatalf("ParseAndResolve: %v", err)
	}
	a := expr.(AnyAllExpr)
	if _, ok := a.Subject.(*Identifier).Resolved.(ColumnPath); !ok {
		t.Errorf("expected Subject resolved, got %#v", a.Subject)
	}
	if _, ok := a.Array.(*Identifier).Resolved.(ColumnPath); !ok {
		t.Errorf("expected Array resolved, got %#v", a.Array)
	}
}

func TestResolveExpressions_ConcatWsSubExpressionsResolve(t *testing.T) {
	// Separator is itself a generic Expression (not a raw string), so a bare
	// JSON string there parses as an *Identifier* to resolve, same as
	// anywhere else — ["x"] is query.ts's one-element-array escape hatch for
	// an actual string literal, used below once Separator is meant to be a
	// literal rather than deliberately a column reference under test.
	if _, err := ParseAndResolve(t, `["concat_ws", "no_such_column", "id"]`); err == nil {
		t.Fatalf("expected an unresolvable Separator to be rejected")
	}
	if _, err := ParseAndResolve(t, `["concat_ws", ["-"], "no_such_column"]`); err == nil {
		t.Fatalf("expected an unresolvable Args entry to be rejected")
	}
	expr, err := ParseAndResolve(t, `["concat_ws", ["-"], "id", "name"]`)
	if err != nil {
		t.Fatalf("ParseAndResolve: %v", err)
	}
	c := expr.(ConcatWsExpr)
	for i, a := range c.Args {
		if _, ok := a.(*Identifier).Resolved.(ColumnPath); !ok {
			t.Errorf("expected Args[%d] resolved, got %#v", i, a)
		}
	}
}

func TestResolveExpressions_ArrLstSubExpressionsResolve(t *testing.T) {
	if _, err := ParseAndResolve(t, `["arr", "id", "no_such_column"]`); err == nil {
		t.Fatalf("expected an unresolvable \"arr\" item to be rejected")
	}
	if _, err := ParseAndResolve(t, `["lst", "id", "no_such_column"]`); err == nil {
		t.Fatalf("expected an unresolvable \"lst\" item to be rejected")
	}
}

func TestResolveExpressions_SliceSubExpressionsResolve(t *testing.T) {
	// Array/From/To are three distinct fields ; use an unresolvable
	// identifier in each position individually (rather than a literal
	// number, which resolveExpr's default no-op case would pass through
	// whether or not it was ever actually threaded) to prove all three are
	// actually resolved, not just Array (copy-paste risk on the other two).
	// venue has no array column ; use warehouse.addresses (addr_t[]).
	if _, err := parseAndResolveOn(t, "warehouse", `["slice", "no_such_column", "id", "id"]`); err == nil {
		t.Fatalf("expected an unresolvable Array to be rejected")
	}
	if _, err := parseAndResolveOn(t, "warehouse", `["slice", "addresses", "no_such_column", "id"]`); err == nil {
		t.Fatalf("expected an unresolvable From to be rejected")
	}
	if _, err := parseAndResolveOn(t, "warehouse", `["slice", "addresses", "id", "no_such_column"]`); err == nil {
		t.Fatalf("expected an unresolvable To to be rejected")
	}

	expr, err := parseAndResolveOn(t, "warehouse", `["slice", "addresses", "id", "id"]`)
	if err != nil {
		t.Fatalf("ParseAndResolve: %v", err)
	}
	s := expr.(SliceExpr)
	if _, ok := s.Array.(*Identifier).Resolved.(ColumnPath); !ok {
		t.Errorf("expected Array resolved, got %#v", s.Array)
	}
	if _, ok := s.From.(*Identifier).Resolved.(ColumnPath); !ok {
		t.Errorf("expected From resolved, got %#v", s.From)
	}
	if _, ok := s.To.(*Identifier).Resolved.(ColumnPath); !ok {
		t.Errorf("expected To resolved, got %#v", s.To)
	}

	// A slice's landing is opaque : chaining "." past it must be rejected,
	// same as any other non-composite/non-shape landing.
	if _, err := parseAndResolveOn(t, "warehouse", `[".", ["slice", "addresses", 1, 2], "x"]`); err == nil {
		t.Fatalf("expected chaining \".\" past a slice to be rejected")
	}
}

func TestResolveExpressions_RootFunctionArgumentBareIdentifier_Error(t *testing.T) {
	// A root-level function call (no parent) has nothing to correlate a bare
	// identifier against — only literals/params are legal there.
	err := resolveQueryExpectError(t, `{"relation": "fn_plain_add", "schema": "public", "arguments": ["no_such_column", 2]}`)
	if err == nil {
		t.Fatalf("expected a bare identifier in a root function call's arguments to be rejected (no parent scope)")
	}
}

func TestResolveExpressions_GetSetOnRelationlessFunctionNode_Error(t *testing.T) {
	// fn_plain_add returns a scalar (int), not a relation : Relation is nil,
	// so a "select" trying to get/set a column has nothing to resolve
	// against — must be a clean error, not a nil-pointer panic.
	err := resolveQueryExpectError(t, `{
		"relation": "fn_plain_add", "schema": "public", "arguments": [1, 2],
		"select": {"x": ["get", "id"]}
	}`)
	if err == nil {
		t.Fatalf("expected get/set on a relation-less function node to be rejected")
	}
}

func TestResolveExpressions_SelfHopFromOrderByIntoOwnShape_Error(t *testing.T) {
	// Same rule as the where/select self-hop cases, extended to order_by :
	// ctx.resolvingOwn is set for node's ENTIRE own-expression block
	// (where/select/distinct_on/order_by alike), not just where/select.
	err := resolveQueryExpectError(t, `{
		"relation": "director",
		"schema": "public",
		"alias": "d",
		"select": ["own-and", {"x": "name"}],
		"order_by": [[".", "d", "x"]]
	}`)
	if err == nil {
		t.Fatalf("expected order_by hopping into its own node's computed select key via a self-alias to be rejected")
	}
}

func TestDeriveShapes_BareCompositeChainIsWritable(t *testing.T) {
	// Regression : a bare composite "." chain used directly as a select
	// value (e.g. {"c": [".", "home", "city"]}) is ONE reference to the
	// terminal sub-field, and must be writable on its own, same as any other
	// single clean reference. The pre-fix walkSelectForWritability treated
	// FoldDot like a generic binary fold — walking BOTH "home" (the
	// navigation prefix) and "home.city" (the terminal) each with
	// coalesceOnly forced false — which meant a composite chain used bare in
	// select was NEVER writable, structurally, regardless of duplication.
	node := mustResolveQuery(t, `{
		"relation": "venue",
		"schema": "public",
		"write_mode": "update",
		"select": {"id": "id", "c": [".", "home", "city"]}
	}`)
	homeCityKey := (ColumnPath{Node: node, Path: []*pg.Column{
		node.Relation.ColumnsMap["home"],
		node.Relation.ColumnsMap["home"].Type.Relation.ColumnsMap["city"],
	}}).Key()
	found := false
	for _, ex := range node.Shape.Extractors {
		if ex.Path.Key() == homeCityKey {
			found = true
			if len(ex.JsonPath) != 1 || ex.JsonPath[0] != "c" {
				t.Errorf("expected JsonPath [c], got %v", ex.JsonPath)
			}
		}
	}
	if !found {
		t.Errorf("expected a bare composite \".\" chain (home.city) to be a writable extractor")
	}
}

func TestDeriveShapes_ChildHopChainIsNotParentWritable(t *testing.T) {
	// Regression : a "." chain that hops INTO A CHILD (e.g.
	// [".", "movies", "title"], as opposed to genuine same-node composite
	// navigation like [".", "home", "city"]) lands on a ColumnPath whose
	// Node is the CHILD, not the node doing the selecting. That write target
	// belongs to the child's own Shape derivation, never the parent's —
	// found while fixing TestDeriveShapes_BareCompositeChainIsWritable :  an
	// early version of that fix recorded ANY FoldDot landing on a ColumnPath
	// unconditionally, which spuriously attributed the CHILD's column as a
	// clean, writable extractor of the PARENT.
	node := mustResolveQuery(t, `{
		"relation": "director",
		"schema": "public",
		"write_mode": "update",
		"select": {"id": "id", "t": [".", "movies", "title"]},
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}}}
	}`)
	for _, ex := range node.Shape.Extractors {
		if ex.Path.Node != node {
			t.Errorf("expected no extractor attributed to a foreign node, got %#v (node=%p, expected=%p)", ex, ex.Path.Node, node)
		}
	}
}

func TestDeriveShapes_IndexedCompositeChainIsNotWritable(t *testing.T) {
	// Conservative-by-design exclusion : a "." chain that hops through an
	// ["index", ...] anywhere along the way (e.g.
	// [".", ["index", "addresses", 1], "city"]) must NOT become a writable
	// extractor, even as a single, otherwise-clean reference — ColumnPath's
	// Key() carries no record of WHICH array element was navigated through
	// (only ElementType, which a further "." hop clears again once it lands
	// on the element's own field), so two different indices would otherwise
	// collapse to the identical extractor key with no way for a write-side
	// extractor to know which array element a value belongs to. Whether an
	// indexed element should be a legal write target at all is an open
	// design question, not something to default into silently.
	node := mustResolveQuery(t, `{
		"relation": "warehouse",
		"schema": "public",
		"write_mode": "update",
		"select": {"id": "id", "c": [".", ["index", "addresses", 1], "city"]}
	}`)
	for _, ex := range node.Shape.Extractors {
		if len(ex.JsonPath) == 1 && ex.JsonPath[0] == "c" {
			t.Errorf("expected an indexed composite chain to NOT be a writable extractor, got %#v", ex)
		}
	}
}

func TestDeriveShapes_DuplicateCompositeChainIsNotWritable(t *testing.T) {
	// Same sub-field referenced twice via a composite chain : must NOT be
	// writable, same exactly-once rule as a plain column.
	node := mustResolveQuery(t, `{
		"relation": "venue",
		"schema": "public",
		"write_mode": "update",
		"select": {"c1": [".", "home", "city"], "c2": [".", "home", "city"]}
	}`)
	homeCityKey := (ColumnPath{Node: node, Path: []*pg.Column{
		node.Relation.ColumnsMap["home"],
		node.Relation.ColumnsMap["home"].Type.Relation.ColumnsMap["city"],
	}}).Key()
	for _, ex := range node.Shape.Extractors {
		if ex.Path.Key() == homeCityKey {
			t.Errorf("expected home.city (referenced twice) to NOT be a writable extractor")
		}
	}
}

func TestDeriveShapes_CompositeChainAndContainingColumnAreIndependent(t *testing.T) {
	// "home" (the whole composite column) and "home.city" (a sub-field of
	// it) referenced as TWO DIFFERENT select keys are independent write
	// targets (composite sub-fields are independently writable, this
	// session's decision) — neither should count as an occurrence of the
	// other, so both come out writable.
	node := mustResolveQuery(t, `{
		"relation": "venue",
		"schema": "public",
		"write_mode": "update",
		"select": {"whole": "home", "sub": [".", "home", "city"]}
	}`)
	homeKey := (ColumnPath{Node: node, Path: []*pg.Column{node.Relation.ColumnsMap["home"]}}).Key()
	homeCityKey := (ColumnPath{Node: node, Path: []*pg.Column{
		node.Relation.ColumnsMap["home"],
		node.Relation.ColumnsMap["home"].Type.Relation.ColumnsMap["city"],
	}}).Key()
	foundHome, foundHomeCity := false, false
	for _, ex := range node.Shape.Extractors {
		if ex.Path.Key() == homeKey {
			foundHome = true
		}
		if ex.Path.Key() == homeCityKey {
			foundHomeCity = true
		}
	}
	if !foundHome {
		t.Errorf("expected the whole \"home\" column to be independently writable")
	}
	if !foundHomeCity {
		t.Errorf("expected \"home.city\" to be independently writable, unaffected by \"home\" also being selected")
	}
}
