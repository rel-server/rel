package querystring

import (
	"reflect"
	"testing"
)

func TestDecodeStructural_Flat(t *testing.T) {
	got, err := DecodeStructural("relation=movie&schema=api&alias=m")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]any{"relation": "movie", "schema": "api", "alias": "m"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestDecodeStructural_DotPathNested(t *testing.T) {
	got, err := DecodeStructural("join.actors.relation=actor&join.actors.on.actor_id=movie_id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]any{
		"join": map[string]any{
			"actors": map[string]any{
				"relation": "actor",
				"on":       map[string]any{"actor_id": "movie_id"},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestDecodeStructural_RepeatedKeyBuildsArray(t *testing.T) {
	got, err := DecodeStructural("insert_columns=a&insert_columns=b&insert_columns=c")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]any{"insert_columns": []any{"a", "b", "c"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestDecodeStructural_DeepNesting(t *testing.T) {
	got, err := DecodeStructural("join.actors.join.awards.relation=award")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]any{
		"join": map[string]any{
			"actors": map[string]any{
				"join": map[string]any{
					"awards": map[string]any{"relation": "award"},
				},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestDecodeStructural_PercentAndPlusDecoding(t *testing.T) {
	got, err := DecodeStructural("where=" + "like%28name%2C%27a+b%27%29")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]any{"where": "like(name,'a b')"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestDecodeStructural_ConflictingLeafAndPathIsError(t *testing.T) {
	if _, err := DecodeStructural("a=1&a.b=2"); err == nil {
		t.Fatalf("expected an error for a key used both as a leaf and a nested path")
	}
}

func TestDecodeStructural_SemicolonInValueSurvives(t *testing.T) {
	// Go's net/url.ParseQuery rejects any raw query string containing a
	// literal ';' outright (empty result + error) — this package must not
	// use it, since specs/query-json.md's own_except_and grammar relies on
	// a literal ';' appearing inside one key's value.
	got, err := DecodeStructural("select=own_except_and(a,b;total:x)")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]any{"select": "own_except_and(a,b;total:x)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestDecodeStructural_Empty(t *testing.T) {
	got, err := DecodeStructural("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected an empty map, got %#v", got)
	}
}
