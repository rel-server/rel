package sso

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/rel-server/rel/config"
	jwtpkg "github.com/rel-server/rel/jwt"
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

// assertSessionRole fails unless rec set a valid session cookie for role.
func assertSessionRole(t *testing.T, rec *httptest.ResponseRecorder, role string) {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name != testCfg.Jwt.CookieName {
			continue
		}
		claims, err := jwtpkg.Verify(testCfg.Jwt, c.Value)
		if err != nil {
			t.Fatalf("session cookie does not verify: %v", err)
		}
		if got := jwtpkg.Role(claims); got != role {
			t.Fatalf("session role = %q, want %q", got, role)
		}
		return
	}
	t.Fatalf("expected the session cookie to be set")
}
