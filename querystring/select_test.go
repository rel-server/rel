package querystring

import (
	"reflect"
	"testing"
)

func TestCompileSelect(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want any
	}{
		{"plain comma-list", "movie_id,name,actors", map[string]any{
			"movie_id": []any{".", "movie_id"}, "name": []any{".", "name"}, "actors": []any{".", "actors"},
		}},
		{"aliased computed entry", "movie_id,name,total:agg(sum,orders.amount)", map[string]any{
			"movie_id": []any{".", "movie_id"},
			"name":     []any{".", "name"},
			"total":    []any{"agg", "sum", []any{[]any{".", "orders.amount"}}},
		}},
		{"full bare", "*", []any{"*"}},
		{"own bare", "*~", []any{"*~"}},
		{"full except", "*(a,b)", []any{"*", []string{"a", "b"}}},
		{"own except", "*~(a,b)", []any{"*~", []string{"a", "b"}}},
		{"own and", "*~(;actors,total:agg(sum,orders.amount))", []any{"*~", map[string]any{
			"actors": []any{".", "actors"},
			"total":  []any{"agg", "sum", []any{[]any{".", "orders.amount"}}},
		}}},
		{"own except and", "*~(a,b; total:agg(sum,orders.amount))", []any{
			"*~",
			[]string{"a", "b"},
			map[string]any{"total": []any{"agg", "sum", []any{[]any{".", "orders.amount"}}}},
		}},
		{"colon inside quoted literal doesn't split alias early", "label:'a:b'", map[string]any{
			"label": "a:b",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := compileSelect(tc.in)
			if err != nil {
				t.Fatalf("compileSelect(%q): unexpected error: %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("compileSelect(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestCompileSelect_BareNonIdentifierIsError(t *testing.T) {
	if _, err := compileSelect("gte(a,b)"); err == nil {
		t.Fatalf("expected an error : a non-identifier entry with no alias has no object key")
	}
}

func TestCompileSelect_StarFollowedByTrailingCommaIsAnError(t *testing.T) {
	// "*" isn't a valid identifier-start character, so it can never be one
	// entry of a plain comma-list — trailing content after it (that isn't
	// "(...)") is a hard parse error, not a silent fallthrough.
	if _, err := compileSelect("*,name"); err == nil {
		t.Fatalf("expected an error : trailing input after a bare \"*\" is invalid")
	}
}
