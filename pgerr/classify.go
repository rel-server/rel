// This file implements specs/error-handling.md ## Postgres-raised codes'
// PG_* family and ## Postgres error detail's tiered field allow-list —
// additive to rscode.go's RSxxx handling, not a replacement : RSStatus
// keeps its existing signature and its three existing call sites
// (rpc/response.go, static/static.go, server's check_session path)
// untouched. Classify is the new, broader entry point for a write/read
// execution error that may or may not be RSxxx.
package pgerr

import (
	"errors"

	"github.com/ceymard/rel/errcode"
	"github.com/jackc/pgx/v5/pgconn"
)

// Tier is specs/error-handling.md ## Postgres error detail's three-way
// split of what a client response is allowed to contain — never about what
// gets logged, which always gets the full *pgconn.PgError regardless.
type Tier int

const (
	// TierUnclassified is the conservative default : not one of the
	// SQLSTATEs this package recognizes. Detail (below) is nil ; a caller
	// only gets a generic message/pg_error unless dev mode is on.
	TierUnclassified Tier = iota
	// TierConstraintViolation is unique/foreign_key/not_null/check : the
	// client's own submitted data triggered this, so Detail is always
	// safe to send, production included.
	TierConstraintViolation
	// TierPermissionDenied is insufficient_privilege : status/code are
	// always shown, but the object name Postgres's own message may embed
	// is exactly the fingerprinting risk specs/TODO.md flags, so the
	// message stays generic unless dev mode is on.
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

// pgClassTable maps well-known SQLSTATEs to their (status, code, tier) —
// specs/error-handling.md ### Postgres-raised codes' "small fixed table".
// Deliberately small : only classes with unambiguous HTTP semantics and a
// clear tier ; everything else stays TierUnclassified.
var pgClassTable = map[string]classified{
	"23505": {409, "PG_UNIQUE_VIOLATION", TierConstraintViolation},
	"23503": {409, "PG_FOREIGN_KEY_VIOLATION", TierConstraintViolation},
	"23502": {400, "PG_NOT_NULL_VIOLATION", TierConstraintViolation},
	"23514": {400, "PG_CHECK_VIOLATION", TierConstraintViolation},
	"42501": {403, "PG_PERMISSION_DENIED", TierPermissionDenied},
}

// IsRSCode reports whether code is RSxxx-shaped — used by a caller that
// needs to tell an author-chosen, always-safe RSxxx message (rpc.md ##
// Postgres Exceptions) apart from every other code sharing
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
		// Tier is not meaningful for RSxxx : the raised message IS the
		// response body per rpc.md ## Postgres Exceptions' existing
		// convention, unconditionally — a caller uses Detail.Message
		// directly here rather than consulting tier at all.
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
// classified error's full Detail (raw Postgres message/constraint/column
// names, or — for a caller with no separate structured field — folded
// straight into its one text channel) : unconditionally true for a
// constraint violation (the client's own submitted data caused it), true
// for permission-denied/unclassified only under dev — the same condition
// FallbackMessage's own callers gate on.
//
// The single source of truth for this decision, shared between /rel's
// structured envelope (server/response.go, which additionally gets to
// show Detail as its own separate pg_error field when this is true) and
// /rpc's plain-text body (rpc/response.go, which folds Detail straight
// into its one channel instead) — the two render an "allowed" case
// differently, but must never independently drift on WHICH tier allows
// what or under which condition, which is exactly what happened before
// this was factored out : both packages carried their own copy of this
// same switch.
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
