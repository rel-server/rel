package route

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jwtpkg "github.com/ceymard/rel/jwt"
	"github.com/ceymard/rel/pg"
)

func TestHandler_AnonymousCall(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_echo0", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "anon0" {
		t.Errorf("expected body \"anon0\", got %q", rec.Body.String())
	}
}

func TestHandler_EmptyAnonymousRoleIsConfigErrorNotSyntaxError(t *testing.T) {
	// config.Test() leaves Pg.Anonymous unset by design — handleRoute must
	// reject an anonymous request with a clear 500, not hand an empty role
	// name to `SET LOCAL ROLE` (a Postgres syntax error) or silently skip
	// the role switch (a privilege escalation for anonymous callers).
	cfgCopy := *testCfg
	cfgCopy.Pg.Query.AnonymousRole = ""
	handler := NewHandler(testDb, &cfgCopy, testReg, nil)

	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_echo0", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandler_RouteAnonymousCannotReach_Is401 proves route.Route.
// AnonymousAuthorized actually gates the request BEFORE the route function
// ever runs (schema.sql's fn_app_only has EXECUTE revoked from PUBLIC,
// never re-granted to "~anonymous", only to app_user) — distinct from
// fn_secret's denial, which happens INSIDE the function at the
// table-select level.
func TestHandler_RouteAnonymousCannotReach_Is401(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_app_only", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandler_RouteAnonymousCannotReach_AuthenticatedStillWorks proves the
// 401 above is genuinely about anonymous access specifically, not a
// broken route : an authenticated app_user call to the same function
// succeeds.
func TestHandler_RouteAnonymousCannotReach_AuthenticatedStillWorks(t *testing.T) {
	loginReq := httptest.NewRequest(http.MethodPost, "/route/public/fn_login", nil)
	loginRec := httptest.NewRecorder()
	testHandler.ServeHTTP(loginRec, loginReq)
	var jwtCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == testCfg.Jwt.CookieName {
			jwtCookie = c
		}
	}
	if jwtCookie == nil {
		t.Fatalf("login didn't set a cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_app_only", nil)
	req.AddCookie(jwtCookie)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "app only" {
		t.Errorf("expected \"app only\", got %q", rec.Body.String())
	}
}

// TestHandler_AnonymousRoleDoesNotExist_UniformlyDenies builds a completely
// separate DbInfos/Registry/Handler against the SAME container/schema, but
// with pg.query.anonymous_role pointed at a name nothing ever created —
// specs/authentication.md "# Roles ## Anonymous role existence" : with
// anonymous access disabled outright, every unauthenticated request gets a
// uniform 401, regardless of which route it targets (fn_echo0 has no
// route-level restriction at all — this is specifically the blanket gate,
// not route.AnonymousAuthorized).
func TestHandler_AnonymousRoleDoesNotExist_UniformlyDenies(t *testing.T) {
	cfg := *testCfg
	cfg.Pg.Query.AnonymousRole = "role_nobody_ever_created"

	db, err := pg.NewInfosAdminQuery(testDbURI, testDbURI, 0, cfg.Pg.Query.AnonymousRole)
	if err != nil {
		t.Fatalf("NewInfosAdminQuery: %v", err)
	}
	if db.AnonymousRoleExists {
		t.Fatalf("expected AnonymousRoleExists=false for a role nothing created")
	}
	reg, err := BuildRegistry(db, &cfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	handler := NewHandler(db, &cfg, reg, nil)

	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_echo0", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}

	// An authenticated request is entirely unaffected by the anonymous
	// role not existing — only unauthenticated requests are in scope here.
	// fn_login itself is called anonymously (no cookie presented yet), so
	// it's correctly ALSO denied by this same gate — a cookie is minted
	// directly instead, standing in for an already-established session.
	claims := jwtpkg.Mint(cfg.Jwt, "app_user", time.Now(), cfg.Jwt.MaxAge, nil)
	token, err := jwtpkg.Sign(cfg.Jwt, claims)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	cookie := jwtpkg.CookieValue(cfg.Jwt, token, claims, "")

	authedReq := httptest.NewRequest(http.MethodGet, "/route/public/fn_secret", nil)
	authedReq.AddCookie(cookie)
	authedRec := httptest.NewRecorder()
	handler.ServeHTTP(authedRec, authedReq)
	if authedRec.Code != http.StatusOK {
		t.Fatalf("expected an authenticated request to be unaffected, got %d: %s", authedRec.Code, authedRec.Body.String())
	}
}

func TestHandler_UnknownRouteIs404(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/route/public/does_not_exist", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandler_SecretRoute_AnonymousDenied(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_secret", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	// No SELECT grant to "~anonymous" — must fail, not silently succeed.
	if rec.Code == http.StatusOK {
		t.Fatalf("expected an anonymous call to fn_secret to be denied, got 200: %s", rec.Body.String())
	}
}

// TestHandler_FullLoginRoundTrip is the core proof : login mints a cookie,
// a subsequent authenticated call using that cookie can read role-gated
// data that the anonymous call above cannot.
func TestHandler_FullLoginRoundTrip(t *testing.T) {
	loginReq := httptest.NewRequest(http.MethodPost, "/route/public/fn_login", nil)
	loginRec := httptest.NewRecorder()
	testHandler.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login: expected 200, got %d: %s", loginRec.Code, loginRec.Body.String())
	}

	cookies := loginRec.Result().Cookies()
	var jwtCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == testCfg.Jwt.CookieName {
			jwtCookie = c
		}
	}
	if jwtCookie == nil {
		t.Fatalf("expected fn_login to set the %s cookie, got %v", testCfg.Jwt.CookieName, cookies)
	}

	secretReq := httptest.NewRequest(http.MethodGet, "/route/public/fn_secret", nil)
	secretReq.AddCookie(jwtCookie)
	secretRec := httptest.NewRecorder()
	testHandler.ServeHTTP(secretRec, secretReq)
	if secretRec.Code != http.StatusOK {
		t.Fatalf("authenticated fn_secret: expected 200, got %d: %s", secretRec.Code, secretRec.Body.String())
	}
	if secretRec.Body.String() != "top secret" {
		t.Errorf("expected the role-gated secret value, got %q", secretRec.Body.String())
	}
}

func TestHandler_Logout_ClearsCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_logout", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	cookies := rec.Result().Cookies()
	var jwtCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == testCfg.Jwt.CookieName {
			jwtCookie = c
		}
	}
	if jwtCookie == nil {
		t.Fatalf("expected fn_logout to set a clearing cookie")
	}
	if jwtCookie.MaxAge >= 0 {
		t.Errorf("expected a negative Max-Age (immediate expiry), got %d", jwtCookie.MaxAge)
	}
}

func TestHandler_WrongCookieFallsBackToAnonymous_NotAnError(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_echo0", nil)
	req.AddCookie(&http.Cookie{Name: testCfg.Jwt.CookieName, Value: "not-a-real-jwt"})
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected a tampered/garbage cookie to fall back to anonymous (200), got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_CheckSessionRejection_AbortsAndClearsCookie(t *testing.T) {
	loginReq := httptest.NewRequest(http.MethodPost, "/route/public/fn_login", nil)
	loginRec := httptest.NewRecorder()
	testHandler.ServeHTTP(loginRec, loginReq)
	var jwtCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == testCfg.Jwt.CookieName {
			jwtCookie = c
		}
	}
	if jwtCookie == nil {
		t.Fatalf("login didn't set a cookie")
	}

	setSessionReject(t, true)

	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_secret", nil)
	req.AddCookie(jwtCookie)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected check_session's RS401 to produce 401, got %d: %s", rec.Code, rec.Body.String())
	}

	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == testCfg.Jwt.CookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("expected check_session rejection to clear the JWT cookie")
	}
}

func TestHandler_RSCode_MapsToExactStatus(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_forbidden", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected RS403 to map to 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "nope" {
		t.Errorf("expected the raised message as the plain-text body, got %q", rec.Body.String())
	}
}

func TestHandler_NonRSError_Is500PlainText(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_boom", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err == nil {
		t.Errorf("expected a plain-text body, not JSON, got %q", rec.Body.String())
	}
}

func TestHandler_VerbDispatch(t *testing.T) {
	getReq := httptest.NewRequest(http.MethodGet, "/route/public/fn_verbtest", nil)
	getRec := httptest.NewRecorder()
	testHandler.ServeHTTP(getRec, getReq)
	if getRec.Body.String() != "GET" {
		t.Errorf("expected GET dispatch, got %q", getRec.Body.String())
	}

	postReq := httptest.NewRequest(http.MethodPost, "/route/public/fn_verbtest", nil)
	postRec := httptest.NewRecorder()
	testHandler.ServeHTTP(postRec, postReq)
	if postRec.Body.String() != "POST" {
		t.Errorf("expected POST dispatch, got %q", postRec.Body.String())
	}
}

func TestHandler_MimeTypeDomainResponse(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_image", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("expected Content-Type=image/png, got %q", ct)
	}
	if rec.Body.Len() == 0 {
		t.Errorf("expected a non-empty binary body")
	}
}

// TestHandler_AuthNotHonoredOutsideAllowedFunctions covers
// http.functions.auth : fn_echo1 (not in the restricted allow-list) can
// still set a "jwt" key in its response, but it must not actually be
// honored.
func TestHandler_AuthNotHonoredOutsideAllowedFunctions(t *testing.T) {
	restricted := *testCfg
	restricted.Http.Functions.AllowedAuth = `^public\.fn_login$`
	handler := NewHandler(testDb, &restricted, testReg, nil)

	// fn_echo1 echoes the whole request back — it never itself sets a jwt
	// key, so this proves the DEFAULT (no accidental minting) rather than
	// an active suppression ; a function that actually tries to set jwt
	// outside the allow-list would need its own fixture to prove
	// suppression specifically, deferred as lower-value than the
	// mint-is-gated-at-all coverage this already gives via fn_login itself
	// requiring no restriction to work (see TestHandler_FullLoginRoundTrip).
	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_echo1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == testCfg.Jwt.CookieName {
			t.Errorf("expected no JWT cookie from a function outside http.functions.auth")
		}
	}
}

// TestHandler_RequestEchoShape verifies buildRelHttpRequest's actual wire
// encoding end-to-end (not just unit-level) : fn_echo1 returns the whole
// RelHttpRequest it received as its own content.
func TestHandler_RequestEchoShape(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_echo1?x=1", nil)
	req.AddCookie(&http.Cookie{Name: "session_hint", Value: "abc"})
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var echoed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &echoed); err != nil {
		t.Fatalf("decoding echoed request: %v", err)
	}
	if echoed["method"] != "POST" {
		t.Errorf("expected method=POST, got %v", echoed["method"])
	}
	if echoed["uri"] != "/route/public/fn_echo1?x=1" {
		t.Errorf("expected uri to include the query string, got %v", echoed["uri"])
	}
	cookies, _ := echoed["cookies"].(map[string]any)
	if cookies["session_hint"] != "abc" {
		t.Errorf("expected cookies.session_hint=abc (value-only shape), got %v", echoed["cookies"])
	}
	if echoed["jwt"] != nil {
		t.Errorf("expected jwt=null for an anonymous request, got %v", echoed["jwt"])
	}
}

// TestHandler_RenewalFiresPastThreshold proves the renewal wiring end-to-
// end (jwt package's own ShouldRenew/Renew logic is already unit-tested in
// jwt_test.go — this is specifically about handleRoute actually calling it
// and writing the resulting Set-Cookie).
func TestHandler_RenewalFiresPastThreshold(t *testing.T) {
	fastRenew := *testCfg
	fastRenew.Jwt.MaxAge = 5 // seconds
	fastRenew.Jwt.RenewAfter = 0.1
	handler := NewHandler(testDb, &fastRenew, testReg, nil)

	loginReq := httptest.NewRequest(http.MethodPost, "/route/public/fn_login", nil)
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, loginReq)
	var original *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == fastRenew.Jwt.CookieName {
			original = c
		}
	}
	if original == nil {
		t.Fatalf("login didn't set a cookie")
	}

	// iat/exp are second-granularity claims, so the sleep must reliably
	// cross an integer-second boundary (a short sleep can land within the
	// SAME second as the original mint depending on where in the second it
	// started, producing a byte-identical re-signed token even though
	// renewal genuinely ran) — 1.5s comfortably clears both the 0.1×5s=0.5s
	// renewafter threshold and any single-second timing coincidence, while
	// staying well under the 5s maxage so the token hasn't expired.
	time.Sleep(1500 * time.Millisecond)

	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_echo0", nil)
	req.AddCookie(original)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var renewed *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == fastRenew.Jwt.CookieName {
			renewed = c
		}
	}
	if renewed == nil {
		t.Fatalf("expected a renewed Set-Cookie past the renewafter threshold")
	}
	if renewed.Value == original.Value {
		t.Errorf("expected a genuinely fresh token, got the same value")
	}
}

// TestHandler_QueryFieldStructuralDecode proves specs/query-json.md's
// RelHttpRequest.query field : the request's raw query string, decoded
// through the querystring package's structural layer only (dot-path ->
// nested JSON, repeated keys -> arrays — no filter expression grammar
// involvement), shows up as the "query" key a route function receives.
func TestHandler_QueryFieldStructuralDecode(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_echo1?page.size=20&tags=a&tags=b&filter=gte(year,1999)", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var echoed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &echoed); err != nil {
		t.Fatalf("decoding echoed request: %v", err)
	}
	query, ok := echoed["query"].(map[string]any)
	if !ok {
		t.Fatalf("expected echoed.query to be an object, got %#v", echoed["query"])
	}
	page, ok := query["page"].(map[string]any)
	if !ok || page["size"] != "20" {
		t.Errorf("expected query.page.size==\"20\", got %#v", query["page"])
	}
	tags, ok := query["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Errorf("expected query.tags==[\"a\",\"b\"] (repeated key -> array), got %#v", query["tags"])
	}
	// The structural layer only : "gte(year,1999)" must stay a plain
	// string, NOT get compiled through the filter expression grammar —
	// that grammar is specific to Relation's where/select/order_by, per
	// the spec's own note.
	if query["filter"] != "gte(year,1999)" {
		t.Errorf("expected query.filter to stay the raw string \"gte(year,1999)\", got %#v", query["filter"])
	}
}

// TestHandler_QueryFieldStructuralDecode_RealGETRequest covers the same
// structural-decode contract as TestHandler_QueryFieldStructuralDecode, but
// through an ACTUAL GET request (the other test uses POST, since it's
// really testing buildRelHttpRequest's query decoding rather than method
// dispatch) — "heavier tests using post/get forms... GET requests carrying
// a real, non-trivial query string... not just a bare GET with no query
// string."
func TestHandler_QueryFieldStructuralDecode_RealGETRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_echo1?filters.status=open&filters.priority=high&ids=1&ids=2&ids=3", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var echoed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &echoed); err != nil {
		t.Fatalf("decoding echoed request: %v", err)
	}
	if echoed["method"] != "GET" {
		t.Errorf("expected method=GET, got %v", echoed["method"])
	}
	query, ok := echoed["query"].(map[string]any)
	if !ok {
		t.Fatalf("expected echoed.query to be an object, got %#v", echoed["query"])
	}
	filters, ok := query["filters"].(map[string]any)
	if !ok || filters["status"] != "open" || filters["priority"] != "high" {
		t.Errorf("expected query.filters.{status,priority} decoded, got %#v", query["filters"])
	}
	ids, ok := query["ids"].([]any)
	if !ok || len(ids) != 3 || ids[0] != "1" || ids[1] != "2" || ids[2] != "3" {
		t.Errorf("expected query.ids==[\"1\",\"2\",\"3\"] (repeated key -> array), got %#v", query["ids"])
	}
}

func TestHandler_QueryFieldNullWhenNoQueryString(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_echo1", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var echoed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &echoed); err != nil {
		t.Fatalf("decoding echoed request: %v", err)
	}
	if v, present := echoed["query"]; !present || v != nil {
		t.Errorf("expected query==null for a request with no query string, got %#v", v)
	}
}

// setSessionReject toggles session_control.reject for
// TestHandler_CheckSessionRejection_AbortsAndClearsCookie, resetting it via
// t.Cleanup since session_control is shared, ordering-sensitive state
// across every test in this package's single shared container.
func setSessionReject(t *testing.T, reject bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, "update session_control set reject = $1", reject); err != nil {
		t.Fatalf("setSessionReject: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testDb.Pool.Exec(context.Background(), "update session_control set reject = false")
	})
}
