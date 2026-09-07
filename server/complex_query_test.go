// End-to-end coverage for specs/complex-query.md's ComplexQuery envelope,
// against the real HTTP handler (httptest, no listener) and a real Postgres
// (this package's own TestMain/testDb).
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
)

// complexHandler builds a NewRelHandler off a clone of testCfg with the
// given Allow* flags — never mutates the shared testCfg/testHandler other
// tests in this package rely on.
func complexHandler(t *testing.T, configure func(*config.Config)) http.Handler {
	t.Helper()
	cfg := *testCfg
	if configure != nil {
		configure(&cfg)
	}
	return NewRelHandler(testDb, &cfg, nil)
}

func errCode(t *testing.T, body []byte) errcode.Code {
	t.Helper()
	var v struct {
		Code errcode.Code `json:"code"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("unmarshal error response %s: %v", body, err)
	}
	return v.Code
}

// TestComplexQuery_NoFlags_BareShape proves a ComplexQuery with no flags at
// all keeps today's bare-array response (specs/complex-query.md ## Response
// shape's back-compat guarantee).
func TestComplexQuery_NoFlags_BareShape(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Complex No Flags Director')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	handler := complexHandler(t, nil)
	rec := postRelTo(t, handler, `{
		"query": {"relation": "director", "schema": "public", "select": ["own"], "where": ["=", "name", ["Complex No Flags Director"]]}
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes())
	if len(rows) != 1 || rows[0]["name"] != "Complex No Flags Director" {
		t.Fatalf("expected bare array with 1 row, got %s", rec.Body.String())
	}
}

// TestComplexQuery_Count proves a granted `count` produces the envelope
// with count/offset/limit alongside the paginated result.
func TestComplexQuery_Count(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Complex Count A'), ('Complex Count B'), ('Complex Count C')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	handler := complexHandler(t, func(c *config.Config) { c.Pg.Query.AllowCount = true })

	rec := postRelTo(t, handler, `{
		"query": {"relation": "director", "schema": "public", "select": ["own"], "where": ["like", "name", ["Complex Count%"]], "limit": 1},
		"count": true
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Result []map[string]any `json:"result"`
		Count  *int             `json:"count"`
		Offset *int             `json:"offset"`
		Limit  *int             `json:"limit"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v : %s", err, rec.Body.String())
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
	handler := complexHandler(t, nil) // AllowCount defaults false
	rec := postRelTo(t, handler, `{
		"query": {"relation": "director", "schema": "public", "select": ["own"], "limit": 1},
		"count": true
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v : %s", err, rec.Body.String())
	}
	if _, ok := env["count"]; ok {
		t.Errorf("expected no \"count\" key when ungranted, got %s", rec.Body.String())
	}
	if _, ok := env["result"]; !ok {
		t.Errorf("expected \"result\" still present, got %s", rec.Body.String())
	}
}

// TestComplexQuery_Count_OnWrite_Rejected proves count+data is QUERY_COUNT_IS_READ_ONLY.
func TestComplexQuery_Count_OnWrite_Rejected(t *testing.T) {
	handler := complexHandler(t, func(c *config.Config) { c.Pg.Query.AllowCount = true })
	rec := postRelTo(t, handler, `{
		"query": {"relation": "director", "schema": "public", "select": ["own"]},
		"data": [{"name": "Should Not Insert"}],
		"count": true
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
	if code := errCode(t, rec.Body.Bytes()); code != errcode.QueryCountIsReadOnly {
		t.Errorf("expected %s, got %s", errcode.QueryCountIsReadOnly, code)
	}
}

// TestComplexQuery_Stats_OnRead_Rejected proves stats without data is QUERY_STATS_IS_WRITE_ONLY.
func TestComplexQuery_Stats_OnRead_Rejected(t *testing.T) {
	handler := complexHandler(t, func(c *config.Config) { c.Pg.Query.AllowStats = true })
	rec := postRelTo(t, handler, `{
		"query": {"relation": "director", "schema": "public", "select": ["own"]},
		"stats": true
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
	if code := errCode(t, rec.Body.Bytes()); code != errcode.QueryStatsIsWriteOnly {
		t.Errorf("expected %s, got %s", errcode.QueryStatsIsWriteOnly, code)
	}
}

// TestComplexQuery_Stats_OnWrite proves a granted `stats` reports the
// write's own insert count.
func TestComplexQuery_Stats_OnWrite(t *testing.T) {
	handler := complexHandler(t, func(c *config.Config) { c.Pg.Query.AllowStats = true })
	rec := postRelTo(t, handler, `{
		"query": {"relation": "director", "schema": "public", "select": ["own"]},
		"data": [{"name": "Complex Stats Director"}],
		"stats": true
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
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
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v : %s", err, rec.Body.String())
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

// TestComplexQuery_StatsQueryPlanConflict proves the two are rejected
// together on a write.
func TestComplexQuery_StatsQueryPlanConflict(t *testing.T) {
	handler := complexHandler(t, func(c *config.Config) { c.Pg.Query.AllowStats, c.Pg.Query.AllowQueryPlan = true, true })
	rec := postRelTo(t, handler, `{
		"query": {"relation": "director", "schema": "public", "select": ["own"]},
		"data": [{"name": "Should Not Run"}],
		"stats": true,
		"query_plan": true
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
	if code := errCode(t, rec.Body.Bytes()); code != errcode.QueryStatsQueryPlanConflict {
		t.Errorf("expected %s, got %s", errcode.QueryStatsQueryPlanConflict, code)
	}
}

// TestComplexQuery_UnsupportedReturns proves returns:"none" alone (no other
// flag) is QUERY_UNSUPPORTED_RETURNS.
func TestComplexQuery_UnsupportedReturns(t *testing.T) {
	handler := complexHandler(t, nil)
	rec := postRelTo(t, handler, `{
		"query": {"relation": "director", "schema": "public", "select": ["own"]},
		"returns": "none"
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
	if code := errCode(t, rec.Body.Bytes()); code != errcode.QueryUnsupportedReturns {
		t.Errorf("expected %s, got %s", errcode.QueryUnsupportedReturns, code)
	}
}

// TestComplexQuery_Returns_None_OnWrite_SkipsResult proves a write's
// returns:"none" omits "result" while still reporting stats.
func TestComplexQuery_Returns_None_OnWrite_SkipsResult(t *testing.T) {
	handler := complexHandler(t, func(c *config.Config) { c.Pg.Query.AllowStats = true })
	rec := postRelTo(t, handler, `{
		"query": {"relation": "director", "schema": "public", "select": ["own"]},
		"data": [{"name": "Complex Returns None Director"}],
		"returns": "none",
		"stats": true
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v : %s", err, rec.Body.String())
	}
	if _, ok := env["result"]; ok {
		t.Errorf(`expected no "result" key, got %s`, rec.Body.String())
	}
	if _, ok := env["stats"]; !ok {
		t.Errorf(`expected "stats" present, got %s`, rec.Body.String())
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
	handler := complexHandler(t, func(c *config.Config) { c.Pg.Query.AllowRollback, c.Pg.Query.AllowStats = true, true })
	rec := postRelTo(t, handler, `{
		"query": {"relation": "director", "schema": "public", "select": ["own"]},
		"data": [{"name": "Complex Rollback Director"}],
		"stats": true,
		"rollback": true
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Result []map[string]any `json:"result"`
		Stats  []struct {
			Inserted int `json:"inserted"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v : %s", err, rec.Body.String())
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

// TestComplexQuery_Rollback_NotGranted proves an ungranted rollback is a
// hard QUERY_ROLLBACK_NOT_GRANTED error, not a silent degrade.
func TestComplexQuery_Rollback_NotGranted(t *testing.T) {
	handler := complexHandler(t, nil) // AllowRollback defaults false
	rec := postRelTo(t, handler, `{
		"query": {"relation": "director", "schema": "public", "select": ["own"]},
		"data": [{"name": "Should Not Run Either"}],
		"rollback": true
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
	if code := errCode(t, rec.Body.Bytes()); code != errcode.QueryRollbackNotGranted {
		t.Errorf("expected %s, got %s", errcode.QueryRollbackNotGranted, code)
	}

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
	handler := complexHandler(t, func(c *config.Config) { c.Pg.Query.AllowSql, c.Pg.Query.AllowQueryPlan = true, true })
	rec := postRelTo(t, handler, `{
		"query": {"relation": "director", "schema": "public", "select": ["own"], "where": ["=", "name", ["Complex Sql Plan Director"]]},
		"sql": true,
		"query_plan": true
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
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
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v : %s", err, rec.Body.String())
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
	handler := complexHandler(t, func(c *config.Config) { c.Pg.Query.AllowSql = true })
	rec := postRelTo(t, handler, `{
		"query": {"relation": "director", "schema": "public", "select": ["own"]},
		"data": [{"name": "Complex Sql Write Director"}],
		"sql": true
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d : %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Sql []struct {
			Path   []string `json:"path"`
			Insert string   `json:"insert"`
			Select string   `json:"select"`
		} `json:"sql"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v : %s", err, rec.Body.String())
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
