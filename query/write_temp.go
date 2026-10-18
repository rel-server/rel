// Typed temp tables of the write path (specs/typed-write-loading.md ## Temp row
// types, ## Connection lifecycle, ## Loading) : per destination table, one temp
// table holding the rows of a write request and one used only for its row type
// by the fast path, loaded from a single binary array parameter.
package query

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rel-server/rel/pg"
	"github.com/samber/oops"
)

// Postgres allows at most 1600 columns in a table.
const maxTempColumns = 1600

// tempColumn is one column of the destination table, as a temp table holds it.
type tempColumn struct {
	name   string
	decl   string // the temp column's type : the declared type in binary mode, text otherwise
	oid    uint32 // the oid of that type, which a record field must repeat
	binary bool
	enc    fieldEncoder
}

// tempType names one temp table and the oids of its row type and array type, known once it exists on a connection.
type tempType struct {
	name      string
	rowOID    uint32
	arrayOID  uint32
	typeNames [2]string // the names registered in the connection's type map
}

// tempTable describes the two temp tables of one destination table.
type tempTable struct {
	rel       *pg.Relation
	prefix    string // starts every metadata column name
	cols      []tempColumn
	colIdx    map[*pg.Column]int // position of each table column in cols
	maskWords int
	general   tempType
	fast      tempType
}

// metadata column names
func (t *tempTable) rowIDCol() string    { return t.prefix + "row_id" }
func (t *tempTable) nodeIDCol() string   { return t.prefix + "node_id" }
func (t *tempTable) parentIDCol() string { return t.prefix + "parent_id" }
func (t *tempTable) maskCol(word int) string {
	return fmt.Sprintf("%smask%d", t.prefix, word)
}

// newTempTable plans the temp tables of rel. It fails when the table has too many columns.
func newTempTable(rel *pg.Relation) (*tempTable, error) {
	t := &tempTable{rel: rel, prefix: "__rel_", maskWords: rel.MaskWords()}
	// Lengthen the prefix until no column of the table starts with it.
	for clash := true; clash; {
		clash = false
		for _, c := range rel.Columns {
			if strings.HasPrefix(c.Name, t.prefix) {
				clash = true
				t.prefix += "_"
				break
			}
		}
	}
	if n := 3 + len(rel.Columns) + t.maskWords; n > maxTempColumns {
		return nil, oops.With("relation", rel.Identifier.String(), "columns", len(rel.Columns)).
			Errorf("write: %s has too many columns to write through temp tables (%d, the limit is %d)", rel.Identifier.String(), len(rel.Columns), maxTempColumns-3-t.maskWords)
	}
	t.colIdx = make(map[*pg.Column]int, len(rel.Columns))
	for i, c := range rel.Columns {
		t.colIdx[c] = i
		enc, binary := columnEncoder(c.Type)
		tc := tempColumn{name: c.Name, enc: enc, binary: binary, decl: "text", oid: oidText}
		if binary {
			tc.decl, tc.oid = c.DeclaredType, uint32(c.Type.PgOid)
		}
		t.cols = append(t.cols, tc)
	}
	// The layout is part of the name : a table whose columns changed gets new temp tables, and the ones
	// built for its old shape stay unused until the connection closes.
	sig := t.layoutSignature()
	t.general.name = fmt.Sprintf("__rel_w_%d_%s", rel.PgRelId, sig)
	t.fast.name = fmt.Sprintf("__rel_f_%d_%s", rel.PgRelId, sig)
	for _, tt := range []*tempType{&t.general, &t.fast} {
		tt.typeNames = [2]string{"rel_" + tt.name, "_rel_" + tt.name}
	}
	return t, nil
}

// layoutSignature hashes what decides the shape of the temp tables : the metadata prefix, each
// column's name and type, and the number of mask words.
func (t *tempTable) layoutSignature() string {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s|%d", t.prefix, t.maskWords)
	for _, c := range t.cols {
		fmt.Fprintf(h, "|%s %s", c.name, c.decl)
	}
	return fmt.Sprintf("%08x", h.Sum64()&0xffffffff)
}

func quoteIdent(name string) string { return pgx.Identifier{name}.Sanitize() }

func (tt *tempType) qualified() string { return "pg_temp." + quoteIdent(tt.name) }

// keepRowsAtCommit makes the general temp tables created from now on keep their rows past a commit.
// Only tests set it : they run each statement in its own transaction and read the rows back
// afterwards, while the server writes and reads back within one transaction.
var keepRowsAtCommit = false

// createStatements returns the statements creating both temp tables and granting them to public.
func (t *tempTable) createStatements() []string {
	var data, masks strings.Builder
	for i, c := range t.cols {
		if i > 0 {
			data.WriteString(", ")
		}
		data.WriteString(quoteIdent(c.name) + " " + c.decl)
	}
	for w := 0; w < t.maskWords; w++ {
		masks.WriteString(", " + quoteIdent(t.maskCol(w)) + " int8")
	}
	meta := fmt.Sprintf("%s int4, %s int4, %s int4, ", quoteIdent(t.rowIDCol()), quoteIdent(t.nodeIDCol()), quoteIdent(t.parentIDCol()))
	onCommit := "delete rows"
	if keepRowsAtCommit {
		onCommit = "preserve rows"
	}
	return []string{
		fmt.Sprintf("create temp table if not exists %s (%s%s%s) on commit %s", quoteIdent(t.general.name), meta, data.String(), masks.String(), onCommit),
		fmt.Sprintf("create temp table if not exists %s (%s%s)", quoteIdent(t.fast.name), data.String(), masks.String()),
		"grant all on " + t.general.qualified() + " to public",
		"grant all on " + t.fast.qualified() + " to public",
	}
}

// lookupSQL returns the one statement reading the oids of both row types and their array types. It
// reads pg_type rather than casting a name to regtype : Postgres resolves such a cast when it
// prepares the statement, before the batch has created the temp schema.
func (t *tempTable) lookupSQL() string {
	one := func(name string) string {
		return fmt.Sprintf("(select oid from pg_type where typname = '%s' and typnamespace = pg_my_temp_schema())", name)
	}
	return "select " + strings.Join([]string{
		one(t.general.name), one("_" + t.general.name), one(t.fast.name), one("_" + t.fast.name),
	}, ", ")
}

// ---- raw binary parameters -----------------------------------------------------------

// rawBinary is a parameter value already in Postgres' binary format. A connection can send it for
// the array types of its temp tables once ensureTempTables has registered them.
type rawBinary []byte

// String, MarshalText and friends keep a debug log from printing megabytes of rows.
func (r rawBinary) String() string { return fmt.Sprintf("<%d bytes of binary rows>", len(r)) }

// MarshalText makes JSON and text log handlers print the same short description.
func (r rawBinary) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

type rawBinaryCodec struct{}

func (rawBinaryCodec) FormatSupported(f int16) bool { return f == pgtype.BinaryFormatCode }
func (rawBinaryCodec) PreferredFormat() int16       { return pgtype.BinaryFormatCode }
func (rawBinaryCodec) PlanEncode(_ *pgtype.Map, _ uint32, format int16, value any) pgtype.EncodePlan {
	if _, ok := value.(rawBinary); ok && format == pgtype.BinaryFormatCode {
		return rawBinaryPlan{}
	}
	return nil
}
func (rawBinaryCodec) PlanScan(*pgtype.Map, uint32, int16, any) pgtype.ScanPlan { return nil }
func (rawBinaryCodec) DecodeDatabaseSQLValue(*pgtype.Map, uint32, int16, []byte) (driver.Value, error) {
	return nil, oops.Errorf("write: a temp row type cannot be read back as a Go value")
}
func (rawBinaryCodec) DecodeValue(*pgtype.Map, uint32, int16, []byte) (any, error) {
	return nil, oops.Errorf("write: a temp row type cannot be read back as a Go value")
}

type rawBinaryPlan struct{}

func (rawBinaryPlan) Encode(value any, buf []byte) ([]byte, error) {
	return append(buf, value.(rawBinary)...), nil
}

// ---- lifecycle ----------------------------------------------------------------------

// pgxConnOf finds the *pgx.Conn behind a Querier : a *pgx.Conn itself, or a *pgxpool.Conn.
func pgxConnOf(q Querier) (*pgx.Conn, bool) {
	switch c := q.(type) {
	case *pgx.Conn:
		return c, true
	case interface{ Conn() *pgx.Conn }:
		return c.Conn(), true
	}
	return nil, false
}

// ensureTempTables makes sure rel's temp tables exist on conn and that conn can send parameters of
// their types, creating them on first use and doing nothing after. The connection's type map is the
// record of what is ready, so it goes with the connection. It must run outside a transaction : a
// rolled back transaction would take the tables away and leave the record stale.
func ensureTempTables(ctx context.Context, q Querier, rel *pg.Relation) (*tempTable, error) {
	t, err := newTempTable(rel)
	if err != nil {
		return nil, err
	}
	conn, ok := pgxConnOf(q)
	if !ok {
		return nil, oops.Errorf("write: the connection cannot register the temp row types")
	}
	m := conn.TypeMap()
	if known, ok := m.TypeForName(t.general.typeNames[1]); ok {
		fast, _ := m.TypeForName(t.fast.typeNames[1])
		t.general.arrayOID, t.fast.arrayOID = known.OID, fast.OID
		g, _ := m.TypeForName(t.general.typeNames[0])
		f, _ := m.TypeForName(t.fast.typeNames[0])
		t.general.rowOID, t.fast.rowOID = g.OID, f.OID
		return t, nil
	}
	if conn.PgConn().TxStatus() != 'I' {
		return nil, oops.With("relation", rel.Identifier.String()).Errorf("write: temp tables must be created outside a transaction")
	}

	var b pgx.Batch
	for _, s := range t.createStatements() {
		b.Queue(s)
	}
	b.Queue(t.lookupSQL())
	res := conn.SendBatch(ctx, &b)
	defer res.Close()
	for range t.createStatements() {
		if _, err := res.Exec(); err != nil {
			return nil, oops.With("relation", rel.Identifier.String()).Wrapf(err, "write: creating temp tables")
		}
	}
	var gRow, gArr, fRow, fArr uint32
	if err := res.QueryRow().Scan(&gRow, &gArr, &fRow, &fArr); err != nil {
		return nil, oops.With("relation", rel.Identifier.String()).Wrapf(err, "write: reading temp row types")
	}
	t.general.rowOID, t.general.arrayOID, t.fast.rowOID, t.fast.arrayOID = gRow, gArr, fRow, fArr
	for _, tt := range []tempType{t.general, t.fast} {
		m.RegisterType(&pgtype.Type{Name: tt.typeNames[0], OID: tt.rowOID, Codec: rawBinaryCodec{}})
		m.RegisterType(&pgtype.Type{Name: tt.typeNames[1], OID: tt.arrayOID, Codec: rawBinaryCodec{}})
	}
	return t, nil
}

// ---- loading ------------------------------------------------------------------------

// loadSQL returns the statement putting a rawBinary array of general (or fast) rows into its temp table.
func (t *tempTable) loadSQL(general bool) string {
	tt := t.fast
	if general {
		tt = t.general
	}
	q := tt.qualified()
	return fmt.Sprintf("insert into %s select * from unnest($1::%s[])", q, q)
}

// rowMeta is the metadata of a general row. ParentID is only meaningful when HasParent is set.
type rowMeta struct {
	RowID, NodeID, ParentID int32
	HasParent               bool
}

// rowBatch builds the binary array of rows to load into one temp table.
type rowBatch struct {
	t       *tempTable
	general bool
	buf     []byte
	count   int32
}

const arrayCountOffset = 12 // ndim, flags, element oid, then the size of the one dimension

// newBatch starts a batch of general rows, or fast rows. sizeHint is the expected number of rows.
func (t *tempTable) newBatch(general bool, sizeHint int) *rowBatch {
	tt := t.fast
	if general {
		tt = t.general
	}
	b := &rowBatch{t: t, general: general, buf: make([]byte, 0, 20+sizeHint*(16+8*len(t.cols)))}
	b.buf = appendInt32(b.buf, 1) // one dimension
	b.buf = appendInt32(b.buf, 0) // no NULL elements : rows are never NULL
	b.buf = binaryAppendUint32(b.buf, tt.rowOID)
	b.buf = appendInt32(b.buf, 0) // size, patched by param
	b.buf = appendInt32(b.buf, 1) // lower bound
	return b
}

// addRow appends a row : vals holds a Node per column of the table (nil, or a null, for a column the
// row lacks) and mask the presence words. meta is ignored for fast rows.
func (b *rowBatch) addRow(meta rowMeta, vals []Node, mask []int64, ec *encodeContext) error {
	t := b.t
	if len(vals) != len(t.cols) || len(mask) != t.maskWords {
		return oops.Errorf("write: a row has %d values and %d mask words, the table needs %d and %d", len(vals), len(mask), len(t.cols), t.maskWords)
	}
	start := len(b.buf)
	b.buf = append(b.buf, 0, 0, 0, 0)
	nfields := len(t.cols) + t.maskWords
	if b.general {
		nfields += 3
	}
	b.buf = appendInt32(b.buf, int32(nfields))
	if b.general {
		b.buf = appendIntField(b.buf, oidInt4, int64(meta.RowID), 4)
		b.buf = appendIntField(b.buf, oidInt4, int64(meta.NodeID), 4)
		if meta.HasParent {
			b.buf = appendIntField(b.buf, oidInt4, int64(meta.ParentID), 4)
		} else {
			b.buf = appendNullField(b.buf, oidInt4)
		}
	}
	for i, c := range t.cols {
		v := vals[i]
		if v == nil || v.Kind() == KindNull {
			b.buf = appendNullField(b.buf, c.oid)
			continue
		}
		b.buf = binaryAppendUint32(b.buf, c.oid)
		var err error
		if b.buf, err = appendLengthPrefixed(b.buf, v, ec, c.enc); err != nil {
			var pe *pgconn.PgError
			if errors.As(err, &pe) { // name what the client sent, as Postgres would
				pe.ColumnName, pe.TableName, pe.SchemaName = c.name, t.rel.Identifier.Name, t.rel.Identifier.Schema
			}
			return oops.With("column", c.name).Wrapf(err, "write: column %q", c.name)
		}
	}
	for _, w := range mask {
		b.buf = appendIntField(b.buf, oidInt8, w, 8)
	}
	putInt32(b.buf[start:], int32(len(b.buf)-start-4))
	b.count++
	return nil
}

func appendNullField(dst []byte, oid uint32) []byte {
	dst = binaryAppendUint32(dst, oid)
	return appendInt32(dst, -1)
}

func appendIntField(dst []byte, oid uint32, v int64, size int) []byte {
	dst = binaryAppendUint32(dst, oid)
	dst = appendInt32(dst, int32(size))
	if size == 4 {
		return appendInt32(dst, int32(v))
	}
	return appendInt64(dst, v)
}

// Len returns how many rows the batch holds.
func (b *rowBatch) Len() int { return int(b.count) }

// param returns the parameter to send to loadSQL. The batch must not be added to afterwards.
func (b *rowBatch) param() rawBinary {
	if b.count == 0 { // an array with no element has no dimension
		head := b.buf[:0:0]
		head = appendInt32(head, 0)
		head = appendInt32(head, 0)
		return rawBinary(binaryAppendUint32(head, binaryUint32(b.buf[8:12])))
	}
	putInt32(b.buf[arrayCountOffset:], b.count)
	return rawBinary(b.buf)
}

func binaryUint32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// ---- SQL references ------------------------------------------------------------------

// colRef is the SQL expression for table column col of a row of this temp table seen through
// alias, as a value of the column's own type : a text-mode column is cast to it without modifiers
// (the statement that assigns the value then applies them).
func (t *tempTable) colRef(alias string, col *pg.Column) string {
	ref := alias + "." + quoteIdent(col.Name)
	if t.cols[t.colIdx[col]].binary {
		return ref
	}
	return "(" + ref + "::" + col.Type.PgIdentifier.EscapedString() + ")"
}

// rawColRef is colRef without the cast, for a NULL test.
func (t *tempTable) rawColRef(alias string, col *pg.Column) string {
	return alias + "." + quoteIdent(col.Name)
}

// fieldPath selects the composite sub-fields path out of the SQL expression base.
func fieldPath(base string, path []*pg.Column) string {
	for _, c := range path {
		base = "(" + base + ")." + quoteIdent(c.Name)
	}
	return base
}

// cpRef is colRef for a column path : a plain column, or a sub-field of a composite one.
func (t *tempTable) cpRef(alias string, path []*pg.Column) string {
	return fieldPath(t.colRef(alias, path[0]), path[1:])
}

// maskWordRef is the SQL reference to mask word w.
func (t *tempTable) maskWordRef(alias string, w int) string {
	return alias + "." + quoteIdent(t.maskCol(w))
}

// maskBitLiteral is the SQL integer literal with only bit set.
func maskBitLiteral(bit int) string {
	if bit == 63 {
		return "(-9223372036854775807-1)"
	}
	return fmt.Sprintf("%d", int64(1)<<bit)
}

// maskTest is the SQL condition that the row seen through alias supplied the slot.
func (t *tempTable) maskTest(alias string, slot pg.MaskSlot) string {
	return fmt.Sprintf("(%s & %s::int8) <> 0", t.maskWordRef(alias, slot.Word), maskBitLiteral(slot.Bit))
}

// qualifiedGeneral is the general temp table's name as SQL.
func (t *tempTable) qualifiedGeneral() string { return t.general.qualified() }

// rowIDRef, nodeIDRef and parentIDRef are the metadata columns seen through alias.
func (t *tempTable) rowIDRef(alias string) string  { return alias + "." + quoteIdent(t.rowIDCol()) }
func (t *tempTable) nodeIDRef(alias string) string { return alias + "." + quoteIdent(t.nodeIDCol()) }
func (t *tempTable) parentIDRef(alias string) string {
	return alias + "." + quoteIdent(t.parentIDCol())
}

// assignKey is the "col = src.col" assignment writing a generated key back into a temp row ; a
// text-mode column stores its text.
func (t *tempTable) assignKey(col *pg.Column, src string) string {
	v := src + "." + quoteIdent(col.Name)
	if !t.cols[t.colIdx[col]].binary {
		v += "::text"
	}
	return quoteIdent(col.Name) + " = " + v
}
