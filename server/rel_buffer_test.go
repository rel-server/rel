package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// A response that has not reached the response buffer's size yet can still be replaced by an error,
// whatever happens while it is produced : a row failing after the first, a commit refused.

func TestRelHandler_RowFailingAfterTheFirstIsACleanError(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `delete from director where name like 'LateRow %'`); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	var second int
	for _, name := range []string{"LateRow 1", "LateRow 2"} {
		if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ($1) returning id`, name).Scan(&second); err != nil {
			t.Fatalf("seeding: %v", err)
		}
	}

	// 1/(id - second) divides by zero on the second row only. No order_by : a sort would compute the
	// select list for every row before returning the first, and the failure would come before any output.
	rec := postRel(t, `{
		"relation": "director", "schema": "public",
		"select": {"x": ["/", 1, ["-", ["col", "id"], `+itoa(second)+`]]},
		"where": ["like", ["col", "name"], "LateRow %"]
	}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("expected an error status, the second row divides by zero ; got 200 : %s", rec.Body.String())
	}
	if resp := decodeJSON[map[string]any](t, rec.Body.Bytes()); resp["status"] != "error" {
		t.Errorf("expected the error envelope, got %s", rec.Body.String())
	}
}

func TestRelHandler_CommitRefusedIsAnError(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `delete from deferred_check`); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// Both rows pass statement by statement ; the unique constraint is checked at commit.
	rec := postRel(t, `{
		"query": {"relation": "deferred_check", "schema": "public", "select": ["*~"], "write_mode": "insert"},
		"data": [{"id": 1, "n": 7}, {"id": 2, "n": 7}]
	}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("expected an error status, the commit fails ; got 200 : %s", rec.Body.String())
	}
	if resp := decodeJSON[map[string]any](t, rec.Body.Bytes()); resp["status"] != "error" {
		t.Errorf("expected the error envelope, got %s", rec.Body.String())
	}
	var count int
	if err := testDb.Pool.QueryRow(ctx, `select count(*) from deferred_check`).Scan(&count); err != nil {
		t.Fatalf("querying back: %v", err)
	}
	if count != 0 {
		t.Errorf("expected nothing written, found %d row(s)", count)
	}
}

// Past the buffer, a response is on its way and a later failure can only cut it short.
func TestRelHandler_RowFailingAfterTheBufferIsFullCutsTheBodyShort(t *testing.T) {
	cfg := *testCfg
	cfg.Http.ResponseBuffer = 0
	handler := NewRelHandler(testDb, &cfg, nil)

	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `delete from director where name like 'LateRow %'`); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	var second int
	for _, name := range []string{"LateRow 1", "LateRow 2"} {
		if err := testDb.Pool.QueryRow(ctx, `insert into director (name) values ($1) returning id`, name).Scan(&second); err != nil {
			t.Fatalf("seeding: %v", err)
		}
	}

	rec := postRelTo(t, handler, `{
		"relation": "director", "schema": "public",
		"select": {"x": ["/", 1, ["-", ["col", "id"], `+itoa(second)+`]]},
		"where": ["like", ["col", "name"], "LateRow %"]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the 200 already sent with the first row, got %d : %s", rec.Code, rec.Body.String())
	}
	if json.Valid(rec.Body.Bytes()) {
		t.Fatalf("expected a cut-short body, got complete JSON : %s", rec.Body.String())
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
