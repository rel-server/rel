package rpc

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
	mux.Handle("/rpc/", NewHandler(testDb, &cfg, testReg, nil))
	return websec.Middleware(&cfg)(mux), dir
}

// TestTemplate_RendersDataReqNonce proves all three VarMap variables
// (Data/Req/Nonce) are actually populated and accessible.
func TestTemplate_RendersDataReqNonce(t *testing.T) {
	handler, _ := newTemplateTestHandler(t, `Hello {{ Data.name }}! method={{ Req.method }} nonce-len={{ len(Nonce) }}`)

	req := httptest.NewRequest(http.MethodGet, "/rpc/public/fn_template", nil)
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

	req := httptest.NewRequest(http.MethodGet, "/rpc/public/fn_template_missing", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestTemplate_RuntimeError_Is500 proves a template referencing a field
// that doesn't exist errors at execution time, also a 500.
func TestTemplate_RuntimeError_Is500(t *testing.T) {
	handler, _ := newTemplateTestHandler(t, `{{ Data.name.nonexistent.deeper }}`)

	req := httptest.NewRequest(http.MethodGet, "/rpc/public/fn_template", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on a template runtime error, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestTemplate_NoTemplatesConfigured_Is500 proves a route setting
// "template" when no http.templates.path directory is configured/found is
// a 500, not a panic or silent fallback.
func TestTemplate_NoTemplatesConfigured_Is500(t *testing.T) {
	cfg := *testCfg
	cfg.Http.Templates.Path = "" // NewTemplateSet returns nil for ""
	mux := http.NewServeMux()
	mux.Handle("/rpc/", NewHandler(testDb, &cfg, testReg, nil))
	handler := websec.Middleware(&cfg)(mux)

	req := httptest.NewRequest(http.MethodGet, "/rpc/public/fn_template", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}
