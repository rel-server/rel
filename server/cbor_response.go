package server

import (
	"encoding/json"
	"io"

	"github.com/jackc/pgx/v5"
	"github.com/rel-server/rel/cborenc"
	"github.com/rel-server/rel/query"
)

// CBOR responses (specs/cbor.md ## Responses) : the same shapes as the JSON ones, with rows read from Postgres in
// its binary format and turned into CBOR by a query.ResultEncoder, never through JSON. A resolvedItem whose enc is
// non-nil is answered in CBOR.

// withBinaryResults prefixes args with the request for binary result columns, which pgx reads off the arguments.
func withBinaryResults(args []any) []any {
	out := make([]any, 0, len(args)+1)
	out = append(out, pgx.QueryResultFormats{pgx.BinaryFormatCode})
	return append(out, args...)
}

// queryArgs is args, asking for binary results when the item is answered in CBOR.
func (ri *resolvedItem) queryArgs(args []any) []any {
	if ri.enc == nil {
		return args
	}
	return withBinaryResults(args)
}

// copyRaw returns a copy of the current row's first column in its wire format, nil for NULL.
func copyRaw(rows pgx.Rows) []byte {
	raw := rows.RawValues()[0]
	if raw == nil {
		return nil
	}
	return append([]byte{}, raw...)
}

// streamRowsCBOR is streamRows for CBOR : an indefinite array, so the rows stream, or an empty definite one.
func streamRowsCBOR(w io.Writer, rows pgx.Rows, enc *query.ResultEncoder, cleanErrorPossible bool) error {
	hasFirst := rows.Next()
	if err := rows.Err(); err != nil {
		if cleanErrorPossible {
			return &cleanStreamError{err}
		}
		return err
	}
	if !hasFirst {
		_, err := w.Write(cborenc.AppendArrayHead(nil, 0))
		return err
	}
	if _, err := w.Write([]byte{cborenc.IndefiniteArray}); err != nil {
		return err
	}
	oid := rows.FieldDescriptions()[0].DataTypeOID
	var buf []byte
	for {
		var err error
		if buf, err = enc.AppendValue(buf[:0], oid, rows.RawValues()[0]); err != nil {
			return err
		}
		if _, err = w.Write(buf); err != nil {
			return err
		}
		if !rows.Next() {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err := w.Write([]byte{cborenc.Break})
	return err
}

// writeScalarCBOR writes a scalar function root's one value, null when the function returned no row.
func writeScalarCBOR(w io.Writer, rows pgx.Rows, enc *query.ResultEncoder) error {
	var out []byte
	if !rows.Next() {
		out = cborenc.AppendNull(nil)
	} else {
		var err error
		if out, err = enc.AppendValue(nil, rows.FieldDescriptions()[0].DataTypeOID, rows.RawValues()[0]); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err := w.Write(out)
	return err
}

// writeBufferedRowsCBOR is writeBufferedRows for rows kept as Postgres binary records.
func writeBufferedRowsCBOR(w io.Writer, rows [][]byte, enc *query.ResultEncoder) error {
	buf := cborenc.AppendArrayHead(nil, len(rows))
	var err error
	for _, raw := range rows {
		if buf, err = enc.AppendValue(buf, query.RecordOID, raw); err != nil {
			return err
		}
		if len(buf) > 32<<10 {
			if _, err = w.Write(buf); err != nil {
				return err
			}
			buf = buf[:0]
		}
	}
	_, err = w.Write(buf)
	return err
}

// appendGoValue appends a small Go value (envelope parts : stats, sql, EXPLAIN's plan) through its JSON form.
func appendGoValue(dst []byte, v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return cborenc.AppendJSON(dst, raw)
}

// writeEnvelopeCBOR is writeEnvelope for CBOR : a map whose size is known up front, "result" last.
func writeEnvelopeCBOR(w io.Writer, ri *resolvedItem, data envelopeData, streamResult func(io.Writer) error) error {
	n := 0
	if data.count != nil {
		n += 2
		if data.limit != nil {
			n++
		}
	}
	for _, present := range []bool{data.stats != nil, data.queryPlan != nil, data.sql != nil, ri.returns != "none"} {
		if present {
			n++
		}
	}
	buf := cborenc.AppendMapHead(nil, n)
	var err error
	key := func(k string, v any) {
		if err == nil {
			buf = cborenc.AppendText(buf, k)
			buf, err = appendGoValue(buf, v)
		}
	}
	if data.count != nil {
		key("count", *data.count)
		key("offset", zeroIfNil(data.offset))
		if data.limit != nil {
			key("limit", *data.limit)
		}
	}
	if data.stats != nil {
		key("stats", data.stats)
	}
	if data.queryPlan != nil {
		key("query_plan", data.queryPlan)
	}
	if data.sql != nil {
		key("sql", data.sql)
	}
	if err != nil {
		return err
	}
	if ri.returns != "none" {
		buf = cborenc.AppendText(buf, "result")
	}
	if _, err := w.Write(buf); err != nil {
		return err
	}
	if ri.returns != "none" {
		return streamResult(w)
	}
	return nil
}
