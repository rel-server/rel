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

package pg

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Covers migrations.md ## Reloading step 4 : a fresh *DbInfos reflecting a
// DDL change made between two calls, reusing the exact same pool pointer.
func TestReIntrospect_ReflectsSchemaChangeAndReusesPool(t *testing.T) {
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, testDbURI)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	// A dedicated schema/table, cleaned up afterward, so this test doesn't
	// leave state behind for the rest of the package's shared container.
	if _, err := pool.Exec(ctx, "drop schema if exists reintrospect_test cascade"); err != nil {
		t.Fatalf("drop schema (pre-clean): %v", err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, "drop schema if exists reintrospect_test cascade")
	}()

	if _, err := pool.Exec(ctx, "create schema reintrospect_test"); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	db1, err := ReIntrospect(ctx, testDbURI, pool, "")
	if err != nil {
		t.Fatalf("ReIntrospect (before): %v", err)
	}
	if db1.Pool != pool {
		t.Fatalf("expected ReIntrospect to reuse the exact same pool pointer")
	}
	for _, r := range db1.Relations {
		if r.Identifier.Schema == "reintrospect_test" && r.Identifier.Name == "widgets" {
			t.Fatalf("did not expect reintrospect_test.widgets to exist yet")
		}
	}

	if _, err := pool.Exec(ctx, "create table reintrospect_test.widgets (id int primary key)"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	db2, err := ReIntrospect(ctx, testDbURI, pool, "")
	if err != nil {
		t.Fatalf("ReIntrospect (after): %v", err)
	}
	if db2.Pool != pool {
		t.Fatalf("expected ReIntrospect to reuse the exact same pool pointer")
	}

	var found bool
	for _, r := range db2.Relations {
		if r.Identifier.Schema == "reintrospect_test" && r.Identifier.Name == "widgets" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected the second ReIntrospect call to reflect the newly created reintrospect_test.widgets table")
	}
}

// Covers authentication.md's Anonymous role existence check running again
// on reload, not just at startup.
func TestReIntrospect_AnonymousRoleCheck(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDbURI)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	db, err := ReIntrospect(ctx, testDbURI, pool, "postgres")
	if err != nil {
		t.Fatalf("ReIntrospect: %v", err)
	}
	if !db.AnonymousRoleExists {
		t.Errorf("expected AnonymousRoleExists=true for the (real) postgres role")
	}

	db2, err := ReIntrospect(ctx, testDbURI, pool, "plain_login_role_does_not_exist_xyz")
	if err != nil {
		t.Fatalf("ReIntrospect: %v", err)
	}
	if db2.AnonymousRoleExists {
		t.Errorf("expected AnonymousRoleExists=false for a nonexistent role")
	}
}
