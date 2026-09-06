package server

import (
	"net/http"
	"testing"

	"github.com/rel-server/rel/errcode"
)

// TestRelHandler_QueryCompileErrors_AttachSpecificCodes : ## Rel-internal
// codes' query-compile-error family reaches the response, one per code.
func TestRelHandler_QueryCompileErrors_AttachSpecificCodes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want errcode.Code
	}{
		{
			name: "unknown relation",
			body: `{"relation": "does_not_exist_xyz", "schema": "public", "select": ["own"]}`,
			want: errcode.UnknownIdentifier,
		},
		{
			name: "unknown column",
			body: `{"relation": "director", "schema": "public", "select": {"x": "not_a_real_column"}}`,
			want: errcode.UnknownIdentifier,
		},
		{
			name: "join not indexed",
			body: `{
				"relation": "unindexed_parent", "schema": "public", "select": ["own"],
				"join": {"children": {"relation": "unindexed_child", "schema": "public", "on": {"parent_id": "id"}}}
			}`,
			want: errcode.JoinMissingIndex,
		},
		{
			name: "unknown write_mode",
			body: `{"relation": "director", "schema": "public", "select": ["own"], "write_mode": "not_a_real_mode"}`,
			want: errcode.WriteForbidden,
		},
		{
			name: "ambiguous function call",
			body: `{"relation": "director", "schema": "public", "select": {"x": ["call", "fn_ambig", 1]}}`,
			want: errcode.QueryInvalidExpression,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postRel(t, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get(errcode.Header); got != string(tt.want) {
				t.Errorf("X-Rel-Errorcode = %q, want %s", got, tt.want)
			}
		})
	}
}

// TestRelHandler_WriteForbiddenFunctionRoot_HasItsOwnCode : ### Function-
// rooted nodes' unwritable rule gets its own code, not generic WRITE_FORBIDDEN.
func TestRelHandler_WriteForbiddenFunctionRoot_HasItsOwnCode(t *testing.T) {
	body := `{
		"query": {"function": "fn_directors", "schema": "public", "select": ["own"]},
		"data": [{"id": 1, "name": "Someone"}]
	}`
	rec := postRel(t, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(errcode.Header); got != string(errcode.WriteForbiddenFunctionRoot) {
		t.Errorf("X-Rel-Errorcode = %q, want %s", got, errcode.WriteForbiddenFunctionRoot)
	}
}
