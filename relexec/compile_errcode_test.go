package relexec

import (
	"testing"

	"github.com/rel-server/rel/errcode"
)

// TestPrepare_QueryCompileErrors_AttachSpecificCodes : ## Rel-internal
// codes' query-compile-error family reaches the error, one per code.
func TestPrepare_QueryCompileErrors_AttachSpecificCodes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want errcode.Code
	}{
		{
			name: "unknown relation",
			body: `{"relation": "does_not_exist_xyz", "schema": "public", "select": ["*~"]}`,
			want: errcode.UnknownIdentifier,
		},
		{
			name: "unknown column",
			body: `{"relation": "director", "schema": "public", "select": {"x": ["col", "not_a_real_column"]}}`,
			want: errcode.UnknownIdentifier,
		},
		{
			name: "join not indexed",
			body: `{
				"relation": "unindexed_parent", "schema": "public", "select": ["*~"],
				"join": {"children": {"relation": "unindexed_child", "schema": "public", "on": {"parent_id": "id"}}}
			}`,
			want: errcode.JoinMissingIndex,
		},
		{
			name: "unknown write_mode",
			body: `{"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "not_a_real_mode"}`,
			want: errcode.WriteForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := run(t, tt.body, Options{})
			assertClientFault(t, err, tt.want)
		})
	}
}

// TestPrepare_WriteForbiddenFunctionRoot_HasItsOwnCode : ### Function-
// rooted nodes' unwritable rule gets its own code, not generic WRITE_FORBIDDEN.
func TestPrepare_WriteForbiddenFunctionRoot_HasItsOwnCode(t *testing.T) {
	body := `{
		"query": {"function": "fn_directors", "schema": "public", "select": ["*~"]},
		"data": [{"id": 1, "name": "Someone"}]
	}`
	_, err := run(t, body, Options{})
	assertClientFault(t, err, errcode.WriteForbiddenFunctionRoot)
}
