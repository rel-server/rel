package writer

import "testing"

func TestWriter_Indentation(t *testing.T) {
	w := New()
	w.Write("a\n")
	w.Indented(func() {
		w.Write("b\n")
		w.Indented(func() {
			w.Write("c")
		})
	})
	got := w.String()
	want := "a\n  b\n    c"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWriter_Surround(t *testing.T) {
	w := New()
	w.Surround("(", ")", func() {
		w.Write("x")
	})
	if got, want := w.String(), "(x)"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWriter_Paren(t *testing.T) {
	w := New()
	w.Paren(func() {
		w.Write("x")
	})
	if got, want := w.String(), "(x)"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWriter_List_Inline(t *testing.T) {
	w := New()
	List(w, ", ", []string{"a", "b", "c"}, func(s string) { w.Write(s) })
	if got, want := w.String(), "a, b, c"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWriter_SurroundList_InlineVsExploded(t *testing.T) {
	items := []string{"a", "b"}

	inline := New()
	SurroundList(inline, "(", ", ", ")", items, func(s string) { inline.Write(s) })
	if got, want := inline.String(), "(a, b)"; got != want {
		t.Fatalf("inline: got %q, want %q", got, want)
	}

	exploded := New()
	SurroundList(exploded, "(\n", ",\n", "\n)", items, func(s string) { exploded.Write(s) })
	if got, want := exploded.String(), "(\n  a,\n  b\n)"; got != want {
		t.Fatalf("exploded: got %q, want %q", got, want)
	}
}

func TestWriter_ListSeq(t *testing.T) {
	w := New()
	seq := func(yield func(string) bool) {
		for _, s := range []string{"x", "y", "z"} {
			if !yield(s) {
				return
			}
		}
	}
	ListSeq(w, "-", seq, func(s string) { w.Write(s) })
	if got, want := w.String(), "x-y-z"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWriter_SeparatedBy(t *testing.T) {
	w := New()
	items := []string{"p", "q", "r"}
	i := 0
	w.SeparatedBy(", ", func() bool {
		w.Write(items[i])
		i++
		return i < len(items)
	})
	if got, want := w.String(), "p, q, r"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWriter_UnindentWithoutIndent_Panics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("expected Unindent without a matching Indent to panic")
		}
	}()
	New().Unindent()
}
