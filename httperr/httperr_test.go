package httperr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rel-server/rel/errcode"
)

// captureLogs runs fn with the default logger writing JSON lines to the returned buffer's decoder.
func captureLogs(t *testing.T, fn func()) []map[string]any {
	t.Helper()
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	fn()
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("decoding log line %q: %v", raw, err)
		}
		lines = append(lines, m)
	}
	return lines
}

func pgErr(code, message, detail string) error {
	return fmt.Errorf("running query: %w", &pgconn.PgError{Code: code, Message: message, Detail: detail})
}

// One row per tier : what each rendering says in production and under dev, and whether the
// structured detail rides along (JSON only).
func TestPostgresError_TextPerTier(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		status     int
		code       errcode.Code
		jsonProd   string
		jsonProdPg bool
		jsonDev    string
		jsonDevPg  bool
		plainProd  string
		plainDev   string
	}{
		{
			name: "RSxxx message is always shown", err: pgErr("RS404", "no such thing", ""),
			status: 404, code: "RS404",
			jsonProd: "no such thing", jsonDev: "no such thing",
			plainProd: "no such thing", plainDev: "no such thing",
		},
		{
			name: "constraint violation", err: pgErr("23505", "duplicate key", "Key (a)=(1) already exists."),
			status: 409, code: "PG_UNIQUE_VIOLATION",
			jsonProd: "a database constraint was violated", jsonProdPg: true,
			jsonDev: "a database constraint was violated", jsonDevPg: true,
			plainProd: "duplicate key: Key (a)=(1) already exists.", plainDev: "duplicate key: Key (a)=(1) already exists.",
		},
		{
			name: "constraint violation without detail", err: pgErr("23502", "null value", ""),
			status: 400, code: "PG_NOT_NULL_VIOLATION",
			jsonProd: "a database constraint was violated", jsonProdPg: true,
			jsonDev: "a database constraint was violated", jsonDevPg: true,
			plainProd: "null value", plainDev: "null value",
		},
		{
			name: "request invalid", err: pgErr("42883", "function f(text) does not exist", "hint"),
			status: 400, code: "PG_UNDEFINED_FUNCTION",
			jsonProd: "function f(text) does not exist", jsonProdPg: true,
			jsonDev: "function f(text) does not exist", jsonDevPg: true,
			plainProd: "function f(text) does not exist", plainDev: "function f(text) does not exist",
		},
		{
			name: "permission denied", err: pgErr("42501", "permission denied for table secrets", ""),
			status: 403, code: "PG_PERMISSION_DENIED",
			jsonProd: "insufficient permissions for this operation",
			jsonDev:  "permission denied for table secrets", jsonDevPg: true,
			plainProd: "insufficient permissions for this operation", plainDev: "permission denied for table secrets",
		},
		{
			name: "unclassified", err: pgErr("XX000", "internal detail", ""),
			status: 500, code: errcode.Internal,
			jsonProd: "internal error",
			jsonDev:  "internal detail", jsonDevPg: true,
			plainProd: "internal error", plainDev: "internal detail",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, ok := FromPostgres(c.err)
			if !ok {
				t.Fatal("not classified")
			}
			if e.Status != c.status || e.Code != c.code {
				t.Fatalf("got %d %s, want %d %s", e.Status, e.Code, c.status, c.code)
			}
			if got, pg := e.JSONText(false); got != c.jsonProd || (pg != nil) != c.jsonProdPg {
				t.Errorf("JSONText(prod) = %q, pg=%v", got, pg != nil)
			}
			if got, pg := e.JSONText(true); got != c.jsonDev || (pg != nil) != c.jsonDevPg {
				t.Errorf("JSONText(dev) = %q, pg=%v", got, pg != nil)
			}
			if got := e.PlainText(false); got != c.plainProd {
				t.Errorf("PlainText(prod) = %q, want %q", got, c.plainProd)
			}
			if got := e.PlainText(true); got != c.plainDev {
				t.Errorf("PlainText(dev) = %q, want %q", got, c.plainDev)
			}
		})
	}
}

func TestFromPostgres_NotAPostgresError(t *testing.T) {
	if _, ok := FromPostgres(errors.New("plain")); ok {
		t.Fatal("a plain error must not classify")
	}
}

func TestPlainText_NonPostgres(t *testing.T) {
	if got := BadRequest(errcode.MalformedBody, errors.New("bad body")).PlainText(false); got != "bad body" {
		t.Errorf("4xx shows its error, got %q", got)
	}
	if got := Server(errcode.Internal, errors.New("secret path /etc")).PlainText(true); got != "internal error" {
		t.Errorf("5xx without Public is redacted even under dev, got %q", got)
	}
	e := Server(errcode.DBUnavailable, errors.New("secret"))
	e.Public = "acquiring connection"
	if got := e.PlainText(false); got != "acquiring connection" {
		t.Errorf("5xx shows Public, got %q", got)
	}
}

func TestLog_OnlyServerFaults(t *testing.T) {
	lines := captureLogs(t, func() {
		BadRequest(errcode.MalformedBody, errors.New("client mistake")).Log(context.Background())
		Unauthorized(errcode.AnonymousDisabled, errors.New("nope")).Log(context.Background())
	})
	if len(lines) != 0 {
		t.Fatalf("a 4xx must not be logged, got %#v", lines)
	}

	lines = captureLogs(t, func() {
		e, _ := FromPostgres(pgErr("XX000", "boom", ""))
		e.Log(context.Background())
	})
	if len(lines) != 1 || lines[0]["msg"] != "request failed" {
		t.Fatalf("expected one \"request failed\" line, got %#v", lines)
	}
	if lines[0]["pg_error"] == nil || lines[0]["code"] != string(errcode.Internal) {
		t.Errorf("expected code and pg_error on the line, got %#v", lines[0])
	}
}

func TestWritePlain(t *testing.T) {
	rec := httptest.NewRecorder()
	WritePlain(rec, http.StatusTeapot, "SOME_CODE", "short and stout")
	if rec.Code != http.StatusTeapot || rec.Body.String() != "short and stout" {
		t.Errorf("got %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get(errcode.Header) != "SOME_CODE" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("headers: %v", rec.Header())
	}
}

func TestWriteServer_LogsInFullAndShowsTheFixedMessage(t *testing.T) {
	underlying := errors.New("boom: acquiring connection failed")
	var rec *httptest.ResponseRecorder
	lines := captureLogs(t, func() {
		rec = httptest.NewRecorder()
		WriteServer(context.Background(), rec, http.StatusInternalServerError, errcode.DBUnavailable, "acquiring connection", underlying)
	})
	if rec.Code != 500 || rec.Body.String() != "acquiring connection" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if len(lines) != 1 || lines[0]["error"] != underlying.Error() || lines[0]["code"] != string(errcode.DBUnavailable) {
		t.Fatalf("expected the full underlying error logged, got %#v", lines)
	}
}

func TestWritePostgres(t *testing.T) {
	t.Run("not a Postgres error is a logged, redacted 500", func(t *testing.T) {
		underlying := errors.New("some opaque failure")
		var rec *httptest.ResponseRecorder
		lines := captureLogs(t, func() {
			rec = httptest.NewRecorder()
			WritePostgres(context.Background(), rec, underlying, false)
		})
		if rec.Code != 500 || rec.Body.String() != "internal error" {
			t.Fatalf("got %d %q", rec.Code, rec.Body.String())
		}
		if len(lines) != 1 || lines[0]["error"] != underlying.Error() {
			t.Fatalf("expected the full error logged, got %#v", lines)
		}
	})
	t.Run("a 4xx Postgres error is tiered and not logged", func(t *testing.T) {
		var rec *httptest.ResponseRecorder
		lines := captureLogs(t, func() {
			rec = httptest.NewRecorder()
			WritePostgres(context.Background(), rec, pgErr("23505", "duplicate key", "Key (a)=(1)."), false)
		})
		if rec.Code != 409 || rec.Body.String() != "duplicate key: Key (a)=(1)." || len(lines) != 0 {
			t.Fatalf("got %d %q, logs %#v", rec.Code, rec.Body.String(), lines)
		}
	})
	t.Run("a 5xx Postgres error is logged with pg_error and redacted", func(t *testing.T) {
		var rec *httptest.ResponseRecorder
		lines := captureLogs(t, func() {
			rec = httptest.NewRecorder()
			WritePostgres(context.Background(), rec, pgErr("XX000", "internal detail", ""), false)
		})
		if rec.Code != 500 || rec.Body.String() != "internal error" {
			t.Fatalf("got %d %q", rec.Code, rec.Body.String())
		}
		if len(lines) != 1 || lines[0]["pg_error"] == nil {
			t.Fatalf("expected a logged pg_error, got %#v", lines)
		}
	})
}
