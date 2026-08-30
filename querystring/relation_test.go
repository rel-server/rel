package querystring

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ceymard/rel/query"
)

// TestDecodeRelation_SpecWorkedExample reproduces specs/query_json.md's own
// ## Structural layer worked example verbatim : the raw query string at the
// top of that section must decode to exactly the JSON shown right below it.
func TestDecodeRelation_SpecWorkedExample(t *testing.T) {
	raw := "relation=movie&schema=api&alias=m" +
		"&select=movie_id,name,actors" +
		"&where=and(gte(year,1999),like(name,'%25needle%25'))" +
		"&order_by=name,-year" +
		"&limit=20&offset=0" +
		"&distinct=true&distinct_on=name,year" +
		"&join.actors.relation=actor&join.actors.schema=api" +
		"&join.actors.on.actor_id=movie_id" +
		"&join.actors.select=name" +
		"&join.actors.where=and(eq(active,true),eq(m.language,'en'))" +
		"&join.actors.join.awards.relation=award" +
		"&join.actors.join.awards.on.actor_id=actor_id"

	out, err := DecodeRelation(raw)
	if err != nil {
		t.Fatalf("DecodeRelation: unexpected error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshaling compiled JSON: %v", err)
	}

	wantJSON := `{
	  "relation": "movie", "schema": "api", "alias": "m",
	  "select": {"movie_id": "movie_id", "name": "name", "actors": "actors"},
	  "where": ["and", [">=", "year", 1999], ["like", "name", ["%needle%"]]],
	  "order_by": ["name", ["desc", "year"]],
	  "limit": 20, "offset": 0,
	  "distinct": true, "distinct_on": ["name", "year"],
	  "join": {
	    "actors": {
	      "relation": "actor", "schema": "api",
	      "on": {"actor_id": "movie_id"},
	      "select": {"name": "name"},
	      "where": ["and", ["=", "active", true], ["=", "m.language", ["en"]]],
	      "join": {
	        "awards": { "relation": "award", "on": {"actor_id": "actor_id"} }
	      }
	    }
	  }
	}`
	var want map[string]any
	if err := json.Unmarshal([]byte(wantJSON), &want); err != nil {
		t.Fatalf("unmarshaling expected JSON: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DecodeRelation(%q) =\n%#v\nwant\n%#v", raw, got, want)
	}

	// The whole architecture rests on this : the compiled bytes must feed
	// straight into the EXISTING query.ParseQuery pipeline unchanged (see
	// this session's own architecture decision — no second, parallel
	// Expression parser).
	pq, err := query.ParseQuery(out)
	if err != nil {
		t.Fatalf("query.ParseQuery(compiled bytes): unexpected error: %v", err)
	}
	if pq.Relation == nil {
		t.Fatalf("expected pq.Relation to be set, got %#v", pq)
	}
}

func TestDecodeRelation_RejectsWriteModeAtRoot(t *testing.T) {
	if _, err := DecodeRelation("relation=movie&write_mode=merge"); err == nil {
		t.Fatalf("expected a 400-worthy error for write_mode at the root")
	}
}

func TestDecodeRelation_RejectsWriteModeInNestedJoin(t *testing.T) {
	raw := "relation=movie&join.actors.relation=actor&join.actors.on.actor_id=movie_id&join.actors.write_mode=merge"
	if _, err := DecodeRelation(raw); err == nil {
		t.Fatalf("expected a 400-worthy error for write_mode nested inside join.actors")
	}
}

func TestDecodeRelation_RejectsOnConflictInsertColumnsUpdateColumns(t *testing.T) {
	for _, raw := range []string{
		"relation=movie&on_conflict=id",
		"relation=movie&insert_columns=a&insert_columns=b",
		"relation=movie&update_columns=a",
	} {
		if _, err := DecodeRelation(raw); err == nil {
			t.Fatalf("expected an error for %q", raw)
		}
	}
}

func TestDecodeRelation_ArgumentIndexOutOfRangeIsRejected(t *testing.T) {
	// A single crafted key must not be able to force a huge allocation
	// (arguments.<n> -> make([]any, n+1)) or overflow strconv.Atoi into a
	// panic from make() — both are a 400, not a resource-exhaustion/crash.
	for _, raw := range []string{
		"relation=movie&function=f&arguments.100000000=x",
		"relation=movie&function=f&arguments.99999999999999999999=x",
	} {
		if _, err := DecodeRelation(raw); err == nil {
			t.Fatalf("expected an out-of-range error for %q", raw)
		}
	}
}

func TestDecodeRelation_SelectAliasNamedWriteModeIsNotFalselyRejected(t *testing.T) {
	// A user-chosen select alias happening to be spelled "write_mode" must
	// not false-positive the GET-only rejection — that check walks the
	// decoded Relation STRUCTURE (only the real write_mode field, and only
	// through "join"), never a blind key-name scan.
	out, err := DecodeRelation("relation=movie&select=write_mode:name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshaling: %v", err)
	}
	sel, ok := got["select"].(map[string]any)
	if !ok || sel["write_mode"] != "name" {
		t.Fatalf("expected select.write_mode == \"name\", got %#v", got["select"])
	}
}

func TestDecodeRelation_UnrecognizedKeyIsError(t *testing.T) {
	if _, err := DecodeRelation("relation=movie&totally_unknown_field=x"); err == nil {
		t.Fatalf("expected an error for an unrecognized top-level key")
	}
}

func TestDecodeQueryField_StructuralOnly(t *testing.T) {
	// /rpc's query field decode must NOT run the filter expression grammar
	// — a value like "gte(year,1999)" stays a plain string, not a compiled
	// Expression array.
	got, err := DecodeQueryField("filter=gte(year,1999)&page.size=20")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]any{
		"filter": "gte(year,1999)",
		"page":   map[string]any{"size": "20"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestDecodeQueryField_EmptyIsNil(t *testing.T) {
	got, err := DecodeQueryField("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for an empty query string, got %#v", got)
	}
}
