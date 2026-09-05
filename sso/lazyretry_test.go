package sso

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ceymard/rel/config"
)

// TestOidc_LazyRetry_DiscoveryFailsThenSucceeds covers specs/oauth-saml.md
// ## Metadata fetch is lazy for OIDC : discovery fails at construction
// time (the issuer 404s, as if the IdP side isn't wired up yet), the
// first /login attempt fails 503 and retries inline, and — once the
// issuer starts responding, with no process restart involved — the next
// /login attempt succeeds.
func TestOidc_LazyRetry_DiscoveryFailsThenSucceeds(t *testing.T) {
	var up atomic.Bool
	issuer := newFakeOidcIssuer(t)
	realHandler := issuer.server.Config.Handler
	failingIssuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			http.NotFound(w, r)
			return
		}
		realHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(failingIssuer.Close)
	// The fake issuer's own discovery document advertises URLs rooted at
	// its ORIGINAL server, not failingIssuer's — good enough here, since
	// this test only exercises whether discovery itself (the .well-known
	// fetch) succeeds, never a full login round trip through failingIssuer.
	issuer.server.Close()
	issuer.server = failingIssuer

	redirectURL := "https://app.example.com/auth/oidc/test/callback"
	cfg := config.OpenidProvider{Issuer: failingIssuer.URL, ClientID: "c", ClientSecret: "s"}
	e := newOidcEndpoint("test", cfg, redirectURL)

	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/test/login", nil)
	rec := httptest.NewRecorder()
	oidcLoginHandler(e, redirectURL)(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 while discovery is down, got %d: %s", rec.Code, rec.Body.String())
	}

	up.Store(true)

	rec2 := httptest.NewRecorder()
	oidcLoginHandler(e, redirectURL)(rec2, req)
	if rec2.Code != http.StatusFound {
		t.Fatalf("expected the retried discovery to succeed and redirect (302), got %d: %s", rec2.Code, rec2.Body.String())
	}
}

// TestOidc_LazyRetry_NeverRetriesOnceReady proves a successful discovery
// is permanent — a later outage at the issuer's .well-known endpoint must
// not un-ready an already-ready endpoint.
func TestOidc_LazyRetry_NeverRetriesOnceReady(t *testing.T) {
	issuer := newFakeOidcIssuer(t)
	redirectURL := "https://app.example.com/auth/oidc/test/callback"
	e := newTestOidcEndpoint(t, issuer, redirectURL)

	issuer.server.Close() // discovery would now fail if attempted again

	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/test/login", nil)
	rec := httptest.NewRecorder()
	oidcLoginHandler(e, redirectURL)(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("expected an already-ready endpoint to keep working, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestSaml_LazyRetry_MetadataFetchFailsThenSucceeds mirrors the OIDC case
// for SAML's idp_metadata_url fetch.
func TestSaml_LazyRetry_MetadataFetchFailsThenSucceeds(t *testing.T) {
	f := newSamlTestFixture(t, "https://app.example.com", true)
	f.endpoint.ready = false // fetchMetadata hasn't been attempted at all yet

	var up atomic.Bool
	metadataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			http.NotFound(w, r)
			return
		}
		buf, err := xml.MarshalIndent(f.idp.Metadata(), "", "  ")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/samlmetadata+xml")
		_, _ = w.Write(buf)
	}))
	t.Cleanup(metadataServer.Close)
	f.endpoint.cfg.IdpMetadataUrl = metadataServer.URL

	req := httptest.NewRequest(http.MethodGet, "/auth/saml/test/login", nil)
	rec := httptest.NewRecorder()
	samlLoginHandler(f.endpoint)(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 while the IdP metadata endpoint is down, got %d: %s", rec.Code, rec.Body.String())
	}

	up.Store(true)

	rec2 := httptest.NewRecorder()
	samlLoginHandler(f.endpoint)(rec2, req)
	if rec2.Code != http.StatusFound {
		t.Fatalf("expected the retried metadata fetch to succeed and redirect (302), got %d: %s", rec2.Code, rec2.Body.String())
	}
}
