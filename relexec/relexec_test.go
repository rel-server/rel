package relexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/rel-server/rel/cborenc"
	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/query"
	"github.com/rel-server/rel/wellknown"
)

var (
	testDb  *pg.DbInfos
	testCfg *config.Config
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithOrderedInitScripts("../pg/testdata/schema.sql", "testdata/fixture.sql"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		panic(err)
	}
	defer func() { _ = container.Terminate(ctx) }()

	uri, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic(err)
	}
	if testDb, err = pg.NewInfos(uri); err != nil {
		panic(err)
	}
	testCfg = config.Test()

	m.Run()
}

// plainBegin is the Begin a test needs : no role, no claims.
func plainBegin(ctx context.Context, conn *pgxpool.Conn) error {
	_, err := conn.Exec(ctx, "begin")
	return err
}

// run prepares and executes body (one query document) and returns what was written to the sink.
func run(t *testing.T, body string, opts Options) (string, error) {
	t.Helper()
	return runOn(t, testDb, body, opts)
}

// runOn is run against db.
func runOn(t *testing.T, db *pg.DbInfos, body string, opts Options) (string, error) {
	t.Helper()
	return runWith(t, db, nil, body, opts)
}

// runWith is runOn with a registry of well-known queries.
func runWith(t *testing.T, db *pg.DbInfos, reg *wellknown.Registry, body string, opts Options) (string, error) {
	t.Helper()
	pq, err := query.ParseQuery([]byte(body))
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	items := pq.Sequence
	if items == nil {
		items = []query.ParsedQuery{pq}
	}
	prepared, err := Prepare(&query.ResolveContext{Db: db, Config: testCfg}, reg, items, opts)
	if err != nil {
		return "", err
	}
	conn, err := db.Pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer conn.Release()

	var out bytes.Buffer
	err = prepared.Execute(context.Background(), conn, plainBegin, &out)
	return out.String(), err
}

func countRows(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := testDb.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func asError(t *testing.T, err error) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("expected an *Error, got %T : %v", err, err)
	}
	return e
}

func TestPrepare_UnknownRelationIsAClientFaultOnItsItem(t *testing.T) {
	// No connection is involved : Prepare only reads the database metadata.
	_, err := run(t, `[
		{"relation": "director", "schema": "public", "select": ["*~"]},
		{"relation": "no_such_relation", "schema": "public", "select": ["*~"]}
	]`, Options{})
	e := asError(t, err)
	if e.Fault != ClientFault || e.Item != 1 {
		t.Fatalf("expected a client fault on item 1, got fault=%v item=%d : %v", e.Fault, e.Item, e)
	}
}

func TestPrepare_ReportsWritesAndTargets(t *testing.T) {
	pq, err := query.ParseQuery([]byte(`[
		{"relation": "director", "schema": "public", "select": ["*~"]},
		{"query": {"relation": "movie", "schema": "public", "select": ["*~"], "write_mode": "insert"}, "data": [{"title": "x", "director_id": 1}]}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Prepare(&query.ResolveContext{Db: testDb, Config: testCfg}, nil, pq.Sequence, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !p.AnyWrite() {
		t.Error("expected AnyWrite")
	}
	if got := p.Targets(); len(got) != 2 || got[0] != "public.director" || got[1] != "public.movie" {
		t.Errorf("unexpected targets %v", got)
	}
}

func TestExecute_ReadsInOneTransaction(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `delete from director where name = 'Relexec Read'`); err != nil {
		t.Fatal(err)
	}
	if _, err := testDb.Pool.Exec(ctx, `insert into director (name) values ('Relexec Read')`); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, `{"relation": "director", "schema": "public", "select": {"name": ["col", "name"]}, "where": ["=", ["col", "name"], "Relexec Read"]}`, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if out != `[{"name":"Relexec Read"}]` {
		t.Errorf("unexpected body %s", out)
	}
}

func TestExecute_SequenceIsAnArrayOfItsItems(t *testing.T) {
	out, err := run(t, `[
		{"relation": "director", "schema": "public", "select": {"name": ["col", "name"]}, "where": ["=", ["col", "name"], "Relexec Nobody"]},
		{"relation": "director", "schema": "public", "select": {"name": ["col", "name"]}, "where": ["=", ["col", "name"], "Relexec Nobody"]}
	]`, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if out != `[[],[]]` {
		t.Errorf("unexpected body %s", out)
	}
}

func TestExecute_WriteIsCommittedAndReadBack(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `delete from director where name = 'Relexec Written'`); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, `{
		"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"},
		"data": [{"name": "Relexec Written"}]
	}`, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"name":"Relexec Written"`) {
		t.Errorf("unexpected body %s", out)
	}
	if n := countRows(t, `select count(*) from director where name = 'Relexec Written'`); n != 1 {
		t.Errorf("expected the write to be committed, found %d row(s)", n)
	}
}

func TestExecute_RolledBackItemStillAnswersWithItsRows(t *testing.T) {
	ctx := context.Background()
	if _, err := testDb.Pool.Exec(ctx, `delete from director where name = 'Relexec Rolled Back'`); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, `{
		"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"},
		"data": [{"name": "Relexec Rolled Back"}],
		"rollback": true
	}`, Options{Policy: Policy{Rollback: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"name":"Relexec Rolled Back"`) {
		t.Errorf("unexpected body %s", out)
	}
	if n := countRows(t, `select count(*) from director where name = 'Relexec Rolled Back'`); n != 0 {
		t.Errorf("expected the item's own effects to be undone, found %d row(s)", n)
	}
}

func TestPrepare_RollbackNeedsTheGrant(t *testing.T) {
	_, err := run(t, `{
		"query": {"relation": "director", "schema": "public", "select": ["*~"], "write_mode": "insert"},
		"data": [{"name": "x"}],
		"rollback": true
	}`, Options{})
	if e := asError(t, err); e.Fault != ClientFault {
		t.Fatalf("expected a client fault, got %v", e)
	}
}

// A commit the database refuses (a deferred constraint) : a response that fits the buffer is
// replaced by the error, nothing of it has been written.
func TestExecute_RefusedCommitWithinTheBufferWritesNothing(t *testing.T) {
	if _, err := testDb.Pool.Exec(context.Background(), `delete from deferred_check`); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, deferredViolation, Options{Buffer: 64 << 10})
	e := asError(t, err)
	if e.Started || out != "" {
		t.Fatalf("expected nothing written, got started=%v body=%q", e.Started, out)
	}
	if n := countRows(t, `select count(*) from deferred_check`); n != 0 {
		t.Errorf("expected nothing persisted, found %d row(s)", n)
	}
}

// Without a buffer the body is on its way before the commit is attempted : the error says so.
func TestExecute_RefusedCommitWithoutABufferHasStarted(t *testing.T) {
	if _, err := testDb.Pool.Exec(context.Background(), `delete from deferred_check`); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, deferredViolation, Options{Buffer: 0})
	e := asError(t, err)
	if !e.Started || out == "" {
		t.Fatalf("expected the body to have started, got started=%v body=%q", e.Started, out)
	}
	if n := countRows(t, `select count(*) from deferred_check`); n != 0 {
		t.Errorf("expected nothing persisted, found %d row(s)", n)
	}
}

func TestExecute_BeginFailureIsReturnedAndNothingIsWritten(t *testing.T) {
	pq, _ := query.ParseQuery([]byte(`{"relation": "director", "schema": "public", "select": {"name": ["col", "name"]}}`))
	prepared, err := Prepare(&query.ResolveContext{Db: testDb, Config: testCfg}, nil, []query.ParsedQuery{pq}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := testDb.Pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()

	want := NewError(ServerFault, "CUSTOM", errors.New("no such role"))
	var out bytes.Buffer
	err = prepared.Execute(context.Background(), conn, func(context.Context, *pgxpool.Conn) error { return want }, &out)
	if e := asError(t, err); e != want || e.Started || out.Len() != 0 {
		t.Fatalf("expected Begin's own error untouched and nothing written, got %v started=%v body=%q", e, e.Started, out.String())
	}
}

func TestPrepare_CBORFormatBuildsEncoders(t *testing.T) {
	out, err := run(t, `{"relation": "director", "schema": "public", "select": {"name": ["col", "name"]}, "where": ["=", ["col", "name"], "Relexec Nobody"]}`, Options{Format: cborenc.CBOR})
	if err != nil {
		t.Fatal(err)
	}
	if out != "\x80" { // an empty definite CBOR array
		t.Errorf("unexpected body %x", out)
	}
}

const deferredViolation = `{
	"query": {"relation": "deferred_check", "schema": "public", "select": ["*~"], "write_mode": "insert"},
	"data": [{"id": 1, "n": 7}, {"id": 2, "n": 7}]
}`

// assertClientFault checks err is a client fault carrying code.
func assertClientFault(t *testing.T, err error, code errcode.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got no error", code)
	}
	e := asError(t, err)
	if e.Fault != ClientFault || e.Code != code {
		t.Errorf("expected a client fault %s, got fault=%v code=%s : %v", code, e.Fault, e.Code, e)
	}
}

func decodeJSON[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	return v
}

// assertClientError checks err is a client fault, whatever its code.
func assertClientError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a client fault, got no error")
	}
	if e := asError(t, err); e.Fault != ClientFault {
		t.Errorf("expected a client fault, got fault=%v : %v", e.Fault, e)
	}
}
