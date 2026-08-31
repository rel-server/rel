// Response framing : the manual "["/","/"]" streaming specs/query-engine.md's
// ## Response Shape asks for (no json_agg, constant memory, first byte
// before the query finishes), plus the error envelope specs/error-handling.md
// specifies in full.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/ceymard/rel/errcode"
	"github.com/ceymard/rel/pgerr"
	"github.com/jackc/pgx/v5"
	"github.com/samber/oops"
)

// errorResponse is the RelErrorResponse envelope — specs/error-handling.md
// ## `RelErrorResponse`, updated shape. Status is always the literal string
// "error" (a success/error discriminant), never an HTTP status code — the
// numeric status lives only in the HTTP response line itself and the
// X-Rel-Errorcode header.
type errorResponse struct {
	Error      string        `json:"error"`
	Status     string        `json:"status"`
	Code       errcode.Code  `json:"code"`
	PgError    *pgerr.Detail `json:"pg_error,omitempty"`
	Stacktrace []string      `json:"stacktrace,omitempty"`
}

// requestError carries everything writeError needs to build the envelope :
// the HTTP status, the stable Code, and — for an error that unwrapped to a
// classified *pgconn.PgError via pgerr.Classify — the tier and Detail that
// decide what's safe to show, per specs/error-handling.md ## Postgres error
// detail. pgTier/pgDetail are nil/zero for an ordinary rel-internal error,
// in which case writeError falls back to err's own message text (safe for
// 4xx, gated for 5xx — see writeError).
type requestError struct {
	status   int
	code     errcode.Code
	err      error
	pgTier   pgerr.Tier
	pgDetail *pgerr.Detail
}

func (e *requestError) Error() string { return e.err.Error() }
func (e *requestError) Unwrap() error { return e.err }

// badRequest/serverError : 400 for a problem with the query/data itself,
// 500 for everything else. code is always supplied at the call site — see
// specs/error-handling.md ## Error codes' "there is no unclassified/silent
// case."
func badRequest(code errcode.Code, err error) *requestError {
	return &requestError{status: http.StatusBadRequest, code: code, err: err}
}
func serverError(code errcode.Code, err error) *requestError {
	return &requestError{status: http.StatusInternalServerError, code: code, err: err}
}

// unauthorized is specs/authentication.md "# Roles ## Anonymous role
// existence" : 401 for an unauthenticated request when anonymous access is
// disabled outright (db.AnonymousRoleExists false).
func unauthorized(code errcode.Code, err error) *requestError {
	return &requestError{status: http.StatusUnauthorized, code: code, err: err}
}

// methodNotAllowed is 405 — matches http.Error's own convention of an
// Allow header alongside the status, unlike this package's other
// constructors : the caller (NewRelHandler) sets it before calling
// writeError, since requestError itself carries no header-writing hook.
func methodNotAllowed(err error) *requestError {
	return &requestError{status: http.StatusMethodNotAllowed, code: errcode.MethodNotAllowed, err: err}
}

// pgClassified builds a *requestError from a pgerr.Classify result — the
// one constructor that populates pgTier/pgDetail, used wherever a Postgres
// execution error (a write, a read calling into a function, check_session)
// needs the tiered ## Postgres error detail treatment rather than the
// generic err.Error() fallback.
func pgClassified(status int, code errcode.Code, tier pgerr.Tier, detail *pgerr.Detail, err error) *requestError {
	return &requestError{status: status, code: code, err: err, pgTier: tier, pgDetail: detail}
}

// writeError writes err as the RelErrorResponse envelope, per
// specs/error-handling.md. If nothing has been written to w yet, this is a
// clean response ; if called after streaming has already begun (partway
// through a row), the output is already invalid JSON regardless — that's an
// inherent limitation of streaming a response before its query has
// finished, not something this function can fix, see rel.go's own note on
// the same point.
func writeError(w http.ResponseWriter, err error, dev bool) {
	status := http.StatusInternalServerError
	code := errcode.Internal
	var pgTier pgerr.Tier
	var pgDetail *pgerr.Detail

	if reqErr, ok := errors.AsType[*requestError](err); ok {
		status = reqErr.status
		code = reqErr.code
		pgTier = reqErr.pgTier
		pgDetail = reqErr.pgDetail
	}

	resp := errorResponse{Status: "error", Code: code}

	switch {
	case pgDetail != nil:
		// A classified Postgres error : ## Postgres error detail's tiering,
		// never err.Error() — the underlying wrapped chain can embed rel's
		// own generated SQL (query/write_dml.go's "insert: %w\nsql: %s"
		// pattern), which must never reach a client regardless of tier.
		resp.Error, resp.PgError = pgResponseText(code, pgTier, pgDetail, dev)
	case status >= 500:
		// An ordinary (non-Postgres-classified) 5xx : safe-by-default,
		// full detail only under dev — same reasoning as the Postgres
		// unclassified tier, since a wrapped chain can just as easily
		// embed a file path or connection detail (specs/error-handling.md
		// ## `RelErrorResponse`, updated shape's closing paragraph).
		if dev {
			resp.Error = err.Error()
		} else {
			resp.Error = "internal error"
		}
	default:
		// An ordinary 4xx : always the full message — it describes the
		// client's own malformed input, never server-internal state.
		resp.Error = err.Error()
	}

	if status >= 500 && dev {
		if oopsErr, ok := oops.AsOops(err); ok {
			resp.Stacktrace = formatStackFrames(oopsErr)
		}
	}

	w.Header().Set(errcode.Header, string(code))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

// pgResponseText builds (error, pg_error) for a classified Postgres error
// per tier — specs/error-handling.md ## Postgres error detail's three
// tiers.
func pgResponseText(code errcode.Code, tier pgerr.Tier, detail *pgerr.Detail, dev bool) (errorText string, pgError *pgerr.Detail) {
	if pgerr.IsRSCode(code) {
		// The raised message IS the safe, author-chosen text — always
		// shown, exactly like today's RSxxx convention.
		return detail.Message, nil
	}
	if !pgerr.AllowsDetail(tier, dev) {
		return pgerr.FallbackMessage(tier), nil
	}
	if tier == pgerr.TierConstraintViolation {
		// A short, safe headline — the raw detail.Message isn't needed
		// here the way rpc/response.go's pgPlainText needs it, since this
		// package has pgError (the full Detail) as its own separate field
		// to carry the specifics in.
		return "a database constraint was violated", detail
	}
	// TierPermissionDenied or TierUnclassified, with AllowsDetail true —
	// only reachable under dev (see AllowsDetail, pgerr/classify.go).
	return detail.Message, detail
}

// formatStackFrames renders oopsErr's captured frames as
// specs/error-handling.md ## Stack traces' RelErrorResponse.stacktrace :
// one "file:line function" string per frame.
func formatStackFrames(oopsErr oops.OopsError) []string {
	frames := oopsErr.StackFrames()
	if len(frames) == 0 {
		return nil
	}
	out := make([]string, len(frames))
	for i, f := range frames {
		out[i] = fmt.Sprintf("%s:%d %s", f.File, f.Line, f.Function)
	}
	return out
}

// streamRows writes rows (each expected to have a single already-JSON
// column, per row_to_json) as a JSON array : "[", comma-separated row text
// straight from Postgres, "]" — manual streaming, not json_agg, per
// ## Response Shape's own "why" (constant memory on both ends, first byte
// before the query finishes).
//
// The first row is peeked BEFORE writing the opening "[" : pgx defers a
// query's own execution errors (a permission error, say) to the first
// rows.Next()/rows.Err() call rather than to Query itself, so writing "["
// unconditionally first would mean an error discovered one line later
// always looks like a truncated response, even for the very first item of
// a single-item request where a clean envelope was still possible. If
// cleanErrorPossible and that peek itself fails, the error is wrapped as
// *cleanStreamError for the caller to still build a clean envelope from —
// any failure past the peek (a later row, a write) returns its raw error,
// same truncation limitation as before.
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
