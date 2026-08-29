package query

import (
	"testing"

	"github.com/ceymard/rel/pg"
)

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
	if err := DeriveShapes(node); err != nil {
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
	return DeriveShapes(node)
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
