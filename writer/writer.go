// Package writer builds generated code text with blocks, lists and indentation. Writer knows no target language ;
// target-specific rules go in a wrapper type, like SQLWriter.
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

// Write appends s ; each line after a newline starts with the current indentation.
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

// Indented brackets fn with Indent/Unindent ; indentation only shows after a newline, so one-line content is
// unaffected.
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

// Paren is Surround("(", ")", fn).
func (w *Writer) Paren(fn func()) *Writer {
	return w.Surround("(", ")", fn)
}

// List writes items separated by sep, calling fn once per item.
//
//	writer.List(w, ", ", cols, func(c Column) { w.Write(c.Name) })
func List[T any](w *Writer, sep string, items []T, fn func(T)) *Writer {
	for i, item := range items {
		if i > 0 {
			w.Write(sep)
		}
		fn(item)
	}
	return w
}

// ListSeq is List for an iter.Seq[T].
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

// SeparatedBy calls fn until it reports no more items ; fn writes exactly one item per call.
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

// SurroundList is Surround around an indented List, inline or exploded depending on whether start/sep/end
// contain "\n":
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
