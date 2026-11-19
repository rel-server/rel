package tsgen

import (
	"strings"
	"testing"

	"github.com/rel-server/rel/pg"
)

func TestTSTypeExpr_CBORDomainIsUnknown(t *testing.T) {
	bytea := &pg.Type{PgIdentifier: pg.SqlIdentifier{Schema: "pg_catalog", Name: "bytea"}}
	cbor := &pg.Type{PgIdentifier: pg.SqlIdentifier{Schema: "blob", Name: "cbor"}, BaseType: bytea, CBOR: true}
	body := &pg.Type{PgIdentifier: pg.SqlIdentifier{Schema: "public", Name: "doc_body"}, BaseType: cbor, CBOR: true}
	tc := newTypeCollector()
	tsTypeExpr(body, tc)
	decls := tc.declarations()
	if !strings.Contains(decls, typeInterfaceName("blob", "cbor")+" = unknown\n") || strings.Contains(decls, "Bytea") {
		t.Errorf("a cbor domain over bytea should be unknown, through a domain over it too ; got:\n%s", decls)
	}
}
