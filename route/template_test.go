package route

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rel-server/rel/websec"
)

// newTemplateTestHandler points http.templates.path at a real temp dir ;
// config.Test()'s own default path doesn't exist in the test environment.
func newTemplateTestHandler(t *testing.T, templateBody string) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "greet.jet"), []byte(templateBody), 0o644); err != nil {
		t.Fatalf("writing template fixture: %v", err)
	}
	cfg := *testCfg
	cfg.Http.Templates.Path = dir
	mux := http.NewServeMux()
	mux.Handle("/route/", NewHandler(testDb, &cfg, testReg, nil))
	return websec.Middleware(&cfg)(websec.NonceMiddleware(&cfg)(mux)), dir
}

// TestTemplate_RendersDataReqNonce proves all three VarMap variables
// (Data/Req/Nonce) are actually populated and accessible.
func TestTemplate_RendersDataReqNonce(t *testing.T) {
	handler, _ := newTemplateTestHandler(t, `Hello {{ Data.name }}! method={{ Req.method }} nonce-len={{ len(Nonce) }}`)

	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_template", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Hello World!") {
		t.Errorf("expected Data.name interpolated, got %q", body)
	}
	if !strings.Contains(body, "method=GET") {
		t.Errorf("expected Req.method interpolated, got %q", body)
	}
	if strings.Contains(body, "nonce-len=0") {
		t.Errorf("expected a non-empty Nonce, got %q", body)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html" {
		t.Errorf("expected content_type text/html (set by the route itself, not implied by template), got %q", got)
	}
}

// TestTemplate_LoadFailure_Is500 proves a template that fails to load
// (missing file) is a 500, never a silent fallback to resp.content.
func TestTemplate_LoadFailure_Is500(t *testing.T) {
	handler, _ := newTemplateTestHandler(t, `irrelevant`)

	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_template_missing", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestTemplate_RuntimeError_Is500 : a clean 500, no partial body ; writes
// leading text before the failure so unbuffered rendering would leak it.
func TestTemplate_RuntimeError_Is500(t *testing.T) {
	handler, _ := newTemplateTestHandler(t, `some leading output before the failure {{ Data.name.nonexistent.deeper }}`)

	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_template", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on a template runtime error, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "leading output") {
		t.Errorf("expected no partially-rendered body to leak through on a runtime error, got %q", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got == "text/html" {
		t.Errorf("expected the route's own text/html Content-Type NOT to be set on a failed render, got %q", got)
	}
}

// TestTemplate_ReqReflectsNestedJwtClaims : Req.jwt is JSON null (present,
// not absent) when anonymous, nested claims when authenticated.
func TestTemplate_ReqReflectsNestedJwtClaims(t *testing.T) {
	handler, _ := newTemplateTestHandler(t, `{{if isset(Req.jwt)}}role={{ Req.jwt.role }}{{else}}jwt-is-nil=true{{end}}`)

	// Jet's isset(nil) is false for both a nil interface and an absent key,
	// so this only proves "jwt always present, null when anonymous" holds.
	anonReq := httptest.NewRequest(http.MethodGet, "/route/public/fn_template", nil)
	anonRec := httptest.NewRecorder()
	handler.ServeHTTP(anonRec, anonReq)
	if anonRec.Code != http.StatusOK {
		t.Fatalf("anonymous: expected 200, got %d: %s", anonRec.Code, anonRec.Body.String())
	}
	if !strings.Contains(anonRec.Body.String(), "jwt-is-nil=true") {
		t.Errorf("expected Req.jwt to read as unset/null for an anonymous request, got %q", anonRec.Body.String())
	}

	// Authenticated call : log in through the same handler first to get a
	// real, valid cookie, then reuse it on the template request.
	loginReq := httptest.NewRequest(http.MethodPost, "/route/public/fn_login", nil)
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, loginReq)
	var jwtCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == testCfg.Jwt.CookieName {
			jwtCookie = c
		}
	}
	if jwtCookie == nil {
		t.Fatalf("login didn't set a cookie")
	}

	authedReq := httptest.NewRequest(http.MethodGet, "/route/public/fn_template", nil)
	authedReq.AddCookie(jwtCookie)
	authedRec := httptest.NewRecorder()
	handler.ServeHTTP(authedRec, authedReq)
	if authedRec.Code != http.StatusOK {
		t.Fatalf("authenticated: expected 200, got %d: %s", authedRec.Code, authedRec.Body.String())
	}
	if !strings.Contains(authedRec.Body.String(), "role=app_user") {
		t.Errorf("expected Req.jwt.role=app_user for an authenticated request, got %q", authedRec.Body.String())
	}
}

// TestTemplate_NoTemplatesConfigured_Is500 : no configured templates.path
// is a 500, never a panic or silent fallback.
func TestTemplate_NoTemplatesConfigured_Is500(t *testing.T) {
	cfg := *testCfg
	cfg.Http.Templates.Path = "" // NewTemplateSet returns nil for ""
	mux := http.NewServeMux()
	mux.Handle("/route/", NewHandler(testDb, &cfg, testReg, nil))
	handler := websec.Middleware(&cfg)(mux)

	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_template", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}
