// Package pgerr implements jwt-roles-and-http.md's ## Postgres Exceptions
// "RSxxx" convention, shared between /rpc (rpc/response.go, plain-text
// body) and /rel's check_session rejection (server/rel.go, JSON envelope) —
// the two response framings stay separate, only the SQLSTATE→status mapping
// is shared.
package pgerr

import (
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5/pgconn"
)

var rsCodePattern = regexp.MustCompile(`^RS(\d{3})$`)

// RSStatus reports whether err unwraps to a *pgconn.PgError whose SQLSTATE
// matches the RSxxx convention, returning the numeric status and the raised
// message (the response body) if so.
func RSStatus(err error) (status int, message string, ok bool) {
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
