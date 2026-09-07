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
	cfg.Http.TypeScript.Schemas = "public"
	handler := NewTypeScriptHandler(testDb, &cfg, nil)

	req := httptest.NewRequest(http.MethodGet, "/rel/database.ts?schemas=nope", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
}

func TestResolveTypeScriptSchemas(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		param      string
		want       []string
		wantErr    bool
	}{
		{"no whitelist, no param", "", "", nil, false},
		{"no whitelist, param passes through", "", "a,b", []string{"a", "b"}, false},
		{"whitelist, no param", "a,b", "", []string{"a", "b"}, false},
		{"whitelist, param narrows", "a,b", "a", []string{"a"}, false},
		{"whitelist, param outside is rejected", "a,b", "c", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveTypeScriptSchemas(c.configured, c.param)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if err == nil && !equalStrings(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
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
	cfg.Http.TypeScript.Schemas = "public"
	handler := NewDatabaseJSONHandler(testDb, &cfg, nil)

	req := httptest.NewRequest(http.MethodGet, "/rel/database.json?schemas=nope", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
