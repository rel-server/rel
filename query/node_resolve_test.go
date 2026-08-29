package query

import (
	"context"
	"testing"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/pg"
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
	raw := mustParseRelation(t, `{"relation": "pg_sleep", "schema": "pg_catalog", "arguments": [1]}`)
	if _, err := ctx.ResolveQuery(raw); err == nil {
		t.Fatalf("expected pg_catalog.pg_sleep to be rejected by the default blacklist")
	}
}

func TestResolveQuery_FunctionOverloadResolution(t *testing.T) {
	ctx := &ResolveContext{Db: testDb, Config: testCfg}
	raw := mustParseRelation(t, `{"relation": "fn_overload", "schema": "public", "arguments": [1]}`)
	node, err := ctx.ResolveQuery(raw)
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if node.Function == nil || node.Function.PgNargs != 1 {
		t.Fatalf("expected the 1-arg overload, got %#v", node.Function)
	}

	raw2 := mustParseRelation(t, `{"relation": "fn_overload", "schema": "public", "arguments": [1, 2]}`)
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

func TestResolveQuery_MaxDepth(t *testing.T) {
	shallow := &ResolveContext{Db: testDb, Config: &config.Config{
		Query:     config.Query{MaxDepth: 1},
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
