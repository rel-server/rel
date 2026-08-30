package rpc

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

// TestDeploymentShapedRoleSwitch exercises the full /rpc JWT lifecycle —
// login with a real (pgcrypto) credential check, cookie mint, an
// authenticated role-gated call — against a connecting role that is a
// genuine, non-superuser LOGIN role granted membership in every role it
// needs to SET LOCAL ROLE into, matching the deployment prerequisite
// specs/TODO.md now documents. Every other test in this package connects
// as the testcontainers module's default superuser (see schema.sql's own
// note), which can SET ROLE to anything regardless of grants and would
// silently mask a missing grant here — that's the exact class of bug
// (found empirically, "permission denied to set role") this test exists
// to catch on every future change, not just once by hand.
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

	// NewInfosAdminQuery, threading through the real anonymous role name —
	// "anonymous call still works" below needs DbInfos.AnonymousRoleExists
	// true, same reasoning as rpc_test.go's TestMain.
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
		req := httptest.NewRequest(http.MethodPost, "/rpc/public/fn_login_with_credentials", strings.NewReader(`{"username":"alice","password":"wrong"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("anonymous call still works", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/rpc/public/fn_echo0", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("full round trip under the non-superuser connecting role", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/rpc/public/fn_login_with_credentials", strings.NewReader(`{"username":"alice","password":"correct horse battery staple"}`))
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
		req2 := httptest.NewRequest(http.MethodGet, "/rpc/public/fn_secret", nil)
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
