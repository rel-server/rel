package route

import (
	"context"
	"net/http"
	"testing"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/pg"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

var testDb *pg.DbInfos
var testCfg *config.Config
var testReg *Registry
var testHandler http.Handler
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

	testCfg = config.Test()
	testCfg.Pg.Query.AnonymousRole = "~anonymous"

	// NewInfosAdminQuery (not NewInfos), threading through the real
	// anonymous role name : the fixture's "~anonymous" role must be found
	// (DbInfos.AnonymousRoleExists) for this package's many
	// anonymous-access test scenarios to keep working under the new
	// anonymous-role-existence gate (specs/authentication.md "# Roles
	// ## Anonymous role existence").
	testDb, err = pg.NewInfosAdminQuery(uri, uri, 0, testCfg.Pg.Query.AnonymousRole)
	if err != nil {
		panic(err)
	}

	testCfg.Http.Functions.CheckSession = "public.fn_check_session"

	testReg, err = BuildRegistry(testDb, testCfg)
	if err != nil {
		panic(err)
	}
	testHandler = NewHandler(testDb, testCfg, testReg, nil)

	m.Run()
}
