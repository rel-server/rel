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
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestNewInfosAdminQuery_AnonymousRoleExistence exercises
// specs/jwt-roles-and-http.md "# Roles ## Anonymous role existence"
// directly against NewInfosAdminQuery's own new anonymousRole parameter :
// a role that exists in pg_roles sets AnonymousRoleExists true, a name
// that doesn't (a typo, or simply never created) sets it false — non-
// fatal either way, never an error return.
func TestNewInfosAdminQuery_AnonymousRoleExistence(t *testing.T) {
	db, err := NewInfosAdminQuery(testDbURI, testDbURI, 0, "plain_login_role_does_not_exist_xyz")
	if err != nil {
		t.Fatalf("NewInfosAdminQuery: %v", err)
	}
	if db.AnonymousRoleExists {
		t.Errorf("expected AnonymousRoleExists=false for a role name nothing created")
	}

	db2, err := NewInfosAdminQuery(testDbURI, testDbURI, 0, "postgres")
	if err != nil {
		t.Fatalf("NewInfosAdminQuery: %v", err)
	}
	if !db2.AnonymousRoleExists {
		t.Errorf("expected AnonymousRoleExists=true for the well-known postgres role")
	}

	db3, err := NewInfosAdminQuery(testDbURI, testDbURI, 0, "")
	if err != nil {
		t.Fatalf("NewInfosAdminQuery: %v", err)
	}
	if db3.AnonymousRoleExists {
		t.Errorf("expected AnonymousRoleExists=false for an empty (unconfigured) anonymous role")
	}
}

// TestAnonymousAndPublicPrivilegeQuery_NonSuperuser runs the actual
// has_schema_privilege/has_function_privilege two-conjunct query
// rpc.BuildRegistry uses (specs/jwt-roles-and-http.md "# HTTP ## Anonymous
// route authorization") against testdata/anon_priv.sql's fixture, under a
// non-superuser connecting role (plain_login_role) — both privilege
// functions ask about ANOTHER role (probe_role, PUBLIC), never the
// connecting role's own, so this must keep working the same way
// nonsuperuser_test.go already proves for the rest of introspection.
func TestAnonymousAndPublicPrivilegeQuery_NonSuperuser(t *testing.T) {
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithOrderedInitScripts("testdata/schema.sql", "testdata/anon_priv.sql"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("mapped port: %v", err)
	}
	uri := fmt.Sprintf("postgres://plain_login_role:test-password@%s:%s/postgres?sslmode=disable", host, port.Port())

	t.Run("pg_roles existence check under a non-superuser introspection login", func(t *testing.T) {
		// NewInfosAdminQuery itself, connecting AS plain_login_role (not the
		// container's default superuser) — the actual code path
		// TestNewInfosAdminQuery_AnonymousRoleExistence above exercises only
		// under a superuser connection ; this proves the same query keeps
		// working (and returns the CORRECT answer, not just "doesn't error")
		// under the non-superuser constraint this file is about.
		dbFound, err := NewInfosAdminQuery(uri, uri, 0, "probe_role")
		if err != nil {
			t.Fatalf("NewInfosAdminQuery as plain_login_role: %v", err)
		}
		defer dbFound.Pool.Close()
		if !dbFound.AnonymousRoleExists {
			t.Errorf("expected AnonymousRoleExists=true for probe_role (exists), got false")
		}

		dbMissing, err := NewInfosAdminQuery(uri, uri, 0, "role_nobody_ever_created_xyz")
		if err != nil {
			t.Fatalf("NewInfosAdminQuery as plain_login_role: %v", err)
		}
		defer dbMissing.Pool.Close()
		if dbMissing.AnonymousRoleExists {
			t.Errorf("expected AnonymousRoleExists=false for a role nothing created, got true")
		}
	})

	pool, err := pgxpool.New(ctx, uri)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	oidOf := func(t *testing.T, schema, name string) int {
		t.Helper()
		var oid int
		if err := pool.QueryRow(ctx, "select oid::integer from pg_proc where pronamespace = $1::regnamespace and proname = $2", schema, name).Scan(&oid); err != nil {
			t.Fatalf("resolving oid for %s.%s: %v", schema, name, err)
		}
		return oid
	}

	check := func(t *testing.T, role string, oid int) (anonOK, publicOK bool) {
		t.Helper()
		err := pool.QueryRow(ctx, `
			select has_schema_privilege($1, f.pronamespace, 'USAGE') and has_function_privilege($1, f.oid, 'EXECUTE'),
			       has_schema_privilege('public', f.pronamespace, 'USAGE') and has_function_privilege('public', f.oid, 'EXECUTE')
			from pg_proc f where f.oid = $2
		`, role, oid).Scan(&anonOK, &publicOK)
		if err != nil {
			t.Fatalf("privilege query: %v", err)
		}
		return
	}

	t.Run("locked schema : EXECUTE granted via PUBLIC default but USAGE withheld — must be unreachable", func(t *testing.T) {
		oid := oidOf(t, "locked_schema", "fn_locked")
		anonOK, publicOK := check(t, "probe_role", oid)
		if anonOK {
			t.Errorf("expected probe_role NOT reachable (no schema USAGE grant), got reachable")
		}
		if publicOK {
			t.Errorf("expected PUBLIC NOT reachable (no schema USAGE grant), got reachable")
		}
	})

	t.Run("open schema : both USAGE and EXECUTE explicitly granted — reachable", func(t *testing.T) {
		oid := oidOf(t, "open_schema", "fn_open")
		anonOK, _ := check(t, "probe_role", oid)
		if !anonOK {
			t.Errorf("expected probe_role reachable (explicit USAGE+EXECUTE grants), got unreachable")
		}
	})

	t.Run("open schema : a role with no grants at all is not reachable", func(t *testing.T) {
		oid := oidOf(t, "open_schema", "fn_open")
		anonOK, publicOK := check(t, "plain_login_role", oid)
		if anonOK {
			t.Errorf("expected plain_login_role NOT reachable (no grants on open_schema), got reachable")
		}
		if publicOK {
			t.Errorf("expected PUBLIC NOT reachable on open_schema (only probe_role was granted), got reachable")
		}
	})
}
