// Package pgerr maps Postgres errors to responses : the RSxxx convention and the PG_* classification.
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
