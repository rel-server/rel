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

	"github.com/jackc/pgx/v5"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/pgerr"
	"github.com/samber/oops"
)

// errorResponse is the RelErrorResponse envelope (error-handling.md).
// Status is always "error" ; the numeric HTTP status is never repeated here.
type errorResponse struct {
	Error      string        `json:"error"`
	Status     string        `json:"status"`
	Code       errcode.Code  `json:"code"`
	PgError    *pgerr.Detail `json:"pg_error,omitempty"`
	Stacktrace []string      `json:"stacktrace,omitempty"`
}

// requestError carries what writeError needs : status, code, and — for a
// classified *pgconn.PgError — the tier/Detail deciding what's safe to show.
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
// 500 for everything else (error-handling.md ## Error codes).
func badRequest(code errcode.Code, err error) *requestError {
	return &requestError{status: http.StatusBadRequest, code: code, err: err}
}
func serverError(code errcode.Code, err error) *requestError {
	return &requestError{status: http.StatusInternalServerError, code: code, err: err}
}

// unauthorized is authentication.md "## Roles ## Anonymous role existence" :
// 401 when anonymous access is disabled outright.
func unauthorized(code errcode.Code, err error) *requestError {
	return &requestError{status: http.StatusUnauthorized, code: code, err: err}
}

// methodNotAllowed is 405 ; the caller sets the Allow header itself before
// calling writeError, since requestError carries no header-writing hook.
func methodNotAllowed(err error) *requestError {
	return &requestError{status: http.StatusMethodNotAllowed, code: errcode.MethodNotAllowed, err: err}
}

// pgClassified is the one constructor that populates pgTier/pgDetail, for
// a Postgres execution error needing ## Postgres error detail's tiering.
func pgClassified(status int, code errcode.Code, tier pgerr.Tier, detail *pgerr.Detail, err error) *requestError {
	return &requestError{status: status, code: code, err: err, pgTier: tier, pgDetail: detail}
}

// writeError writes err as the RelErrorResponse envelope (error-handling.md) ;
// only clean if nothing has been written to w yet, invalid JSON otherwise.
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
		// Never err.Error() : the wrapped chain can embed rel's own
		// generated SQL (query/write_dml.go), regardless of tier.
		resp.Error, resp.PgError = pgResponseText(code, pgTier, pgDetail, dev)
	case status >= 500:
		// Safe-by-default, full detail only under dev : a wrapped chain
		// can embed a file path or connection detail.
		if dev {
			resp.Error = err.Error()
		} else {
			resp.Error = "internal error"
		}
	default:
		// A 4xx always gets the full message — client input, never
		// server-internal state.
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

// pgResponseText builds (error, pg_error) per ## Postgres error detail's
// three tiers.
func pgResponseText(code errcode.Code, tier pgerr.Tier, detail *pgerr.Detail, dev bool) (errorText string, pgError *pgerr.Detail) {
	if pgerr.IsRSCode(code) {
		// The raised message is the safe, author-chosen text — always shown.
		return detail.Message, nil
	}
	if !pgerr.AllowsDetail(tier, dev) {
		return pgerr.FallbackMessage(tier), nil
	}
	if tier == pgerr.TierConstraintViolation {
		// A short headline ; pgError (the full Detail) carries the specifics.
		return "a database constraint was violated", detail
	}
	// TierPermissionDenied/TierUnclassified with AllowsDetail true — only
	// reachable under dev.
	return detail.Message, detail
}

// formatStackFrames renders oopsErr's frames as ## Stack traces'
// RelErrorResponse.stacktrace : one "file:line function" string per frame.
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
