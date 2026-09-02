package writer

import "testing"

func TestEscapeSQLId_PlainIdentifier(t *testing.T) {
	if got, want := EscapeSQLId("rooms"), "rooms"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEscapeSQLId_ReservedKeyword(t *testing.T) {
	if got, want := EscapeSQLId("order"), `"order"`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEscapeSQLId_MixedCaseRequiresQuoting(t *testing.T) {
	// validUnquotedId only matches all-lowercase — Postgres folds unquoted
	// identifiers to lowercase, so a mixed-case name must be quoted or it
	// would silently refer to something else.
	if got, want := EscapeSQLId("MixedCase"), `"MixedCase"`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEscapeSQLId_EmbeddedQuoteIsDoubled(t *testing.T) {
	if got, want := EscapeSQLId(`weird"name`), `"weird""name"`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEscapeSQLId_SchemaQualified(t *testing.T) {
	// Each dot-separated part is quoted independently : hotel.rooms stays
	// entirely unquoted, hotel."order" quotes only the reserved part.
	if got, want := EscapeSQLId("hotel.rooms"), "hotel.rooms"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := EscapeSQLId("hotel.order"), `hotel."order"`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSQLWriter_Id(t *testing.T) {
	w := NewSQL()
	w.Id("hotel.order")
	if got, want := w.String(), `hotel."order"`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSQLWriter_Bind_SequentialPlaceholdersAndArgs(t *testing.T) {
	w := NewSQL()
	w.Write("select ")
	w.Bind("alice")
	w.Write(", ")
	w.Bind(42)

	if got, want := w.String(), "select $1, $2"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	args := w.Args()
	if len(args) != 2 || args[0] != "alice" || args[1] != 42 {
		t.Fatalf("expected Args() == [alice, 42], got %#v", args)
	}
}

func TestSQLWriter_Bind_FreshPerWriter(t *testing.T) {
	// Each SQLWriter has its own $N sequence — a second, independent
	// statement's Bind calls must start back at $1, not continue counting
	// from an unrelated writer (see Bind's doc comment : one SQLWriter per
	// statement).
	w1 := NewSQL()
	w1.Bind("x")
	w1.Bind("y")

	w2 := NewSQL()
	w2.Bind("z")

	if got, want := w2.String(), "$1"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if len(w2.Args()) != 1 || w2.Args()[0] != "z" {
		t.Fatalf("expected w2.Args() == [z], got %#v", w2.Args())
	}
}

func TestSQLWriter_BindParam_SharesPositionalSequenceWithBind(t *testing.T) {
	w := NewSQL()
	w.Write("select ")
	w.Bind("literal")
	w.Write(", ")
	w.BindParam("name")
	w.Write(", ")
	w.Bind(7)

	if got, want := w.String(), "select $1, $2, $3"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSQLWriter_Args_PanicsWithUnresolvedParam(t *testing.T) {
	w := NewSQL()
	w.BindParam("x")

	defer func() {
		if recover() == nil {
			t.Fatalf("expected Args() to panic on an unresolved $param")
		}
	}()
	w.Args()
}

func TestSQLWriter_ParamNames_DistinctInFirstOccurrenceOrder(t *testing.T) {
	w := NewSQL()
	w.BindParam("b")
	w.BindParam("a")
	w.BindParam("b")

	got := w.ParamNames()
	want := []string{"b", "a"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestSQLWriter_ResolveArgs_MixesLiteralsAndNamedParams(t *testing.T) {
	w := NewSQL()
	w.Bind("literal")
	w.BindParam("name")
	w.Bind(7)
	w.BindParam("other")

	args, err := w.ResolveArgs(map[string]any{"name": "Alice", "other": 42})
	if err != nil {
		t.Fatalf("ResolveArgs: %v", err)
	}
	want := []any{"literal", "Alice", 7, 42}
	if len(args) != len(want) {
		t.Fatalf("got %#v, want %#v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("arg %d: got %#v, want %#v", i, args[i], want[i])
		}
	}
}

func TestSQLWriter_ResolveArgs_MissingParamErrors(t *testing.T) {
	w := NewSQL()
	w.BindParam("required")

	if _, err := w.ResolveArgs(map[string]any{}); err == nil {
		t.Fatalf("expected an error for a missing param value")
	}
}
