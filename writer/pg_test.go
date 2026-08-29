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
