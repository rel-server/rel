package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/rel-server/rel/errcode"
)

func cborBody(t *testing.T, v any) []byte {
	t.Helper()
	b, err := cbor.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func postCBOR(t *testing.T, body []byte, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/rel", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	return rec
}

func TestRelHandler_CBORRequestBody(t *testing.T) {
	ctx := context.Background()

	// a nested write sent as CBOR : the response is JSON, as it is for a JSON request
	body := cborBody(t, map[string]any{
		"query": map[string]any{
			"relation": "director", "schema": "public",
			"select":     map[string]any{"id": []any{"col", "id"}, "name": []any{"col", "name"}, "movies": []any{".", "movies"}},
			"write_mode": "insert",
			"join": map[string]any{"movies": map[string]any{"relation": "movie", "schema": "public",
				"on": map[string]any{"director_id": "id"}, "write_mode": "insert", "select": []any{"*~"}}},
		},
		"data": []any{map[string]any{"name": "CBOR Director", "movies": []any{map[string]any{"title": "CBOR Movie A"}, map[string]any{"title": "CBOR Movie B"}}}},
	})
	rec := postCBOR(t, body, "application/cbor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d : %s", rec.Code, rec.Body.String())
	}
	rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes())
	if len(rows) != 1 || rows[0]["name"] != "CBOR Director" || len(rows[0]["movies"].([]any)) != 2 {
		t.Errorf("response = %s", rec.Body.String())
	}
	var n int
	if err := testDb.Pool.QueryRow(ctx, `select count(*) from movie m join director d on d.id = m.director_id where d.name = 'CBOR Director'`).Scan(&n); err != nil || n != 2 {
		t.Errorf("movies written = %d (%v)", n, err)
	}

	// a read, with the media type's parameters and case ignored
	read := cborBody(t, map[string]any{"relation": "director", "schema": "public", "select": []any{"*~"},
		"where": []any{"=", []any{"col", "name"}, "CBOR Director"}})
	for _, ct := range []string{"application/cbor", "Application/CBOR; charset=binary"} {
		rec = postCBOR(t, read, ct)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s : status %d : %s", ct, rec.Code, rec.Body.String())
		}
		if rows := decodeJSON[[]map[string]any](t, rec.Body.Bytes()); len(rows) != 1 {
			t.Errorf("%s : read %s", ct, rec.Body.String())
		}
	}

	// a sequence of two writes, with the typed values only CBOR has
	seq := cborBody(t, []any{
		map[string]any{"query": map[string]any{"relation": "tz_t", "schema": "public", "select": []any{"*~"}, "write_mode": "insert"},
			"data": []any{map[string]any{"id": 9101, "at": cbor.Tag{Number: 1, Content: int64(1700000000)}, "plain": "2026-10-18T14:30:00",
				"day": cbor.Tag{Number: 100, Content: int64(20000)}}}},
		map[string]any{"query": map[string]any{"relation": "tz_t", "schema": "public", "select": []any{"*~"}, "write_mode": "insert"},
			"data": []any{map[string]any{"id": 9102, "at": time.Unix(1700000001, 0).UTC().Format(time.RFC3339)}}},
	})
	rec = postCBOR(t, seq, "application/cbor")
	if rec.Code != http.StatusOK {
		t.Fatalf("sequence : status %d : %s", rec.Code, rec.Body.String())
	}
	var stored string
	if err := testDb.Pool.QueryRow(ctx, `select to_char(at at time zone 'UTC', 'YYYY-MM-DD HH24:MI:SS') || ' ' || day::text from tz_t where id = 9101`).Scan(&stored); err != nil || stored != "2023-11-14 22:13:20 2024-10-04" {
		t.Errorf("stored = %q (%v)", stored, err)
	}
}

func TestRelHandler_CBORRequestErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     []byte
		wantCode errcode.Code
		status   int
	}{
		{"not CBOR at all", []byte("{not cbor"), errcode.QueryMalformedCBOR, http.StatusBadRequest},
		{"truncated", cborBody(t, map[string]any{"relation": "director"})[:5], errcode.QueryMalformedCBOR, http.StatusBadRequest},
		{"a map key that is not text", cborBody(t, map[int]string{1: "x"}), errcode.QueryMalformedCBOR, http.StatusBadRequest},
		{"trailing bytes", append(cborBody(t, map[string]any{"relation": "director"}), 0x01), errcode.QueryMalformedCBOR, http.StatusBadRequest},
		{"a value the write cannot read", cborBody(t, map[string]any{
			"query": map[string]any{"relation": "director", "schema": "public", "select": []any{"*~"}, "write_mode": "insert"},
			"data":  []any{map[string]any{"name": []byte{1, 2}}},
		}), "PG_INVALID_TEXT_REPRESENTATION", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := postCBOR(t, tc.body, "application/cbor")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d : %s", rec.Code, tc.status, rec.Body.String())
			}
			if got := errcode.Code(rec.Header().Get(errcode.Header)); got != tc.wantCode {
				t.Errorf("X-Rel-Errorcode = %q, want %s", got, tc.wantCode)
			}
		})
	}
	// a JSON body sent as JSON keeps its own error code
	rec := postRel(t, "{nope")
	if got := errcode.Code(rec.Header().Get(errcode.Header)); got != errcode.QueryMalformedJSON {
		t.Errorf("JSON : X-Rel-Errorcode = %q", got)
	}
}
