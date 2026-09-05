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

package query

import (
	"context"
	"fmt"
	"testing"
)

// A computed field is usable by bare name — no "call", no declared alias
// needed — exactly like a real column.
func TestCompileSelect_ComputedFieldBareIdentifier(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Bare Name Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"name": "name", "display": "director_display_name"},
		"where": ["=", "name", ["Bare Name Director"]]
	}`)
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	if rows[0]["display"] != "Bare Name Director (director)" {
		t.Errorf("expected display=%q, got %#v", "Bare Name Director (director)", rows[0])
	}
}

// A computed field is usable in `where`, same as a real column.
func TestCompileSelect_ComputedFieldInWhere(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Where Clause Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"name": "name"},
		"where": ["=", "director_display_name", ["Where Clause Director (director)"]]
	}`)
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	if rows[0]["name"] != "Where Clause Director" {
		t.Errorf("expected name=%q, got %#v", "Where Clause Director", rows[0])
	}
}

// A "." hop into a to-one child's own computed field compiles as a
// correlated scalar subquery, same shape a plain column hop gets.
func TestCompileSelect_DotChainIntoChildComputedField(t *testing.T) {
	ctx := context.Background()
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ('Hop Target Director') returning id`).Scan(&directorID); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	var movieID int
	if err := testDb.Pool.QueryRow(ctx, `insert into movie (director_id, title) values ($1, 'Hop Target Movie') returning id`, directorID).Scan(&movieID); err != nil {
		t.Fatalf("insert movie: %v", err)
	}

	node := mustResolveQuery(t, fmt.Sprintf(`{
		"relation": "movie", "schema": "public",
		"select": {"title": "title", "director_display": [".", "director", "director_display_name"]},
		"where": ["=", "id", %d],
		"join": {"director": {"relation": "director", "schema": "public", "on": {"id": "director_id"}}}
	}`, movieID))
	sql, args := mustCompileSelect(t, node)
	rows := runSelect(t, sql, args)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d : %s", len(rows), sql)
	}
	if rows[0]["director_display"] != "Hop Target Director (director)" {
		t.Errorf("expected director_display=%q, got %#v", "Hop Target Director (director)", rows[0])
	}
}

// own/full must never auto-include a computed field.
func TestResolveQuery_FullNeverIncludesComputedField(t *testing.T) {
	node := mustResolveQuery(t, `{"relation": "director", "schema": "public", "select": ["full"]}`)
	if _, ok := node.Shape.Fields["director_display_name"]; ok {
		t.Errorf("expected \"full\" to never auto-include the director_display_name computed field, but it did")
	}
}

// Excepting a computed field's name from own/full is an error — it was
// never included in the first place, so there's nothing to except.
func TestResolveQuery_OwnExceptComputedFieldIsError(t *testing.T) {
	err := resolveQueryExpectError(t, `{"relation": "director", "schema": "public", "select": ["own_except", ["director_display_name"]]}`)
	if err == nil {
		t.Fatalf("expected an error excepting a computed field's name from own_except, got none")
	}
}

// A computed field appearing in a write's select is simply never a write
// target — not an error, just excluded from writability accounting.
func TestResolveQuery_ComputedFieldNeverBreaksWritability(t *testing.T) {
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"id": "id", "name": "name", "display": "director_display_name"}
	}`)
	if node.Shape == nil || !node.Shape.Writable {
		t.Fatalf("expected the node to remain writable (id present and clean) despite the computed field, got %+v", node.Shape)
	}
}

// A join alias colliding with an eligible computed field's name is a
// genuine per-query ambiguity — LookupInScope must reject it, unlike a
// same-named real column (excluded at introspection instead).
func TestResolveQuery_ComputedFieldVsJoinAliasIsAmbiguous(t *testing.T) {
	// director_display_name is eligible on "director" itself (its argument
	// type) — join alias must collide on the SAME node that owns the
	// computed field to trigger the ambiguity.
	err := resolveQueryExpectError(t, `{
		"relation": "director", "schema": "public",
		"select": {"x": "director_display_name"},
		"join": {"director_display_name": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}}}
	}`)
	if err == nil {
		t.Fatalf("expected an ambiguous-identifier error when a join alias collides with a computed field name, got none")
	}
}
