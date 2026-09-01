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
		{"bare identifier", "movie_id", "movie_id"},
		{"dotted identifier", "actors.name", "actors.name"},
		{"string literal", "'open'", []any{"open"}},
		{"string literal with doubled quote", "'it''s open'", []any{"it's open"}},
		{"number", "1999", float64(1999)},
		{"negative number", "-4", float64(-4)},
		{"decimal", "1.5", float64(1.5)},
		{"true", "true", true},
		{"false", "false", false},
		{"null", "null", nil},
		{"gte word form", "gte(year,1999)", []any{">=", "year", float64(1999)}},
		{"like with wildcard", "like(name,'%needle%')", []any{"like", "name", []any{"%needle%"}}},
		{
			"and/gte/like nested",
			"and(gte(year,1999),like(name,'%needle%'))",
			[]any{"and", []any{">=", "year", float64(1999)}, []any{"like", "name", []any{"%needle%"}}},
		},
		{"in", "in(status,'open','closed')", []any{"in", "status", "open", "closed"}},
		{"not_in", "not_in(status,'open')", []any{"not_in", "status", "open"}},
		{"between", "between(0,age,150)", []any{"between", float64(0), "age", float64(150)}},
		{"not_between", "not_between(0,age,150)", []any{"not_between", float64(0), "age", float64(150)}},
		{"bigint", "bigint('9223372036854775807')", []any{"bigint", "9223372036854775807"}},
		{"numeric", "numeric('123.456')", []any{"numeric", "123.456"}},
		{"is_null unary", "is_null(name)", []any{"is_null", "name"}},
		{"neg unary", "neg(age)", []any{"-", "age"}},
		{"call bare function", "call(pg_catalog.upper,name)", []any{"call", map[string]any{"schema": "pg_catalog", "name": "upper"}, "name"}},
		{"call unqualified function", "call(upper,name)", []any{"call", "upper", "name"}},
		{"unrecognized identifier is a plain function call", "lower(name)", []any{"call", "lower", "name"}},
		{"agg", "agg(sum,orders.amount)", []any{"agg", "sum", []any{"orders.amount"}}},
		{"concat_ws", "concat_ws(' ',a,b)", []any{"concat_ws", []any{" "}, "a", "b"}},
		{"format", "format('Hello %s',name)", []any{"format", "Hello %s", "name"}},
		{"any word form", "any(gte,x,y)", []any{"any", ">=", "x", "y"}},
		{"eq with parent alias", "eq(m.language,'en')", []any{"=", "m.language", []any{"en"}}},
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

func TestParseFullExpr_InCandidateMustBeLiteral(t *testing.T) {
	if _, err := parseFullExpr("in(status,open)"); err == nil {
		t.Fatalf("expected an error : an unquoted in() candidate must not be silently treated as a column")
	}
}

func TestParseFullExpr_OwnFullFamilyNotUsableAsSubExpression(t *testing.T) {
	if _, err := parseFullExpr("and(eq(a,1),own_except(x))"); err == nil {
		t.Fatalf("expected an error : own_except(...) is select-only, not a sub-expression")
	}
}

func TestParseFullExpr_NegSubBnotMatchEnforceTheirOwnArity(t *testing.T) {
	// neg/bnot (unary) and sub/match (binary/folded) exist specifically so
	// "-" and "~"'s two different-arity query.ts meanings never collide in
	// this grammar (specs/query-json.md) — using the wrong word for the
	// argument count given must be a clear error, not a silent swap to the
	// OTHER operator's meaning.
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
	want := []any{"in", "status", "a,b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestParseTopLevelExprList(t *testing.T) {
	got, err := parseTopLevelExprList("movie_id,name,actors")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []any{"movie_id", "name", "actors"}
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
