// This file implements specs/error-handling.md ## Postgres-raised codes'
// PG_* family and ## Postgres error detail's tiered field allow-list —
// additive to rscode.go's RSxxx handling, not a replacement : RSStatus
// keeps its existing signature and its three existing call sites
// (route/response.go, static/static.go, server's check_session path)
// untouched. Classify is the new, broader entry point for a write/read
// execution error that may or may not be RSxxx.
package pgerr

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rel-server/rel/errcode"
)

// Tier is specs/error-handling.md ## Postgres error detail's three-way
// split of what a client response is allowed to contain — never about what
// gets logged, which always gets the full *pgconn.PgError regardless.
type Tier int

const (
	// TierUnclassified is the conservative default : Detail is nil, a
	// caller gets only a generic message unless dev mode is on.
	TierUnclassified Tier = iota
	// TierConstraintViolation is unique/foreign_key/not_null/check, or a
	// value that doesn't parse as its column's type (22P02) : the client's
	// own data triggered this, so Detail is always safe to send.
	TierConstraintViolation
	// TierPermissionDenied is insufficient_privilege : the object name
	// Postgres embeds is a fingerprinting risk, generic unless dev mode is on.
	TierPermissionDenied
)

// Detail is the field allow-list specs/error-handling.md ###
// `pg_error` field allow-list specifies — built explicitly field-by-field
// from *pgconn.PgError, never from its .Error() string or the raw struct,
// so a field added to pgconn.PgError in a future dependency bump can't
// silently start leaking through this type. Where/InternalQuery/Position/
// InternalPosition/File/Line/Routine are deliberately absent : they
// describe rel's own generated SQL and Postgres's internal execution path,
// not the client's data.
type Detail struct {
	Message        string `json:"message"`
	Detail         string `json:"detail,omitempty"`
	SchemaName     string `json:"schema_name,omitempty"`
	TableName      string `json:"table_name,omitempty"`
	ColumnName     string `json:"column_name,omitempty"`
	ConstraintName string `json:"constraint_name,omitempty"`
}

// classified is one entry in the PG_* table below.
type classified struct {
	status int
	code   errcode.Code
	tier   Tier
}

// pgClassTable maps well-known SQLSTATEs to (status, code, tier) — the
// "small fixed table" of specs/error-handling.md ### Postgres-raised codes.
var pgClassTable = map[string]classified{
	"23505": {409, "PG_UNIQUE_VIOLATION", TierConstraintViolation},
	"23503": {409, "PG_FOREIGN_KEY_VIOLATION", TierConstraintViolation},
	"23502": {400, "PG_NOT_NULL_VIOLATION", TierConstraintViolation},
	"23514": {400, "PG_CHECK_VIOLATION", TierConstraintViolation},
	"22P02": {400, "PG_INVALID_TEXT_REPRESENTATION", TierConstraintViolation},
	"42501": {403, "PG_PERMISSION_DENIED", TierPermissionDenied},
}

// IsRSCode reports whether code is RSxxx-shaped — used by a caller that
// needs to tell an author-chosen, always-safe RSxxx message
// (docs/content/http/index.md ## Errors are just exceptions) apart from every other code sharing
// TierUnclassified, since Classify itself has no separate tier value for
// that distinction (see its RSxxx branch's own doc comment).
func IsRSCode(code errcode.Code) bool {
	return len(code) == 5 && code[0] == 'R' && code[1] == 'S'
}

// Classify unwraps err to a *pgconn.PgError and classifies it : RSxxx takes
// precedence over the PG_* table (an RSxxx code is a PL/pgSQL author's own
// deliberate status pick, which must win over any generic classification of
// the same underlying SQLSTATE-shaped code). ok is false when err doesn't
// unwrap to a *pgconn.PgError at all — the caller's own generic
// Internal/Unclassified fallback applies in that case, per
// specs/error-handling.md ## Error codes.
func Classify(err error) (status int, code errcode.Code, tier Tier, detail *Detail, ok bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return 0, "", TierUnclassified, nil, false
	}

	if rsStatus, message, isRS := RSStatus(err); isRS {
		// Tier isn't meaningful for RSxxx : the raised message IS the
		// response body, unconditionally (docs/content/http/index.md ## Errors are just exceptions).
		return rsStatus, errcode.Code(pgErr.Code), TierUnclassified, &Detail{Message: message}, true
	}

	d := &Detail{
		Message:        pgErr.Message,
		Detail:         pgErr.Detail,
		SchemaName:     pgErr.SchemaName,
		TableName:      pgErr.TableName,
		ColumnName:     pgErr.ColumnName,
		ConstraintName: pgErr.ConstraintName,
	}

	if c, known := pgClassTable[pgErr.Code]; known {
		return c.status, c.code, c.tier, d, true
	}

	return 500, errcode.Internal, TierUnclassified, d, true
}

// AllowsDetail reports whether specs/error-handling.md ## Postgres error
// detail's tiering allows a client-facing response to include the
// classified error's full Detail : unconditionally true for a constraint
// violation (the client's own submitted data caused it), true for
// permission-denied/unclassified only under dev. The single source of
// truth for this decision — shared between /rel's structured envelope and
// /route's plain-text body, which render an "allowed" case differently but
// must never independently drift on which tier allows what.
func AllowsDetail(tier Tier, dev bool) bool {
	switch tier {
	case TierConstraintViolation:
		return true
	case TierPermissionDenied:
		return dev
	default: // TierUnclassified
		return dev
	}
}

// FallbackMessage is the generic, safe-by-default text specs/error-handling.md
// ## Postgres error detail names for tier 2 (permission-denied) and tier 3
// (unclassified) in production — used whenever AllowsDetail(tier, dev) is
// false. Meaningless for TierConstraintViolation, which AllowsDetail never
// refuses.
func FallbackMessage(tier Tier) string {
	if tier == TierPermissionDenied {
		return "insufficient permissions for this operation"
	}
	return "internal error"
}
