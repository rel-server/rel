// Response framing : the manual "["/","/"]" streaming specs/querying.md's
// ## Response Shape asks for (no json_agg, constant memory, first byte
// before the query finishes), plus the error envelope confirmed this
// session.
package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// errorResponse is the confirmed error envelope : {"error", "status":
// "error", "pg_error"?}. Status is always the literal string "error" (a
// success/error discriminant), never an HTTP status code — the numeric
// status lives only in the HTTP response line itself.
type errorResponse struct {
	Error   string `json:"error"`
	Status  string `json:"status"`
	PgError string `json:"pg_error,omitempty"`
}

// requestError carries the HTTP status one handler-level error should
// produce, alongside the underlying error it wraps (so errors.As can still
// find a *pgconn.PgError through it for the "pg_error" field).
type requestError struct {
	status int
	err    error
}

func (e *requestError) Error() string { return e.err.Error() }
func (e *requestError) Unwrap() error { return e.err }

// badRequest/serverError : 400 for a problem with the query/data itself,
// 500 for everything else (confirmed this session ; 401 for a bad role is
// reserved but unreachable until auth exists — see the plan's Scope note).
func badRequest(err error) *requestError {
	return &requestError{status: http.StatusBadRequest, err: err}
}
func serverError(err error) *requestError {
	return &requestError{status: http.StatusInternalServerError, err: err}
}

// writeError writes err as the confirmed error envelope. If nothing has
// been written to w yet, this is a clean response ; if called after
// streaming has already begun (partway through a row), the output is
// already invalid JSON regardless — that's an inherent limitation of
// streaming a response before its query has finished, not something this
// function can fix, see rel.go's own note on the same point.
func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if reqErr, ok := errors.AsType[*requestError](err); ok {
		status = reqErr.status
	}
	resp := errorResponse{Error: err.Error(), Status: "error"}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		resp.PgError = pgErr.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

// streamRows writes rows (each expected to have a single already-JSON
// column, per row_to_json) as a JSON array : "[", comma-separated row text
// straight from Postgres, "]" — manual streaming, not json_agg, per
// ## Response Shape's own "why" (constant memory on both ends, first byte
// before the query finishes).
func streamRows(w io.Writer, rows pgx.Rows) error {
	if _, err := w.Write([]byte("[")); err != nil {
		return err
	}
	first := true
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		if !first {
			if _, err := w.Write([]byte(",")); err != nil {
				return err
			}
		}
		first = false
		if _, err := w.Write(raw); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err := w.Write([]byte("]"))
	return err
}
