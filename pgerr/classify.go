// The PG_* error codes and the tiered allow-list of Postgres error detail.
package pgerr

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rel-server/rel/errcode"
)

// Tier classifies how much of a Postgres error a client response may contain ; logs always get all of it.
type Tier int

const (
	// TierUnclassified is the conservative default : Detail is nil, a
	// caller gets only a generic message unless dev mode is on.
	TierUnclassified Tier = iota
	// TierConstraintViolation is unique/foreign_key/not_null/check, a value
	// that doesn't parse as its column's type (22P02), or a write naming a
	// GENERATED ALWAYS column (428C9) : the client's own request triggered
	// this, so Detail is always safe to send.
	TierConstraintViolation
	// TierRequestInvalid is a request Postgres could not make sense of (an overload it cannot pick, a parameter
	// it cannot type) : Detail is always safe to send, and the message is the headline.
	TierRequestInvalid
	// TierPermissionDenied is insufficient_privilege : the object name
	// Postgres embeds is a fingerprinting risk, generic unless dev mode is on.
	TierPermissionDenied
)

// Detail is the allow-list of `pg_error` fields a client response may carry, copied field by field.
// Where/InternalQuery/Position/File/Line/Routine are left out : they describe rel's generated SQL.
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

// pgClassTable maps SQLSTATEs to (status, code, tier).
var pgClassTable = map[string]classified{
	"23505": {409, "PG_UNIQUE_VIOLATION", TierConstraintViolation},
	"23503": {409, "PG_FOREIGN_KEY_VIOLATION", TierConstraintViolation},
	"23502": {400, "PG_NOT_NULL_VIOLATION", TierConstraintViolation},
	"23514": {400, "PG_CHECK_VIOLATION", TierConstraintViolation},
	"22P02": {400, "PG_INVALID_TEXT_REPRESENTATION", TierConstraintViolation},
	"428C9": {400, "PG_GENERATED_ALWAYS_VIOLATION", TierConstraintViolation},
	// Postgres could not pick a function or operator overload for the argument
	// types the request produced, or could not type a parameter : the request's own doing.
	"42883": {400, "PG_UNDEFINED_FUNCTION", TierRequestInvalid},
	"42725": {400, "PG_AMBIGUOUS_FUNCTION", TierRequestInvalid},
	"42P18": {400, "PG_INDETERMINATE_DATATYPE", TierRequestInvalid},
	"42P10": {400, "PG_INVALID_COLUMN_REFERENCE", TierRequestInvalid},
	"42501": {403, "PG_PERMISSION_DENIED", TierPermissionDenied},
}

// IsRSCode reports whether code is RSxxx-shaped, whose message is always safe to show ; Classify gives it no
// tier of its own.
func IsRSCode(code errcode.Code) bool {
	return len(code) == 5 && code[0] == 'R' && code[1] == 'S'
}

// Classify unwraps err to a *pgconn.PgError and classifies it ; RSxxx takes precedence over the PG_* table. ok
// is false when err is not a *pgconn.PgError.
func Classify(err error) (status int, code errcode.Code, tier Tier, detail *Detail, ok bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return 0, "", TierUnclassified, nil, false
	}

	if rsStatus, message, isRS := RSStatus(err); isRS {
		// Tier isn't meaningful for RSxxx : the raised message is the response body.
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

// AllowsDetail reports whether a client response may include Detail : always for a constraint violation or an
// invalid request, under dev only for the others.
func AllowsDetail(tier Tier, dev bool) bool {
	switch tier {
	case TierConstraintViolation, TierRequestInvalid:
		return true
	case TierPermissionDenied:
		return dev
	default: // TierUnclassified
		return dev
	}
}

// FallbackMessage is the generic text for when AllowsDetail is false.
func FallbackMessage(tier Tier) string {
	if tier == TierPermissionDenied {
		return "insufficient permissions for this operation"
	}
	return "internal error"
}
