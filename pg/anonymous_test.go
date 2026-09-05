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

// Exercises authentication.md's Anonymous role existence check : pg_roles
// existence sets AnonymousRoleExists, non-fatal either way.
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

// Exercises route.BuildRegistry's has_schema_privilege/has_function_privilege
// query (route.md Anonymous route authorization) under a non-superuser login.
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
		// Same query as the superuser test above, but connecting AS
		// plain_login_role — proves the correct answer, not just "no error."
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

	// Mirrors route.applyAnonymousAuthorization's actual query : anonOK
	// requires an EXPLICIT EXECUTE grant (never one credited only via
	// PUBLIC), while publicOK keeps using has_function_privilege, which
	// does credit PUBLIC — see specs/route.md ## Anonymous route
	// authorization.
	check := func(t *testing.T, role string, oid int) (anonOK, publicOK bool) {
		t.Helper()
		err := pool.QueryRow(ctx, `
			select has_schema_privilege($1, f.pronamespace, 'USAGE')
			         and exists (
			           select 1
			           from pg_roles r,
			                aclexplode(coalesce(f.proacl, acldefault('f', f.proowner))) as a(grantor, grantee, privilege_type, is_grantable)
			           where r.rolname = $1
			             and a.privilege_type = 'EXECUTE'
			             and a.grantee <> 0
			             and pg_has_role(r.oid, a.grantee, 'USAGE')
			         ),
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

	t.Run("genuinely PUBLIC-reachable via defaults : probe_role can really call it, but anon check must still reject it", func(t *testing.T) {
		oid := oidOf(t, "default_only_schema", "fn_default_only")
		anonOK, publicOK := check(t, "probe_role", oid)
		if anonOK {
			t.Errorf("expected probe_role NOT reachable (EXECUTE never explicitly granted to probe_role, only inherited via PUBLIC), got reachable")
		}
		if !publicOK {
			t.Errorf("expected PUBLIC reachable (schema USAGE granted to PUBLIC, EXECUTE left at CREATE FUNCTION's own default), got unreachable")
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
