package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/websec"
)

// wrapWithWebsec builds the same middleware chain boot.BuildMux composes
// (websec.Middleware around a mux), reusing testDb/testReg as the inner
// handler's own dependencies — needed for CORS preflight/actual-response
// tests, which only engage at that outer layer, and for CSP tests, which
// read RelHttpRequest.csp_nonce off the context websec.Middleware stashes.
func wrapWithWebsec(t *testing.T, mutate func(cfg *config.Config)) http.Handler {
	t.Helper()
	cfg := *testCfg
	if mutate != nil {
		mutate(&cfg)
	}
	mux := http.NewServeMux()
	mux.Handle("/rpc/", NewHandler(testDb, &cfg, testReg, nil))
	return websec.Middleware(&cfg)(mux)
}

func decodeJSON(raw []byte, v any) error {
	return json.Unmarshal(raw, v)
}

func TestCors_PreflightAnsweredDirectly_NeverReachesRoute(t *testing.T) {
	handler := wrapWithWebsec(t, func(cfg *config.Config) {
		cfg.Http.Cors.AllowedOrigins = "https://example.com"
	})

	req := httptest.NewRequest(http.MethodOptions, "/rpc/public/fn_echo0", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Errorf("expected Access-Control-Allow-Origin, got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Errorf("expected Access-Control-Allow-Methods on a preflight response")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("expected an empty body (route never invoked), got %q", rec.Body.String())
	}
}

// TestCors_PlainOptionsWithoutBothHeaders_FallsThroughToRealRoute proves an
// OPTIONS request missing either Origin or Access-Control-Request-Method
// is NOT treated as a preflight — a real fn__options route must answer it.
func TestCors_PlainOptionsWithoutBothHeaders_FallsThroughToRealRoute(t *testing.T) {
	handler := wrapWithWebsec(t, nil)

	req := httptest.NewRequest(http.MethodOptions, "/rpc/public/fn_optroute", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from the real fn_optroute__OPTIONS route, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "real options route" {
		t.Errorf("expected the real route's own body, got %q", rec.Body.String())
	}
}

func TestCors_DisallowedOrigin_NoHeadersSent(t *testing.T) {
	handler := wrapWithWebsec(t, func(cfg *config.Config) {
		cfg.Http.Cors.AllowedOrigins = "https://allowed.example.com"
	})

	req := httptest.NewRequest(http.MethodGet, "/rpc/public/fn_echo0", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (CORS never blocks the request itself), got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected no Access-Control-Allow-Origin for a disallowed origin, got %q", got)
	}
}

func TestCsp_DefaultHeaderPresentWithSynthesizedNonceDirectives(t *testing.T) {
	handler := wrapWithWebsec(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/rpc/public/fn_echo0", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatalf("expected a Content-Security-Policy header by default")
	}
	if !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("expected default-src 'self' in %q", csp)
	}
	if !strings.Contains(csp, "script-src 'self' 'nonce-") {
		t.Errorf("expected a synthesized script-src with nonce in %q", csp)
	}
	if !strings.Contains(csp, "style-src 'self' 'nonce-") {
		t.Errorf("expected a synthesized style-src with nonce in %q", csp)
	}
}

// TestCsp_PerResponseOverride proves RelHttpResponse.csp (fn_csp_override
// in schema.sql) replaces the process-wide default for that one response.
func TestCsp_PerResponseOverride(t *testing.T) {
	handler := wrapWithWebsec(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/rpc/public/fn_csp_override", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("expected the per-response override's own default-src 'none' in %q", csp)
	}
	if strings.Contains(csp, "'self'") {
		t.Errorf("expected the process-wide default NOT to leak through: %q", csp)
	}
}

// TestRequest_CspNonce proves RelHttpRequest.csp_nonce is populated and
// matches the response header's own nonce value.
func TestRequest_CspNonce(t *testing.T) {
	handler := wrapWithWebsec(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/rpc/public/fn_echo1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var echoed struct {
		CspNonce string `json:"csp_nonce"`
	}
	if err := decodeJSON(rec.Body.Bytes(), &echoed); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if echoed.CspNonce == "" {
		t.Fatalf("expected a non-empty csp_nonce on RelHttpRequest")
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "'nonce-"+echoed.CspNonce+"'") {
		t.Errorf("expected the response's own CSP header to carry the same nonce %q, got %q", echoed.CspNonce, csp)
	}
}
