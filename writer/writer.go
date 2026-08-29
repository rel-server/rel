// Package writer provides a small text-building writer shared by rel's SQL
// and TypeScript code generators. The goal is that the *shape* of generated
// code — blocks, lists, indentation — is visible directly in the Go source
// that builds it, rather than buried in ad hoc string concatenation.
package writer

import (
	"bytes"
	"fmt"
	"iter"
	"regexp"
	"strings"
)

type Writer struct {
	buf         bytes.Buffer
	indentation int
	indentUnit  string
	atLineStart bool
	args        []any
}

func New() *Writer {
	return &Writer{indentUnit: "  ", atLineStart: true}
}

// Write appends s to the buffer. Every newline in s is followed by the
// current indentation on the next write, so indentation only has to be
// gotten right once, here, rather than at every call site that happens to
// emit a "\n".
func (w *Writer) Write(s string) *Writer {
	if s == "" {
		return w
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if i > 0 {
			w.buf.WriteByte('\n')
			w.atLineStart = true
		}
		if line == "" {
			continue
		}
		if w.atLineStart {
			for range w.indentation {
				w.buf.WriteString(w.indentUnit)
			}
			w.atLineStart = false
		}
		w.buf.WriteString(line)
	}
	return w
}

func (w *Writer) Indent() *Writer {
	w.indentation++
	return w
}

func (w *Writer) Unindent() *Writer {
	if w.indentation == 0 {
		panic("writer: Unindent without matching Indent")
	}
	w.indentation--
	return w
}

// Indented brackets fn with Indent/Unindent. Since indentation only takes
// effect after an actual "\n", it's harmless to wrap content that ends up
// entirely on one line — indent and explode are independently controlled by
// whether the strings passed to Write/Surround/List contain newlines.
func (w *Writer) Indented(fn func()) *Writer {
	w.Indent()
	fn()
	w.Unindent()
	return w
}

// example:
//
//	w.Surround("(", ")", func() {
//	  w.Write("...")
//	})
func (w *Writer) Surround(start, end string, fn func()) *Writer {
	w.Write(start)
	fn()
	w.Write(end)
	return w
}

// List writes items separated by sep, calling fn once per item.
//
//	writer.List(w, ", ", cols, func(c Column) { w.Write(c.Name) })
//
// A free function, not a method, because Go methods can't be generic.
func List[T any](w *Writer, sep string, items []T, fn func(T)) *Writer {
	for i, item := range items {
		if i > 0 {
			w.Write(sep)
		}
		fn(item)
	}
	return w
}

// ListSeq is List for an iter.Seq[T] instead of a slice — for sources that
// aren't already materialized (a map iterated in a computed order, a
// generator, anything where forcing a []T first would be wasted work).
func ListSeq[T any](w *Writer, sep string, items iter.Seq[T], fn func(T)) *Writer {
	first := true
	for item := range items {
		if !first {
			w.Write(sep)
		}
		first = false
		fn(item)
	}
	return w
}

// SeparatedBy is the escape hatch for sources that are neither a slice nor
// an iter.Seq — fn must write exactly one item as a side effect and report
// whether more items follow. Prefer List or ListSeq when they fit; this
// exists for callback-driven iteration that doesn't.
func (w *Writer) SeparatedBy(sep string, fn func() (more bool)) *Writer {
	for {
		more := fn()
		if !more {
			break
		}
		w.Write(sep)
	}
	return w
}

// SurroundList is Surround wrapped around List with automatic indentation —
// the common shape for argument/column lists, inline or exploded purely
// based on whether start/sep/end contain "\n":
//
//	writer.SurroundList(w, "(", ", ", ")", cols, func(c Column) { w.Write(c.Name) })
//	writer.SurroundList(w, "(\n", ",\n", "\n)", cols, func(c Column) { w.Write(c.Name) })
func SurroundList[T any](w *Writer, start, sep, end string, items []T, fn func(T)) *Writer {
	w.Write(start)
	w.Indented(func() {
		List(w, sep, items, fn)
	})
	w.Write(end)
	return w
}

// Bind appends value as the next positional placeholder ($1, $2, ...) and
// writes the placeholder, never the value itself — standard parameterized
// SQL, so pgx sends values out-of-band instead of interpolating them into
// the query text. Params are expected to be encountered and bound in
// tree-walk order; there's no name-based dedup here. A "well-known"
// precompiled query with reusable named placeholders is a distinct concern
// with its own mechanism, not yet designed — this is deliberately just
// positional.
func (w *Writer) Bind(value any) *Writer {
	w.args = append(w.args, value)
	return w.Write(fmt.Sprintf("$%d", len(w.args)))
}

// Args returns the bind values collected via Bind, in $N order — pass
// alongside String() as the arguments to a prepared statement exec.
func (w *Writer) Args() []any {
	return w.args
}

func (w *Writer) String() string {
	return w.buf.String()
}

var validUnquotedId = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// reservedKeywords are Postgres's fully "reserved" keywords — the ones that
// are never valid as an unquoted identifier, regardless of position. Not
// exhaustive of every keyword Postgres knows (unreserved/type/function-name
// keywords are still fine unquoted); this is the set that actually matters
// for correctness.
var reservedKeywords = map[string]bool{
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

// EscapeId escapes a Postgres identifier, quoting only when needed: a
// "schema.name" input is split and each part quoted independently, so
// hotel.rooms stays unquoted but hotel."order" gets quoted only where it
// must be.
func EscapeId(id string) string {
	parts := strings.Split(id, ".")
	for i, p := range parts {
		parts[i] = escapeIdPart(p)
	}
	return strings.Join(parts, ".")
}

func escapeIdPart(p string) string {
	if p != "" && validUnquotedId.MatchString(p) && !reservedKeywords[p] {
		return p
	}
	return `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
}

// Id writes an escaped identifier.
func (w *Writer) Id(id string) *Writer {
	return w.Write(EscapeId(id))
}
