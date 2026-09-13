package querystring

import (
	"reflect"
	"testing"
)

func TestParseFullExpr(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want any
	}{
		{"bare identifier", "movie_id", []any{".", "movie_id"}},
		{"dotted identifier", "actors.name", []any{".", "actors.name"}},
		{"string literal", "'open'", "open"},
		{"string literal with doubled quote", "'it''s open'", "it's open"},
		{"number", "1999", float64(1999)},
		{"negative number", "-4", float64(-4)},
		{"decimal", "1.5", float64(1.5)},
		{"true", "true", true},
		{"false", "false", false},
		{"null", "null", nil},
		{"gte word form", "gte(year,1999)", []any{">=", []any{".", "year"}, float64(1999)}},
		{"like with wildcard", "like(name,'%needle%')", []any{"like", []any{".", "name"}, "%needle%"}},
		{
			"and/gte/like nested",
			"and(gte(year,1999),like(name,'%needle%'))",
			[]any{"and", []any{">=", []any{".", "year"}, float64(1999)}, []any{"like", []any{".", "name"}, "%needle%"}},
		},
		{"in", "in(status,'open','closed')", []any{"in", []any{".", "status"}, "open", "closed"}},
		{"not_in", "not_in(status,'open')", []any{"not_in", []any{".", "status"}, "open"}},
		{"between", "between(0,age,150)", []any{"between", float64(0), []any{".", "age"}, float64(150)}},
		{"not_between", "not_between(0,age,150)", []any{"not_between", float64(0), []any{".", "age"}, float64(150)}},
		{"bigint", "bigint('9223372036854775807')", []any{"bigint", "9223372036854775807"}},
		{"numeric", "numeric('123.456')", []any{"numeric", "123.456"}},
		{"is_null unary", "is_null(name)", []any{"is_null", []any{".", "name"}}},
		{"neg unary", "neg(age)", []any{"-", []any{".", "age"}}},
		{"call bare function", "call(pg_catalog.upper,name)", []any{"call", map[string]any{"schema": "pg_catalog", "name": "upper"}, []any{".", "name"}}},
		{"call unqualified function", "call(upper,name)", []any{"call", "upper", []any{".", "name"}}},
		{"unrecognized identifier is a plain function call", "lower(name)", []any{"call", "lower", []any{".", "name"}}},
		{"agg", "agg(sum,orders.amount)", []any{"agg", "sum", []any{[]any{".", "orders.amount"}}}},
		{"concat_ws", "concat_ws(' ',a,b)", []any{"concat_ws", " ", []any{".", "a"}, []any{".", "b"}}},
		{"format", "format('Hello %s',name)", []any{"format", "Hello %s", []any{".", "name"}}},
		{"any word form", "any(gte,x,y)", []any{"any", ">=", []any{".", "x"}, []any{".", "y"}}},
		{"eq with parent alias", "eq(m.language,'en')", []any{"=", []any{".", "m.language"}, "en"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFullExpr(tc.in)
			if err != nil {
				t.Fatalf("parseFullExpr(%q): unexpected error: %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseFullExpr(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseFullExpr_InCandidateIsNowAColumnReferenceLikeAnywhereElse(t *testing.T) {
	// The old carve-out (an unquoted in() candidate must be quoted to be a
	// literal) is gone : a bare, unquoted candidate is a [".", ...]
	// reference now, same as any other unquoted value — no special-casing.
	got, err := parseFullExpr("in(status,open)")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []any{"in", []any{".", "status"}, []any{".", "open"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestParseFullExpr_StarNotUsableAsSubExpression(t *testing.T) {
	// "*"/"*~" aren't valid identifier-start characters, so they can never
	// even be parsed as a nested call/atom — this is now a plain lexer
	// error, not a dedicated "select-only" rejection.
	if _, err := parseFullExpr("and(eq(a,1),*)"); err == nil {
		t.Fatalf("expected an error : \"*\" cannot appear inside a general expression")
	}
}

func TestParseFullExpr_NegSubBnotMatchEnforceTheirOwnArity(t *testing.T) {
	// Wrong argument count for the word used must error, not silently
	// swap to the other same-symbol operator's meaning (specs/query-json.md).
	for _, in := range []string{"neg(5,3)", "bnot(name,'foo')", "match(status)", "sub(5)"} {
		if _, err := parseFullExpr(in); err == nil {
			t.Fatalf("parseFullExpr(%q) : expected an arity error, got none", in)
		}
	}

	got, err := parseFullExpr("neg(5)")
	if err != nil {
		t.Fatalf("neg(5): unexpected error: %v", err)
	}
	if want := []any{"-", 5.0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("neg(5) = %#v, want %#v", got, want)
	}

	got, err = parseFullExpr("sub(5,3,1)")
	if err != nil {
		t.Fatalf("sub(5,3,1): unexpected error: %v", err)
	}
	if want := []any{"-", 5.0, 3.0, 1.0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sub(5,3,1) = %#v, want %#v", got, want)
	}
}

func TestParseFullExpr_CommaInsideNestedLiteralDoesNotSplitWrong(t *testing.T) {
	got, err := parseFullExpr("in(status,'a,b')")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []any{"in", []any{".", "status"}, "a,b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestParseTopLevelExprList(t *testing.T) {
	got, err := parseTopLevelExprList("movie_id,name,actors")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []any{[]any{".", "movie_id"}, []any{".", "name"}, []any{".", "actors"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestParseTopLevelExprList_CommaInsideCallNotSplit(t *testing.T) {
	got, err := parseTopLevelExprList("movie_id,in(status,'a,b')")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries (comma inside literal must not split), got %d: %#v", len(got), got)
	}
}
