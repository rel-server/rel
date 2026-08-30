package rpc

import (
	"net/http"

	"github.com/ceymard/rel/pgerr"
)

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
	if status, message, ok := pgerr.RSStatus(err); ok {
		writePlainError(w, status, message)
		return
	}
	writePlainError(w, http.StatusInternalServerError, "internal error")
}
