package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/pg"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

var testDb *pg.DbInfos
var testCfg *config.Config
var testHandler http.Handler
var testDbURI string

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithOrderedInitScripts("../pg/testdata/schema.sql", "testdata/roles.sql"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		panic(err)
	}
	defer func() {
		_ = container.Terminate(ctx)
	}()

	uri, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic(err)
	}
	testDbURI = uri

	testCfg = config.Test()
	testCfg.Pg.Query.AnonymousRole = "~anonymous"

	// This package's anonymous-access scenarios need AnonymousRoleExists
	// true, hence the real role name here.
	testDb, err = pg.NewInfosAdminQuery(uri, uri, 0, testCfg.Pg.Query.AnonymousRole)
	if err != nil {
		panic(err)
	}

	testHandler = NewRelHandler(testDb, testCfg, nil)

	m.Run()
}

// postRel POSTs body to the handler directly (httptest, no real listener)
// and returns the recorded response.
func postRel(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	return postRelTo(t, testHandler, body)
}

func postRelTo(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/rel", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeJSON[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	return v
}

// docs/content/typescript/index.md ## Typed wire values : int8 and numeric arrive as bare JSON numbers, with
// every digit Postgres has. Rounding to the nearest float64 (9007199254740992, a different value) happens in
// a client's JSON.parse, which is why the client can be given a JSON implementation that keeps big numbers.
// A WHERE clause filtering on an int8 column compares numerically.
// TestRelHandler_ReadFailureAfterWriteRollsBackTheWrite : a read failing
// mid-stream must roll back the whole transaction, write included.
// TestRelHandler_WhereWordFormOperatorSynonyms_HTTP : a POST /rel body
// using word-form operators ("gte" not ">=") must match the symbolic form.
func TestRelHandler_ParseError(t *testing.T) {
	rec := postRel(t, `{not valid json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
	resp := decodeJSON[map[string]any](t, rec.Body.Bytes())
	if resp["status"] != "error" {
		t.Errorf(`expected status="error", got %v`, resp["status"])
	}
}

func TestRelHandler_UnresolvableRelation_BadRequest(t *testing.T) {
	rec := postRel(t, `{"relation": "no_such_relation_at_all", "schema": "public"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
}

// TestRelHandler_WellKnown_UnregisteredName_BarePosition : testHandler's
// nil *wellknown.Registry makes any bare well-known query "unregistered".
func TestRelHandler_WellKnown_UnregisteredName_BarePosition(t *testing.T) {
	rec := postRel(t, `{"wellknown": "some_query"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Rel-Errorcode"); got != "WELL_KNOWN_UNKNOWN_QUERY" {
		t.Fatalf("expected WELL_KNOWN_UNKNOWN_QUERY, got %q", got)
	}
}

// TestRelHandler_WellKnown_UnregisteredName_WrappedWritePosition is the
// wrapped-write counterpart of the bare-position test above.
func TestRelHandler_WellKnown_UnregisteredName_WrappedWritePosition(t *testing.T) {
	rec := postRel(t, `{"query": {"wellknown": "some_query"}, "data": [{"a": 1}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Rel-Errorcode"); got != "WELL_KNOWN_UNKNOWN_QUERY" {
		t.Fatalf("expected WELL_KNOWN_UNKNOWN_QUERY, got %q", got)
	}
}

// insertDirectors adds one director per name and returns their ids.
// TestRelHandler_UnknownRole_FailsCleanly proves a role the database doesn't know is a server
// error that leaves the pooled connection usable (the batched begin + SET LOCAL ROLE is rolled back).
func TestRelHandler_UnknownRole_FailsCleanly(t *testing.T) {
	cfg := *testCfg
	cfg.Pg.Query.AnonymousRole = "no_such_role_at_all"
	broken := NewRelHandler(testDb, &cfg, nil)
	for range 3 { // more than one request, so a connection left in an aborted transaction would show
		rec := postRelTo(t, broken, `{"relation": "director", "schema": "public", "limit": 1, "select": ["*~"]}`)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d : %s", rec.Code, rec.Body.String())
		}
	}
	if rec := postRel(t, `{"relation": "director", "schema": "public", "limit": 1, "select": ["*~"]}`); rec.Code != http.StatusOK {
		t.Fatalf("expected the pool to stay usable, got %d : %s", rec.Code, rec.Body.String())
	}
}

// TestRelHandler_UnresolvableOverload_Is400 proves Postgres' refusal to pick a function overload for the
// argument types a request produced (sum of text) is the request's own error : a 400 with its own code.
func TestRelHandler_UnresolvableOverload_Is400(t *testing.T) {
	rec := postRel(t, `{"relation": "agg_parent", "schema": "public", "join": {"kids": {"relation": "agg_child", "schema": "public", "on": {"parent_id": "id"}}},
		"select": {"v": ["agg", "sum", [["::", [".", "kids", "amount"], "text"]]]}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d : %s", rec.Code, rec.Body.String())
	}
	if code := errCode(t, rec.Body.Bytes()); code != "PG_UNDEFINED_FUNCTION" {
		t.Errorf("expected PG_UNDEFINED_FUNCTION, got %s : %s", code, rec.Body.String())
	}
}

// errCode is the "code" of an error envelope.
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

// complexHandler builds a NewRelHandler off a clone of testCfg changed by configure, never
// mutating the shared testCfg/testHandler other tests rely on.
func complexHandler(t *testing.T, configure func(*config.Config)) http.Handler {
	t.Helper()
	cfg := *testCfg
	if configure != nil {
		configure(&cfg)
	}
	return NewRelHandler(testDb, &cfg, nil)
}
