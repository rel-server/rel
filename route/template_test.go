package route

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ceymard/rel/websec"
)

// newTemplateTestHandler builds a fresh handler with http.templates.path
// pointed at a real temp directory containing greet.jet — config.Test()'s
// own default (DefaultHttpTemplatesPath, "/template") doesn't exist in the
// test environment, so NewTemplateSet would build a *jet.Set pointed at a
// nonexistent directory (fine for GetTemplate to fail against, but not what
// these tests want to exercise for the happy path).
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
	return websec.Middleware(&cfg)(mux), dir
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

// TestTemplate_RuntimeError_Is500 proves a template referencing a field
// that doesn't exist errors at execution time, also a 500 — and, critically,
// that the failure produces a CLEAN 500 with no partially-written body, not
// a 200 with a truncated/corrupted document. writeTemplateResponse buffers
// the entire render before writing anything (bytes.Buffer, then one single
// w.Write call) specifically so a mid-render failure can still be turned
// into a proper error response instead of bytes already having reached the
// client — this test writes some literal text BEFORE the failing
// expression, so a regression back to a non-buffered, straight-to-w.Write
// per-node renderer would leak that leading text into a 200 response.
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

// TestTemplate_ReqReflectsNestedJwtClaims proves the "Req" VarMap's jwt
// field is the exact RelHttpRequest.jwt the route function itself received
// : an authenticated call sees nested claims (Req.jwt.role), an anonymous
// call sees Req.jwt as a JSON null (an object key present, not absent).
func TestTemplate_ReqReflectsNestedJwtClaims(t *testing.T) {
	handler, _ := newTemplateTestHandler(t, `{{if isset(Req.jwt)}}role={{ Req.jwt.role }}{{else}}jwt-is-nil=true{{end}}`)

	// Anonymous call : Req.jwt must be nil (JSON null), not merely absent —
	// Jet's isset(nil) is false for a Go nil interface, same as an absent
	// map key, so this specifically proves buildRelHttpRequest's own "jwt
	// key always present, null when anonymous" contract survives into Req.
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

// TestTemplate_NoTemplatesConfigured_Is500 proves a route setting
// "template" when no http.templates.path directory is configured/found is
// a 500, not a panic or silent fallback.
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
