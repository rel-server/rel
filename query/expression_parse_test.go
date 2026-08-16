package query

import "testing"

func mustParse(t *testing.T, src string) Expression {
	t.Helper()
	expr, err := ParseExpression([]byte(src))
	if err != nil {
		t.Fatalf("ParseExpression(%s): %v", src, err)
	}
	return expr
}

func TestParseExpression_Atoms(t *testing.T) {
	if _, ok := mustParse(t, `null`).(NullLiteral); !ok {
		t.Errorf("null did not parse as NullLiteral")
	}
	if _, ok := mustParse(t, `true`).(BoolLiteral); !ok {
		t.Errorf("true did not parse as BoolLiteral")
	}
	if n, ok := mustParse(t, `42`).(NumberLiteral); !ok || n.Value != 42 {
		t.Errorf("42 did not parse as NumberLiteral(42), got %#v", mustParse(t, `42`))
	}
	if _, ok := mustParse(t, `"*"`).(Star); !ok {
		t.Errorf(`"*" did not parse as Star`)
	}
	if id, ok := mustParse(t, `"movie_id"`).(Identifier); !ok || id.Name != "movie_id" {
		t.Errorf(`"movie_id" did not parse as Identifier{movie_id}`)
	}
	if s, ok := mustParse(t, `["hello"]`).(StringLiteral); !ok || s.Value != "hello" {
		t.Errorf(`["hello"] did not parse as StringLiteral{hello}`)
	}
}

func TestParseExpression_UnaryVsFoldedMinusDisambiguation(t *testing.T) {
	// ["-", x] : exactly one operand -> unary negate, not a degenerate fold.
	if u, ok := mustParse(t, `["-", 1]`).(UnaryExpr); !ok || u.Op != UnaryNeg {
		t.Errorf(`["-", 1] did not parse as unary negate, got %#v`, mustParse(t, `["-", 1]`))
	}

	// ["-", 4, 3, 2, 1] -> folds left-associatively into nested FoldedExpr.
	expr := mustParse(t, `["-", 4, 3, 2, 1]`)
	outer, ok := expr.(FoldedExpr)
	if !ok || outer.Op != FoldSub {
		t.Fatalf("expected outer FoldedExpr(-), got %#v", expr)
	}
	if lit, ok := outer.Right.(NumberLiteral); !ok || lit.Value != 1 {
		t.Errorf("expected outer right operand 1, got %#v", outer.Right)
	}
	mid, ok := outer.Left.(FoldedExpr)
	if !ok || mid.Op != FoldSub {
		t.Fatalf("expected middle FoldedExpr(-), got %#v", outer.Left)
	}
	if lit, ok := mid.Right.(NumberLiteral); !ok || lit.Value != 2 {
		t.Errorf("expected middle right operand 2, got %#v", mid.Right)
	}
	inner, ok := mid.Left.(FoldedExpr)
	if !ok || inner.Op != FoldSub {
		t.Fatalf("expected inner FoldedExpr(-), got %#v", mid.Left)
	}
	if l, ok := inner.Left.(NumberLiteral); !ok || l.Value != 4 {
		t.Errorf("expected inner left operand 4, got %#v", inner.Left)
	}
	if r, ok := inner.Right.(NumberLiteral); !ok || r.Value != 3 {
		t.Errorf("expected inner right operand 3, got %#v", inner.Right)
	}
}

func TestParseExpression_ComparisonFold(t *testing.T) {
	// ["<", 1, 2, 3, 4] -> ["and", ["<",1,2], ["and", ["<",2,3], ["<",3,4]]]
	expr := mustParse(t, `["<", 1, 2, 3, 4]`)
	outer, ok := expr.(FoldedExpr)
	if !ok || outer.Op != FoldAnd {
		t.Fatalf("expected outer FoldedExpr(and), got %#v", expr)
	}
	// "and" itself isn't a comparison op, so it left-folds too :
	// (p1 and p2) and p3, not a flat and([p1,p2,p3]).
	inner, ok := outer.Left.(FoldedExpr)
	if !ok || inner.Op != FoldAnd {
		t.Fatalf("expected left operand FoldedExpr(and), got %#v", outer.Left)
	}
	first, ok := inner.Left.(FoldedExpr)
	if !ok || first.Op != FoldLt {
		t.Fatalf("expected first comparison FoldedExpr(<), got %#v", inner.Left)
	}
	if l, ok := first.Left.(NumberLiteral); !ok || l.Value != 1 {
		t.Errorf("expected first comparison left 1, got %#v", first.Left)
	}
}

func TestParseExpression_UnaryVsBinaryTildeDisambiguation(t *testing.T) {
	if u, ok := mustParse(t, `["~", "flags"]`).(UnaryExpr); !ok || u.Op != UnaryBitNot {
		t.Errorf(`["~", "flags"] did not parse as unary bitwise-not, got %#v`, mustParse(t, `["~", "flags"]`))
	}
	if b, ok := mustParse(t, `["~", "name", ["^A"]]`).(BinaryExpr); !ok || b.Op != BinaryRegexMatch {
		t.Errorf(`["~", "name", ["^A"]] did not parse as binary regex match, got %#v`, mustParse(t, `["~", "name", ["^A"]]`))
	}
}

func TestParseExpression_InWithLiteralCandidate(t *testing.T) {
	expr, ok := mustParse(t, `["in", "status", "a", "b"]`).(InExpr)
	if !ok {
		t.Fatalf("expected InExpr, got %#v", expr)
	}
	if len(expr.Candidates) != 2 || !expr.Candidates[0].IsLiteral || expr.Candidates[0].Literal != "a" {
		t.Errorf("expected literal candidates [a b], got %#v", expr.Candidates)
	}
}

func TestParseExpression_Object(t *testing.T) {
	obj, ok := mustParse(t, `{"movie": ["full-except", ["year"]], "actors": "actors"}`).(ObjectExpr)
	if !ok {
		t.Fatalf("expected ObjectExpr, got %#v", obj)
	}
	if _, ok := obj.Fields["movie"].(FullExceptExpr); !ok {
		t.Errorf("expected movie field to be FullExceptExpr, got %#v", obj.Fields["movie"])
	}
	if id, ok := obj.Fields["actors"].(Identifier); !ok || id.Name != "actors" {
		t.Errorf("expected actors field to be Identifier{actors}, got %#v", obj.Fields["actors"])
	}
}

func TestParseExpression_GetSetDefaultKeyword(t *testing.T) {
	expr, ok := mustParse(t, `["get-set", "id", "default", 0]`).(GetSetExpr)
	if !ok {
		t.Fatalf("expected GetSetExpr, got %#v", expr)
	}
	if _, ok := expr.DefaultGet.(DefaultKeyword); !ok {
		t.Errorf("expected DefaultGet to be DefaultKeyword, got %#v", expr.DefaultGet)
	}
	if n, ok := expr.DefaultSet.(NumberLiteral); !ok || n.Value != 0 {
		t.Errorf("expected DefaultSet to be NumberLiteral(0), got %#v", expr.DefaultSet)
	}
}

func TestParseExpression_AggAndCall(t *testing.T) {
	agg, ok := mustParse(t, `["agg", "api.array_agg", ["name"], [">=", "year", 1999]]`).(AggExpr)
	if !ok {
		t.Fatalf("expected AggExpr, got %#v", agg)
	}
	if agg.Identifier != "api.array_agg" || len(agg.Arguments) != 1 || agg.Filter == nil {
		t.Errorf("unexpected AggExpr shape: %#v", agg)
	}

	call, ok := mustParse(t, `["call", "api.slugify", "name"]`).(CallExpr)
	if !ok || call.Identifier != "api.slugify" || len(call.Arguments) != 1 {
		t.Errorf("unexpected CallExpr shape: %#v", call)
	}
}
