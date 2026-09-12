package route

import (
	"context"
	"net/http"
	"testing"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
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

	// NewInfosAdminQuery (not NewInfos) so AnonymousRoleExists is true for
	// this package's anonymous-access test scenarios.
	testDb, err = pg.NewInfosAdminQuery(uri, uri, 0, testCfg.Pg.Query.AnonymousRole)
	if err != nil {
		panic(err)
	}

	// specs/new-routes.md fixtures : config-declared routes, exercised by
	// routeset_test.go's BuildRouteSet tests.
	testCfg.Route = map[string]map[string]config.RouteDecl{
		"public": {
			"fn_new_configonly": {Path: "/new/configonly"},
			"fn_new_override":   {Path: "/new/from-config"},
		},
	}

	testReg, err = BuildRegistry(testDb, testCfg)
	if err != nil {
		panic(err)
	}
	testHandler = NewHandler(testDb, testCfg, testReg, nil, nil)

	m.Run()
}
