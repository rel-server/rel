package sso

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/crewjam/saml"

	"github.com/rel-server/rel/config"
	jwtpkg "github.com/rel-server/rel/jwt"
)

func TestMergeUserinfoClaims_UserinfoWinsOnCollision(t *testing.T) {
	idToken := map[string]any{"email": "idtoken@example.com", "sub": "123"}
	userinfo := map[string]any{"email": "userinfo@example.com", "name": "Alice"}

	got := mergeUserinfoClaims(idToken, userinfo)

	want := map[string]any{"email": "userinfo@example.com", "sub": "123", "name": "Alice"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mergeUserinfoClaims = %v, want %v", got, want)
	}
	// idToken itself must be untouched.
	if idToken["email"] != "idtoken@example.com" {
		t.Errorf("mergeUserinfoClaims mutated its idTokenClaims argument in place")
	}
}

func TestExtractSamlClaims_AttributesAreAlwaysStringArrays(t *testing.T) {
	assertion := &saml.Assertion{
		Subject: &saml.Subject{NameID: &saml.NameID{Value: "alice@example.com"}},
		AttributeStatements: []saml.AttributeStatement{
			{Attributes: []saml.Attribute{
				{Name: "email", Values: []saml.AttributeValue{{Value: "alice@example.com"}}},
				{Name: "groups", Values: []saml.AttributeValue{{Value: "admins"}, {Value: "users"}}},
			}},
		},
	}

	claims := extractSamlClaims(assertion)

	email, ok := claims["email"].([]string)
	if !ok || len(email) != 1 || email[0] != "alice@example.com" {
		t.Errorf("email = %#v, want single-element string[]", claims["email"])
	}
	groups, ok := claims["groups"].([]string)
	if !ok || len(groups) != 2 || groups[0] != "admins" || groups[1] != "users" {
		t.Errorf("groups = %#v, want [admins users]", claims["groups"])
	}
	nameID, ok := claims["NameID"].([]string)
	if !ok || len(nameID) != 1 || nameID[0] != "alice@example.com" {
		t.Errorf("NameID = %#v, want [alice@example.com]", claims["NameID"])
	}
}

func TestExtractSamlClaims_NoSubjectNoNameID(t *testing.T) {
	assertion := &saml.Assertion{}
	claims := extractSamlClaims(assertion)
	if _, ok := claims["NameID"]; ok {
		t.Errorf("expected no NameID key when Subject is nil, got %v", claims)
	}
}

func TestResolveCallbackFunction_PerEntryWinsOverFallback(t *testing.T) {
	cfg := &config.Config{Http: config.Http{Functions: config.HttpFunctions{SsoCallback: "auth.fallback"}}}
	if got := resolveCallbackFunction("auth.specific", cfg); got != "auth.specific" {
		t.Errorf("got %q, want auth.specific", got)
	}
	if got := resolveCallbackFunction("", cfg); got != "auth.fallback" {
		t.Errorf("got %q, want auth.fallback (the fallback)", got)
	}
}

// The callback runs as the connection's own role, with no claims setting, even when the request
// carries a session: the user is not signed in yet (invokeCallback's comment). This pins it so
// nobody "fixes" it into a role switch, which would break the "security definer" callbacks the
// docs show.
func TestInvokeCallback_RunsAsTheConnectionRoleWithNoClaimsSetting(t *testing.T) {
	token, err := jwtpkg.Sign(testCfg.Jwt, jwtpkg.Mint(testCfg.Jwt, "app_user", time.Now(), 3600, nil))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/test/callback", nil)
	req.AddCookie(&http.Cookie{Name: testCfg.Jwt.CookieName, Value: token})
	rec := httptest.NewRecorder()

	invokeCallback(rec, req, testCfg, testDb, nil, "auth.echo_session", ssoIdentity{Protocol: "oidc", Name: "test"}, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		CurrentUser   string `json:"current_user"`
		ClaimsSetting string `json:"claims_setting"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	if got.CurrentUser != "postgres" || got.ClaimsSetting != "" {
		t.Errorf("callback ran as %q with claims setting %q, want the connection role and none", got.CurrentUser, got.ClaimsSetting)
	}
}

// A callback function that fails inside Postgres is a server fault : it is logged in full, like
// every other 5xx, while the client only gets the redacted text.
func TestInvokeCallback_PostgresServerFaultIsLogged(t *testing.T) {
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/test/callback", nil)
	rec := httptest.NewRecorder()
	invokeCallback(rec, req, testCfg, testDb, nil, "auth.fails", ssoIdentity{Protocol: "oidc", Name: "test"}, nil)

	if rec.Code != http.StatusInternalServerError || rec.Body.String() != "internal error" {
		t.Fatalf("expected a redacted 500, got %d %q", rec.Code, rec.Body.String())
	}
	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(bytes.SplitN(buf.Bytes(), []byte("\n"), 2)[0]), &line); err != nil {
		t.Fatalf("expected a log line, got %q: %v", buf.String(), err)
	}
	if line["msg"] != "request failed" || line["pg_error"] == nil {
		t.Errorf("expected a \"request failed\" line carrying pg_error, got %#v", line)
	}
}
