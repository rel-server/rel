// Postgres/SQL-specific generation concerns : bind-parameter tracking and
// identifier quoting/reserved-keyword rules. Kept out of writer.go (which
// stays target-agnostic) so a future TypeScript generator — with entirely
// different identifier and literal rules — doesn't inherit any of this ; see
// writer.go's package doc comment for the full split rationale.
package writer

import (
	"fmt"
	"regexp"
	"strings"
)

// SQLWriter wraps *Writer with the Postgres-specific pieces : Write/Indent/
// Surround/Paren/SeparatedBy are all still reached directly through the
// embedded *Writer (promoted, unchanged) ; List/ListSeq/SurroundList are
// free generic functions, so call them as writer.List(w.Writer, ...) —
// Go's method promotion doesn't extend to generic free functions, and
// duplicating them here just to accept *SQLWriter would defeat the point of
// keeping them generic in the first place.
//
// Chaining caveat : Bind/Id return *SQLWriter (so they keep chaining into
// each other), but a promoted call like w.Write("x") returns the embedded
// *Writer, which has no Bind/Id — so w.Write("x").Bind(v) doesn't compile.
// Call Write and Bind/Id as separate statements on w rather than chaining
// across the two ; this is the first place a new call site tends to hit it.
//
// Every package-level SQL-specific identifier here (EscapeSQLId and friends)
// is deliberately prefixed rather than left as the bare "EscapeId" the type
// itself might suggest — a future TypeScript-target file added to this same
// package needs its own identifier-escaping rules and name, and an unwary
// bare name here is exactly the kind of thing that would collide with it
// (or worse, get silently reused by mistake).
type SQLWriter struct {
	*Writer
	args []any
}

// NewSQL returns a fresh SQLWriter, ready to write one SQL statement.
func NewSQL() *SQLWriter {
	return &SQLWriter{Writer: New()}
}

// Bind appends value as the next positional placeholder ($1, $2, ...) and
// writes the placeholder, never the value itself — standard parameterized
// SQL, so pgx sends values out-of-band instead of interpolating them into
// the query text. Params are expected to be encountered and bound in
// tree-walk order; there's no name-based dedup here. A "well-known"
// precompiled query with reusable named placeholders is a distinct concern
// with its own mechanism, not yet designed — this is deliberately just
// positional.
//
// One SQLWriter per statement : Postgres's extended protocol binds params
// per-statement, so a multi-statement write (per-node INSERT/UPDATE/DELETE)
// needs a fresh SQLWriter — and thus a fresh $1.. sequence — per statement,
// not a shared running counter across all of them.
func (w *SQLWriter) Bind(value any) *SQLWriter {
	w.args = append(w.args, value)
	w.Write(fmt.Sprintf("$%d", len(w.args)))
	return w
}

// Args returns the bind values collected via Bind, in $N order — pass
// alongside String() as the arguments to a prepared statement exec.
func (w *SQLWriter) Args() []any {
	return w.args
}

var validUnquotedSQLId = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// sqlReservedKeywords are Postgres's fully "reserved" keywords — the ones
// that are never valid as an unquoted identifier, regardless of position.
// Not exhaustive of every keyword Postgres knows (unreserved/type/
// function-name keywords are still fine unquoted); this is the set that
// actually matters for correctness.
var sqlReservedKeywords = map[string]bool{
	"all": true, "analyse": true, "analyze": true, "and": true, "any": true,
	"array": true, "as": true, "asc": true, "asymmetric": true, "both": true,
	"case": true, "cast": true, "check": true, "collate": true, "column": true,
	"constraint": true, "create": true, "current_catalog": true, "current_date": true,
	"current_role": true, "current_time": true, "current_timestamp": true,
	"current_user": true, "default": true, "deferrable": true, "desc": true,
	"distinct": true, "do": true, "else": true, "end": true, "except": true,
	"false": true, "fetch": true, "for": true, "foreign": true, "from": true,
	"grant": true, "group": true, "having": true, "in": true, "initially": true,
	"intersect": true, "into": true, "lateral": true, "leading": true, "limit": true,
	"localtime": true, "localtimestamp": true, "not": true, "null": true,
	"offset": true, "on": true, "only": true, "or": true, "order": true,
	"placing": true, "primary": true, "references": true, "returning": true,
	"select": true, "session_user": true, "some": true, "symmetric": true,
	"table": true, "then": true, "to": true, "trailing": true, "true": true,
	"union": true, "unique": true, "user": true, "using": true, "variadic": true,
	"when": true, "where": true, "window": true, "with": true,
}

// EscapeSQLId escapes a Postgres identifier, quoting only when needed: a
// "schema.name" input is split and each part quoted independently, so
// hotel.rooms stays unquoted but hotel."order" gets quoted only where it
// must be.
func EscapeSQLId(id string) string {
	parts := strings.Split(id, ".")
	for i, p := range parts {
		parts[i] = escapeSQLIdPart(p)
	}
	return strings.Join(parts, ".")
}

func escapeSQLIdPart(p string) string {
	if p != "" && validUnquotedSQLId.MatchString(p) && !sqlReservedKeywords[p] {
		return p
	}
	return `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
}

// Id writes an escaped identifier.
func (w *SQLWriter) Id(id string) *SQLWriter {
	w.Write(EscapeSQLId(id))
	return w
}
