package rpc

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

	testDb, err = pg.NewInfos(uri)
	if err != nil {
		panic(err)
	}

	testCfg = config.Test()
	testCfg.Pg.Query.AnonymousRole = "~anonymous"
	testCfg.Http.Functions.CheckSession = "public.fn_check_session"

	testReg, err = BuildRegistry(testDb, testCfg)
	if err != nil {
		panic(err)
	}
	testHandler = NewHandler(testDb, testCfg, testReg)

	m.Run()
}
