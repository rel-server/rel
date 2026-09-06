package route

// Tests for specs/new-routes.md ## Middleware, driven through testHandler
// like e2e_test.go — proving the chain-execution logic in middleware.go
// against real HTTP requests and real Postgres, not just the resolution
// logic MiddlewareChain already covers in routeset_test.go.

import (
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/static"
)

// TestMiddleware_PassThroughMergesContext proves a non-terminal middleware
// merges its content into request.context, and that a later middleware's
// conflicting key wins over an earlier one's (## Middleware).
func TestMiddleware_PassThroughMergesContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/mw/chain/echo", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var ctx map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &ctx); err != nil {
		t.Fatalf("decoding echoed context %q: %v", rec.Body.String(), err)
	}
	if ctx["from_a"] != "a" {
		t.Errorf(`expected from_a="a" (fn_mw_a's own key), got %#v`, ctx)
	}
	if ctx["from_b"] != "b" {
		t.Errorf(`expected from_b="b" (fn_mw_b's own key), got %#v`, ctx)
	}
	if ctx["shared"] != "b" {
		t.Errorf(`expected shared="b" (the later middleware, fn_mw_b, wins the conflict), got %#v`, ctx)
	}
}

// TestMiddleware_RSxxxRejection proves an RSxxx exception raised by a
// middleware short-circuits the request exactly like a route function's
// own would, and the guarded route never runs.
func TestMiddleware_RSxxxRejection(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/mw/rejected/target", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (RS403 from the middleware), got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestMiddleware_RejectionDoesNotRenew is a regression test for
// jwt/middleware.go's own documented invariant (Renew must run after
// Check) : an authenticated request past jwt.renew_after, rejected by
// middleware, must not carry a renewed Set-Cookie — a middleware
// rejecting a request has no business handing back a fresh cookie for the
// very session it just rejected.
func TestMiddleware_RejectionDoesNotRenew(t *testing.T) {
	cfg := *testCfg
	cfg.Jwt.MaxAge = 5
	cfg.Jwt.RenewAfter = 0.1
	handler := NewHandler(testDb, &cfg, testReg, nil)

	now := time.Now()
	token, err := jwtpkg.Sign(cfg.Jwt, jwtpkg.Claims{
		"role":      "app_user",
		"iat":       float64(now.Unix()),
		"exp":       float64(now.Add(time.Duration(cfg.Jwt.MaxAge) * time.Second).Unix()),
		"auth_time": float64(now.Unix()),
	})
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}
	time.Sleep(1500 * time.Millisecond) // cross the renew_after threshold

	req := httptest.NewRequest(http.MethodGet, "/mw/rejected/target", nil)
	req.AddCookie(&http.Cookie{Name: cfg.Jwt.CookieName, Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (RS403 from the middleware), got %d: %s", rec.Code, rec.Body.String())
	}
	if cookies := rec.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("expected no Set-Cookie on a rejected request, got %v", cookies)
	}
}

// TestMiddleware_TerminalShortCircuit proves a middleware terminating via
// status (no exception) sends its own response as-is and the guarded route
// never runs at all.
func TestMiddleware_TerminalShortCircuit(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/mw/terminal/target", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402 from the terminating middleware, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "payment required") {
		t.Errorf("expected the middleware's own content as the body, got %q", rec.Body.String())
	}

	var count int
	if err := testDb.Pool.QueryRow(req.Context(), "select count(*) from mw_terminal_calls").Scan(&count); err != nil {
		t.Fatalf("counting mw_terminal_calls: %v", err)
	}
	if count != 0 {
		t.Errorf("expected the guarded route to never run, but mw_terminal_calls has %d row(s)", count)
	}
}

// TestMiddleware_StreamUploadRunsOnceBeforeFirstCallOnly proves
// specs/new-routes.md ## Middleware's stream_upload wrinkle : the chain
// runs once, ahead of the first (metadata-only) call, never repeated
// before the second — a full two-call upload must show exactly 1 recorded
// middleware invocation, not 2.
func TestMiddleware_StreamUploadRunsOnceBeforeFirstCallOnly(t *testing.T) {
	if _, err := testDb.Pool.Exec(context.Background(), "delete from mw_stream_calls"); err != nil {
		t.Fatalf("clearing mw_stream_calls: %v", err)
	}

	var body strings.Builder
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "hello.txt")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte("hello")); err != nil {
		t.Fatalf("writing part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}

	dir := t.TempDir()
	cfg := *testCfg
	cfg.Http.Static.Path = dir
	staticSrv := static.New(cfg.Http)
	handler := NewHandler(testDb, &cfg, testReg, staticSrv)

	req := httptest.NewRequest(http.MethodPost, "/new/stream?path=mw-counted.txt", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var count int
	if err := testDb.Pool.QueryRow(context.Background(), "select count(*) from mw_stream_calls").Scan(&count); err != nil {
		t.Fatalf("counting mw_stream_calls: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 middleware invocation, got %d", count)
	}
}

// TestMiddleware_MissingGrantIsPermissionDenied is the Impacts-section
// regression test: a covering middleware missing an EXECUTE grant for the
// request's resolved role is an ordinary Postgres permission-denied error
// (42501), classified 403 like any other route call's — never a silent
// skip, and never the 500 an earlier spec draft claimed (fixed alongside
// this test).
func TestMiddleware_MissingGrantIsPermissionDenied(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ungranted/target", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (permission denied on the ungranted middleware), got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestMiddleware_OrderingShortestPrefixFirst proves three overlapping
// middleware run shortest-prefix-first.
func TestMiddleware_OrderingShortestPrefixFirst(t *testing.T) {
	if _, err := testDb.Pool.Exec(context.Background(), "delete from mw_order_calls"); err != nil {
		t.Fatalf("clearing mw_order_calls: %v", err)
	}

	httpReq := httptest.NewRequest(http.MethodGet, "/order/deep/target", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, httpReq)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rows, err := testDb.Pool.Query(httpReq.Context(), "select name from mw_order_calls order by id")
	if err != nil {
		t.Fatalf("querying mw_order_calls: %v", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scanning mw_order_calls: %v", err)
		}
		names = append(names, name)
	}

	want := []string{"root", "mid", "deep"}
	if len(names) != len(want) {
		t.Fatalf("expected %v, got %v", want, names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("expected order %v, got %v", want, names)
			break
		}
	}
}
