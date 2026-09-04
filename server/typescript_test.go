package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
