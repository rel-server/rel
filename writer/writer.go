// Package writer provides a small text-building writer shared by rel's SQL
// and TypeScript code generators. The goal is that the *shape* of generated
// code — blocks, lists, indentation — is visible directly in the Go source
// that builds it, rather than buried in ad hoc string concatenation.
//
// Writer itself (this file) is target-language-agnostic : Write/Indent/
// Surround/Paren/List/SurroundList have no Postgres- or TypeScript-specific
// knowledge. Target-specific concerns (identifier-quoting rules, reserved
// keywords, bind-parameter syntax) live in their own files/types wrapping
// *Writer — see pg.go's SQLWriter for the Postgres/SQL side. A future
// TypeScript generator gets its own such wrapper rather than adding more
// methods to *Writer directly, so neither target's rules leak into the
// other's dependency graph.
package writer

import (
	"bytes"
	"iter"
	"strings"
)

type Writer struct {
	buf         bytes.Buffer
	indentation int
	indentUnit  string
	atLineStart bool
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

// Paren is Surround("(", ")", fn) — the convention every parenthesized
// sub-expression should go through, rather than a bare Write("(") /
// Write(")") pair : one call site can't forget its own matching close the
// way two separate Write calls could drift apart under future edits.
func (w *Writer) Paren(fn func()) *Writer {
	return w.Surround("(", ")", fn)
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

func (w *Writer) String() string {
	return w.buf.String()
}
