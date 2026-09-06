package sso

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	jwtlib "github.com/golang-jwt/jwt/v5"

	"github.com/ceymard/rel/config"
)

// fakeOidcIssuer is a minimal, in-process OIDC issuer : discovery, JWKS,
// and a token endpoint that always mints a fresh ID token for whatever
// nonce the test asks for (tokenIDTokenNonce/tokenIDTokenExtraClaims) —
// enough to drive oidcCallbackHandler's real exchange/verify/claims path
// without needing a real browser round trip through /authorize.
type fakeOidcIssuer struct {
	server              *httptest.Server
	key                 *rsa.PrivateKey
	kid                 string
	tokenIDTokenNonce   string
	tokenIDTokenExtra   map[string]any
	tokenIDTokenSubject string
	userinfoExtra       map[string]any
}

func newFakeOidcIssuer(t *testing.T) *fakeOidcIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating issuer key: %v", err)
	}
	f := &fakeOidcIssuer{key: key, kid: "test-key", tokenIDTokenSubject: "user-123"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                f.server.URL,
			"authorization_endpoint":                f.server.URL + "/authorize",
			"token_endpoint":                        f.server.URL + "/token",
			"userinfo_endpoint":                     f.server.URL + "/userinfo",
			"jwks_uri":                              f.server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		jwk := jose.JSONWebKey{Key: &f.key.PublicKey, KeyID: f.kid, Algorithm: "RS256", Use: "sig"}
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		clientID := r.FormValue("client_id")
		if clientID == "" {
			// oauth2's default AuthStyle sends client credentials via HTTP
			// Basic auth rather than the POST body.
			clientID, _, _ = r.BasicAuth()
		}
		idToken := f.mustSignIDToken(t, clientID)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "test-access-token",
			"refresh_token": "test-refresh-token",
			"token_type":    "Bearer",
			"id_token":      idToken,
		})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		claims := map[string]any{"sub": f.tokenIDTokenSubject}
		for k, v := range f.userinfoExtra {
			claims[k] = v
		}
		_ = json.NewEncoder(w).Encode(claims)
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeOidcIssuer) mustSignIDToken(t *testing.T, clientID string) string {
	t.Helper()
	now := time.Now()
	claims := jwtlib.MapClaims{
		"iss":   f.server.URL,
		"sub":   f.tokenIDTokenSubject,
		"aud":   clientID,
		"exp":   now.Add(5 * time.Minute).Unix(),
		"iat":   now.Unix(),
		"nonce": f.tokenIDTokenNonce,
	}
	for k, v := range f.tokenIDTokenExtra {
		claims[k] = v
	}
	token := jwtlib.NewWithClaims(jwtlib.SigningMethodRS256, claims)
	token.Header["kid"] = f.kid
	signed, err := token.SignedString(f.key)
	if err != nil {
		t.Fatalf("signing id_token: %v", err)
	}
	return signed
}

func newTestOidcEndpoint(t *testing.T, issuer *fakeOidcIssuer, redirectURL string) *oidcEndpoint {
	t.Helper()
	cfg := config.OpenidProvider{
		Issuer:       issuer.server.URL,
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		Scopes:       []string{"openid", "email"},
	}
	e := newOidcEndpoint("test", cfg, redirectURL)
	if !e.ensureReady(t.Context(), redirectURL) {
		t.Fatalf("expected discovery against the fake issuer to succeed")
	}
	return e
}

// TestOidc_LoginRedirectsAndSetsStateCookie covers /login's own contract :
// a redirect to the issuer's authorization endpoint, carrying client_id
// and a fresh state+nonce also stashed in a short-lived cookie.
func TestOidc_LoginRedirectsAndSetsStateCookie(t *testing.T) {
	issuer := newFakeOidcIssuer(t)
	e := newTestOidcEndpoint(t, issuer, "https://app.example.com/auth/oidc/test/callback")

	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/test/login", nil)
	rec := httptest.NewRecorder()
	oidcLoginHandler(e, "https://app.example.com/auth/oidc/test/callback")(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, issuer.server.URL+"/authorize") {
		t.Errorf("expected redirect to the issuer's authorize endpoint, got %q", loc)
	}
	if !strings.Contains(loc, "client_id=test-client") {
		t.Errorf("expected client_id in the redirect URL, got %q", loc)
	}
	var stateCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == oidcStateCookiePrefix+"test" {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatalf("expected a state cookie to be set")
	}
	if !strings.Contains(stateCookie.Value, ".") {
		t.Errorf("expected cookie value shaped as state.nonce, got %q", stateCookie.Value)
	}
}

// TestOidc_CallbackHappyPath drives the full exchange/verify/claims path
// against the fake issuer, and asserts the configured callback function
// (auth.sso_callback, sso_test.go's TestMain) actually mints a session for
// the matching user row (testdata/schema.sql : alice@example.com -> app_user).
func TestOidc_CallbackHappyPath(t *testing.T) {
	issuer := newFakeOidcIssuer(t)
	issuer.tokenIDTokenExtra = map[string]any{"email": "alice@example.com"}
	redirectURL := "https://app.example.com/auth/oidc/test/callback"
	e := newTestOidcEndpoint(t, issuer, redirectURL)

	issuer.tokenIDTokenNonce = "the-nonce"
	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/test/callback?state=the-state&code=the-code", nil)
	req.AddCookie(&http.Cookie{Name: oidcStateCookiePrefix + "test", Value: "the-state.the-nonce"})
	rec := httptest.NewRecorder()

	oidcCallbackHandler(e, testDb, testCfg, nil)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == testCfg.Jwt.CookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatalf("expected the JWT cookie to be set on a successful login")
	}
}

// TestOidc_CallbackRejectsNonceMismatch proves the nonce (not just state)
// is actually checked — a callback with a valid state but a token minted
// against a DIFFERENT nonce must be rejected, not silently accepted.
func TestOidc_CallbackRejectsNonceMismatch(t *testing.T) {
	issuer := newFakeOidcIssuer(t)
	issuer.tokenIDTokenExtra = map[string]any{"email": "alice@example.com"}
	redirectURL := "https://app.example.com/auth/oidc/test/callback"
	e := newTestOidcEndpoint(t, issuer, redirectURL)

	issuer.tokenIDTokenNonce = "wrong-nonce"
	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/test/callback?state=the-state&code=the-code", nil)
	req.AddCookie(&http.Cookie{Name: oidcStateCookiePrefix + "test", Value: "the-state.the-nonce"})
	rec := httptest.NewRecorder()

	oidcCallbackHandler(e, testDb, testCfg, nil)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on nonce mismatch, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestOidc_CallbackRejectsStateMismatch is TestOidc_CallbackRejectsNonceMismatch's
// counterpart for the outer CSRF-protection value.
func TestOidc_CallbackRejectsStateMismatch(t *testing.T) {
	issuer := newFakeOidcIssuer(t)
	redirectURL := "https://app.example.com/auth/oidc/test/callback"
	e := newTestOidcEndpoint(t, issuer, redirectURL)

	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/test/callback?state=wrong-state&code=the-code", nil)
	req.AddCookie(&http.Cookie{Name: oidcStateCookiePrefix + "test", Value: "the-state.the-nonce"})
	rec := httptest.NewRecorder()

	oidcCallbackHandler(e, testDb, testCfg, nil)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on state mismatch, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestOidc_CallbackRejectsUnknownIdentity covers the RSxxx rejection path
// : a valid, verified login for an identity the callback function's own
// users table has no row for must fail with the raised status and set no
// cookie — mirrors docs/content/http/index.md's Postgres-exception convention.
func TestOidc_CallbackRejectsUnknownIdentity(t *testing.T) {
	issuer := newFakeOidcIssuer(t)
	issuer.tokenIDTokenExtra = map[string]any{"email": "nobody@example.com"}
	redirectURL := "https://app.example.com/auth/oidc/test/callback"
	e := newTestOidcEndpoint(t, issuer, redirectURL)

	issuer.tokenIDTokenNonce = "the-nonce"
	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/test/callback?state=the-state&code=the-code", nil)
	req.AddCookie(&http.Cookie{Name: oidcStateCookiePrefix + "test", Value: "the-state.the-nonce"})
	rec := httptest.NewRecorder()

	oidcCallbackHandler(e, testDb, testCfg, nil)(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 (RS401), got %d: %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == testCfg.Jwt.CookieName && c.Value != "" {
			t.Errorf("expected no JWT cookie set on rejection, got %q", c.Value)
		}
	}
}

// TestOidc_FetchUserinfoMergesOverIdToken proves the fetch_userinfo path
// actually calls the userinfo endpoint and that its claims win over the ID
// token's own on collision, per ## Claims shape.
func TestOidc_FetchUserinfoMergesOverIdToken(t *testing.T) {
	issuer := newFakeOidcIssuer(t)
	issuer.tokenIDTokenExtra = map[string]any{"email": "from-id-token@example.com"}
	issuer.userinfoExtra = map[string]any{"email": "alice@example.com"}
	redirectURL := "https://app.example.com/auth/oidc/test/callback"

	cfg := config.OpenidProvider{
		Issuer:        issuer.server.URL,
		ClientID:      "test-client",
		ClientSecret:  "test-secret",
		Scopes:        []string{"openid", "email"},
		FetchUserinfo: true,
	}
	e := newOidcEndpoint("test", cfg, redirectURL)
	if !e.ensureReady(t.Context(), redirectURL) {
		t.Fatalf("expected discovery to succeed")
	}

	issuer.tokenIDTokenNonce = "the-nonce"
	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/test/callback?state=the-state&code=the-code", nil)
	req.AddCookie(&http.Cookie{Name: oidcStateCookiePrefix + "test", Value: "the-state.the-nonce"})
	rec := httptest.NewRecorder()

	oidcCallbackHandler(e, testDb, testCfg, nil)(rec, req)

	// alice@example.com (userinfo's value) has a users row ; the ID
	// token's own from-id-token@example.com does not — a 200 here proves
	// userinfo's claim value is the one that actually reached the
	// callback function.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (userinfo's email should have won), got %d: %s", rec.Code, rec.Body.String())
	}
}
