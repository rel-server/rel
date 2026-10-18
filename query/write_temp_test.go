package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rel-server/rel/pg"
)

// fakeRelation builds a relation of n int4 columns, for layouts that need no real table.
func fakeRelation(t *testing.T, relid, n int, names ...string) *pg.Relation {
	t.Helper()
	int4 := testDb.TypeMapByOid[oidInt4]
	if int4 == nil {
		t.Fatal("int4 is not introspected")
	}
	r := &pg.Relation{PgRelId: relid, Identifier: pg.SqlIdentifier{Schema: "public", Name: fmt.Sprintf("fake_%d", relid)}}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("c%d", i)
		if i < len(names) {
			name = names[i]
		}
		r.Columns = append(r.Columns, &pg.Column{Name: name, Type: int4, DeclaredType: "integer"})
	}
	return r
}

func TestTempTable_MetadataPrefixAvoidsClashes(t *testing.T) {
	cases := []struct {
		columns []string
		prefix  string
	}{
		{[]string{"id", "name"}, "__rel_"},
		{[]string{"__rel_row_id"}, "__rel__"},
		{[]string{"__rel__x", "__rel_y"}, "__rel___"},
		{[]string{"_rel_x", "rel_"}, "__rel_"},
	}
	for _, c := range cases {
		tt, err := newTempTable(fakeRelation(t, 990001, len(c.columns), c.columns...))
		if err != nil {
			t.Fatal(err)
		}
		if tt.prefix != c.prefix {
			t.Errorf("columns %v: prefix %q, want %q", c.columns, tt.prefix, c.prefix)
		}
		for _, name := range []string{tt.rowIDCol(), tt.nodeIDCol(), tt.parentIDCol(), tt.maskCol(0)} {
			for _, col := range c.columns {
				if name == col {
					t.Errorf("metadata column %q clashes with a table column", name)
				}
			}
		}
	}
}

func TestTempTable_MaskWordsAndColumnLimit(t *testing.T) {
	for _, c := range []struct{ cols, words int }{{1, 1}, {63, 1}, {64, 1}, {65, 2}, {128, 2}, {129, 3}} {
		tt, err := newTempTable(fakeRelation(t, 990002, c.cols))
		if err != nil {
			t.Fatal(err)
		}
		if tt.maskWords != c.words {
			t.Errorf("%d columns: %d mask words, want %d", c.cols, tt.maskWords, c.words)
		}
		if got := strings.Count(strings.Join(tt.createStatements(), "\n"), "mask"); got != 2*c.words {
			t.Errorf("%d columns: %d mask columns in the DDL, want %d (two tables)", c.cols, got, 2*c.words)
		}
	}
	// 3 metadata columns and 25 mask words leave room for 1572 columns
	if _, err := newTempTable(fakeRelation(t, 990003, 1573)); err == nil {
		t.Error("1573 columns cannot fit with the metadata and mask columns, but were accepted")
	}
	if _, err := newTempTable(fakeRelation(t, 990003, 1572)); err != nil {
		t.Errorf("1572 columns should fit: %v", err)
	}
}

func tempTestConn(t *testing.T) *pgxpool.Conn {
	t.Helper()
	conn, err := testDb.Pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Cleanup(conn.Release)
	return conn
}

func TestEnsureTempTables_Lifecycle(t *testing.T) {
	ctx := context.Background()
	conn := tempTestConn(t)
	rel := fakeRelation(t, 990010, 3)

	first, err := ensureTempTables(ctx, conn, rel)
	if err != nil {
		t.Fatal(err)
	}
	if first.general.rowOID == 0 || first.general.arrayOID == 0 || first.fast.rowOID == 0 || first.fast.arrayOID == 0 {
		t.Fatalf("oids not read: %+v %+v", first.general, first.fast)
	}
	var n int
	if err := conn.QueryRow(ctx, `select count(*) from pg_class where relnamespace = pg_my_temp_schema() and relname in ($1, $2)`, first.general.name, first.fast.name).Scan(&n); err != nil || n != 2 {
		t.Fatalf("temp tables: %d, %v", n, err)
	}

	// a second call finds the connection ready and gives the same oids
	second, err := ensureTempTables(ctx, conn, rel)
	if err != nil {
		t.Fatal(err)
	}
	if second.general != first.general || second.fast != first.fast {
		t.Errorf("second call gave %+v %+v, want %+v %+v", second.general, second.fast, first.general, first.fast)
	}

	// not inside a transaction : a rollback would take the tables and leave the record stale
	if _, err := conn.Exec(ctx, "begin"); err != nil {
		t.Fatal(err)
	}
	_, err = ensureTempTables(ctx, conn, fakeRelation(t, 990011, 2))
	_, _ = conn.Exec(ctx, "rollback")
	if err == nil || !strings.Contains(err.Error(), "outside a transaction") {
		t.Errorf("inside a transaction: err = %v", err)
	}
	// a table already prepared is fine inside a transaction
	if _, err := conn.Exec(ctx, "begin"); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureTempTables(ctx, conn, rel); err != nil {
		t.Errorf("prepared table inside a transaction: %v", err)
	}
	_, _ = conn.Exec(ctx, "rollback")
}

func TestEnsureTempTables_GrantedToPublic(t *testing.T) {
	ctx := context.Background()
	conn := tempTestConn(t)
	tt, err := ensureTempTables(ctx, conn, fakeRelation(t, 990012, 2))
	if err != nil {
		t.Fatal(err)
	}
	var acl *string
	if err := conn.QueryRow(ctx, `select relacl::text from pg_class where relnamespace = pg_my_temp_schema() and relname = $1`, tt.general.name).Scan(&acl); err != nil {
		t.Fatal(err)
	}
	// an ACL item with an empty grantee is PUBLIC : {owner=arwdDxt/owner,=arwdDxt/owner}
	if acl == nil || !strings.Contains(*acl, ",=arwdDx") {
		t.Errorf("acl = %v, want all privileges for PUBLIC", acl)
	}
}

// inTx runs f in a transaction that is rolled back : the general temp table empties at commit,
// so a test that reads rows back must stay inside one.
func inTx(t *testing.T, conn *pgxpool.Conn, f func()) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), "begin"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), "rollback") }()
	f()
}

// load sends the batch the way the write path does : in a pgx batch, with the raw parameter.
func load(t *testing.T, conn *pgxpool.Conn, tt *tempTable, general bool, b *rowBatch) error {
	t.Helper()
	var batch pgx.Batch
	batch.Queue(tt.loadSQL(general), b.param())
	res := conn.Conn().SendBatch(context.Background(), &batch)
	_, err := res.Exec()
	if cerr := res.Close(); err == nil {
		err = cerr
	}
	return err
}

func TestLoad_WideTableMaskWords(t *testing.T) {
	ctx := context.Background()
	conn := tempTestConn(t)
	rel := fakeRelation(t, 990020, 130)
	tt, err := ensureTempTables(ctx, conn, rel)
	if err != nil {
		t.Fatal(err)
	}
	if tt.maskWords != 3 {
		t.Fatalf("mask words = %d", tt.maskWords)
	}
	vals := make([]Node, 130)
	vals[5], vals[129] = encNode(t, `7`), encNode(t, `"8"`)
	topBit := uint64(1) << 63
	mask := []int64{int64(topBit | 1<<5), 1, 1 << 1}
	b := tt.newBatch(true, 2)
	if err := b.addRow(rowMeta{RowID: 10, NodeID: 3, ParentID: 9, HasParent: true}, vals, mask, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.addRow(rowMeta{RowID: 11, NodeID: 3}, make([]Node, 130), []int64{0, 0, 0}, nil); err != nil {
		t.Fatal(err)
	}
	inTx(t, conn, func() { loadReadWide(t, conn, tt, b) })
}

func loadReadWide(t *testing.T, conn *pgxpool.Conn, tt *tempTable, b *rowBatch) {
	t.Helper()
	ctx := context.Background()
	if err := load(t, conn, tt, true, b); err != nil {
		t.Fatal(err)
	}
	var rowID, nodeID int32
	var parent *int32
	var c5, c129 *int32
	var m0, m1, m2 int64
	err := conn.QueryRow(ctx, `select __rel_row_id, __rel_node_id, __rel_parent_id, c5, c129, __rel_mask0, __rel_mask1, __rel_mask2 from `+tt.general.qualified()+` where __rel_row_id = 10`).
		Scan(&rowID, &nodeID, &parent, &c5, &c129, &m0, &m1, &m2)
	if err != nil {
		t.Fatal(err)
	}
	if rowID != 10 || nodeID != 3 || parent == nil || *parent != 9 || c5 == nil || *c5 != 7 || c129 == nil || *c129 != 8 {
		t.Errorf("row 10: %d %d %v %v %v", rowID, nodeID, parent, c5, c129)
	}
	if uint64(m0) != uint64(1)<<63|1<<5 || m1 != 1 || m2 != 2 {
		t.Errorf("masks = %x %x %x", uint64(m0), m1, m2)
	}
	var parentIsNull bool
	if err := conn.QueryRow(ctx, `select __rel_parent_id is null and c5 is null from `+tt.general.qualified()+` where __rel_row_id = 11`).Scan(&parentIsNull); err != nil || !parentIsNull {
		t.Errorf("row 11: parent and c5 should be NULL (%v, %v)", parentIsNull, err)
	}
}

func TestLoad_EmptyAndFastBatches(t *testing.T) {
	ctx := context.Background()
	conn := tempTestConn(t)
	tt, err := ensureTempTables(ctx, conn, fakeRelation(t, 990021, 2))
	if err != nil {
		t.Fatal(err)
	}
	if err := load(t, conn, tt, true, tt.newBatch(true, 0)); err != nil {
		t.Errorf("an empty batch: %v", err)
	}
	// the fast type has no metadata columns : read its rows straight from the parameter
	b := tt.newBatch(false, 2)
	for i := 0; i < 2; i++ {
		if err := b.addRow(rowMeta{}, []Node{encNode(t, fmt.Sprint(i+1)), nil}, []int64{int64(i)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	var sum, masks int64
	err = conn.QueryRow(ctx, fmt.Sprintf(`select sum(c0), sum(__rel_mask0) from unnest($1::%s[])`, tt.fast.qualified()), b.param()).Scan(&sum, &masks)
	if err != nil {
		t.Fatal(err)
	}
	if sum != 3 || masks != 1 {
		t.Errorf("sum = %d, masks = %d", sum, masks)
	}
}

func pgCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// TestLoad_ModifiersCheckedAtLoad : binary-mode columns repeat the declared type, so Postgres' receive
// functions enforce lengths and precision when the rows load.
func TestLoad_ModifiersCheckedAtLoad(t *testing.T) {
	ctx := context.Background()
	conn := tempTestConn(t)
	rel := encT(t)
	tt, err := ensureTempTables(ctx, conn, rel)
	if err != nil {
		t.Fatal(err)
	}
	idx := func(name string) int {
		for i, c := range tt.cols {
			if c.name == name {
				return i
			}
		}
		t.Fatalf("no column %s", name)
		return -1
	}
	row := func(col, js string) *rowBatch {
		vals := make([]Node, len(tt.cols))
		vals[idx(col)] = encNode(t, js)
		b := tt.newBatch(true, 1)
		if err := b.addRow(rowMeta{RowID: 1, NodeID: 1}, vals, make([]int64, tt.maskWords), nil); err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, c := range []struct{ col, js, code string }{
		{"vc", `"abcdef"`, "22001"}, {"chr", `"abcd"`, "22001"}, {"nums", `[1000000]`, "22003"},
		{"pos", `0`, "23514"},
	} {
		inTx(t, conn, func() {
			if err := load(t, conn, tt, true, row(c.col, c.js)); pgCode(err) != c.code {
				t.Errorf("%s %s: code %q (%v), want %s", c.col, c.js, pgCode(err), err, c.code)
			}
		})
	}
	// precision is applied by the receive function : 1.234 is stored as 1.23
	inTx(t, conn, func() {
		if err := load(t, conn, tt, true, row("nums", `[1.234, 2]`)); err != nil {
			t.Fatal(err)
		}
		var got string
		if err := conn.QueryRow(ctx, `select nums::text from pg_temp.`+quoteIdent(tt.general.name)+` where nums is not null`).Scan(&got); err != nil || got != "{1.23,2.00}" {
			t.Errorf("nums = %q (%v)", got, err)
		}
	})
}

// readBackSQL selects the temp table's columns the way a statement reading them does :
// text-mode columns cast to their type without modifiers.
func readBackSQL(tt *tempTable, rel *pg.Relation) string {
	var cols []string
	for i, c := range tt.cols {
		if c.binary {
			cols = append(cols, quoteIdent(c.name))
		} else {
			cols = append(cols, fmt.Sprintf("%s::%s as %s", quoteIdent(c.name), rel.Columns[i].Type.PgIdentifier.EscapedString(), quoteIdent(c.name)))
		}
	}
	return "select " + strings.Join(cols, ", ") + " from pg_temp." + quoteIdent(tt.general.name) + " where " + quoteIdent(tt.rowIDCol()) + " = $1"
}

// TestLoad_RowsMatchTheJSONPath loads whole rows of every kind of column and compares what is in
// the table, read back like the write statements read it and assigned to enc_t's own columns,
// with what jsonb_populate_record makes of the same JSON : the path that exists today.
func TestLoad_RowsMatchTheJSONPath(t *testing.T) {
	ctx := context.Background()
	conn := tempTestConn(t)
	rel := encT(t)
	tt, err := ensureTempTables(ctx, conn, rel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `create temp table if not exists enc_dest (like enc_t)`); err != nil {
		t.Fatal(err)
	}
	rows := []string{
		`{"id": 1, "i4s": [1,2,3], "txts": ["a","b c","d,e"], "nums": [1.234, null], "uuids": ["a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"],
		  "tss": ["2026-10-18T14:30:00Z"], "bools": [true, "off"], "jsonbs": [{"a":1}, [1,2]], "jsons": [[1,2],[3,4]],
		  "inner_c": {"a": 1, "b": "x"}, "outer_c": {"i": {"a": 2}, "tags": ["q"], "n": 1.5, "t": "2026-10-18T14:30:00+02:00", "j": {"k": 1}},
		  "inners": [{"a":1},{"b":"y"}], "outers": [{"n": 2}], "iv": "1 day 02:00:00", "ivs": ["1 day", "2 hours"],
		  "ivc": {"code": "a,b", "span": "3 days"}, "ivcs": [{"code": "x"}, null], "pos": 5, "poss": [1,2], "mood": "ok", "moods": ["sad"],
		  "ip": "10.0.0.1/24", "ips": ["::1"], "rng": "[1,5)", "rngs": ["empty"], "pt": "(1,2)", "tsv": "a b", "cash": "$12.50",
		  "bits": "1010", "vc": "abc", "chr": "ab", "bytes": "\\x00ff", "byteas": ["\\x01", null]}`,
		`{"id": 2}`,
		`{"id": 3, "i4s": null, "inner_c": null, "iv": null, "ivs": null}`,
		`{"id": 4, "i4s": [], "txts": [], "inners": [], "ivs": []}`,
	}
	inTx(t, conn, func() {
		for n, src := range rows {
			rowID := int32(100 + n)
			root := encNode(t, src)
			vals := make([]Node, len(tt.cols))
			mask := make([]int64, tt.maskWords)
			for i, c := range tt.cols {
				if v, ok := root.ObjectGet(c.name); ok {
					vals[i] = v
					slot, _ := rel.MaskSlotFor(rel.Columns[i])
					mask[slot.Word] |= 1 << slot.Bit
				}
			}
			b := tt.newBatch(true, 1)
			if err := b.addRow(rowMeta{RowID: rowID, NodeID: 1}, vals, mask, nil); err != nil {
				t.Fatalf("row %d: %v", n, err)
			}
			if err := load(t, conn, tt, true, b); err != nil {
				t.Fatalf("row %d: loading: %v", n, err)
			}
			if _, err := conn.Exec(ctx, `truncate enc_dest`); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Exec(ctx, "insert into enc_dest "+readBackSQL(tt, rel), rowID); err != nil {
				t.Fatalf("row %d: reading back: %v", n, err)
			}
			var ours, oracle []byte
			if err := conn.QueryRow(ctx, `select to_jsonb(d)::text from enc_dest d`).Scan(&ours); err != nil {
				t.Fatal(err)
			}
			// the oracle reads the same JSON through jsonb_populate_record, the way the current path does
			if err := conn.QueryRow(ctx, `select to_jsonb(jsonb_populate_record(null::enc_t, $1::jsonb))::text`, strings.NewReplacer(`"\\x00ff"`, `"\\x00ff"`).Replace(src)).Scan(&oracle); err != nil {
				t.Fatalf("row %d: the oracle rejects it: %v", n, err)
			}
			var a, o map[string]any
			_ = json.Unmarshal(ours, &a)
			_ = json.Unmarshal(oracle, &o)
			for col := range o {
				if fmt.Sprint(a[col]) != fmt.Sprint(o[col]) {
					t.Errorf("row %d, column %s: %v, the oracle gives %v", n, col, a[col], o[col])
				}
			}
			var gotMask int64
			if err := conn.QueryRow(ctx, `select __rel_mask0 from pg_temp.`+quoteIdent(tt.general.name)+` where __rel_row_id = $1`, rowID).Scan(&gotMask); err != nil {
				t.Fatal(err)
			}
			if gotMask != mask[0] {
				t.Errorf("row %d: mask %x, want %x", n, uint64(gotMask), uint64(mask[0]))
			}
		}
	})
}

// TestLoad_UnderASwitchedRole : the server runs a request as the caller's role (SET LOCAL ROLE), which has no
// privileges of its own on the pool user's temp tables.
func TestLoad_UnderASwitchedRole(t *testing.T) {
	ctx := context.Background()
	conn := tempTestConn(t)
	if _, err := conn.Exec(ctx, `do $$ begin create role enc_low_role nologin; exception when duplicate_object then null; end $$`); err != nil {
		t.Fatal(err)
	}
	tt, err := ensureTempTables(ctx, conn, fakeRelation(t, 990030, 2))
	if err != nil {
		t.Fatal(err)
	}
	// the role must also be allowed to use the pool user's own temp schema
	b := tt.newBatch(true, 1)
	if err := b.addRow(rowMeta{RowID: 1, NodeID: 1}, []Node{encNode(t, `5`), nil}, []int64{1}, nil); err != nil {
		t.Fatal(err)
	}
	inTx(t, conn, func() {
		if _, err := conn.Exec(ctx, `set local role enc_low_role`); err != nil {
			t.Fatal(err)
		}
		if err := load(t, conn, tt, true, b); err != nil {
			t.Fatalf("load as another role: %v", err)
		}
		var c0 int32
		if err := conn.QueryRow(ctx, `select c0 from `+tt.general.qualified()).Scan(&c0); err != nil || c0 != 5 {
			t.Errorf("read as another role: %d, %v", c0, err)
		}
	})
}

// TestEnsureTempTables_SchemaChange : a reload resets the pool (pg.ReIntrospect), so a table whose
// columns changed starts over on fresh connections, with no temp table built for its old columns.
func TestEnsureTempTables_SchemaChange(t *testing.T) {
	ctx := context.Background()
	for _, q := range []string{`drop table if exists public.layout_change_t`, `create table public.layout_change_t (id int primary key)`} {
		if _, err := testDb.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = testDb.Pool.Exec(context.Background(), `drop table if exists public.layout_change_t`) })

	reintrospect := func() *pg.Relation {
		infos, err := pg.ReIntrospect(ctx, testDbURI, testDb.Pool, "")
		if err != nil {
			t.Fatal(err)
		}
		r := infos.ResolveRelation("public", "layout_change_t")
		if r == nil {
			t.Fatal("layout_change_t was not introspected")
		}
		return r
	}
	conn := tempTestConn(t)
	before, err := ensureTempTables(ctx, conn, reintrospect())
	if err != nil {
		t.Fatal(err)
	}
	if len(before.cols) != 1 {
		t.Fatalf("columns = %d", len(before.cols))
	}

	if _, err := testDb.Pool.Exec(ctx, `alter table public.layout_change_t add column extra varchar(4)`); err != nil {
		t.Fatal(err)
	}
	rel := reintrospect() // resets the pool : the connection above is closed when released
	conn.Release()
	conn = tempTestConn(t)
	after, err := ensureTempTables(ctx, conn, rel)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.cols) != 2 {
		t.Fatalf("columns = %d", len(after.cols))
	}

	b := after.newBatch(true, 1)
	if err := b.addRow(rowMeta{RowID: 1, NodeID: 1}, []Node{encNode(t, `1`), encNode(t, `"abc"`)}, make([]int64, after.maskWords), nil); err != nil {
		t.Fatal(err)
	}
	inTx(t, conn, func() {
		if err := load(t, conn, after, true, b); err != nil {
			t.Fatalf("loading into the new layout: %v", err)
		}
		var extra string
		if err := conn.QueryRow(ctx, `select extra from `+after.general.qualified()).Scan(&extra); err != nil || extra != "abc" {
			t.Errorf("extra = %q (%v)", extra, err)
		}
	})
	// a value too long for the new column's declared type is refused by the receive function
	b = after.newBatch(true, 1)
	if err := b.addRow(rowMeta{RowID: 2, NodeID: 1}, []Node{encNode(t, `2`), encNode(t, `"too long"`)}, make([]int64, after.maskWords), nil); err != nil {
		t.Fatal(err)
	}
	inTx(t, conn, func() {
		if err := load(t, conn, after, true, b); pgCode(err) != "22001" {
			t.Errorf("code %q (%v), want 22001", pgCode(err), err)
		}
	})
}
