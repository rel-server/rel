package sso

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rel-server/rel/config"
)

// TestMount_NoopWhenUnconfigured proves a deployment using neither OIDC
// nor SAML pays no cost — no cert generated, no routes registered.
func TestMount_NoopWhenUnconfigured(t *testing.T) {
	router := chi.NewRouter()
	cfg := config.Test()
	cfg.Http.PublicHost = "app.example.com"
	Mount(router, testDb, cfg, nil)

	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/anything/login", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 (nothing mounted), got %d", rec.Code)
	}
}

// TestMount_OidcRoutesRegistered proves Mount actually wires
// /auth/oidc/{name}/login onto the router when configured.
func TestMount_OidcRoutesRegistered(t *testing.T) {
	issuer := newFakeOidcIssuer(t)
	router := chi.NewRouter()
	cfg := config.Test()
	cfg.Http.PublicHost = "app.example.com"
	cfg.Http.Functions.SsoCallback = "auth.sso_callback"
	cfg.Openid = map[string]config.OpenidProvider{
		"test": {Issuer: issuer.server.URL, ClientID: "c", ClientSecret: "s"},
	}
	Mount(router, testDb, cfg, nil)

	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/test/login", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Errorf("expected 302, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestMount_EntryWithNoEffectiveHostIsSkipped covers specs/oauth-saml.md
// ## Configuration — HTTP : an entry is skipped (not a fatal error for the
// others) when neither its own public_host nor http.public_host resolves
// to anything — here http.public_host is deliberately left empty.
func TestMount_EntryWithNoEffectiveHostIsSkipped(t *testing.T) {
	issuer := newFakeOidcIssuer(t)
	router := chi.NewRouter()
	cfg := config.Test()
	cfg.Http.Functions.SsoCallback = "auth.sso_callback"
	cfg.Openid = map[string]config.OpenidProvider{
		"test": {Issuer: issuer.server.URL, ClientID: "c", ClientSecret: "s"},
	}
	Mount(router, testDb, cfg, nil)

	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/test/login", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 (entry skipped, no route registered), got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestMount_PerEntryHostOverride proves an entry's own public_host is used
// to build its redirect_uri instead of http.public_host.
func TestMount_PerEntryHostOverride(t *testing.T) {
	issuer := newFakeOidcIssuer(t)
	router := chi.NewRouter()
	cfg := config.Test()
	cfg.Http.PublicHost = "global.example.com"
	cfg.Http.Functions.SsoCallback = "auth.sso_callback"
	cfg.Openid = map[string]config.OpenidProvider{
		"test": {Issuer: issuer.server.URL, ClientID: "c", ClientSecret: "s", PublicHost: "override.example.com"},
	}
	Mount(router, testDb, cfg, nil)

	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/test/login", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parsing Location: %v", err)
	}
	redirectURI := loc.Query().Get("redirect_uri")
	if !strings.Contains(redirectURI, "override.example.com") {
		t.Errorf("expected redirect_uri to use the per-entry override host, got %q", redirectURI)
	}
}

func TestResolveHost(t *testing.T) {
	if got := resolveHost("", "global.example.com"); got != "global.example.com" {
		t.Errorf("empty per-entry should fall back to global, got %q", got)
	}
	if got := resolveHost("override.example.com", "global.example.com"); got != "override.example.com" {
		t.Errorf("non-empty per-entry should win, got %q", got)
	}
	if got := resolveHost("", ""); got != "" {
		t.Errorf("both empty should stay empty, got %q", got)
	}
}

func TestRootURLFor(t *testing.T) {
	if got := rootURLFor("app.example.com"); got != "https://app.example.com" {
		t.Errorf("rootURLFor should always build https, got %q", got)
	}
}
