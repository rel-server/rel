// GET /rel : specs/query-json.md end to end against a real Postgres,
// reusing testDb/testHandler from rel_test.go's TestMain (same shared
// container/schema — pg/testdata/schema.sql's director/movie fixture,
// director.id <-(idx_movie_director)- movie.director_id).
package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func getRel(t *testing.T, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/rel?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	return rec
}

func TestRelHandler_GET_ReadWithJoinAndWhere(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('GET Test Director')`); err != nil {
		t.Fatalf("insert director: %v", err)
	}
	var directorID int
	if err := testDb.Pool.QueryRow(ctx, `select id from director where name = 'GET Test Director'`).Scan(&directorID); err != nil {
		t.Fatalf("select director id: %v", err)
	}
	if _, err := testDb.Pool.Exec(ctx, `insert into movie (director_id, title) values ($1, 'GET Test Movie')`, directorID); err != nil {
		t.Fatalf("insert movie: %v", err)
	}

	rawQuery := url.Values{
		"relation":                   {"director"},
		"schema":                     {"public"},
		"select":                     {"name,movies"},
		"where":                      {"eq(name,'GET Test Director')"},
		"join.movies.relation":       {"movie"},
		"join.movies.schema":         {"public"},
		"join.movies.on.director_id": {"id"},
		"join.movies.select":         {"title"},
		"join.movies.where":          {"like(title,'GET Test%')"},
	}.Encode()

	rec := getRel(t, rawQuery)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes())
	if len(rows) != 1 || rows[0]["name"] != "GET Test Director" {
		t.Fatalf("expected 1 row named 'GET Test Director', got %v", rows)
	}
	movies, ok := rows[0]["movies"].([]any)
	if !ok || len(movies) != 1 {
		t.Fatalf("expected 1 joined movie, got %v", rows[0]["movies"])
	}
	movie, ok := movies[0].(map[string]any)
	if !ok || movie["title"] != "GET Test Movie" {
		t.Fatalf("expected joined movie title 'GET Test Movie', got %v", movies[0])
	}
}

// TestRelHandler_GET_OwnExceptAndWithLiteralSemicolon : net/url.ParseQuery
// rejects a raw ';' outright, which is why this package hand-rolls splitting.
func TestRelHandler_GET_OwnExceptAndWithLiteralSemicolon(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Semicolon Director')`); err != nil {
		t.Fatalf("insert director: %v", err)
	}

	rec := getRel(t, "relation=director&schema=public"+
		"&where=eq(name,'Semicolon%20Director')"+
		"&select=own_except_and(id;%20label:name)")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes())
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %v", rows)
	}
	if _, hasID := rows[0]["id"]; hasID {
		t.Errorf("expected \"id\" to be excluded by own_except_and, got %v", rows[0])
	}
	if rows[0]["name"] != "Semicolon Director" || rows[0]["label"] != "Semicolon Director" {
		t.Errorf("expected own's \"name\" plus the and-map's computed \"label\", got %v", rows[0])
	}
}

func TestRelHandler_GET_RejectsWriteModeAtRoot(t *testing.T) {
	rec := getRel(t, "relation=director&schema=public&write_mode=merge")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for write_mode on GET /rel, got %d : %s", rec.Code, rec.Body.String())
	}
}

func TestRelHandler_GET_RejectsWriteModeNestedInJoin(t *testing.T) {
	rawQuery := url.Values{
		"relation":                   {"director"},
		"schema":                     {"public"},
		"join.movies.relation":       {"movie"},
		"join.movies.on.director_id": {"id"},
		"join.movies.write_mode":     {"merge"},
	}.Encode()
	rec := getRel(t, rawQuery)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for join.movies.write_mode on GET /rel, got %d : %s", rec.Code, rec.Body.String())
	}
}

func TestRelHandler_GET_RejectsOnConflict(t *testing.T) {
	rec := getRel(t, "relation=director&schema=public&on_conflict=id")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for on_conflict on GET /rel, got %d : %s", rec.Code, rec.Body.String())
	}
}

func TestRelHandler_GET_ParseErrorIsBadRequest(t *testing.T) {
	rec := getRel(t, "relation=director&where=gte(year")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a malformed filter expression, got %d : %s", rec.Code, rec.Body.String())
	}
}

func TestRelHandler_GET_UnknownRelationIsBadRequest(t *testing.T) {
	rec := getRel(t, "relation=does_not_exist_at_all&schema=public")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unresolvable relation, got %d : %s", rec.Code, rec.Body.String())
	}
}
