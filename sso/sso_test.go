package sso

import (
	"context"
	"testing"

	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
)

var testDb *pg.DbInfos
var testCfg *config.Config

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithInitScripts("testdata/schema.sql"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		panic(err)
	}
	defer func() { _ = container.Terminate(ctx) }()

	uri, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic(err)
	}

	testCfg = config.Test()
	testCfg.Pg.Query.AnonymousRole = "~anonymous"
	testCfg.Http.Functions.SsoCallback = "auth.sso_callback"

	testDb, err = pg.NewInfosAdminQuery(uri, uri, 0, testCfg.Pg.Query.AnonymousRole)
	if err != nil {
		panic(err)
	}

	m.Run()
}
