package query

import (
	"context"
	"testing"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

var testDb *pg.DbInfos
var testCfg *config.Config

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithInitScripts("../pg/testdata/schema.sql"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		panic(err)
	}
	defer func() {
		_ = container.Terminate(ctx)
	}()

	uri, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic(err)
	}

	testDb, err = pg.NewInfos(uri)
	if err != nil {
		panic(err)
	}

	testCfg = config.Test()

	m.Run()
}

func mustParseRelation(t *testing.T, src string) *rawRelation {
	t.Helper()
	pq, err := ParseQuery([]byte(src))
	if err != nil {
		t.Fatalf("ParseQuery(%s): %v", src, err)
	}
	if pq.Relation == nil {
		t.Fatalf("ParseQuery(%s) did not produce a bare Relation: %#v", src, pq)
	}
	return pq.Relation
}

func TestResolveQuery_SimpleRelation(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	raw := mustParseRelation(t, `{"relation": "director", "schema": "public"}`)
	node, err := ctx.ResolveQuery(raw)
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if node.Relation == nil || node.Relation.Identifier.Name != "director" {
		t.Fatalf("unexpected node.Relation: %#v", node.Relation)
	}
	if node.WriteMode != INSERT {
		t.Errorf("expected root default write_mode INSERT, got %v", node.WriteMode)
	}
	if node.OnConflictConstraintName == "" {
		t.Errorf("expected on_conflict to default to the primary key")
	}
}

func TestResolveQuery_UnqualifiedSearchPath(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	raw := mustParseRelation(t, `{"relation": "director"}`)
	node, err := ctx.ResolveQuery(raw)
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if node.Relation == nil || node.Relation.Identifier.Schema != "public" {
		t.Fatalf("expected public.director via search path, got %#v", node.Relation)
	}
}

func TestResolveQuery_NestedJoin_OutgoingAndIncoming(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	raw := mustParseRelation(t, `{
		"relation": "director",
		"schema": "public",
		"join": {
			"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}}
		}
	}`)
	node, err := ctx.ResolveQuery(raw)
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if len(node.IncomingNodes) != 1 || len(node.OutgoingNodes) != 0 {
		t.Fatalf("expected movie under director to be Incoming (to-many), got Outgoing=%d Incoming=%d", len(node.OutgoingNodes), len(node.IncomingNodes))
	}
	child := node.IncomingNodes[0]
	if child.OuterAlias != "movies" {
		t.Errorf("expected OuterAlias %q, got %q", "movies", child.OuterAlias)
	}
	if child.WriteMode != MERGE {
		t.Errorf("expected incoming child default write_mode MERGE, got %v", child.WriteMode)
	}
	if len(child.JoinColumns) != 1 || child.JoinColumns[0].Local.Name != "director_id" || child.JoinColumns[0].Distant.Name != "id" {
		t.Fatalf("unexpected JoinColumns: %#v", child.JoinColumns)
	}

	// reverse direction : movie -> director is outgoing (to-one)
	raw2 := mustParseRelation(t, `{
		"relation": "movie",
		"schema": "public",
		"join": {
			"director": {"relation": "director", "schema": "public", "on": {"id": "director_id"}}
		}
	}`)
	node2, err := ctx.ResolveQuery(raw2)
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if len(node2.OutgoingNodes) != 1 || len(node2.IncomingNodes) != 0 {
		t.Fatalf("expected director under movie to be Outgoing (to-one), got Outgoing=%d Incoming=%d", len(node2.OutgoingNodes), len(node2.IncomingNodes))
	}
	if node2.OutgoingNodes[0].WriteMode != UPSERT {
		t.Errorf("expected outgoing child default write_mode UPSERT, got %v", node2.OutgoingNodes[0].WriteMode)
	}
}

func TestResolveQuery_DeleteBearingOnOutgoing_Rejected(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	raw := mustParseRelation(t, `{
		"relation": "movie",
		"schema": "public",
		"join": {
			"director": {"relation": "director", "schema": "public", "on": {"id": "director_id"}, "write_mode": "merge"}
		}
	}`)
	if _, err := ctx.ResolveQuery(raw); err == nil {
		t.Fatalf("expected a delete-bearing write_mode on an outgoing relation to be rejected")
	}
}

func TestResolveQuery_OnConflict(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}

	raw := mustParseRelation(t, `{"relation": "director", "schema": "public", "on_conflict": "director_pkey"}`)
	node, err := ctx.ResolveQuery(raw)
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if node.OnConflictConstraintName != "director_pkey" {
		t.Errorf("expected constraint name form to resolve, got %q", node.OnConflictConstraintName)
	}

	raw2 := mustParseRelation(t, `{"relation": "target_t", "schema": "public", "on_conflict": ["x", "y"]}`)
	node2, err := ctx.ResolveQuery(raw2)
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if len(node2.OnConflictColumns) != 2 {
		t.Errorf("expected column-list form to resolve, got %#v", node2.OnConflictColumns)
	}
}

func TestResolveQuery_RelationBlacklist(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	raw := mustParseRelation(t, `{"relation": "tables", "schema": "information_schema"}`)
	if _, err := ctx.ResolveQuery(raw); err == nil {
		t.Fatalf("expected information_schema.tables to be rejected by the default blacklist")
	}
}

func TestResolveQuery_FunctionBlacklist(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	raw := mustParseRelation(t, `{"function": "pg_sleep", "schema": "pg_catalog", "arguments": [1]}`)
	if _, err := ctx.ResolveQuery(raw); err == nil {
		t.Fatalf("expected pg_catalog.pg_sleep to be rejected by the default blacklist")
	}
}

func TestResolveQuery_FunctionOverloadResolution(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	raw := mustParseRelation(t, `{"function": "fn_overload", "schema": "public", "arguments": [1]}`)
	node, err := ctx.ResolveQuery(raw)
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if node.Function == nil || node.Function.PgNargs != 1 {
		t.Fatalf("expected the 1-arg overload, got %#v", node.Function)
	}

	raw2 := mustParseRelation(t, `{"function": "fn_overload", "schema": "public", "arguments": [1, 2]}`)
	node2, err := ctx.ResolveQuery(raw2)
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if node2.Function == nil || node2.Function.PgNargs != 2 {
		t.Fatalf("expected the 2-arg overload, got %#v", node2.Function)
	}
}

func TestResolveQuery_InsertColumnsValidation(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	raw := mustParseRelation(t, `{"relation": "director", "schema": "public", "insert_columns": ["no_such_column"]}`)
	if _, err := ctx.ResolveQuery(raw); err == nil {
		t.Fatalf("expected an unknown insert_columns entry to be rejected")
	}
}

func TestResolveQuery_UpdateColumnsValidation(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	raw := mustParseRelation(t, `{"relation": "director", "schema": "public", "update_columns": ["no_such_column"]}`)
	if _, err := ctx.ResolveQuery(raw); err == nil {
		t.Fatalf("expected an unknown update_columns entry to be rejected")
	}
}

func TestResolveQuery_UnknownRelation(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	raw := mustParseRelation(t, `{"relation": "no_such_relation", "schema": "public"}`)
	if _, err := ctx.ResolveQuery(raw); err == nil {
		t.Fatalf("expected an unknown relation to be rejected")
	}
}

func TestResolveQuery_UnknownFunction(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	raw := mustParseRelation(t, `{"function": "no_such_function", "schema": "public"}`)
	if _, err := ctx.ResolveQuery(raw); err == nil {
		t.Fatalf("expected an unknown function to be rejected")
	}
}

func TestResolveQuery_FunctionNamedArguments(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	// {a: 1} must resolve to the 1-arg overload only, not the 2-arg one
	// (b required, not supplied) — functionAcceptsNames must check required names are covered, not just that given names are valid.
	raw := mustParseRelation(t, `{"function": "fn_overload", "schema": "public", "arguments": {"a": 1}}`)
	node, err := ctx.ResolveQuery(raw)
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if node.Function == nil || node.Function.PgNargs != 1 {
		t.Fatalf("expected the 1-arg overload via named args, got %#v", node.Function)
	}
}

func TestResolveQuery_AmbiguousFunction(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	raw := mustParseRelation(t, `{"function": "fn_ambig", "schema": "public", "arguments": [1]}`)
	if _, err := ctx.ResolveQuery(raw); err == nil {
		t.Fatalf("expected fn_ambig(int)/fn_ambig(text) to be rejected as ambiguous by arity-only matching")
	}
}

func TestResolveQuery_TableValuedFunctionRoot(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	raw := mustParseRelation(t, `{
		"function": "fn_directors",
		"schema": "public"
	}`)
	node, err := ctx.ResolveQuery(raw)
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if node.Function == nil {
		t.Fatalf("expected Function to be set for a function-rooted node")
	}
	if node.Relation == nil || node.Relation.Identifier.Name != "director" {
		t.Fatalf("expected Relation to resolve to director via GetRelationByType, got %#v", node.Relation)
	}

	// and it must be genuinely joinable into, not just carry a Relation
	// pointer that nothing else uses
	rawWithJoin := mustParseRelation(t, `{
		"function": "fn_directors",
		"schema": "public",
		"join": {
			"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}}
		}
	}`)
	nodeWithJoin, err := ctx.ResolveQuery(rawWithJoin)
	if err != nil {
		t.Fatalf("ResolveQuery with join under a function root: %v", err)
	}
	if len(nodeWithJoin.IncomingNodes) != 1 {
		t.Fatalf("expected 1 incoming child joined under the function root, got %d", len(nodeWithJoin.IncomingNodes))
	}
}

func TestResolveQuery_MaxDepth(t *testing.T) {
	shallow := &ResolveContext{Db: testDb, Config: &config.Config{
		Pg:        config.Pg{Query: config.PgQuery{MaxDepth: 1}},
		Blacklist: testCfg.Blacklist,
	}}

	root := mustParseRelation(t, `{"relation": "director", "schema": "public"}`)
	if _, err := shallow.ResolveQuery(root); err != nil {
		t.Fatalf("expected a root-only query to fit within MaxDepth=1, got %v", err)
	}

	nested := mustParseRelation(t, `{
		"relation": "director",
		"schema": "public",
		"join": {
			"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}}
		}
	}`)
	if _, err := shallow.ResolveQuery(nested); err == nil {
		t.Fatalf("expected a depth-2 query to exceed MaxDepth=1")
	}
}

// JoinColumns must be deterministically sorted-by-local-column-name
// regardless of Go's randomized map iteration — re-parses fresh each loop to get a genuinely different iteration order per run.
func TestResolveQuery_JoinColumnsOrderIsDeterministic(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	src := `{
		"relation": "target_t",
		"schema": "public",
		"join": {
			"src": {"relation": "src_t", "schema": "public", "on": {"b": "y", "a": "x"}}
		}
	}`
	for i := range 30 {
		raw := mustParseRelation(t, src)
		node, err := ctx.ResolveQuery(raw)
		if err != nil {
			t.Fatalf("iteration %d: ResolveQuery: %v", i, err)
		}
		if len(node.IncomingNodes) != 1 {
			t.Fatalf("iteration %d: expected 1 incoming child, got %d", i, len(node.IncomingNodes))
		}
		cols := node.IncomingNodes[0].JoinColumns
		if len(cols) != 2 || cols[0].Local.Name != "a" || cols[1].Local.Name != "b" {
			t.Fatalf("iteration %d: expected JoinColumns sorted [a, b] by local name, got %#v", i, cols)
		}
	}
}

// Two children under the same parent, and the alias each self-registers
// under, must also come out in deterministic (sorted-by-alias) order.
func TestResolveQuery_MultipleChildrenUnderOneParent(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	src := `{
		"relation": "director",
		"schema": "public",
		"join": {
			"movies_b": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}},
			"movies_a": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}}
		}
	}`
	for i := range 10 {
		raw := mustParseRelation(t, src)
		node, err := ctx.ResolveQuery(raw)
		if err != nil {
			t.Fatalf("iteration %d: ResolveQuery: %v", i, err)
		}
		if len(node.IncomingNodes) != 2 {
			t.Fatalf("iteration %d: expected 2 incoming children, got %d", i, len(node.IncomingNodes))
		}
		if node.IncomingNodes[0].OuterAlias != "movies_a" || node.IncomingNodes[1].OuterAlias != "movies_b" {
			t.Fatalf("iteration %d: expected children sorted by alias [movies_a, movies_b], got [%s, %s]",
				i, node.IncomingNodes[0].OuterAlias, node.IncomingNodes[1].OuterAlias)
		}
	}
}

func TestResolveQuery_ExplicitWriteModeOverridesRootDefault(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	raw := mustParseRelation(t, `{"relation": "director", "schema": "public", "write_mode": "update"}`)
	node, err := ctx.ResolveQuery(raw)
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if node.WriteMode != UPDATE {
		t.Errorf("expected explicit write_mode \"update\" to override the root's INSERT default, got %v", node.WriteMode)
	}
}

func TestResolveQuery_NoPrimaryKey_OnConflictLeftUnresolved(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	raw := mustParseRelation(t, `{"relation": "no_pk_t", "schema": "public"}`)
	node, err := ctx.ResolveQuery(raw)
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if node.OnConflictConstraintName != "" || len(node.OnConflictColumns) != 0 {
		t.Errorf("expected no on_conflict to resolve to anything for a PK-less relation, got constraint=%q columns=%v",
			node.OnConflictConstraintName, node.OnConflictColumns)
	}
}
