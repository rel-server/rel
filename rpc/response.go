package rpc

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5/pgconn"
)

// rsCodePattern matches jwt-roles-and-http.md ## Postgres Exceptions'
// "RSxxx" convention : exactly "RS" followed by 3 digits, which ARE the
// HTTP status to respond with.
var rsCodePattern = regexp.MustCompile(`^RS(\d{3})$`)

// rsStatus reports whether err unwraps to a *pgconn.PgError whose SQLSTATE
// matches the RSxxx convention, returning the numeric status and the
// raised message (the response body) if so.
func rsStatus(err error) (status int, message string, ok bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return 0, "", false
	}
	m := rsCodePattern.FindStringSubmatch(pgErr.Code)
	if m == nil {
		return 0, "", false
	}
	status = 0
	for _, c := range m[1] {
		status = status*10 + int(c-'0')
	}
	return status, pgErr.Message, true
}

// writePlainError writes a plain-text (NOT JSON) error body — ## Postgres
// Exceptions' own framing : "gives the response status xxx, with whatever
// text was raised as the body." This is deliberately different from
// server/response.go's JSON envelope : /rel and /rpc have separate error
// conventions per their respective spec sections.
func writePlainError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message))
}

// writeErrorForPgErr classifies err (via rsStatus) and writes the
// appropriate plain-text response : an RSxxx code maps directly, anything
// else is a generic 500 (the dev-mode HTML error page from
// 02-error-handling.md stays unimplemented — see specs/TODO.md).
func writeErrorForPgErr(w http.ResponseWriter, err error) {
	if status, message, ok := rsStatus(err); ok {
		writePlainError(w, status, message)
		return
	}
	writePlainError(w, http.StatusInternalServerError, "internal error")
}
