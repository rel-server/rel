package query

import "testing"

func TestParseWellKnownFile_SingleDefinition(t *testing.T) {
	defs, err := ParseWellKnownFile([]byte(`{
		"name": "directors_by_name",
		"params": {
			"name": {"type": "text"}
		},
		"query": {"relation": "director", "schema": "public", "select": ["own"]}
	}`))
	if err != nil {
		t.Fatalf("ParseWellKnownFile: %v", err)
	}
	if len(defs) != 1 {
		t.Fatalf("expected 1 definition, got %d", len(defs))
	}
	def := defs[0]
	if def.Name != "directors_by_name" {
		t.Errorf("expected name=directors_by_name, got %q", def.Name)
	}
	if def.Query == nil {
		t.Fatalf("expected a parsed query tree")
	}
	p, ok := def.Params["name"]
	if !ok {
		t.Fatalf("expected a %q param", "name")
	}
	if p.Type != "text" {
		t.Errorf("expected type=text, got %q", p.Type)
	}
	if p.HasDefault {
		t.Errorf("expected no default (required param), got HasDefault=true")
	}
}

func TestParseWellKnownFile_ArrayOfDefinitions(t *testing.T) {
	defs, err := ParseWellKnownFile([]byte(`[
		{"name": "a", "query": {"relation": "director", "schema": "public", "select": ["own"]}},
		{"name": "b", "query": {"relation": "director", "schema": "public", "select": ["own"]}}
	]`))
	if err != nil {
		t.Fatalf("ParseWellKnownFile: %v", err)
	}
	if len(defs) != 2 || defs[0].Name != "a" || defs[1].Name != "b" {
		t.Fatalf("expected [a, b], got %#v", defs)
	}
}

// TestParseWellKnownFile_DefaultPresenceStates pins the spec's three
// distinct default states (## Definition) : the param's own HasDefault/
// Default fields must tell "no default key" (required), "default: null"
// (optional, defaults to SQL NULL), and "default: <value>" apart — a plain
// map[string]any lookup on the decoded JSON can't distinguish the first two.
func TestParseWellKnownFile_DefaultPresenceStates(t *testing.T) {
	defs, err := ParseWellKnownFile([]byte(`{
		"name": "three_states",
		"params": {
			"required": {"type": "int"},
			"null_default": {"type": "int", "default": null},
			"value_default": {"type": "int", "default": 5}
		},
		"query": {"relation": "director", "schema": "public", "select": ["own"]}
	}`))
	if err != nil {
		t.Fatalf("ParseWellKnownFile: %v", err)
	}
	params := defs[0].Params

	if p := params["required"]; p.HasDefault {
		t.Errorf("required: expected HasDefault=false, got true (default=%s)", p.Default)
	}
	if p := params["null_default"]; !p.HasDefault || string(p.Default) != "null" {
		t.Errorf("null_default: expected HasDefault=true, Default=null, got HasDefault=%v Default=%s", p.HasDefault, p.Default)
	}
	if p := params["value_default"]; !p.HasDefault || string(p.Default) != "5" {
		t.Errorf("value_default: expected HasDefault=true, Default=5, got HasDefault=%v Default=%s", p.HasDefault, p.Default)
	}
}

func TestParseWellKnownFile_MissingName_Errors(t *testing.T) {
	_, err := ParseWellKnownFile([]byte(`{"query": {"relation": "director", "schema": "public", "select": ["own"]}}`))
	if err == nil {
		t.Fatalf("expected an error for a missing name")
	}
}

func TestParseWellKnownFile_MissingQuery_Errors(t *testing.T) {
	_, err := ParseWellKnownFile([]byte(`{"name": "no_query"}`))
	if err == nil {
		t.Fatalf("expected an error for a missing query")
	}
}
