// End-to-end coverage for specs/complex-query.md's ComplexQuery envelope, through Prepare and
// Execute against a real Postgres.
package relexec

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rel-server/rel/errcode"
)

// TestComplexQuery_NoFlags_BareShape proves a ComplexQuery with no flags at
// all keeps today's bare-array response (specs/complex-query.md ## Response
// shape's back-compat guarantee).
func TestComplexQuery_NoFlags_BareShape(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Complex No Flags Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	opts := Options{}
	out, err := run(t, `{
		"query": {"relation": "director", "schema": "public", "select": ["*~"], "where": ["=", ["col", "name"], "Complex No Flags Director"]}
	}`, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rows := decodeJSON[[]map[string]any](t, []byte(out))
	if len(rows) != 1 || rows[0]["name"] != "Complex No Flags Director" {
		t.Fatalf("expected bare array with 1 row, got %s", out)
	}
}

// TestComplexQuery_Count proves a granted `count` produces the envelope
// with count/offset/limit alongside the paginated result.
func TestComplexQuery_Count(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Complex Count A'), ('Complex Count B'), ('Complex Count C')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	opts := Options{Policy: Policy{Count: true}}

	out, err := run(t, `{
		"query": {"relation": "director", "schema": "public", "select": ["*~"], "where": ["like", ["col", "name"], "Complex Count%"], "limit": 1},
		"count": true
	}`, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var env struct {
		Result []map[string]any `json:"result"`
		Count  *int             `json:"count"`
		Offset *int             `json:"offset"`
		Limit  *int             `json:"limit"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal: %v : %s", err, out)
	}
	if len(env.Result) != 1 {
		t.Fatalf("expected 1 paginated row, got %d", len(env.Result))
	}
	if env.Count == nil || *env.Count != 3 {
		t.Fatalf("expected count=3, got %v", env.Count)
	}
	if env.Offset == nil || *env.Offset != 0 {
		t.Fatalf("expected offset=0, got %v", env.Offset)
	}
	if env.Limit == nil || *env.Limit != 1 {
		t.Fatalf("expected limit=1, got %v", env.Limit)
	}
}

// TestComplexQuery_Count_UngrantedDegradesSilently proves an ungranted
// `count` omits the count/offset/limit keys without erroring, while result
// still returns (## Availability).
func TestComplexQuery_Count_UngrantedDegradesSilently(t *testing.T) {
	opts := Options{} // AllowCount defaults false
	out, err := run(t, `{
		"query": {"relation": "director", "schema": "public", "select": ["*~"], "limit": 1},
		"count": true
	}`, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal: %v : %s", err, out)
	}
	if _, ok := env["count"]; ok {
		t.Errorf("expected no \"count\" key when ungranted, got %s", out)
	}
	if _, ok := env["result"]; !ok {
		t.Errorf("expected \"result\" still present, got %s", out)
	}
}

// TestComplexQuery_Count_OnWrite_Rejected proves count+data is QUERY_COUNT_IS_READ_ONLY.
func TestComplexQuery_Count_OnWrite_Rejected(t *testing.T) {
	opts := Options{Policy: Policy{Count: true}}
	_, err := run(t, `{
		"query": {"relation": "director", "schema": "public", "select": ["*~"]},
		"data": [{"name": "Should Not Insert"}],
		"count": true
	}`, opts)
	assertClientFault(t, err, errcode.QueryCountIsReadOnly)
}

// TestComplexQuery_Stats_OnRead_Rejected proves stats without data is QUERY_STATS_IS_WRITE_ONLY.
func TestComplexQuery_Stats_OnRead_Rejected(t *testing.T) {
	opts := Options{Policy: Policy{Stats: true}}
	_, err := run(t, `{
		"query": {"relation": "director", "schema": "public", "select": ["*~"]},
		"stats": true
	}`, opts)
	assertClientFault(t, err, errcode.QueryStatsIsWriteOnly)
}

// TestComplexQuery_Stats_OnWrite proves a granted `stats` reports the
// write's own insert count.
func TestComplexQuery_Stats_OnWrite(t *testing.T) {
	opts := Options{Policy: Policy{Stats: true}}
	out, err := run(t, `{
		"query": {"relation": "director", "schema": "public", "select": ["*~"]},
		"data": [{"name": "Complex Stats Director"}],
		"stats": true
	}`, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var env struct {
		Result []map[string]any `json:"result"`
		Stats  []struct {
			Path      []string `json:"path"`
			Table     string   `json:"table"`
			Submitted int      `json:"submitted"`
			Inserted  int      `json:"inserted"`
		} `json:"stats"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal: %v : %s", err, out)
	}
	if len(env.Result) != 1 {
		t.Fatalf("expected 1 result row, got %d", len(env.Result))
	}
	if len(env.Stats) != 1 {
		t.Fatalf("expected 1 Stat, got %d", len(env.Stats))
	}
	s := env.Stats[0]
	if s.Table != "public.director" || s.Submitted != 1 || s.Inserted != 1 {
		t.Errorf("expected table=public.director submitted=1 inserted=1, got %#v", s)
	}
	if len(s.Path) != 0 {
		t.Errorf("expected root path [], got %#v", s.Path)
	}
}

// TestComplexQuery_Stats_OnNestedWrite proves `stats` reports one entry per
// writable relation touched, each with its own join-key `path` — mirroring
// specs/complex-query.md ## stats's own nested example (there, a two-level
// hotel.properties/room_types upsert) — TestComplexQuery_Stats_OnWrite only
// ever covers a single flat table, never a join.
func TestComplexQuery_Stats_OnNestedWrite(t *testing.T) {
	opts := Options{Policy: Policy{Stats: true}}
	out, err := run(t, `{
		"query": {
			"relation": "director", "schema": "public",
			"select": {"id": ["col", "id"], "name": ["col", "name"], "movies": [".", "movies"]},
			"write_mode": "insert",
			"join": {"movies": {
				"relation": "movie", "schema": "public", "on": {"director_id": "id"},
				"write_mode": "insert", "select": ["*~"]
			}}
		},
		"data": [{"name": "Nested Stats Director", "movies": [{"title": "Nested Stats Movie"}]}],
		"stats": true
	}`, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var env struct {
		Stats []struct {
			Path      []string `json:"path"`
			Table     string   `json:"table"`
			Submitted int      `json:"submitted"`
			Inserted  int      `json:"inserted"`
		} `json:"stats"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal: %v : %s", err, out)
	}
	if len(env.Stats) != 2 {
		t.Fatalf("expected 2 Stat entries (director + movies), got %d : %#v", len(env.Stats), env.Stats)
	}
	byTable := map[string]struct {
		Path      []string
		Submitted int
		Inserted  int
	}{}
	for _, s := range env.Stats {
		byTable[s.Table] = struct {
			Path      []string
			Submitted int
			Inserted  int
		}{s.Path, s.Submitted, s.Inserted}
	}
	director, ok := byTable["public.director"]
	if !ok || len(director.Path) != 0 || director.Submitted != 1 || director.Inserted != 1 {
		t.Errorf("expected public.director stat with path=[] submitted=1 inserted=1, got %#v", byTable["public.director"])
	}
	movie, ok := byTable["public.movie"]
	if !ok || len(movie.Path) != 1 || movie.Path[0] != "movies" || movie.Submitted != 1 || movie.Inserted != 1 {
		t.Errorf(`expected public.movie stat with path=["movies"] submitted=1 inserted=1, got %#v`, byTable["public.movie"])
	}
}

// TestComplexQuery_Write_ReturnsEmbeddedChild proves a write's read-back
// result actually embeds a joined child relation, not just that the SQL
// compiles (query.TestCompileSelectForDataNode_WithEmbeddedChild only
// checks compilation) — end to end, through the response.
func TestComplexQuery_Write_ReturnsEmbeddedChild(t *testing.T) {
	opts := Options{}
	out, err := run(t, `{
		"query": {
			"relation": "director", "schema": "public",
			"select": {"id": ["col", "id"], "name": ["col", "name"], "movies": [".", "movies"]},
			"write_mode": "insert",
			"join": {"movies": {
				"relation": "movie", "schema": "public", "on": {"director_id": "id"},
				"write_mode": "insert", "select": ["*~"]
			}}
		},
		"data": [{"name": "Returns Expand Director", "movies": [{"title": "Returns Expand Movie"}]}]
	}`, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rows := decodeJSON[[]map[string]any](t, []byte(out))
	if len(rows) != 1 {
		t.Fatalf("expected 1 result row, got %d : %s", len(rows), out)
	}
	if rows[0]["name"] != "Returns Expand Director" {
		t.Fatalf("expected the written director's own name in the result, got %#v", rows[0])
	}
	movies, ok := rows[0]["movies"].([]any)
	if !ok || len(movies) != 1 {
		t.Fatalf("expected result[0].movies to embed 1 written movie, got %#v", rows[0]["movies"])
	}
	movie, ok := movies[0].(map[string]any)
	if !ok || movie["title"] != "Returns Expand Movie" {
		t.Fatalf("expected the embedded movie's own title, got %#v", movies[0])
	}
}

// TestComplexQuery_StatsQueryPlanConflict proves the two are rejected
// together on a write.
func TestComplexQuery_StatsQueryPlanConflict(t *testing.T) {
	opts := Options{Policy: Policy{Stats: true, QueryPlan: true}}
	_, err := run(t, `{
		"query": {"relation": "director", "schema": "public", "select": ["*~"]},
		"data": [{"name": "Should Not Run"}],
		"stats": true,
		"query_plan": true
	}`, opts)
	assertClientFault(t, err, errcode.QueryStatsQueryPlanConflict)
}

// TestComplexQuery_UnsupportedReturns proves returns:"none" alone (no other
// flag) is QUERY_UNSUPPORTED_RETURNS.
func TestComplexQuery_UnsupportedReturns(t *testing.T) {
	opts := Options{}
	_, err := run(t, `{
		"query": {"relation": "director", "schema": "public", "select": ["*~"]},
		"returns": "none"
	}`, opts)
	assertClientFault(t, err, errcode.QueryUnsupportedReturns)
}

// TestComplexQuery_Returns_None_OnWrite_SkipsResult proves a write's
// returns:"none" omits "result" while still reporting stats.
func TestComplexQuery_Returns_None_OnWrite_SkipsResult(t *testing.T) {
	opts := Options{Policy: Policy{Stats: true}}
	out, err := run(t, `{
		"query": {"relation": "director", "schema": "public", "select": ["*~"]},
		"data": [{"name": "Complex Returns None Director"}],
		"returns": "none",
		"stats": true
	}`, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal: %v : %s", err, out)
	}
	if _, ok := env["result"]; ok {
		t.Errorf(`expected no "result" key, got %s`, out)
	}
	if _, ok := env["stats"]; !ok {
		t.Errorf(`expected "stats" present, got %s`, out)
	}

	var count int
	if err := testDb.Pool.QueryRow(context.Background(), `select count(*) from director where name = 'Complex Returns None Director'`).Scan(&count); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected the row actually inserted, got count=%d", count)
	}
}

// TestComplexQuery_Rollback_UndoesWrite proves rollback:true persists
// nothing, while still reporting what happened via stats.
func TestComplexQuery_Rollback_UndoesWrite(t *testing.T) {
	opts := Options{Policy: Policy{Rollback: true, Stats: true}}
	out, err := run(t, `{
		"query": {"relation": "director", "schema": "public", "select": ["*~"]},
		"data": [{"name": "Complex Rollback Director"}],
		"stats": true,
		"rollback": true
	}`, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var env struct {
		Result []map[string]any `json:"result"`
		Stats  []struct {
			Inserted int `json:"inserted"`
		} `json:"stats"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal: %v : %s", err, out)
	}
	if len(env.Result) != 1 {
		t.Fatalf("expected 1 result row reflecting what happened before rollback, got %d", len(env.Result))
	}
	if len(env.Stats) != 1 || env.Stats[0].Inserted != 1 {
		t.Fatalf("expected stats to still report inserted=1, got %#v", env.Stats)
	}

	var count int
	if err := testDb.Pool.QueryRow(context.Background(), `select count(*) from director where name = 'Complex Rollback Director'`).Scan(&count); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected the row NOT persisted after rollback, got count=%d", count)
	}
}

// TestComplexQuery_Rollback_UndoesNestedWrite proves rollback:true undoes
// EVERY level of a nested write, not just the root — the existing rollback
// test only ever writes a single flat table.
func TestComplexQuery_Rollback_UndoesNestedWrite(t *testing.T) {
	opts := Options{Policy: Policy{Rollback: true, Stats: true}}
	out, err := run(t, `{
		"query": {
			"relation": "director", "schema": "public",
			"select": {"id": ["col", "id"], "name": ["col", "name"], "movies": [".", "movies"]},
			"write_mode": "insert",
			"join": {"movies": {
				"relation": "movie", "schema": "public", "on": {"director_id": "id"},
				"write_mode": "insert", "select": ["*~"]
			}}
		},
		"data": [{"name": "Nested Rollback Director", "movies": [{"title": "Nested Rollback Movie"}]}],
		"stats": true,
		"rollback": true
	}`, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var env struct {
		Stats []struct {
			Table    string `json:"table"`
			Inserted int    `json:"inserted"`
		} `json:"stats"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal: %v : %s", err, out)
	}
	if len(env.Stats) != 2 {
		t.Fatalf("expected stats to still report both levels despite the rollback, got %#v", env.Stats)
	}
	for _, s := range env.Stats {
		if s.Inserted != 1 {
			t.Errorf("expected inserted=1 for %s, got %#v", s.Table, s)
		}
	}

	ctx := context.Background()
	var directorCount, movieCount int
	if err := testDb.Pool.QueryRow(ctx, `select count(*) from director where name = 'Nested Rollback Director'`).Scan(&directorCount); err != nil {
		t.Fatalf("select back director: %v", err)
	}
	if err := testDb.Pool.QueryRow(ctx, `select count(*) from movie where title = 'Nested Rollback Movie'`).Scan(&movieCount); err != nil {
		t.Fatalf("select back movie: %v", err)
	}
	if directorCount != 0 || movieCount != 0 {
		t.Fatalf("expected neither level persisted after rollback, got director=%d movie=%d", directorCount, movieCount)
	}
}

// TestComplexQuery_Rollback_NotGranted proves an ungranted rollback is a
// hard QUERY_ROLLBACK_NOT_GRANTED error, not a silent degrade.
func TestComplexQuery_Rollback_NotGranted(t *testing.T) {
	opts := Options{} // AllowRollback defaults false
	_, err := run(t, `{
		"query": {"relation": "director", "schema": "public", "select": ["*~"]},
		"data": [{"name": "Should Not Run Either"}],
		"rollback": true
	}`, opts)
	assertClientFault(t, err, errcode.QueryRollbackNotGranted)

	var count int
	if err := testDb.Pool.QueryRow(context.Background(), `select count(*) from director where name = 'Should Not Run Either'`).Scan(&count); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected nothing written when rollback is rejected up front, got count=%d", count)
	}
}

// TestComplexQuery_Sql_And_QueryPlan_OnRead proves both collect a single
// path:[] entry with "select" set, without altering the actual result.
func TestComplexQuery_Sql_And_QueryPlan_OnRead(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Complex Sql Plan Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	opts := Options{Policy: Policy{SQL: true, QueryPlan: true}}
	out, err := run(t, `{
		"query": {"relation": "director", "schema": "public", "select": ["*~"], "where": ["=", ["col", "name"], "Complex Sql Plan Director"]},
		"sql": true,
		"query_plan": true
	}`, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var env struct {
		Result []map[string]any `json:"result"`
		Sql    []struct {
			Path   []string `json:"path"`
			Select string   `json:"select"`
		} `json:"sql"`
		QueryPlan []struct {
			Path   []string        `json:"path"`
			Select json.RawMessage `json:"select"`
		} `json:"query_plan"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal: %v : %s", err, out)
	}
	if len(env.Result) != 1 {
		t.Fatalf("expected 1 result row, got %d", len(env.Result))
	}
	if len(env.Sql) != 1 || env.Sql[0].Select == "" || len(env.Sql[0].Path) != 0 {
		t.Fatalf("expected 1 sql entry with path:[] and select set, got %#v", env.Sql)
	}
	if len(env.QueryPlan) != 1 || len(env.QueryPlan[0].Select) == 0 || len(env.QueryPlan[0].Path) != 0 {
		t.Fatalf("expected 1 query_plan entry with path:[] and select set, got %#v", env.QueryPlan)
	}
}

// TestComplexQuery_Sql_OnWrite_IncludesReadback proves a write's sql
// collects both the DML statement and the read-back SELECT.
func TestComplexQuery_Sql_OnWrite_IncludesReadback(t *testing.T) {
	opts := Options{Policy: Policy{SQL: true}}
	out, err := run(t, `{
		"query": {"relation": "director", "schema": "public", "select": ["*~"]},
		"data": [{"name": "Complex Sql Write Director"}],
		"sql": true
	}`, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var env struct {
		Sql []struct {
			Path   []string `json:"path"`
			Insert string   `json:"insert"`
			Select string   `json:"select"`
		} `json:"sql"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal: %v : %s", err, out)
	}
	if len(env.Sql) != 2 {
		t.Fatalf("expected 2 sql entries (the insert node, plus the read-back), got %d : %#v", len(env.Sql), env.Sql)
	}
	if env.Sql[0].Insert == "" {
		t.Errorf("expected the first entry to carry the insert statement, got %#v", env.Sql[0])
	}
	if env.Sql[1].Select == "" {
		t.Errorf("expected the second entry to carry the read-back select, got %#v", env.Sql[1])
	}
}
