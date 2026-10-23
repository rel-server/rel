package query

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/rel-server/rel/cborenc"
	"github.com/rel-server/rel/pg"
	"github.com/samber/oops"
)

// Native CBOR results (specs/cbor.md ## 2). In CBOR mode the read compiler returns the rows as records and
// arrays of records instead of JSON text (OutputFormat), and a ResultEncoder turns the binary form Postgres sends them in
// into CBOR, a value at a time, as the rows stream. Every field of a record carries its own type, so the
// encoder needs no per-column setup : a field named by the query's shape is a map key, and the type decides
// the CBOR item.

// OutputFormat is how a query's rows are produced.
type OutputFormat uint8

const (
	// FormatJSON is JSON text, built by Postgres (row_to_json, json_agg).
	FormatJSON OutputFormat = iota
	// FormatCBOR is records and arrays of records, in Postgres' binary format, read by a ResultEncoder.
	FormatCBOR
)

// Type OIDs the result encoder knows beside the ones write_encode.go names.
// RecordOID is the type of a row of a FormatCBOR statement : a record.
const RecordOID = 2249

const (
	oidChar        = 18
	oidOid         = 26
	oidRecord      = RecordOID
	oidRecordArray = 2287
	oidVoid        = 2278
)

const (
	infDate       = math.MaxInt32
	negInfDate    = math.MinInt32
	infTimestamp  = math.MaxInt64
	negInfTimeste = math.MinInt64
)

// ResultEncoder turns the binary values of a query's result into CBOR.
type ResultEncoder struct {
	db      *pg.DbInfos
	plan    *recordPlan
	ec      *encodeContext  // the session time zone a timestamp is read in ; nil until BindSession, which means UTC
	structs *StructureTable // nil writes every record as a map
}

// BindSession tells the encoder which connection's rows it will read : a timestamp is answered as the instant its
// wall clock stands for in that connection's TimeZone. It fails when that zone is one rel cannot load, so the
// caller can refuse the request before any row is sent.
func (e *ResultEncoder) BindSession(q Querier) error {
	ec := encodeContextFor(q)
	if ec.zoneErr != nil {
		return errUnknownZone("timestamp", "a timestamp column", ec.zoneName)
	}
	e.ec = ec
	return nil
}

// recordPlan names the fields of the records a node produces, and which of them hold nested records.
type recordPlan struct {
	names    []string
	children []*recordPlan // per field : the plan of the record, or array of records, it holds ; nil for any other
	structID int           // the node's structure in a StructureTable ; -1 when records are written as maps
}

// StructureTable gives the record layouts of one response their ids (specs/cbor.md ## Structures). It is shared by
// the encoders of every item of the response, so an id means one list of names from the first byte to the last, and
// it is written to by one goroutine at a time.
type StructureTable struct {
	ids     map[string]int // a list of names, by the id it was given
	defined []bool         // per id : its definition has been written
	names   [][]string
}

// NewStructureTable returns an empty table. Pass it to NewResultEncoder to have records written as structures.
func NewStructureTable() *StructureTable { return &StructureTable{ids: map[string]int{}} }

// idFor returns the id of a list of names, giving it the next one when it is new, or -1 once the ids run out.
func (t *StructureTable) idFor(names []string) int {
	key := strings.Join(names, "\x00")
	if id, ok := t.ids[key]; ok {
		return id
	}
	if len(t.names) >= cborenc.MaxStructures {
		return -1
	}
	id := len(t.names)
	t.ids[key] = id
	t.names = append(t.names, names)
	t.defined = append(t.defined, false)
	return id
}

// NewResultEncoder prepares to encode the rows of the query rooted at root. db resolves the types the rows hold. A
// non-nil structures makes it write the records of the query's nodes as structures, ids coming from that table.
func NewResultEncoder(db *pg.DbInfos, root *QueryNode, structures *StructureTable) (*ResultEncoder, error) {
	plan, err := planFor(root, structures)
	if err != nil {
		return nil, err
	}
	return &ResultEncoder{db: db, plan: plan, structs: structures}, nil
}

// NewValueEncoder is a ResultEncoder for lone values, as a function returns, rather than the rows of a query : a
// record has fields named by its type, or f1, f2 ... when anonymous.
func NewValueEncoder(db *pg.DbInfos) *ResultEncoder { return &ResultEncoder{db: db} }

// NativeCBORType reports whether a value of type t is encoded by a ResultEncoder, rather than through its JSON form.
func NativeCBORType(t *pg.Type) bool { return nativeCBORType(t) }

// planFor builds the record plan of node, nil when node's value is not a record (a scalar select). Its nodes take
// their structure ids from structures, when there is one.
func planFor(node *QueryNode, structures *StructureTable) (*recordPlan, error) {
	if !isShapeProducingSelect(node.Select) || (node.IsFunction() && !node.Function.ReturnsSet && node.Relation == nil) {
		return nil, nil
	}
	fields, err := selectFieldsFor(node)
	if err != nil {
		return nil, err
	}
	p := &recordPlan{names: make([]string, len(fields)), children: make([]*recordPlan, len(fields)), structID: -1}
	for i, f := range fields {
		p.names[i] = f.key
		if child := embedChildOf(f); child != nil {
			if p.children[i], err = planFor(child, structures); err != nil {
				return nil, err
			}
		}
	}
	if structures != nil {
		p.structID = structures.idFor(p.names)
	}
	return p, nil
}

// AppendValue appends the CBOR for one value of type oid in Postgres' binary format. A NULL is nil data.
func (e *ResultEncoder) AppendValue(dst []byte, oid uint32, data []byte) ([]byte, error) {
	if data == nil {
		return cborenc.AppendNull(dst), nil
	}
	return e.appendValue(dst, oid, data, e.plan)
}

func errShort(what string) error { return oops.Errorf("cbor result: %s is truncated", what) }

// appendValue's scalar cases must be the scalar OIDs nativeCBORType admits (TestResultCBOR_EveryNativeScalarHasAnEncoder
// checks it). They stay a switch and not one table both read from : a table of readers measured 21% slower on the
// per-value dispatch (BenchmarkAppendValue_Scalars), and this runs for every value of every row.
func (e *ResultEncoder) appendValue(dst []byte, oid uint32, d []byte, plan *recordPlan) ([]byte, error) {
	switch oid {
	case oidBool:
		if len(d) != 1 {
			return dst, errShort("a boolean")
		}
		return cborenc.AppendBool(dst, d[0] != 0), nil
	case oidInt2:
		if len(d) != 2 {
			return dst, errShort("an int2")
		}
		return cborenc.AppendInt(dst, int64(int16(binary.BigEndian.Uint16(d)))), nil
	case oidInt4:
		if len(d) != 4 {
			return dst, errShort("an int4")
		}
		return cborenc.AppendInt(dst, int64(int32(binary.BigEndian.Uint32(d)))), nil
	case oidInt8:
		if len(d) != 8 {
			return dst, errShort("an int8")
		}
		return cborenc.AppendInt(dst, int64(binary.BigEndian.Uint64(d))), nil
	case oidOid:
		if len(d) != 4 {
			return dst, errShort("an oid")
		}
		return cborenc.AppendUint(dst, uint64(binary.BigEndian.Uint32(d))), nil
	case oidFloat4:
		if len(d) != 4 {
			return dst, errShort("a float4")
		}
		return append(dst, 0xfa, d[0], d[1], d[2], d[3]), nil // a single : the value the column holds, exactly
	case oidFloat8:
		if len(d) != 8 {
			return dst, errShort("a float8")
		}
		return cborenc.AppendFloat64(dst, math.Float64frombits(binary.BigEndian.Uint64(d))), nil
	case oidNumeric:
		return appendNumericCBOR(dst, d)
	case oidText, oidVarchar, oidBpchar, oidName, oidChar:
		return cborenc.AppendTextBytes(dst, d), nil
	case oidBytea:
		return cborenc.AppendBytes(dst, d), nil
	case oidUUID:
		if len(d) != 16 {
			return dst, errShort("a uuid")
		}
		return cborenc.AppendUUID(dst, [16]byte(d)), nil
	case oidDate:
		if len(d) != 4 {
			return dst, errShort("a date")
		}
		days := int32(binary.BigEndian.Uint32(d))
		switch days {
		case infDate:
			return cborenc.AppendText(dst, "infinity"), nil
		case negInfDate:
			return cborenc.AppendText(dst, "-infinity"), nil
		}
		return cborenc.AppendEpochDate(dst, int64(days)+pgEpochUnixDays), nil
	case oidTimestamptz:
		if len(d) != 8 {
			return dst, errShort("a timestamptz")
		}
		micros := int64(binary.BigEndian.Uint64(d))
		switch micros {
		case infTimestamp:
			return cborenc.AppendText(dst, "infinity"), nil
		case negInfTimeste:
			return cborenc.AppendText(dst, "-infinity"), nil
		}
		sec := floorDiv(micros, microsPerSecond)
		return cborenc.AppendEpoch(dst, sec+pgEpochUnixDays*86400, int(micros-sec*microsPerSecond)), nil
	case oidTimestamp:
		if len(d) != 8 {
			return dst, errShort("a timestamp")
		}
		micros := int64(binary.BigEndian.Uint64(d))
		switch micros {
		case infTimestamp:
			return cborenc.AppendText(dst, "infinity"), nil
		case negInfTimeste:
			return cborenc.AppendText(dst, "-infinity"), nil
		}
		// A wall clock, read as the instant it stands for in the session zone (what ts::timestamptz gives).
		wall := floorDiv(micros, microsPerSecond)
		off := zoneOffsetAt(e.ec.location(), wall)
		micros -= off * microsPerSecond
		sec := floorDiv(micros, microsPerSecond)
		return cborenc.AppendEpoch(dst, sec+pgEpochUnixDays*86400, int(micros-sec*microsPerSecond)), nil
	case oidJSON:
		return appendJSONCBOR(dst, d)
	case oidJSONB:
		if len(d) == 0 || d[0] != 1 {
			return dst, oops.Errorf("cbor result: a jsonb value of an unknown version")
		}
		return appendJSONCBOR(dst, d[1:])
	case oidRecord:
		return e.appendRecord(dst, d, plan, nil)
	case oidRecordArray:
		return e.appendArray(dst, d, plan)
	case oidVoid:
		return cborenc.AppendNull(dst), nil
	}

	t := e.db.TypeMapByOid[int(oid)]
	if t == nil {
		return dst, oops.With("oid", oid).Errorf("cbor result: a value of unknown type %d cannot be encoded", oid)
	}
	if u := t.Underlying(); u != t {
		return e.appendValue(dst, uint32(u.PgOid), d, plan) // a domain is its base type
	}
	switch {
	case t.IsEnum():
		return cborenc.AppendTextBytes(dst, d), nil
	case t.IsArray():
		return e.appendArray(dst, d, plan)
	case t.IsComposite():
		return e.appendRecord(dst, d, nil, t.Relation)
	}
	return dst, oops.With("type", t.PgIdentifier.String()).Errorf("cbor result: type %s has no CBOR form : cast it to text, or select it as a column so rel can convert it", t.PgIdentifier.String())
}

func appendJSONCBOR(dst, text []byte) ([]byte, error) {
	out, err := cborenc.AppendJSON(dst, text)
	if err != nil {
		return dst, oops.Wrapf(err, "cbor result: a json value")
	}
	return out, nil
}

// appendRecord appends a binary record as a map. The keys are the plan's names, the attributes of rel for a named
// composite type, or f1, f2, … as Postgres names the fields of an anonymous record.
func (e *ResultEncoder) appendRecord(dst, d []byte, plan *recordPlan, rel *pg.Relation) ([]byte, error) {
	if len(d) < 4 {
		return dst, errShort("a record")
	}
	n := int(int32(binary.BigEndian.Uint32(d)))
	d = d[4:]
	if n < 0 || n > len(d)/8+1 {
		return dst, errShort("a record")
	}
	if plan != nil && len(plan.names) != n {
		return dst, oops.Errorf("cbor result: a record has %d fields, its query selects %d", n, len(plan.names))
	}
	asStruct := plan != nil && plan.structID >= 0
	switch {
	case !asStruct:
		dst = cborenc.AppendMapHead(dst, n)
	case e.structs.defined[plan.structID]:
		dst = cborenc.AppendStructureRef(dst, plan.structID, n)
	default:
		dst = cborenc.AppendStructureDef(dst, plan.structID, plan.names)
		e.structs.defined[plan.structID] = true
	}
	for i := 0; i < n; i++ {
		if len(d) < 8 {
			return dst, errShort("a record")
		}
		oid := binary.BigEndian.Uint32(d)
		size := int(int32(binary.BigEndian.Uint32(d[4:])))
		d = d[8:]

		var name string
		var child *recordPlan
		switch {
		case plan != nil:
			name, child = plan.names[i], plan.children[i]
		case rel != nil && i < len(rel.Columns):
			name = rel.Columns[i].Name
		default:
			name = "f" + strconv.Itoa(i+1)
		}
		if !asStruct {
			dst = cborenc.AppendText(dst, name)
		}

		if size < 0 {
			dst = cborenc.AppendNull(dst)
			continue
		}
		if size > len(d) {
			return dst, errShort("a record field")
		}
		var err error
		if dst, err = e.appendValue(dst, oid, d[:size], child); err != nil {
			return dst, oops.With("field", name).Wrap(err)
		}
		d = d[size:]
	}
	return dst, nil
}

// appendArray appends a binary array as nested arrays, one level per dimension. plan applies to elements that are records.
func (e *ResultEncoder) appendArray(dst, d []byte, plan *recordPlan) ([]byte, error) {
	if len(d) < 12 {
		return dst, errShort("an array")
	}
	ndim := int(int32(binary.BigEndian.Uint32(d)))
	elem := binary.BigEndian.Uint32(d[8:])
	d = d[12:]
	if ndim < 0 || ndim > 6 || len(d) < ndim*8 {
		return dst, errShort("an array")
	}
	if ndim == 0 {
		return cborenc.AppendArrayHead(dst, 0), nil
	}
	sizes := make([]int, ndim)
	total := 1
	for i := range sizes {
		sizes[i] = int(int32(binary.BigEndian.Uint32(d[i*8:])))
		if sizes[i] < 0 || (sizes[i] > 0 && total > math.MaxInt32/sizes[i]) {
			return dst, errShort("an array")
		}
		total *= sizes[i]
	}
	d = d[ndim*8:]
	if total > len(d)/4 {
		return dst, errShort("an array")
	}
	var walk func(level int) ([]byte, error)
	walk = func(level int) ([]byte, error) {
		dst = cborenc.AppendArrayHead(dst, sizes[level])
		for i := 0; i < sizes[level]; i++ {
			if level < ndim-1 {
				if _, err := walk(level + 1); err != nil {
					return dst, err
				}
				continue
			}
			if len(d) < 4 {
				return dst, errShort("an array")
			}
			size := int(int32(binary.BigEndian.Uint32(d)))
			d = d[4:]
			if size < 0 {
				dst = cborenc.AppendNull(dst)
				continue
			}
			if size > len(d) {
				return dst, errShort("an array element")
			}
			var err error
			if dst, err = e.appendValue(dst, elem, d[:size], plan); err != nil {
				return dst, err
			}
			d = d[size:]
		}
		return dst, nil
	}
	return walk(0)
}

// ---- numeric ------------------------------------------------------------------------

const (
	numericSignNaN    = 0xC000
	numericSignPosInf = 0xD000
	numericSignNegInf = 0xF000
	numericSignNeg    = 0x4000
)

// appendNumericCBOR appends a numeric as a decimal fraction that keeps its display scale ; NaN and the
// infinities, which a decimal fraction cannot hold, are floats.
func appendNumericCBOR(dst, d []byte) ([]byte, error) {
	if len(d) < 8 {
		return dst, errShort("a numeric")
	}
	ndigits := int(binary.BigEndian.Uint16(d))
	weight := int(int16(binary.BigEndian.Uint16(d[2:])))
	sign := binary.BigEndian.Uint16(d[4:])
	dscale := int(binary.BigEndian.Uint16(d[6:]) & 0x3fff)
	d = d[8:]
	switch sign {
	case numericSignNaN:
		return cborenc.AppendFloat64(dst, math.NaN()), nil
	case numericSignPosInf:
		return cborenc.AppendFloat64(dst, math.Inf(1)), nil
	case numericSignNegInf:
		return cborenc.AppendFloat64(dst, math.Inf(-1)), nil
	}
	if len(d) < ndigits*2 {
		return dst, errShort("a numeric")
	}
	if ndigits == 0 {
		return cborenc.AppendDecimal(dst, "0", -int64(dscale)), nil
	}
	// groups of four decimal digits from weight down to the last one that dscale reaches
	after := (dscale + 3) / 4
	top := max(weight, 0)
	var b strings.Builder
	if sign == numericSignNeg {
		b.WriteByte('-')
	}
	groups := top + 1 + after
	if groups > 40000 {
		return dst, oops.Errorf("cbor result: a numeric too large to encode")
	}
	for g := top; g >= -after; g-- {
		i := weight - g
		var v int
		if i >= 0 && i < ndigits {
			v = int(binary.BigEndian.Uint16(d[i*2:]))
		}
		fmt.Fprintf(&b, "%04d", v)
	}
	s := b.String()
	// the digits past dscale are zeros that only padded the last group
	if cut := 4*after - dscale; cut > 0 {
		s = s[:len(s)-cut]
	}
	return cborenc.AppendDecimal(dst, s, -int64(dscale)), nil
}

// ---- dates and times ----------------------------------------------------------------

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// nativeCBORType's scalar list must be the scalar cases of appendValue (see there).
//
// nativeCBORType reports whether a value of type t has a CBOR form of its own : the scalar types the encoder maps,
// enums, domains of those, and arrays and composites made only of them. A column of any other type is read
// as its JSON form (to_jsonb) when the rows are produced in CBOR.
func nativeCBORType(t *pg.Type) bool { return nativeCBORTypeDepth(t, 0) }

func nativeCBORTypeDepth(t *pg.Type, depth int) bool {
	u := t.Underlying()
	if u == nil || depth > 16 {
		return false
	}
	switch u.PgOid {
	case oidBool, oidBytea, oidChar, oidName, oidInt8, oidInt2, oidInt4, oidText, oidOid, oidJSON, oidFloat4, oidFloat8,
		oidBpchar, oidVarchar, oidDate, oidTimestamp, oidTimestamptz, oidNumeric, oidUUID, oidJSONB:
		return true
	}
	switch {
	case u.IsEnum():
		return true
	case u.IsArray():
		return nativeCBORTypeDepth(u.ElementType, depth+1)
	case u.IsComposite():
		for _, col := range u.Relation.Columns {
			if !nativeCBORTypeDepth(col.Type, depth+1) {
				return false
			}
		}
		return true
	}
	return false
}
