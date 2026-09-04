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
			"movie_id": "movie_id", "name": "name", "actors": "actors",
		}},
		{"aliased computed entry", "movie_id,name,total:agg(sum,orders.amount)", map[string]any{
			"movie_id": "movie_id",
			"name":     "name",
			"total":    []any{"agg", "sum", []any{"orders.amount"}},
		}},
		{"own bare", "own", []any{"own"}},
		{"full bare", "full", []any{"full"}},
		{"own_except", "own_except(a,b)", []any{"own_except", []string{"a", "b"}}},
		{"full_except", "full_except(a,b)", []any{"full_except", []string{"a", "b"}}},
		{"own_and", "own_and(actors,total:agg(sum,orders.amount))", []any{"own_and", map[string]any{
			"actors": "actors",
			"total":  []any{"agg", "sum", []any{"orders.amount"}},
		}}},
		{"own_except_and", "own_except_and(a,b; total:agg(sum,orders.amount))", []any{
			"own_except_and",
			[]string{"a", "b"},
			map[string]any{"total": []any{"agg", "sum", []any{"orders.amount"}}},
		}},
		{"colon inside quoted literal doesn't split alias early", "label:'a:b'", map[string]any{
			"label": []any{"a:b"},
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

func TestCompileSelect_OwnPlusCommaListIsNotTheWholeValueForm(t *testing.T) {
	// own/full dispatch only fires when it's the entire value ; "own,name"
	// falls through to the comma-list compiler, "own" as a bare identifier.
	got, err := compileSelect("own,name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]any{"own": "own", "name": "name"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
