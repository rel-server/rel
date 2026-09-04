package route

import (
	"net/http/httptest"
	"testing"

	"github.com/ceymard/rel/errcode"
)

// TestHandler_SecretRoute_PermissionDenied_IsClassified : PG_PERMISSION_DENIED/403,
// generic message in production, real only under dev.
func TestHandler_SecretRoute_PermissionDenied_IsClassified(t *testing.T) {
	req := httptest.NewRequest("GET", "/route/public/fn_secret", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != 403 {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(errcode.Header); got != string(errcode.Code("PG_PERMISSION_DENIED")) {
		t.Errorf("X-Rel-Errorcode = %q, want PG_PERMISSION_DENIED", got)
	}
	if body := rec.Body.String(); body != "insufficient permissions for this operation" {
		t.Errorf("prod body = %q, want the generic message (no table/object names leaked)", body)
	}

	cfg := *testCfg
	cfg.Dev = true
	devHandler := NewHandler(testDb, &cfg, testReg, nil)

	req2 := httptest.NewRequest("GET", "/route/public/fn_secret", nil)
	rec2 := httptest.NewRecorder()
	devHandler.ServeHTTP(rec2, req2)

	if rec2.Code != 403 {
		t.Fatalf("dev mode: expected 403, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if body := rec2.Body.String(); body == "insufficient permissions for this operation" {
		t.Error("dev mode: expected the real Postgres message, got the generic one")
	}
}
