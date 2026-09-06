package route

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/pg"
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
	// An unset anonymous role must be a clear 500, not an empty-role
	// SET LOCAL ROLE syntax error or a silently skipped role switch.
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

// TestHandler_RouteAnonymousCannotReach_Is401 : AnonymousAuthorized gates
// before the route function runs, distinct from fn_secret's own denial.
func TestHandler_RouteAnonymousCannotReach_Is401(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_app_only", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandler_RouteAnonymousCannotReach_AuthenticatedStillWorks proves the
// 401 above is about anonymous access, not a broken route.
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

// TestHandler_AnonymousRoleDoesNotExist_UniformlyDenies : a role nothing
// created gets a uniform 401 (authentication.md ## Anonymous role existence).
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

	// An authenticated request is unaffected ; a cookie is minted directly
	// here rather than via fn_login, which would itself be denied anonymous.
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

// TestHandler_FullLoginRoundTrip : login mints a cookie, a subsequent
// authenticated call reads role-gated data the anonymous call cannot.
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

// TestHandler_AuthNotHonoredOutsideAllowedFunctions : fn_echo1, outside
// http.functions.auth's allow-list, can set "jwt" but it's not honored.
func TestHandler_AuthNotHonoredOutsideAllowedFunctions(t *testing.T) {
	restricted := *testCfg
	restricted.Http.Functions.AllowedAuth = `^public\.fn_login$`
	handler := NewHandler(testDb, &restricted, testReg, nil)

	// fn_echo1 never sets jwt itself, so this proves the default (no
	// accidental mint), not active suppression of a real attempt.
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

// TestHandler_RequestEchoShape verifies buildRelHttpRequest's wire
// encoding end-to-end via fn_echo1's own request echo.
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

// TestHandler_RenewalFiresPastThreshold proves handleRoute actually calls
// the already-unit-tested renewal logic and writes the Set-Cookie.
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

	// iat/exp are second-granularity ; 1.5s reliably crosses a second
	// boundary and clears the 0.5s renewafter threshold, still under 5s maxage.
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

// TestHandler_QueryFieldStructuralDecode : the raw query string, decoded
// through the structural layer only, shows up as "query" (query-json.md).
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
	// Structural layer only : "gte(year,1999)" stays a plain string, never
	// compiled through the filter expression grammar.
	if query["filter"] != "gte(year,1999)" {
		t.Errorf("expected query.filter to stay the raw string \"gte(year,1999)\", got %#v", query["filter"])
	}
}

// TestHandler_QueryFieldStructuralDecode_RealGETRequest covers the same
// contract through an actual GET request with a real query string, not POST.
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

// setSessionReject toggles session_control.reject, resetting via
// t.Cleanup since it's shared state across this package's container.
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
