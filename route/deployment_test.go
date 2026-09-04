package route

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/pg"
)

// TestDeploymentShapedRoleSwitch connects as a non-superuser LOGIN role
// with real grants, unlike every other test's superuser (which would mask a missing grant).
func TestDeploymentShapedRoleSwitch(t *testing.T) {
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithOrderedInitScripts("testdata/schema.sql", "testdata/deploy_roles.sql"),
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
	// query_user_deploy, not the module's default superuser — deploy_roles.sql's
	// whole point.
	uri := fmt.Sprintf("postgres://query_user_deploy:test-password@%s:%s/postgres?sslmode=disable", host, port.Port())
	cfg := config.Test()
	cfg.Pg.Query.AnonymousRole = "~anonymous"

	// Threads the real anonymous role name through so AnonymousRoleExists
	// is true below, same as route_test.go's TestMain.
	db, err := pg.NewInfosAdminQuery(uri, uri, 0, cfg.Pg.Query.AnonymousRole)
	if err != nil {
		t.Fatalf("NewInfosAdminQuery: %v", err)
	}

	reg, err := BuildRegistry(db, cfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	handler := NewHandler(db, cfg, reg, nil)

	t.Run("bad credentials rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/route/public/fn_login_with_credentials", strings.NewReader(`{"username":"alice","password":"wrong"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("anonymous call still works", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/route/public/fn_echo0", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("full round trip under the non-superuser connecting role", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/route/public/fn_login_with_credentials", strings.NewReader(`{"username":"alice","password":"correct horse battery staple"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var cookie *http.Cookie
		for _, c := range rec.Result().Cookies() {
			if c.Name == cfg.Jwt.CookieName {
				cookie = c
			}
		}
		if cookie == nil {
			t.Fatalf("expected a jwt cookie to be set, got %v", rec.Result().Cookies())
		}

		// The operation that 500s with "permission denied to set role" if
		// query_user_deploy weren't granted membership in app_user.
		req2 := httptest.NewRequest(http.MethodGet, "/route/public/fn_secret", nil)
		req2.AddCookie(cookie)
		rec2 := httptest.NewRecorder()
		handler.ServeHTTP(rec2, req2)
		if rec2.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
		}
		if rec2.Body.String() != "top secret" {
			t.Errorf("expected \"top secret\", got %q", rec2.Body.String())
		}
	})
}
