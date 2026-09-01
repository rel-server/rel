package server

import (
	"net/http"
	"testing"

	"github.com/ceymard/rel/errcode"
)

// TestRelHandler_QueryCompileErrors_AttachSpecificCodes proves
// specs/error-handling.md ## Rel-internal codes' query-compile-error family
// (UNKNOWN_IDENTIFIER, JOIN_MISSING_INDEX, WRITE_FORBIDDEN,
// WRITE_FORBIDDEN_FUNCTION_ROOT, QUERY_INVALID_EXPRESSION) actually reaches
// the response now, instead of every resolve/derive-shapes failure falling
// back to the generic errcode.Unclassified specs/TODO.md used to flag as a
// real gap — one representative failure per code, each chosen to exercise a
// different query package call site.
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

// TestRelHandler_WriteForbiddenFunctionRoot_HasItsOwnCode proves the
// function-rooted-node write rejection (specs/query-engine.md ## Reading
// Algorithm ### Function-rooted nodes' "unconditionally UNWRITABLE" rule)
// gets its own WRITE_FORBIDDEN_FUNCTION_ROOT code, distinct from the
// generic WRITE_FORBIDDEN a non-function node's own unwritable identity
// gets — this failure surfaces from ExecuteWriteState (query/write.go), at
// write-execution time, not the earlier resolve loop, so it's routed
// through classifyWriteError rather than the resolve-loop's own
// codeOrUnclassified call.
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
