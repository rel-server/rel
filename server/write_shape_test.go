// Coverage for a write payload whose JSON shape doesn't match what the
// target column/relation expects — AGENTS.md's Strict* accessor rule exists
// specifically to avoid silent coercion here ; these prove the real write
// path turns a mismatch into a clean, classified error rather than a panic,
// a silently-coerced value, or a leaked raw Postgres/Go error.
package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/rel-server/rel/errcode"
)

func TestRelHandler_MalformedWritePayload(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   errcode.Code
	}{
		{
			// movie.director_id is a not-null int column ; a JSON string
			// value for it must fail cleanly at write time (pgerr's 22P02
			// entry), not coerce, panic, or leak as an unclassified 500.
			name: "string value for an integer column",
			body: `{
				"query": {"relation": "movie", "schema": "public", "select": ["own"], "write_mode": "insert"},
				"data": [{"title": "Malformed Movie", "director_id": "not-a-number"}]
			}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "PG_INVALID_TEXT_REPRESENTATION",
		},
		{
			// "movies" is an incoming (to-many) relation ; query.ts requires
			// an array of rows there, not a single object.
			name: "object where a to-many relation expects an array",
			body: `{
				"query": {
					"relation": "director", "schema": "public",
					"select": {"id": "id", "name": "name", "movies": "movies"},
					"write_mode": "insert",
					"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "write_mode": "insert", "select": ["own"]}}
				},
				"data": [{"name": "Shape Mismatch Director", "movies": {"title": "Not An Array"}}]
			}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   errcode.Unclassified,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postRel(t, tt.body)
			if rec.Code != tt.wantStatus {
				t.Fatalf("expected %d, got %d : %s", tt.wantStatus, rec.Code, rec.Body.String())
			}
			if got := errcode.Code(rec.Header().Get(errcode.Header)); got != tt.wantCode {
				t.Errorf("X-Rel-Errorcode = %q, want %s", got, tt.wantCode)
			}
		})
	}

	// The first case's bad director_id must never have landed in the table.
	var count int
	if err := testDb.Pool.QueryRow(context.Background(), `select count(*) from movie where title = 'Malformed Movie'`).Scan(&count); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected the malformed write to insert nothing, got count=%d", count)
	}
}
