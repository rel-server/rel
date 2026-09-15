package query

import (
	"reflect"
	"testing"
)

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
	// A bare string is always a literal now, "*" included — no more
	// special-cased Star, see StarExpr for how "select everything" is spelled.
	if s, ok := mustParse(t, `"*"`).(StringLiteral); !ok || s.Value != "*" {
		t.Errorf(`"*" did not parse as StringLiteral{*}, got %#v`, mustParse(t, `"*"`))
	}
	if s, ok := mustParse(t, `"movie_id"`).(StringLiteral); !ok || s.Value != "movie_id" {
		t.Errorf(`"movie_id" did not parse as StringLiteral{movie_id}, got %#v`, mustParse(t, `"movie_id"`))
	}
	if col, ok := mustParse(t, `["col", "movie_id"]`).(*ColExpr); !ok || col.Column != "movie_id" {
		t.Errorf(`["col", "movie_id"] did not parse as ColExpr{movie_id}, got %#v`, mustParse(t, `["col", "movie_id"]`))
	}
}

func TestParseExpression_StarTags(t *testing.T) {
	full, ok := mustParse(t, `["*"]`).(StarExpr)
	if !ok || full.Own {
		t.Errorf(`["*"] did not parse as StarExpr{Own:false}, got %#v`, mustParse(t, `["*"]`))
	}
	own, ok := mustParse(t, `["*~"]`).(StarExpr)
	if !ok || !own.Own {
		t.Errorf(`["*~"] did not parse as StarExpr{Own:true}, got %#v`, mustParse(t, `["*~"]`))
	}
	withBoth, ok := mustParse(t, `["*", ["id"], {"total": ["col", "amount"]}]`).(StarExpr)
	if !ok || withBoth.Own || len(withBoth.Except) != 1 || withBoth.Except[0] != "id" || len(withBoth.And) != 1 {
		t.Errorf(`["*", ["id"], {"total": ...}] did not parse as expected, got %#v`, mustParse(t, `["*", ["id"], {"total": ["col", "amount"]}]`))
	}
}

func TestParseExpression_SingleElementArrayIsNoLongerALiteralEscape(t *testing.T) {
	// Now that a bare string is already a literal, a one-element array is
	// just an ordinary (and, for anything but a real zero-arity tag,
	// unrecognized) tag dispatch — not a second way to spell a literal.
	if _, err := ParseExpression([]byte(`["ownership"]`)); err == nil {
		t.Errorf(`["ownership"] should be an unrecognized-tag error now, not a literal escape hatch`)
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
	if u, ok := mustParse(t, `["~", ["col", "flags"]]`).(UnaryExpr); !ok || u.Op != UnaryBitNot {
		t.Errorf(`["~", ["col", "flags"]] did not parse as unary bitwise-not, got %#v`, mustParse(t, `["~", ["col", "flags"]]`))
	}
	if b, ok := mustParse(t, `["~", ["col", "name"], "^A"]`).(BinaryExpr); !ok || b.Op != BinaryRegexMatch {
		t.Errorf(`["~", ["col", "name"], "^A"] did not parse as binary regex match, got %#v`, mustParse(t, `["~", ["col", "name"], "^A"]`))
	}
}

func TestParseExpression_InCandidates(t *testing.T) {
	expr, ok := mustParse(t, `["in", ["col", "status"], "a", "b"]`).(InExpr)
	if !ok {
		t.Fatalf("expected InExpr, got %#v", expr)
	}
	if len(expr.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %#v", expr.Candidates)
	}
	a, ok := expr.Candidates[0].(StringLiteral)
	if !ok || a.Value != "a" {
		t.Errorf("expected candidate[0] StringLiteral{a}, got %#v", expr.Candidates[0])
	}
}

func TestParseExpression_Object(t *testing.T) {
	obj, ok := mustParse(t, `{"movie": ["*", ["year"]], "actors": ["col", "actors"]}`).(ObjectExpr)
	if !ok {
		t.Fatalf("expected ObjectExpr, got %#v", obj)
	}
	star, ok := obj.Fields["movie"].(StarExpr)
	if !ok || star.Own || len(star.Except) != 1 || star.Except[0] != "year" {
		t.Errorf("expected movie field to be StarExpr{Except:[year]}, got %#v", obj.Fields["movie"])
	}
	if col, ok := obj.Fields["actors"].(*ColExpr); !ok || col.Column != "actors" {
		t.Errorf("expected actors field to be ColExpr{actors}, got %#v", obj.Fields["actors"])
	}
}

func TestParseExpression_ColDefaultKeyword(t *testing.T) {
	expr, ok := mustParse(t, `["col", "id", "default", 0]`).(*ColExpr)
	if !ok {
		t.Fatalf("expected ColExpr, got %#v", expr)
	}
	if _, ok := expr.DefaultGet.(DefaultKeyword); !ok {
		t.Errorf("expected DefaultGet to be DefaultKeyword, got %#v", expr.DefaultGet)
	}
	if n, ok := expr.DefaultSet.(NumberLiteral); !ok || n.Value != 0 {
		t.Errorf("expected DefaultSet to be NumberLiteral(0), got %#v", expr.DefaultSet)
	}
}

func TestParseExpression_AggAndCall(t *testing.T) {
	// Bare string : unqualified Name, never split on "." — a literal dot
	// stays part of Name, never mistaken for a schema separator.
	agg, ok := mustParse(t, `["agg", "array_agg", [["col", "name"]], [">=", ["col", "year"], 1999]]`).(*AggExpr)
	if !ok {
		t.Fatalf("expected AggExpr, got %#v", agg)
	}
	if agg.Identifier != (FunctionRef{Name: "array_agg"}) || len(agg.Arguments) != 1 || agg.Filter == nil {
		t.Errorf("unexpected AggExpr shape: %#v", agg)
	}

	// Explicit {schema, name} object : the only way to spell a qualified
	// name.
	call, ok := mustParse(t, `["call", {"schema": "api", "name": "slugify"}, ["col", "name"]]`).(*CallExpr)
	if !ok || call.Identifier != (FunctionRef{Schema: "api", Name: "slugify"}) || len(call.Arguments) != 1 {
		t.Errorf("unexpected CallExpr shape: %#v", call)
	}
}

// A bare name in "."'s own leading operand position is ALWAYS a scope
// lookup, same as the single-hop degenerate case — so a chain of bare names
// folds left-to-right with no need to nest an inner "." just to get the
// first hop resolved as a lookup rather than parsed as a StringLiteral.
func TestParseExpression_DotChain_FlatBareNameForm(t *testing.T) {
	flat := mustParse(t, `[".", "chain", "name"]`)
	nested := mustParse(t, `[".", [".", "chain"], "name"]`)

	folded, ok := flat.(FoldedExpr)
	if !ok || folded.Op != FoldDot {
		t.Fatalf(`[".", "chain", "name"] did not parse as FoldedExpr(.), got %#v`, flat)
	}
	left, ok := folded.Left.(*Identifier)
	if !ok || left.Name != "chain" {
		t.Errorf(`expected Left to be Identifier{chain}, got %#v`, folded.Left)
	}
	right, ok := folded.Right.(*Identifier)
	if !ok || right.Name != "name" {
		t.Errorf(`expected Right to be Identifier{name}, got %#v`, folded.Right)
	}

	// The flat and nested forms must produce the identical AST shape — the
	// nested form is no longer the only way to spell this, not a different
	// meaning.
	if !reflect.DeepEqual(flat, nested) {
		t.Errorf("flat and nested forms produced different ASTs:\nflat:   %#v\nnested: %#v", flat, nested)
	}
}

// Chaining isn't limited to two hops — each bare name lands off the
// previous one's own result, arbitrarily deep.
func TestParseExpression_DotChain_ThreeHopsDeep(t *testing.T) {
	expr := mustParse(t, `[".", "a", "b", "c"]`)
	outer, ok := expr.(FoldedExpr)
	if !ok || outer.Op != FoldDot {
		t.Fatalf(`expected outer FoldedExpr(.), got %#v`, expr)
	}
	if id, ok := outer.Right.(*Identifier); !ok || id.Name != "c" {
		t.Errorf("expected outer hop to be Identifier{c}, got %#v", outer.Right)
	}
	middle, ok := outer.Left.(FoldedExpr)
	if !ok || middle.Op != FoldDot {
		t.Fatalf("expected middle FoldedExpr(.), got %#v", outer.Left)
	}
	if id, ok := middle.Right.(*Identifier); !ok || id.Name != "b" {
		t.Errorf("expected middle hop to be Identifier{b}, got %#v", middle.Right)
	}
	if id, ok := middle.Left.(*Identifier); !ok || id.Name != "a" {
		t.Errorf("expected innermost base to be Identifier{a}, got %#v", middle.Left)
	}
}

// A non-bare-name base (a real sub-expression, not a scope lookup) is
// unchanged from before : still requires at least one hop after it.
func TestParseExpression_DotChain_NonNameBaseStillNeedsAHop(t *testing.T) {
	expr := mustParse(t, `[".", ["col", "home"], "city"]`)
	folded, ok := expr.(FoldedExpr)
	if !ok || folded.Op != FoldDot {
		t.Fatalf(`expected FoldedExpr(.), got %#v`, expr)
	}
	if _, ok := folded.Left.(*ColExpr); !ok {
		t.Errorf("expected Left to be *ColExpr, got %#v", folded.Left)
	}
	if id, ok := folded.Right.(*Identifier); !ok || id.Name != "city" {
		t.Errorf("expected Right to be Identifier{city}, got %#v", folded.Right)
	}

	if _, err := ParseExpression([]byte(`[".", ["col", "home"]]`)); err == nil {
		t.Errorf(`[".", ["col", "home"]] (non-bare-name base, no hop) should be an error`)
	}
}
