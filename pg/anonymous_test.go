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

	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Exercises the anonymous role existence check : pg_roles
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

// The pg_roles existence check, introspecting as a non-superuser login.
func TestNewInfosAdminQuery_AnonymousRoleExistence_NonSuperuser(t *testing.T) {
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
}
