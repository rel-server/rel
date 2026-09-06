package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rel-server/rel/config"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/pg"
)

// mintCookie signs role/claims under cfg.Jwt ; /rel never mints its own
// tokens (a route function's job), so tests stand in for that step.
func mintCookie(t *testing.T, cfg *config.Config, role string) *http.Cookie {
	t.Helper()
	claims := jwtpkg.Mint(cfg.Jwt, role, time.Now(), cfg.Jwt.MaxAge, nil)
	token, err := jwtpkg.Sign(cfg.Jwt, claims)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return jwtpkg.CookieValue(cfg.Jwt, token, claims, "")
}

func TestRelHandler_AnonymousWriteDeniedOnRoleGatedTable(t *testing.T) {
	rec := postRel(t, `{
		"query": {"relation": "secret_notes", "schema": "public", "select": ["own"], "write_mode": "insert"},
		"data": [{"note": "should not be allowed"}]
	}`)
	// "~anonymous" has no privileges on secret_notes — a genuine Postgres
	// permission-denied error, proving the switched role actually applied.
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (permission denied), got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRelHandler_AuthenticatedCookieAppliesRole(t *testing.T) {
	cookie := mintCookie(t, testCfg, "authenticated_user")
	rec := postRelWithCookie(t, testHandler, `{
		"query": {"relation": "secret_notes", "schema": "public", "select": ["own"], "write_mode": "insert"},
		"data": [{"note": "authenticated write"}]
	}`, cookie)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestRelHandler_ClaimsSetting_AnonymousIsJSONNull proves rel.jwt.claims is
// always set for /rel too, even anonymously — JSON null, not left unset.
func TestRelHandler_ClaimsSetting_AnonymousIsJSONNull(t *testing.T) {
	rec := postRel(t, `{"function": "current_claims_setting", "schema": "public"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "null" {
		t.Errorf("expected the anonymous rel.jwt.claims setting to read back as JSON null, got %q", rec.Body.String())
	}
}

// TestRelHandler_ClaimsSetting_AuthenticatedMatchesJWT proves an
// authenticated /rel request's rel.jwt.claims setting carries the
// session's own role, readable from an ordinary query-language function
// call.
func TestRelHandler_ClaimsSetting_AuthenticatedMatchesJWT(t *testing.T) {
	cookie := mintCookie(t, testCfg, "authenticated_user")
	rec := postRelWithCookie(t, testHandler, `{"function": "current_claims_setting", "schema": "public"}`, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var claims map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &claims); err != nil {
		t.Fatalf("decoding rel.jwt.claims content: %v", err)
	}
	if claims["role"] != "authenticated_user" {
		t.Errorf("expected rel.jwt.claims to carry role=authenticated_user, got %#v", claims)
	}
}

func TestRelHandler_AnonymousReadDeniedOnRoleGatedTable_CleanEnvelope(t *testing.T) {
	// Same cause as the write test above, but on the read path : the
	// query fails before any bytes are written, so a clean envelope must result.
	rec := postRel(t, `{
		"relation": "secret_notes", "schema": "public", "select": ["own"]
	}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (permission denied), got %d: %s", rec.Code, rec.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("expected a valid JSON error envelope, got %q: %v", rec.Body.String(), err)
	}
	if envelope["status"] != "error" {
		t.Errorf("expected status \"error\", got %v", envelope["status"])
	}
}

func TestRelHandler_RenewalSetsCookie(t *testing.T) {
	cfg := config.Test()
	cfg.Pg.Query.AnonymousRole = "~anonymous"
	cfg.Jwt.MaxAge = 5
	cfg.Jwt.RenewAfter = 0.1 // renew almost immediately
	handler := NewRelHandler(testDb, cfg, nil)

	cookie := mintCookie(t, cfg, "authenticated_user")
	time.Sleep(1500 * time.Millisecond) // cross the renewafter threshold ; see route/handler_test.go's identical note on second-granularity claims

	rec := postRelWithCookie(t, handler, `{
		"relation": "secret_notes", "schema": "public", "select": ["own"]
	}`, cookie)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	renewed := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == cfg.Jwt.CookieName && c.Value != cookie.Value {
			renewed = true
		}
	}
	if !renewed {
		t.Errorf("expected a renewed Set-Cookie, got %v", rec.Result().Cookies())
	}
}

// TestRelHandler_AnonymousRoleDoesNotExist_Is401 : authentication.md's
// Anonymous role existence rule — a role never CREATE ROLEd is 401.
func TestRelHandler_AnonymousRoleDoesNotExist_Is401(t *testing.T) {
	cfg := *testCfg
	cfg.Pg.Query.AnonymousRole = "role_nobody_ever_created"

	db, err := pg.NewInfosAdminQuery(testDbURI, testDbURI, 0, cfg.Pg.Query.AnonymousRole)
	if err != nil {
		t.Fatalf("NewInfosAdminQuery: %v", err)
	}
	if db.AnonymousRoleExists {
		t.Fatalf("expected AnonymousRoleExists=false for a role nothing created")
	}
	handler := NewRelHandler(db, &cfg, nil)

	rec := postRelTo(t, handler, `{"relation": "director", "schema": "public", "select": ["own"]}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}

	// Unaffected when a valid cookie is presented — this gate is
	// specifically about UNAUTHENTICATED requests.
	cookie := mintCookie(t, &cfg, "authenticated_user")
	rec2 := postRelWithCookie(t, handler, `{"relation": "director", "schema": "public", "select": ["own"]}`, cookie)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected an authenticated request to be unaffected, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

func TestRelHandler_EmptyAnonymousRoleIsConfigErrorNotSyntaxError(t *testing.T) {
	cfg := config.Test() // Pg.Anonymous intentionally left unset
	handler := NewRelHandler(testDb, cfg, nil)

	rec := postRelWithCookie(t, handler, `{
		"relation": "secret_notes", "schema": "public", "select": ["own"]
	}`, nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// postRelWithCookie is postRelTo plus an optional request cookie.
func postRelWithCookie(t *testing.T, handler http.Handler, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/rel", bytes.NewBufferString(body))
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
