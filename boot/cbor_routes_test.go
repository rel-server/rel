package boot

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/route"
	"github.com/rel-server/rel/wellknown"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// postcbor's cbor type is only in this image.
const postcborImage = "ghcr.io/ceymard/postgres-convenient:18.7-alpine"

const cborRoutesSchema = `
create extension postcbor;
create schema blob;
create domain blob.cbor as bytea;
create table doc (id int primary key, raw blob.cbor);
create type holder as (id int, raw blob.cbor);

create function r_echo(req jsonb, body cbor) returns cbor language sql as $$ select body $$;
comment on function r_echo(jsonb, cbor) is 'route:: path: "/echo"';
create function r_echo_raw(req jsonb, body blob.cbor) returns blob.cbor language sql as $$ select body $$;
comment on function r_echo_raw(jsonb, blob.cbor) is 'route:: path: "/echo-raw"';
create function r_req(req jsonb, body blob.cbor) returns jsonb language sql as
	$$ select jsonb_build_object('body', req->'body', 'length', length(body)) $$;
comment on function r_req(jsonb, blob.cbor) is 'route:: path: "/req"';
create function r_null() returns cbor language sql as $$ select null::cbor $$;
comment on function r_null() is 'route:: path: "/null"';
create function r_holder() returns holder language sql as $$ select (1, '\xa1616101'::bytea)::holder $$;
comment on function r_holder() is 'route:: path: "/holder"';
create function r_full(req jsonb, out e jsonb, out c cbor) language sql as $$ select '{"status": 201}'::jsonb, '\x83010203'::bytea::cbor $$;
comment on function r_full(jsonb) is 'route:: path: "/full"';
create function r_array(req jsonb, body cbor[]) returns text language sql as $$ select 'no' $$;
comment on function r_array(jsonb, cbor[]) is 'route:: path: "/array", method: "POST"';
create role "~anonymous";
grant usage on schema blob to "~anonymous";
grant all on doc to "~anonymous";
grant execute on all functions in schema public to "~anonymous";
`

func TestBuildMux_CBORRoutesAndParams(t *testing.T) {
	ctx := context.Background()
	container, err := postgres.Run(ctx, postcborImage, postgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("starting postgres container: %v", err)
	}
	defer func() { _ = container.Terminate(ctx) }()
	uri, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	setup, err := pg.NewInfos(uri)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Pool.Exec(ctx, cborRoutesSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	setup.Pool.Close()

	dir := t.TempDir()
	for name, content := range map[string]string{
		"by_raw.json": `{"name": "doc_by_raw", "params": {"b": {"type": "blob.cbor"}},
			"query": {"relation": "doc", "schema": "public", "select": {"id": [".", "id"]}, "where": ["=", [".", "raw"], ["$param", "b", "blob.cbor"]]}}`,
		"misdeclared.json": `{"name": "doc_misdeclared", "params": {"b": {"type": "cbor"}},
			"query": {"relation": "doc", "schema": "public", "select": {"id": [".", "id"]}, "where": ["=", [".", "raw"], ["$param", "b", "bytea"]]}}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Test()
	cfg.Pg.Query.WellKnownDirs = dir
	cfg.Pg.Query.AnonymousRole = "~anonymous"
	db, err := pg.NewInfosAdminQuery(uri, uri, 0, cfg.Pg.Query.AnonymousRole)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Pool.Close()
	reg, err := route.BuildRegistry(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	wkReg, err := wellknown.BuildRegistry(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := BuildMux(db, cfg, reg, wkReg, nil)

	send := func(method, path, contentType, accept string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	unhex := func(s string) []byte {
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	withStructures := unhex("82" + "d9dfff8319e000816161" + "01" + "d9e0008102")
	spliced := unhex("82a1616101a1616102")
	isCBOR := func(t *testing.T, rec *httptest.ResponseRecorder, want []byte) {
		t.Helper()
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/cbor" || !bytes.Equal(rec.Body.Bytes(), want) {
			t.Errorf("status %d, %s, % x : want application/cbor % x", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Bytes(), want)
		}
	}

	t.Run("a cbor argument takes the body spliced, a cbor return is sent as CBOR whatever the Accept", func(t *testing.T) {
		for _, path := range []string{"/echo", "/echo-raw"} {
			isCBOR(t, send("POST", path, "application/cbor", "application/json", withStructures), spliced)
		}
	})

	t.Run("a cbor argument leaves req.body null", func(t *testing.T) {
		rec := send("POST", "/req", "application/cbor", "", spliced)
		if rec.Code != http.StatusOK || rec.Body.String() != `{"body": null, "length": 9}` {
			t.Errorf("status %d : %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("a cbor argument refuses another media type, and CBOR it cannot read", func(t *testing.T) {
		for _, c := range []struct {
			contentType string
			body        []byte
			status      int
		}{
			{"application/json", []byte(`{"a": 1}`), http.StatusUnsupportedMediaType},
			{"application/octet-stream", spliced, http.StatusUnsupportedMediaType},
			{"application/cbor", unhex("8201"), http.StatusBadRequest},
		} {
			if rec := send("POST", "/echo-raw", c.contentType, "", c.body); rec.Code != c.status {
				t.Errorf("%s % x : status %d, want %d : %s", c.contentType, c.body, rec.Code, c.status, rec.Body.String())
			}
		}
	})

	t.Run("an empty body is a NULL argument, and a NULL return CBOR's null", func(t *testing.T) {
		isCBOR(t, send("POST", "/echo-raw", "application/cbor", "", nil), []byte{0xf6})
		isCBOR(t, send("GET", "/null", "", "", nil), []byte{0xf6})
	})

	t.Run("a composite holding a CBOR value is answered in CBOR even to a JSON Accept", func(t *testing.T) {
		rec := send("GET", "/holder", "", "application/json", nil)
		var v map[string]cbor.RawMessage
		if err := cbor.Unmarshal(rec.Body.Bytes(), &v); err != nil || rec.Code != http.StatusOK || !bytes.Equal(v["raw"], unhex("a1616101")) {
			t.Errorf("status %d, %s, % x (%v)", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Bytes(), err)
		}
	})

	t.Run("a full-control route's cbor content is its CBOR", func(t *testing.T) {
		rec := send("GET", "/full", "", "", nil)
		if rec.Code != http.StatusCreated || rec.Header().Get("Content-Type") != "application/cbor" || !bytes.Equal(rec.Body.Bytes(), unhex("83010203")) {
			t.Errorf("status %d, %s, % x", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Bytes())
		}
	})

	t.Run("a cbor[] argument disables the route", func(t *testing.T) {
		if rec := send("POST", "/array", "application/cbor", "", spliced); rec.Code == http.StatusOK {
			t.Errorf("status %d : %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("a cbor well-known param binds its item spliced, and refuses JSON", func(t *testing.T) {
		if _, err := db.Pool.Exec(ctx, `insert into doc values (1, '\x`+hex.EncodeToString(spliced)+`')`); err != nil {
			t.Fatal(err)
		}
		body, err := cbor.Marshal(map[string]any{"wellknown": "doc_by_raw", "params": map[string]any{"b": cbor.RawMessage(withStructures)}})
		if err != nil {
			t.Fatal(err)
		}
		rec := send("POST", "/rel", "application/cbor", "application/json", body)
		if rec.Code != http.StatusOK || rec.Body.String() != `[{"id":1}]` {
			t.Errorf("from CBOR : status %d : %s", rec.Code, rec.Body.String())
		}
		rec = send("POST", "/rel", "application/json", "", []byte(`{"wellknown": "doc_by_raw", "params": {"b": [{"a": 1}, {"a": 2}]}}`))
		if rec.Code != http.StatusBadRequest || errcode.Code(rec.Header().Get(errcode.Header)) != errcode.WellKnownParamTypeMismatch {
			t.Errorf("from JSON : status %d, %s : %s", rec.Code, rec.Header().Get(errcode.Header), rec.Body.String())
		}
	})

	t.Run("a cbor param used without a cbor cast deactivates its query", func(t *testing.T) {
		rec := send("POST", "/rel", "application/json", "", []byte(`{"wellknown": "doc_misdeclared", "params": {"b": null}}`))
		if errcode.Code(rec.Header().Get(errcode.Header)) != errcode.WellKnownUnknownQuery {
			t.Errorf("status %d, %s : %s", rec.Code, rec.Header().Get(errcode.Header), rec.Body.String())
		}
	})
}
