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

// TestCors_Wildcard_NeverSendsCredentials_ReflectsLiteralStar proves ## CORS
// ### `*` as an explicit value's rule end to end : allowed_origins:"*"
// allows any origin, but sends the literal "*" (not the reflected Origin
// value) and NEVER Access-Control-Allow-Credentials — combining a wildcard
// origin with credentials is unsafe and must never happen regardless of
// which origin actually sent the request.
func TestCors_Wildcard_NeverSendsCredentials_ReflectsLiteralStar(t *testing.T) {
	handler := wrapWithWebsec(t, func(cfg *config.Config) {
		cfg.Http.Cors.AllowedOrigins = "*"
	})

	req := httptest.NewRequest(http.MethodGet, "/rpc/public/fn_echo0", nil)
	req.Header.Set("Origin", "https://anything.example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("expected literal \"*\", got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("expected no Access-Control-Allow-Credentials with a wildcard origin, got %q", got)
	}

	// The same "never credentials with a wildcard" rule on a PREFLIGHT
	// response specifically — the traditional leak point, since a preflight
	// is what a browser consults to decide whether to actually send
	// credentials on the real request that follows.
	preflight := httptest.NewRequest(http.MethodOptions, "/rpc/public/fn_echo0", nil)
	preflight.Header.Set("Origin", "https://anything.example.com")
	preflight.Header.Set("Access-Control-Request-Method", "GET")
	preflightRec := httptest.NewRecorder()
	handler.ServeHTTP(preflightRec, preflight)

	if got := preflightRec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("preflight: expected literal \"*\", got %q", got)
	}
	if got := preflightRec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("preflight: expected no Access-Control-Allow-Credentials with a wildcard origin, got %q", got)
	}
}

// TestCors_MultipleConfiguredOrigins_EachReflectedIndividually proves a
// comma-separated allowlist matches EACH configured origin (not just the
// first/last), reflecting that exact origin back with credentials+Vary.
func TestCors_MultipleConfiguredOrigins_EachReflectedIndividually(t *testing.T) {
	handler := wrapWithWebsec(t, func(cfg *config.Config) {
		cfg.Http.Cors.AllowedOrigins = "https://a.example.com,https://b.example.com"
	})

	for _, origin := range []string{"https://a.example.com", "https://b.example.com"} {
		req := httptest.NewRequest(http.MethodGet, "/rpc/public/fn_echo0", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
			t.Errorf("origin %s: expected it reflected back, got %q", origin, got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Errorf("origin %s: expected Access-Control-Allow-Credentials=true, got %q", origin, got)
		}
	}

	// A third, unconfigured origin gets no CORS headers at all.
	req := httptest.NewRequest(http.MethodGet, "/rpc/public/fn_echo0", nil)
	req.Header.Set("Origin", "https://c.example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected no Access-Control-Allow-Origin for an unconfigured origin, got %q", got)
	}
}

// TestCors_Preflight_MethodsAndHeadersReflectConfiguredValues proves the
// preflight response's Access-Control-Allow-Methods/-Headers carry the
// ACTUAL configured values, not merely a non-empty placeholder.
func TestCors_Preflight_MethodsAndHeadersReflectConfiguredValues(t *testing.T) {
	handler := wrapWithWebsec(t, func(cfg *config.Config) {
		cfg.Http.Cors.AllowedOrigins = "https://example.com"
		cfg.Http.Cors.AllowedMethods = "GET, POST, DELETE"
		cfg.Http.Cors.AllowedHeaders = "Content-Type, X-Custom-Header"
	})

	req := httptest.NewRequest(http.MethodOptions, "/rpc/public/fn_echo0", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, DELETE" {
		t.Errorf("expected the configured methods verbatim, got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, X-Custom-Header" {
		t.Errorf("expected the configured headers verbatim, got %q", got)
	}
}

// TestCsp_RawPolicyConfigEndToEnd proves http.csp.policy (the config-level
// raw override, distinct from a per-response RelHttpResponse.csp override)
// actually replaces the ten-directive default on the wire.
func TestCsp_RawPolicyConfigEndToEnd(t *testing.T) {
	handler := wrapWithWebsec(t, func(cfg *config.Config) {
		cfg.Http.Csp.Policy = "default-src 'none'; connect-src 'self'"
	})

	req := httptest.NewRequest(http.MethodGet, "/rpc/public/fn_echo0", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "connect-src 'self'") {
		t.Errorf("expected http.csp.policy's own directives on the wire, got %q", csp)
	}
	// config.Test()'s own ten-directive default (DefaultHttpCspDefaultSrc =
	// "'self'") must not leak through once http.csp.policy is set : its
	// marker directive is "default-src 'self'", distinct from this test's
	// own raw override "default-src 'none'" — and script-src must synthesize
	// from the OVERRIDE's own default-src ('none'), not the config default.
	if strings.Contains(csp, "default-src 'self'") {
		t.Errorf("expected the ten-directive config default NOT to leak through: %q", csp)
	}
	if !strings.Contains(csp, "script-src 'none' 'nonce-") {
		t.Errorf("expected script-src synthesized from the raw policy's own default-src 'none', got %q", csp)
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
