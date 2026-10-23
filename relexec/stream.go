package relexec

import (
	"context"
	"io"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/writer"
)

// cleanStreamError marks a streamItem failure before any response bytes
// were written — the caller can still fall back to writeError's envelope.
type cleanStreamError struct{ err error }

func (e *cleanStreamError) Error() string { return e.err.Error() }
func (e *cleanStreamError) Unwrap() error { return e.err }

// writeItemResponse is streamItem for the bare shape (## Response shape's
// "no flag set"), or writeEnvelope wrapping it for any other shape. sw is
// nil only for a write whose "returns": "none" skipped the read-back
// entirely — nothing to execute or stream then. buffered is non-nil only
// for a rolled-back write (## rollback) : its rows were already captured
// before "rollback to savepoint" erased them, so they're replayed here
// instead of re-querying.
func writeItemResponse(ctx context.Context, w io.Writer, conn *pgxpool.Conn, item *resolvedItem, sw *writer.SQLWriter, args []any, env envelopeData, buffered [][]byte, multi bool, i int) error {
	cleanErrorPossible := !multi && i == 0

	if !item.usesEnvelope() {
		if buffered != nil {
			return item.writeBuffered(w, buffered)
		}
		return streamItem(ctx, w, conn, item, sw, args, cleanErrorPossible)
	}

	// A read's "returns": "none" still executes (a side-effecting function/
	// view still runs), only suppressing "result" itself — unlike a write,
	// where "returns": "none" already skipped compiling sw at all.
	if item.returns == "none" && !item.isWrite && sw != nil {
		if err := executeDiscard(ctx, conn, sw, args, cleanErrorPossible); err != nil {
			return err
		}
	}

	// Past this point, "{" is about to be written — no clean fallback survives it.
	envelope := writeEnvelope
	if item.enc != nil {
		envelope = writeEnvelopeCBOR
	}
	return envelope(w, item, env, func(rw io.Writer) error {
		if buffered != nil {
			return item.writeBuffered(rw, buffered)
		}
		return streamItem(ctx, rw, conn, item, sw, args, false)
	})
}

// writeBuffered writes rows kept aside for a rolled-back write, in the item's format.
func (ri *resolvedItem) writeBuffered(w io.Writer, rows [][]byte) error {
	if ri.enc != nil {
		return writeBufferedRowsCBOR(w, rows, ri.enc)
	}
	return writeBufferedRows(w, rows)
}

// writeBufferedRows writes rows (captured by bufferReadback) as the same
// manually-streamed JSON array streamRows produces for a live query.
func writeBufferedRows(w io.Writer, rows [][]byte) error {
	if _, err := io.WriteString(w, "["); err != nil {
		return err
	}
	for i, raw := range rows {
		if i > 0 {
			if _, err := io.WriteString(w, ","); err != nil {
				return err
			}
		}
		if _, err := w.Write(raw); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "]")
	return err
}

// executeDiscard runs sw for its side effects only, scanning no column —
// server/rel.go's own answer to a read's "returns": "none" + "rollback".
func executeDiscard(ctx context.Context, conn *pgxpool.Conn, sw *writer.SQLWriter, args []any, cleanErrorPossible bool) error {
	logging.FromContext(ctx).Debug("executing read", "sql", sw.String(), "args", args)
	rows, err := conn.Query(ctx, sw.String(), args...)
	if err != nil {
		if cleanErrorPossible {
			return &cleanStreamError{err}
		}
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		if cleanErrorPossible {
			return &cleanStreamError{err}
		}
		return err
	}
	return nil
}

// streamItem runs sw and streams the result : a bare scalar for a scalar
// function root, a JSON array otherwise (## Response Shape).
func streamItem(ctx context.Context, w io.Writer, conn *pgxpool.Conn, item *resolvedItem, sw *writer.SQLWriter, args []any, cleanErrorPossible bool) error {
	root := item.root
	logging.FromContext(ctx).Debug("executing read", "sql", sw.String(), "args", args)
	rows, err := conn.Query(ctx, sw.String(), item.queryArgs(args)...)
	if err != nil {
		if cleanErrorPossible {
			return &cleanStreamError{err}
		}
		return err
	}
	defer rows.Close()

	if item.enc != nil {
		if root.IsFunction() && !root.Function.ReturnsSet {
			return writeScalarCBOR(w, rows, item.enc)
		}
		return streamRowsCBOR(w, rows, item.enc, cleanErrorPossible)
	}

	if root.IsFunction() && !root.Function.ReturnsSet {
		hasRow := rows.Next()
		if err := rows.Err(); err != nil {
			if cleanErrorPossible {
				return &cleanStreamError{err}
			}
			return err
		}
		if hasRow {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			if _, err := w.Write(raw); err != nil {
				return err
			}
		}
		return nil
	}

	return streamRows(w, rows, cleanErrorPossible)
}

// streamRows writes rows as a manually streamed JSON array (## Response
// Shape) ; a failure peeking the first row (before "[") wraps as *cleanStreamError.
func streamRows(w io.Writer, rows pgx.Rows, cleanErrorPossible bool) error {
	hasFirst := rows.Next()
	if err := rows.Err(); err != nil {
		if cleanErrorPossible {
			return &cleanStreamError{err}
		}
		return err
	}
	if !hasFirst {
		_, err := w.Write([]byte("[]"))
		return err
	}

	if _, err := w.Write([]byte("[")); err != nil {
		return err
	}
	for {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		if _, err := w.Write(raw); err != nil {
			return err
		}
		if !rows.Next() {
			break
		}
		if _, err := w.Write([]byte(",")); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err := w.Write([]byte("]"))
	return err
}
