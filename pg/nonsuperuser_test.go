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

// TestNewInfos_NonSuperuserConnectingRole regression-tests
// FillConstraintInformations' pg_catalog-visibility fix directly, next to
// the code it protects — route/deployment_test.go exercises the same bug but
// only reaches it through three layers (route.NewHandler -> BuildRegistry ->
// NewInfos), so a revert of info_constraint.go's continue-not-return fix
// would surface there as an opaque route failure, not as "constraint
// resolution rejects invisible relations."
//
// INFO_QUERY_CONSTRAINTS reads pg_constraint directly (world-readable,
// unfiltered), but the relation map it resolves against comes from
// information_schema.columns, which DOES filter by the connecting role's
// own privileges. Several pg_catalog system tables have real p/u/f
// constraints in pg_constraint but are invisible via information_schema to
// anything but a superuser — every OTHER test in this package connects as
// the testcontainers module's default superuser, which can see all of
// them, silently masking this until tested under a plain login role.
func TestNewInfos_NonSuperuserConnectingRole(t *testing.T) {
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithOrderedInitScripts("testdata/schema.sql", "testdata/nonsuperuser_role.sql"),
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
	if _, err := NewInfos(uri); err != nil {
		t.Fatalf("NewInfos under a non-superuser connecting role: %v", err)
	}
}
