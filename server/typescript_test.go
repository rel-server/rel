package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/rel-server/rel/tsgen"
)

func TestTypeScriptHandler_GET_ReturnsGeneratedDatabaseTS(t *testing.T) {
	handler := NewTypeScriptHandler(testDb, testCfg, nil)

	req := httptest.NewRequest(http.MethodGet, "/rel/database.ts", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"export interface Relations {", "export class Querier"} {
		if !strings.Contains(body, want) {
			t.Errorf("response missing %q", want)
		}
	}
}

func TestTypeScriptHandler_CodecParam(t *testing.T) {
	get := func(cfgCodec, query string) (int, string) {
		cfg := *testCfg
		cfg.TypeScript.Codec = cfgCodec
		rec := httptest.NewRecorder()
		NewTypeScriptHandler(testDb, &cfg, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/rel/database.ts"+query, nil))
		return rec.Code, rec.Body.String()
	}

	if code, body := get("", ""); code != http.StatusOK || !strings.Contains(body, "// Codec: json\n") || strings.Contains(body, "\nimport ") {
		t.Errorf("default: %d, want plain json ; got:\n%.200s", code, body)
	}
	if code, body := get("", "?codec=cbor,temporal"); code != http.StatusOK || !strings.Contains(body, "// Codec: cbor,temporal,bigint\n") || !strings.Contains(body, `from "cbor-x"`) {
		t.Errorf("?codec=cbor,temporal: %d ; got:\n%.200s", code, body)
	}
	// the configured value is the default, and the param replaces it rather than adding to it
	if _, body := get("cbor", ""); !strings.Contains(body, "// Codec: cbor,date,bigint\n") {
		t.Errorf("configured cbor: got:\n%.200s", body)
	}
	if _, body := get("cbor", "?codec=json"); !strings.Contains(body, "// Codec: json\n") {
		t.Errorf("?codec=json over configured cbor: got:\n%.200s", body)
	}
	for _, bad := range []string{"?codec=msgpack", "?codec=json,cbor", "?codec=date,temporal"} {
		if code, _ := get("", bad); code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", bad, code)
		}
	}
}

func TestTypeScriptHandler_PostNotAllowed(t *testing.T) {
	handler := NewTypeScriptHandler(testDb, testCfg, nil)

	req := httptest.NewRequest(http.MethodPost, "/rel/database.ts", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestTypeScriptHandler_SchemasParamOutsideWhitelistIsRejected(t *testing.T) {
	cfg := *testCfg
	cfg.TypeScript.Schemas = "public"
	handler := NewTypeScriptHandler(testDb, &cfg, nil)

	req := httptest.NewRequest(http.MethodGet, "/rel/database.ts?schemas=nope", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
}

func TestDatabaseJSONHandler_GET_ReturnsGeneratedDatabaseJSON(t *testing.T) {
	handler := NewDatabaseJSONHandler(testDb, testCfg, nil)

	req := httptest.NewRequest(http.MethodGet, "/rel/database.json", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("expected application/json content type, got %q", ct)
	}
	var doc tsgen.DatabaseJSON
	if err := sonic.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("response is not valid database.json : %v\n%s", err, rec.Body.String())
	}
	if doc.Version != 1 {
		t.Errorf("expected version 1, got %d", doc.Version)
	}
	if len(doc.Relations) == 0 {
		t.Errorf("expected at least one relation in the generated database.json")
	}
}

func TestDatabaseJSONHandler_PostNotAllowed(t *testing.T) {
	handler := NewDatabaseJSONHandler(testDb, testCfg, nil)

	req := httptest.NewRequest(http.MethodPost, "/rel/database.json", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestDatabaseJSONHandler_SchemasParamOutsideWhitelistIsRejected(t *testing.T) {
	cfg := *testCfg
	cfg.TypeScript.Schemas = "public"
	handler := NewDatabaseJSONHandler(testDb, &cfg, nil)

	req := httptest.NewRequest(http.MethodGet, "/rel/database.json?schemas=nope", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
}
