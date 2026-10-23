// Package httperr decides what a failed request tells its client, and logs the failures that are the
// server's fault (specs/error-handling.md, specs/logging.md ## Error logging).
//
// An Error carries the status, code and cause, plus the Postgres tier and detail when the cause is a
// Postgres error. Deciding what is safe to show (the tiers of ## Postgres error detail) and logging
// a 5xx happen here, once. Writing is left to the caller for a JSON envelope (server/response.go) and
// done here for the plain-text one /route and the SSO callbacks use.
package httperr

import (
	"context"
	"net/http"

	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/pgerr"
)

// internalText is what a client is told when the cause of a 5xx is not for them to see.
const internalText = "internal error"

// Error is a failed request's status and code, and the cause behind them.
type Error struct {
	Status int
	Code   errcode.Code
	Err    error
	// Tier and Detail are set when Err is a classified Postgres error (see FromPostgres) ; Detail is
	// nil otherwise.
	Tier   pgerr.Tier
	Detail *pgerr.Detail
	// Public is the text a plain-text response gives for a 5xx that is not a Postgres error ; empty
	// means "internal error". A 4xx always shows Err.
	Public string
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// BadRequest is a 400 : a problem with the query or data itself.
func BadRequest(code errcode.Code, err error) *Error {
	return &Error{Status: http.StatusBadRequest, Code: code, Err: err}
}

// Server is a 500 : everything else.
func Server(code errcode.Code, err error) *Error {
	return &Error{Status: http.StatusInternalServerError, Code: code, Err: err}
}

// Unauthorized is a 401 (authentication.md "## Roles ## Anonymous role existence").
func Unauthorized(code errcode.Code, err error) *Error {
	return &Error{Status: http.StatusUnauthorized, Code: code, Err: err}
}

// MethodNotAllowed is a 405 ; the caller sets the Allow header itself.
func MethodNotAllowed(err error) *Error {
	return &Error{Status: http.StatusMethodNotAllowed, Code: errcode.MethodNotAllowed, Err: err}
}

// FromPostgres classifies err. ok is false when err is not a Postgres error, and the caller decides
// what it is instead.
func FromPostgres(err error) (e *Error, ok bool) {
	status, code, tier, detail, ok := pgerr.Classify(err)
	if !ok {
		return nil, false
	}
	return &Error{Status: status, Code: code, Err: err, Tier: tier, Detail: detail}, true
}

// Log writes the one "request failed" line a 5xx deserves, in full, whatever the client is shown.
// A 4xx is the client's doing and is not logged.
func (e *Error) Log(ctx context.Context) {
	if e.Status < http.StatusInternalServerError {
		return
	}
	attrs := append([]any{"code", e.Code, "status", e.Status}, logging.Error(e.Err)...)
	if e.Detail != nil {
		attrs = append(attrs, "pg_error", e.Detail)
	}
	logging.FromContext(ctx).Error("request failed", attrs...)
}

// JSONText is the Postgres error's message for a JSON envelope, with the structured detail to put
// beside it (nil when the tier keeps it back). Only meaningful when Detail is set. Never uses
// Err.Error() : that chain can embed rel's own generated SQL.
func (e *Error) JSONText(dev bool) (text string, detail *pgerr.Detail) {
	switch {
	case pgerr.IsRSCode(e.Code):
		// The raised message is the author's own, always safe to show.
		return e.Detail.Message, nil
	case !pgerr.AllowsDetail(e.Tier, dev):
		return pgerr.FallbackMessage(e.Tier), nil
	case e.Tier == pgerr.TierConstraintViolation:
		// A short headline ; the structured detail carries the specifics.
		return "a database constraint was violated", e.Detail
	default:
		return e.Detail.Message, e.Detail
	}
}

// PlainText is the response body of a plain-text response : the tiered message for a Postgres error
// (a constraint violation with its detail appended, since there is no structured field), Err for a
// 4xx, Public for any other 5xx.
func (e *Error) PlainText(dev bool) string {
	switch {
	case e.Detail != nil:
		if pgerr.IsRSCode(e.Code) {
			return e.Detail.Message
		}
		if !pgerr.AllowsDetail(e.Tier, dev) {
			return pgerr.FallbackMessage(e.Tier)
		}
		if e.Tier == pgerr.TierConstraintViolation && e.Detail.Detail != "" {
			return e.Detail.Message + ": " + e.Detail.Detail
		}
		return e.Detail.Message
	case e.Status < http.StatusInternalServerError:
		return e.Err.Error()
	case e.Public != "":
		return e.Public
	default:
		return internalText
	}
}

// WritePlain writes message as a plain-text error response (## Postgres Exceptions), deliberately
// unlike server/response.go's JSON envelope. Nothing is logged : use it for a client's mistake.
func WritePlain(w http.ResponseWriter, status int, code errcode.Code, message string) {
	w.Header().Set(errcode.Header, string(code))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message))
}

// WritePlainError logs e if it is a 5xx, then writes it as a plain-text response.
func WritePlainError(ctx context.Context, w http.ResponseWriter, e *Error, dev bool) {
	e.Log(ctx)
	WritePlain(w, e.Status, e.Code, e.PlainText(dev))
}

// WriteServer is WritePlainError for a server fault whose cause is err and whose client-facing text
// is the fixed message : the client sees exactly message while the logs get err.
func WriteServer(ctx context.Context, w http.ResponseWriter, status int, code errcode.Code, message string, err error) {
	WritePlainError(ctx, w, &Error{Status: status, Code: code, Err: err, Public: message}, false)
}

// WritePostgres writes err as a plain-text response : tiered if it is a Postgres error, otherwise a
// logged 500 "internal error".
func WritePostgres(ctx context.Context, w http.ResponseWriter, err error, dev bool) {
	e, ok := FromPostgres(err)
	if !ok {
		e = Server(errcode.Internal, err)
	}
	WritePlainError(ctx, w, e, dev)
}
