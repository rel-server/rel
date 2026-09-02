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
	slots []sqlBindSlot
}

// sqlBindSlot is one $N placeholder : either a literal value already known
// at compile time (Bind, the common case — name == "") or a well-known
// query's named parameter reserved via BindParam, whose value only exists
// per-request. Both share the same $N sequence (Postgres's placeholders are
// one flat positional list regardless of why each one was written), so they
// live in one ordered slice rather than two independently-numbered ones.
type sqlBindSlot struct {
	name  string
	value any
}

// NewSQL returns a fresh SQLWriter, ready to write one SQL statement.
func NewSQL() *SQLWriter {
	return &SQLWriter{Writer: New()}
}

// Bind appends value as the next positional placeholder ($1, $2, ...) and
// writes the placeholder, never the value itself — standard parameterized
// SQL, so pgx sends values out-of-band instead of interpolating them into
// the query text. Params are expected to be encountered and bound in
// tree-walk order; there's no name-based dedup here.
//
// One SQLWriter per statement : Postgres's extended protocol binds params
// per-statement, so a multi-statement write (per-node INSERT/UPDATE/DELETE)
// needs a fresh SQLWriter — and thus a fresh $1.. sequence — per statement,
// not a shared running counter across all of them.
func (w *SQLWriter) Bind(value any) *SQLWriter {
	w.slots = append(w.slots, sqlBindSlot{value: value})
	w.Write(fmt.Sprintf("$%d", len(w.slots)))
	return w
}

// BindParam reserves the next $N placeholder for a well-known query's named
// parameter (specs/well-known-queries.md ## Definition's `["$param", ...]`)
// instead of binding a value immediately : a well-known query's SQL text is
// compiled once, at load time, while a param's actual value only exists per
// request. Shares the same $N sequence as Bind, so a statement mixing
// literal binds (e.g. from the query's own where clause) and named params
// still gets one coherent, gap-free placeholder list. Resolve the final
// per-request args with ResolveArgs, not Args — Args rejects a statement
// that has any unresolved named slot.
func (w *SQLWriter) BindParam(name string) *SQLWriter {
	w.slots = append(w.slots, sqlBindSlot{name: name})
	w.Write(fmt.Sprintf("$%d", len(w.slots)))
	return w
}

// Args returns the bind values collected via Bind, in $N order — pass
// alongside String() as the arguments to a prepared statement exec. Panics
// if any placeholder on this statement was reserved via BindParam instead :
// those have no value here to return, only a name to resolve later against
// a specific request's own params (see ResolveArgs) — a caller reaching for
// plain Args() on such a statement is a genuine programming error, not a
// runtime condition to recover from.
func (w *SQLWriter) Args() []any {
	out := make([]any, len(w.slots))
	for i, s := range w.slots {
		if s.name != "" {
			panic(fmt.Sprintf("writer: Args() called on a statement with an unresolved $param %q — use ResolveArgs", s.name))
		}
		out[i] = s.value
	}
	return out
}

// ParamNames returns every distinct name reserved via BindParam on this
// statement, in first-occurrence order — the well-known loader's own
// unused/unknown-param validation pass (specs/well-known-queries.md ##
// Definition) walks this per compiled statement rather than re-walking the
// expression tree itself.
func (w *SQLWriter) ParamNames() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range w.slots {
		if s.name == "" || seen[s.name] {
			continue
		}
		seen[s.name] = true
		out = append(out, s.name)
	}
	return out
}

// ResolveArgs builds one request's final positional args slice : literal
// slots (from Bind) pass through unchanged, named slots (from BindParam)
// are substituted from paramValues — already validated and defaulted by the
// caller (a missing key here is checked defensively, but should be
// unreachable once request-time validation has run ; see
// WELL_KNOWN_PARAM_REQUIRED).
func (w *SQLWriter) ResolveArgs(paramValues map[string]any) ([]any, error) {
	out := make([]any, len(w.slots))
	for i, s := range w.slots {
		if s.name == "" {
			out[i] = s.value
			continue
		}
		v, ok := paramValues[s.name]
		if !ok {
			return nil, fmt.Errorf("writer: no value supplied for $param %q", s.name)
		}
		out[i] = v
	}
	return out, nil
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
