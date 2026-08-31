// Copyright 2025 Christophe Eymard
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package pg tests. These are a first pass exercising the specific bugs and
// behaviours worked out while building the introspection layer — meant to be
// folded into a more complete suite later on, not to be exhaustive now.
package pg

import (
	"context"
	"testing"

	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

var testDb *DbInfos
var testDbURI string

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithInitScripts("testdata/schema.sql"),
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
	testDbURI = uri

	testDb, err = NewInfos(uri)
	if err != nil {
		panic(err)
	}

	m.Run()
}

func relationByName(t *testing.T, name string) *Relation {
	t.Helper()
	for _, r := range testDb.Relations {
		if r.Identifier.Name == name {
			return r
		}
	}
	t.Fatalf("relation %q not found in introspected schema", name)
	return nil
}

// ---- regression : function arguments for a plain all-IN function -----------

func TestFunctionArguments_PlainInArgs(t *testing.T) {
	var fn *Function
	for _, f := range testDb.Functions {
		if f.Identifier.Name == "fn_plain_add" {
			fn = f
			break
		}
	}
	if fn == nil {
		t.Fatalf("function fn_plain_add not found in introspected schema")
	}

	if len(fn.Arguments) != 2 {
		t.Fatalf("expected 2 arguments, got %d (%+v) — this is the proallargtypes/proargmodes NULL regression if it comes back empty", len(fn.Arguments), fn.Arguments)
	}

	if fn.Arguments[0].Name != "a" || fn.Arguments[1].Name != "b" {
		t.Errorf("expected argument names [a b], got [%s %s]", fn.Arguments[0].Name, fn.Arguments[1].Name)
	}
	if !fn.Arguments[0].IsIn() || !fn.Arguments[1].IsIn() {
		t.Errorf("expected both arguments to default to IN mode, got %q and %q", fn.Arguments[0].PgMode, fn.Arguments[1].PgMode)
	}
	if fn.Arguments[0].Type == nil || fn.Arguments[0].Type.PgIdentifier.Name != "int4" {
		t.Errorf("expected argument a to resolve to int4, got %+v", fn.Arguments[0].Type)
	}
}

// ---- RecordRelation : RETURNS TABLE(...) gets its own synthetic column list ---

// TestFunction_RecordRelation_BuiltFromTableModeArguments confirms
// pg.Function.RecordRelation resolves movie_counts_by_director's own
// RETURNS TABLE(director_id int, movie_count bigint) columns — the actual
// bug this was built to catch : Postgres's proargmode for a RETURNS TABLE
// pseudo-column is 't' (MODE_TABLE), NOT 'o' (a plain OUT parameter) ; an
// implementation checking IsOut() alone would silently build an empty
// RecordRelation (or none at all) for exactly this, the single most common
// case the feature exists for.
func TestFunction_RecordRelation_BuiltFromTableModeArguments(t *testing.T) {
	var fn *Function
	for _, f := range testDb.Functions {
		if f.Identifier.Name == "movie_counts_by_director" {
			fn = f
			break
		}
	}
	if fn == nil {
		t.Fatalf("function movie_counts_by_director not found in introspected schema")
	}

	if fn.RecordRelation == nil {
		t.Fatalf("expected RecordRelation to be built for a RETURNS TABLE function")
	}
	if !fn.RecordRelation.IsSynthetic {
		t.Errorf("expected RecordRelation.IsSynthetic to be true")
	}
	if len(fn.RecordRelation.Columns) != 2 {
		t.Fatalf("expected 2 columns, got %d (%+v)", len(fn.RecordRelation.Columns), fn.RecordRelation.Columns)
	}
	if c := fn.RecordRelation.ColumnsMap["director_id"]; c == nil || c.Type == nil || c.Type.PgIdentifier.Name != "int4" {
		t.Errorf("expected director_id to resolve to int4, got %+v", c)
	}
	if c := fn.RecordRelation.ColumnsMap["movie_count"]; c == nil || c.Type == nil || c.Type.PgIdentifier.Name != "int8" {
		t.Errorf("expected movie_count to resolve to int8 (bigint), got %+v", c)
	}

	// Structural write/join safety : the whole design relies on these being
	// genuinely unset (nil/empty), not just absent from this one test's
	// assertions — see the RecordRelation-building comment in
	// info_type.go's FillTypeInformations for why that's what actually
	// keeps it unwritable and ineligible as a join's covered/child side.
	if fn.RecordRelation.PrimaryKey != nil {
		t.Errorf("expected no PrimaryKey on a synthetic record relation")
	}
	if len(fn.RecordRelation.Indexes) != 0 {
		t.Errorf("expected no Indexes on a synthetic record relation")
	}
	if fn.RecordRelation.IsIndexed([]string{"director_id"}) {
		t.Errorf("expected IsIndexed to be false for any column set on a synthetic record relation")
	}
	if fn.RecordRelation.FindUniqueConstraint([]string{"director_id"}) != nil {
		t.Errorf("expected FindUniqueConstraint to find nothing on a synthetic record relation")
	}
}

// TestFunction_RecordRelation_NilForOrdinaryFunction confirms the fallback
// doesn't fire for functions that never needed it — a function with no
// OUT/TABLE-mode arguments at all (fn_plain_add) and one whose SETOF return
// type already resolves via the ordinary Type.Relation path (fn_directors)
// should both leave RecordRelation nil.
func TestFunction_RecordRelation_NilForOrdinaryFunction(t *testing.T) {
	for _, name := range []string{"fn_plain_add", "fn_directors"} {
		var fn *Function
		for _, f := range testDb.Functions {
			if f.Identifier.Name == name {
				fn = f
				break
			}
		}
		if fn == nil {
			t.Fatalf("function %s not found in introspected schema", name)
		}
		if fn.RecordRelation != nil {
			t.Errorf("expected %s.RecordRelation to be nil, got %+v", name, fn.RecordRelation)
		}
	}
}

// ---- regression : composite FK column pairing -------------------------------

func TestForeignKey_CompositePairing(t *testing.T) {
	src := relationByName(t, "src_t")
	target := relationByName(t, "target_t")

	rels := src.RelationshipsTo(target)
	if len(rels) != 1 {
		t.Fatalf("expected exactly 1 relationship src_t -> target_t, got %d", len(rels))
	}
	c := rels[0]

	if len(c.Columns) != 2 || len(c.Target.Columns) != 2 {
		t.Fatalf("expected 2 columns on each side, got %d local / %d target", len(c.Columns), len(c.Target.Columns))
	}

	// True declared pairing is b<->y, a<->x (see testdata/schema.sql). If the
	// target side were independently alphabetized instead of following the
	// true conkey/confkey correspondence, this would come back as b<->x, a<->y.
	if c.Columns[0].Name != "b" || c.Target.Columns[0].Name != "y" {
		t.Errorf("expected first pair b<->y, got %s<->%s", c.Columns[0].Name, c.Target.Columns[0].Name)
	}
	if c.Columns[1].Name != "a" || c.Target.Columns[1].Name != "x" {
		t.Errorf("expected second pair a<->x, got %s<->%s", c.Columns[1].Name, c.Target.Columns[1].Name)
	}

	// ResolveJoin must accept the true pairing...
	if _, _, err := src.ResolveJoin(target, map[string]string{"b": "y", "a": "x"}); err != nil {
		t.Errorf("expected the true pairing to resolve, got error: %v", err)
	}

	// ...reject a mapping onto columns with no relationship at all (target_t.z
	// is unconstrained)...
	if _, _, err := src.ResolveJoin(target, map[string]string{"b": "z", "a": "x"}); err == nil {
		t.Errorf("expected a mapping through the unconstrained column z to be rejected, but it resolved")
	}

	// ...and reject the same-set-different-order swap ({b:x, a:y}) even though
	// target_t(x,y) being unique and src_t(b,a) being indexed would otherwise
	// make it pass the generic non-FK eligibility check : it reuses exactly
	// fk_composite's column set with an inverted pairing, which ResolveJoin
	// treats as near-certainly a mistake rather than a deliberate second
	// relationship.
	if _, _, err := src.ResolveJoin(target, map[string]string{"b": "x", "a": "y"}); err == nil {
		t.Errorf("expected the inverted pairing over fk_composite's own column set to be rejected, but it resolved")
	}
}

// ---- ResolveJoin : cardinality follows the child's own uniqueness ----------

func TestResolveJoin_OutgoingIsToMany(t *testing.T) {
	movie := relationByName(t, "movie")
	director := relationByName(t, "director")

	c, isToOne, err := movie.ResolveJoin(director, map[string]string{"director_id": "id"})
	if err != nil {
		t.Fatalf("expected movie -> director to resolve, got error: %v", err)
	}
	if isToOne {
		t.Errorf("expected movie -> director to be to-many (movie.director_id is not unique), got to-one")
	}
	if !c.Type.IsOutgoingForeignKey() {
		t.Errorf("expected an outgoing foreign key constraint, got %v", c.Type)
	}
}

func TestResolveJoin_IncomingIsToOne(t *testing.T) {
	movie := relationByName(t, "movie")
	director := relationByName(t, "director")

	// Same relationship, opposite tree orientation : director embedded as a
	// child of movie. Cardinality flips because it's now director's own
	// column (id, the PK) that's being checked for uniqueness, not movie's.
	c, isToOne, err := director.ResolveJoin(movie, map[string]string{"id": "director_id"})
	if err != nil {
		t.Fatalf("expected director -> movie to resolve, got error: %v", err)
	}
	if !isToOne {
		t.Errorf("expected director -> movie (as child) to be to-one (director.id is the PK), got to-many")
	}
	if !c.Type.IsIncomingForeignKey() {
		t.Errorf("expected an incoming foreign key constraint, got %v", c.Type)
	}
}

// ---- ResolveJoin : unindexed FK is a hard error -----------------------------

func TestResolveJoin_UnindexedIsHardError(t *testing.T) {
	child := relationByName(t, "unindexed_child")
	parent := relationByName(t, "unindexed_parent")

	_, _, err := child.ResolveJoin(parent, map[string]string{"parent_id": "id"})
	if err == nil {
		t.Fatalf("expected an unindexed join to be rejected, but it resolved")
	}
}

// ---- RelationshipsTo : two distinct FKs to the same other relation ---------

func TestRelationshipsTo_MultipleDistinctFKs(t *testing.T) {
	orderT := relationByName(t, "order_t")
	customer := relationByName(t, "customer")

	rels := orderT.RelationshipsTo(customer)
	if len(rels) != 2 {
		t.Fatalf("expected 2 distinct relationships order_t -> customer, got %d", len(rels))
	}

	if _, _, err := orderT.ResolveJoin(customer, map[string]string{"customer_id": "id"}); err != nil {
		t.Errorf("expected on:{customer_id:id} to resolve, got error: %v", err)
	}
	if _, _, err := orderT.ResolveJoin(customer, map[string]string{"billing_customer_id": "id"}); err != nil {
		t.Errorf("expected on:{billing_customer_id:id} to resolve, got error: %v", err)
	}
}

// ---- ResolveJoin : non-FK path, unique on the parent side only ------------

func TestResolveJoin_NonFKUniqueOnParentSide(t *testing.T) {
	account := relationByName(t, "account")
	profile := relationByName(t, "profile")

	// No FK backs this ; eligibility comes from profile.user_email being
	// unique, and it's indexed on account's side (idx_account_email).
	// Cardinality must still be to-many : account.email itself is not unique,
	// even though the join is eligible via the *parent's* uniqueness.
	_, isToOne, err := account.ResolveJoin(profile, map[string]string{"email": "user_email"})
	if err != nil {
		t.Fatalf("expected the non-FK unique+indexed join to resolve, got error: %v", err)
	}
	if isToOne {
		t.Errorf("expected to-many (account.email is not unique, only profile.user_email is), got to-one")
	}
}

// ---- FindConstraintByName / FindUniqueConstraint ---------------------------

func TestFindConstraintByName(t *testing.T) {
	movie := relationByName(t, "movie")
	if movie.PrimaryKey == nil {
		t.Fatalf("expected movie to have a primary key")
	}
	if c := movie.FindConstraintByName(movie.PrimaryKey.Name); c != movie.PrimaryKey {
		t.Errorf("FindConstraintByName(%q) did not return the primary key constraint", movie.PrimaryKey.Name)
	}
}

func TestFindUniqueConstraint(t *testing.T) {
	target := relationByName(t, "target_t")

	if u := target.FindUniqueConstraint([]string{"x", "y"}); u == nil {
		t.Errorf("expected a unique constraint on (x, y)")
	}
	// Order shouldn't matter — it's a set match.
	if u := target.FindUniqueConstraint([]string{"y", "x"}); u == nil {
		t.Errorf("expected a unique constraint on (y, x), order-independent")
	}
	if u := target.FindUniqueConstraint([]string{"x"}); u != nil {
		t.Errorf("expected no unique constraint on (x) alone")
	}
}

// ---- index introspection : INCLUDE / partial / expression exclusion -------

func TestIsIndexed_CoveringIndexIgnoresIncludeColumns(t *testing.T) {
	orders := relationByName(t, "orders")

	if !orders.IsIndexed([]string{"customer_id"}) {
		t.Errorf("expected customer_id to be indexed (idx_covering / idx_plain)")
	}
	if !orders.IsIndexed([]string{"customer_id", "total"}) {
		t.Errorf("expected (customer_id, total) to be indexed (idx_plain)")
	}
	if orders.IsIndexed([]string{"total"}) {
		t.Errorf("total alone must not be considered indexed : it's only a trailing/INCLUDE column, never a leading key column")
	}
}

func TestIsIndexed_ExcludesPartialIndex(t *testing.T) {
	orders := relationByName(t, "orders")

	if orders.IsIndexed([]string{"flag"}) {
		t.Errorf("flag is only covered by a partial index (idx_partial_only) and must not count")
	}
}

func TestIsIndexed_ExcludesExpressionIndex(t *testing.T) {
	orders := relationByName(t, "orders")

	if orders.IsIndexed([]string{"note"}) {
		t.Errorf("note is only covered by an expression index (idx_expr_only) and must not count")
	}
}

func TestIndexes_RawListExcludesPartialAndExpression(t *testing.T) {
	orders := relationByName(t, "orders")

	for _, idx := range orders.Indexes {
		if idx.Name == "idx_partial_only" || idx.Name == "idx_expr_only" {
			t.Errorf("expected %s to be excluded from introspection entirely, but it's present in Indexes", idx.Name)
		}
	}
}

// ---- introspection must be complete, pg_catalog/information_schema included ----

func TestIntrospection_IncludesPgCatalog(t *testing.T) {
	found := false
	for _, r := range testDb.Relations {
		if r.Identifier.Schema == "pg_catalog" && r.Identifier.Name == "pg_class" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected pg_catalog.pg_class to be introspected as an ordinary relation — introspection must see the whole database ; excluding pg_catalog/information_schema is a query-compile-time concern (query-engine.md ### Scoping), not an introspection-time one")
	}
}

func TestIntrospection_IncludesInformationSchema(t *testing.T) {
	found := false
	for _, r := range testDb.Relations {
		if r.Identifier.Schema == "information_schema" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected at least one information_schema relation to be introspected")
	}
}

// ---- regression : composite type <-> relation resolution ------------------

func TestType_CompositeResolvesBackToRelation(t *testing.T) {
	movie := relationByName(t, "movie")

	if movie.Type == nil {
		t.Fatalf("expected movie to have a resolved composite Type")
	}
	if !movie.Type.IsComposite() {
		t.Errorf("expected movie's Type.IsComposite() to be true")
	}
	if movie.Type.Relation != movie {
		t.Errorf("expected movie's Type.Relation to point back to movie itself")
	}
}

// ---- regression : comments (relation, column, function) are introspected --

func TestComments_RelationAndColumn(t *testing.T) {
	director := relationByName(t, "director")

	if director.Comment != "a film director" {
		t.Errorf("expected director's Comment to be %q, got %q", "a film director", director.Comment)
	}

	name := director.ColumnsMap["name"]
	if name == nil {
		t.Fatalf("expected director.name column to exist")
	}
	if name.Comment != "the director's full name" {
		t.Errorf("expected director.name's Comment to be %q, got %q", "the director's full name", name.Comment)
	}

	// A column with no COMMENT ON should read as an empty string, not error.
	id := director.ColumnsMap["id"]
	if id == nil || id.Comment != "" {
		t.Errorf("expected director.id to have an empty Comment, got %q", id.Comment)
	}
}

func TestComments_Function(t *testing.T) {
	var fn *Function
	for _, f := range testDb.Functions {
		if f.Identifier.Name == "fn_plain_add" {
			fn = f
			break
		}
	}
	if fn == nil {
		t.Fatalf("function fn_plain_add not found in introspected schema")
	}
	if fn.Comment != "adds two numbers" {
		t.Errorf("expected fn_plain_add's Comment to be %q, got %q", "adds two numbers", fn.Comment)
	}
}

func TestType_ArrayResolution(t *testing.T) {
	var intArray *Type
	for i := range testDb.Types {
		ty := &testDb.Types[i]
		if ty.PgIdentifier.Schema == "pg_catalog" && ty.PgIdentifier.Name == "_int4" {
			intArray = ty
			break
		}
	}
	if intArray == nil {
		t.Fatalf("expected pg_catalog._int4 (the array type of int4) to be introspected")
	}
	if !intArray.IsArray() {
		t.Errorf("expected _int4.IsArray() to be true")
	}
	if intArray.ElementType == nil || intArray.ElementType.PgIdentifier.Name != "int4" {
		t.Errorf("expected _int4's ElementType to resolve to int4, got %+v", intArray.ElementType)
	}
}

// ---- regression : pg.pool_size (config.Pg.PoolSize) actually caps the
// query pool, never the short-lived introspection connection -------------

func TestNewInfosAdminQuery_PoolSizeAppliesToQueryPoolOnly(t *testing.T) {
	db, err := NewInfosAdminQuery(testDbURI, testDbURI, 3, "")
	if err != nil {
		t.Fatalf("NewInfosAdminQuery: %v", err)
	}
	defer db.Pool.Close()

	if got := db.Pool.Config().MaxConns; got != 3 {
		t.Errorf("expected query pool MaxConns=3, got %d", got)
	}
}

func TestNewInfosAdminQuery_ZeroPoolSizeLeavesPgxDefault(t *testing.T) {
	db, err := NewInfosAdminQuery(testDbURI, testDbURI, 0, "")
	if err != nil {
		t.Fatalf("NewInfosAdminQuery: %v", err)
	}
	defer db.Pool.Close()

	if got := db.Pool.Config().MaxConns; got <= 0 {
		t.Errorf("expected pgx's own positive default MaxConns when poolSize=0, got %d", got)
	}
}
