package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
)

// TestRelHandler_UniqueViolation_AlwaysClassified : ## Postgres error
// detail's tier 1 — shown in full regardless of dev, never leaking SQL.
func TestRelHandler_UniqueViolation_AlwaysClassified(t *testing.T) {
	email := "dup-errcode-test@example.com"
	insert := `{
		"query": {"relation": "profile", "schema": "public", "select": ["own"], "write_mode": "insert"},
		"data": [{"user_email": "` + email + `"}]
	}`
	// First insert succeeds or already exists ; the second is guaranteed to collide.
	_ = postRel(t, insert)

	for _, dev := range []bool{false, true} {
		t.Run(map[bool]string{false: "dev=false", true: "dev=true"}[dev], func(t *testing.T) {
			cfg := *testCfg
			cfg.Dev = dev
			handler := NewRelHandler(testDb, &cfg, nil)

			rec := postRelTo(t, handler, insert)
			if rec.Code != http.StatusConflict {
				t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get(errcode.Header); got != "PG_UNIQUE_VIOLATION" {
				t.Errorf("X-Rel-Errorcode = %q, want PG_UNIQUE_VIOLATION", got)
			}

			var envelope struct {
				Status  string `json:"status"`
				Code    string `json:"code"`
				Error   string `json:"error"`
				PgError *struct {
					Message        string `json:"message"`
					ConstraintName string `json:"constraint_name"`
					TableName      string `json:"table_name"`
				} `json:"pg_error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("invalid JSON: %v: %s", err, rec.Body.String())
			}
			if envelope.Code != "PG_UNIQUE_VIOLATION" {
				t.Errorf("code = %q, want PG_UNIQUE_VIOLATION", envelope.Code)
			}
			if envelope.PgError == nil {
				t.Fatal("expected pg_error to be present regardless of dev mode (tier 1)")
			}
			if envelope.PgError.ConstraintName == "" {
				t.Error("expected pg_error.constraint_name to be populated")
			}
			if envelope.PgError.TableName != "profile" {
				t.Errorf("pg_error.table_name = %q, want profile", envelope.PgError.TableName)
			}

			// The live leak this classification closes : generated SQL must
			// never appear in the response body.
			body := rec.Body.String()
			if strings.Contains(body, "\nsql:") || strings.Contains(body, "insert into") {
				t.Errorf("response body leaks generated SQL: %s", body)
			}
		})
	}
}

// TestRelHandler_UnclassifiedInternalError_DevGated : an ordinary 5xx gets
// a generic message in production, full detail only under dev.
func TestRelHandler_UnclassifiedInternalError_DevGated(t *testing.T) {
	for _, dev := range []bool{false, true} {
		t.Run(map[bool]string{false: "dev=false", true: "dev=true"}[dev], func(t *testing.T) {
			cfg := config.Test() // Pg.Query.AnonymousRole intentionally unset
			cfg.Dev = dev
			handler := NewRelHandler(testDb, cfg, nil)

			rec := postRelWithCookie(t, handler, `{
				"relation": "secret_notes", "schema": "public", "select": ["own"]
			}`, nil)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get(errcode.Header); got != string(errcode.NoRoleConfigured) {
				t.Errorf("X-Rel-Errorcode = %q, want %s", got, errcode.NoRoleConfigured)
			}

			var envelope struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("invalid JSON: %v: %s", err, rec.Body.String())
			}
			if dev {
				if !strings.Contains(envelope.Error, "no role configured") {
					t.Errorf("dev=true : expected full detail in error, got %q", envelope.Error)
				}
			} else {
				if envelope.Error != "internal error" {
					t.Errorf("dev=false : expected generic \"internal error\", got %q", envelope.Error)
				}
			}
		})
	}
}

// TestRelHandler_MethodNotAllowed_Is405 : the fix found while drafting
// specs/error-handling.md — this path used to be a wrongly-classified 400.
func TestRelHandler_MethodNotAllowed_Is405(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/rel", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(errcode.Header); got != string(errcode.MethodNotAllowed) {
		t.Errorf("X-Rel-Errorcode = %q, want %s", got, errcode.MethodNotAllowed)
	}
	if got := rec.Header().Get("Allow"); got != "GET, POST" {
		t.Errorf("Allow header = %q, want \"GET, POST\"", got)
	}
}
