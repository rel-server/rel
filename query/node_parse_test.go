package query

import "testing"

func TestParseQuery_BareRelation(t *testing.T) {
	pq, err := ParseQuery([]byte(`{"relation": "movie", "schema": "api"}`))
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if pq.Relation == nil || pq.Relation.Relation != "movie" || pq.Relation.Schema != "api" {
		t.Fatalf("unexpected ParsedQuery: %#v", pq)
	}
}

func TestParseQuery_WriteQuery(t *testing.T) {
	pq, err := ParseQuery([]byte(`{"query": {"relation": "movie"}, "data": {"title": "x"}}`))
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if pq.Write == nil || pq.Write.Query.Relation != "movie" || len(pq.Write.Data) == 0 {
		t.Fatalf("unexpected ParsedQuery: %#v", pq)
	}
}

func TestParseQuery_WellKnown(t *testing.T) {
	pq, err := ParseQuery([]byte(`{"wellknown": "my_query", "params": {"a": 1}}`))
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if pq.WellKnown == nil || pq.WellKnown.WellKnown != "my_query" || len(pq.WellKnown.Params) == 0 {
		t.Fatalf("unexpected ParsedQuery: %#v", pq)
	}
}

func TestParseQuery_Sequence(t *testing.T) {
	pq, err := ParseQuery([]byte(`[{"relation": "a"}, {"relation": "b"}]`))
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if len(pq.Sequence) != 2 || pq.Sequence[0].Relation.Relation != "a" || pq.Sequence[1].Relation.Relation != "b" {
		t.Fatalf("unexpected ParsedQuery: %#v", pq)
	}
}

func TestParseRawRelation_Join(t *testing.T) {
	pq, err := ParseQuery([]byte(`{
		"relation": "movie",
		"join": {
			"actors": {"relation": "actor", "on": {"movie_id": "id"}}
		}
	}`))
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	child, ok := pq.Relation.Join["actors"]
	if !ok {
		t.Fatalf("expected join alias \"actors\", got %#v", pq.Relation.Join)
	}
	if child.Relation != "actor" || child.On["movie_id"] != "id" {
		t.Fatalf("unexpected child: %#v", child)
	}
}

func TestParseRawRelation_ArgumentsTriState(t *testing.T) {
	// no "arguments" key at all : not a function
	pq, err := ParseQuery([]byte(`{"relation": "movie"}`))
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if pq.Relation.IsFunction {
		t.Errorf("expected IsFunction=false when \"arguments\" is absent")
	}

	// present but empty array : still a function, zero args
	pq, err = ParseQuery([]byte(`{"relation": "fn", "arguments": []}`))
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if !pq.Relation.IsFunction || len(pq.Relation.ArgumentsPositional) != 0 {
		t.Fatalf("expected IsFunction=true with 0 positional args, got %#v", pq.Relation)
	}

	// named form
	pq, err = ParseQuery([]byte(`{"relation": "fn", "arguments": {"a": 1}}`))
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if !pq.Relation.IsFunction || pq.Relation.ArgumentsNamed == nil {
		t.Fatalf("expected IsFunction=true with named args, got %#v", pq.Relation)
	}
	if _, ok := pq.Relation.ArgumentsNamed["a"].(NumberLiteral); !ok {
		t.Errorf("expected arguments.a to be a NumberLiteral, got %#v", pq.Relation.ArgumentsNamed["a"])
	}
}

func TestParseRawRelation_OnConflictForms(t *testing.T) {
	pq, err := ParseQuery([]byte(`{"relation": "movie", "on_conflict": "movie_pkey"}`))
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if pq.Relation.OnConflictConstraintName != "movie_pkey" {
		t.Errorf("expected constraint name form, got %#v", pq.Relation)
	}

	pq, err = ParseQuery([]byte(`{"relation": "movie", "on_conflict": ["a", "b"]}`))
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if len(pq.Relation.OnConflictColumns) != 2 {
		t.Errorf("expected column-list form, got %#v", pq.Relation)
	}
}

func TestParseRawRelation_OrderBy(t *testing.T) {
	pq, err := ParseQuery([]byte(`{"relation": "movie", "order_by": ["year", ["desc", "title"]]}`))
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if len(pq.Relation.OrderBy) != 2 {
		t.Fatalf("expected 2 order_by terms, got %d", len(pq.Relation.OrderBy))
	}
	if pq.Relation.OrderBy[0].Direction != OrderAsc {
		t.Errorf("expected bare expression to default OrderAsc, got %v", pq.Relation.OrderBy[0].Direction)
	}
	if id, ok := pq.Relation.OrderBy[0].Expr.(*Identifier); !ok || id.Name != "year" {
		t.Errorf("expected first term to be Identifier(year), got %#v", pq.Relation.OrderBy[0].Expr)
	}
	if pq.Relation.OrderBy[1].Direction != OrderDesc {
		t.Errorf("expected second term OrderDesc, got %v", pq.Relation.OrderBy[1].Direction)
	}
	if id, ok := pq.Relation.OrderBy[1].Expr.(*Identifier); !ok || id.Name != "title" {
		t.Errorf("expected second term to be Identifier(title), got %#v", pq.Relation.OrderBy[1].Expr)
	}
}

func TestParseRawRelation_OffsetLimit(t *testing.T) {
	pq, err := ParseQuery([]byte(`{"relation": "movie", "offset": 10, "limit": 5}`))
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if pq.Relation.Offset == nil || *pq.Relation.Offset != 10 {
		t.Errorf("unexpected Offset: %#v", pq.Relation.Offset)
	}
	if pq.Relation.Limit == nil || *pq.Relation.Limit != 5 {
		t.Errorf("unexpected Limit: %#v", pq.Relation.Limit)
	}
}

// A 2-element array whose first element is a string that ISN'T one of the
// four direction tags must be parsed as a bare Expression, not misdetected
// as an [direction, Expression] tuple — this is the exact ambiguity the
// tag-membership check in parseOrderByTerm exists to avoid.
func TestParseRawRelation_OrderBy_NoTagCollision(t *testing.T) {
	pq, err := ParseQuery([]byte(`{"relation": "movie", "order_by": [["own-except", ["title"]]]}`))
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if len(pq.Relation.OrderBy) != 1 {
		t.Fatalf("expected 1 order_by term, got %d", len(pq.Relation.OrderBy))
	}
	term := pq.Relation.OrderBy[0]
	if term.Direction != OrderAsc {
		t.Errorf("expected OrderAsc (not mistaken for a direction tuple), got %v", term.Direction)
	}
	except, ok := term.Expr.(OwnExceptExpr)
	if !ok || len(except.Except) != 1 || except.Except[0] != "title" {
		t.Errorf("expected OwnExceptExpr{[title]}, got %#v", term.Expr)
	}
}

func TestParseRawRelation_MissingRelationKey(t *testing.T) {
	if _, err := ParseQuery([]byte(`{"schema": "public"}`)); err == nil {
		t.Fatalf("expected a missing \"relation\" key to be rejected")
	}
}

func TestParseRawRelation_ArgumentsWrongType(t *testing.T) {
	if _, err := ParseQuery([]byte(`{"relation": "fn", "arguments": "not an array or object"}`)); err == nil {
		t.Fatalf("expected a non-array/object \"arguments\" to be rejected")
	}
}

func TestParseQuery_TopLevelWrongType(t *testing.T) {
	if _, err := ParseQuery([]byte(`"not a query"`)); err == nil {
		t.Fatalf("expected a bare string at the top level to be rejected")
	}
	if _, err := ParseQuery([]byte(`42`)); err == nil {
		t.Fatalf("expected a bare number at the top level to be rejected")
	}
}

func TestParseQuery_WriteQuery_MissingData(t *testing.T) {
	if _, err := ParseQuery([]byte(`{"query": {"relation": "movie"}}`)); err == nil {
		t.Fatalf("expected a WriteQuery without \"data\" to be rejected")
	}
}

func TestParseQuery_MixedSequence(t *testing.T) {
	pq, err := ParseQuery([]byte(`[
		{"relation": "a"},
		{"query": {"relation": "b"}, "data": {}},
		{"wellknown": "c"}
	]`))
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if len(pq.Sequence) != 3 {
		t.Fatalf("expected 3 sequence items, got %d", len(pq.Sequence))
	}
	if pq.Sequence[0].Relation == nil || pq.Sequence[1].Write == nil || pq.Sequence[2].WellKnown == nil {
		t.Fatalf("expected [Relation, Write, WellKnown], got %#v", pq.Sequence)
	}
}
