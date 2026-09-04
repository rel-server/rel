package query

import (
	"bytes"
	"fmt"
	"regexp"

	"github.com/bytedance/sonic/ast"
)

// ParseExpression decodes one query.ts Expression node from raw JSON, via
// sonic's ast package rather than encoding/json — see this session's
// discussion for why : sonic's native (SIMD/JIT) path is active for this
// repo's Go version, and the ast API lets recursive descent walk an
// already-parsed tree instead of re-decoding raw bytes at every level.
//
// Every type read off a node uses the Strict* accessors (StrictString,
// StrictBool, StrictFloat64), never the lenient String()/Bool()/Float64()
// ones — those coerce across JSON types (a JSON number's .String() silently
// returns "42"), which would corrupt the tag-dispatch and [string]-literal
// detection below exactly the way a wrong json.Unmarshal target type would
// have with encoding/json. Purely syntactic otherwise, same as before : bare
// strings become Identifier (or Star for "*"), never resolved against a
// scope — see Expression's doc comment.
func ParseExpression(data []byte) (Expression, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return NullLiteral{}, nil
	}
	root, perr := ast.NewParser(string(trimmed)).Parse()
	if perr != 0 {
		return nil, fmt.Errorf("query: invalid expression JSON: %w", perr)
	}
	return parseNode(&root)
}

func parseNode(n *ast.Node) (Expression, error) {
	switch n.TypeSafe() {
	case ast.V_NULL:
		return NullLiteral{}, nil

	case ast.V_TRUE, ast.V_FALSE:
		b, err := n.StrictBool()
		if err != nil {
			return nil, fmt.Errorf("query: invalid boolean expression: %w", err)
		}
		return BoolLiteral{Value: b}, nil

	case ast.V_NUMBER:
		f, err := n.StrictFloat64()
		if err != nil {
			return nil, fmt.Errorf("query: invalid number expression: %w", err)
		}
		return NumberLiteral{Value: f}, nil

	case ast.V_STRING:
		s, err := n.StrictString()
		if err != nil {
			return nil, fmt.Errorf("query: invalid string expression: %w", err)
		}
		if s == "*" {
			return Star{}, nil
		}
		return &Identifier{Name: s}, nil

	case ast.V_OBJECT:
		fields, err := parseObjectFields(n)
		if err != nil {
			return nil, err
		}
		return ObjectExpr{Fields: fields}, nil

	case ast.V_ARRAY:
		return parseArrayExpression(n)

	case ast.V_ERROR:
		return nil, fmt.Errorf("query: invalid expression JSON: %w", n.Check())

	default:
		return nil, fmt.Errorf("query: unexpected JSON value in expression position (type %d)", n.TypeSafe())
	}
}

var unaryOperators = map[string]UnaryOperator{
	string(UnaryNeg): UnaryNeg, string(UnaryNot): UnaryNot, string(UnaryBitNot): UnaryBitNot,
	string(UnaryIsNull): UnaryIsNull, string(UnaryIsTrue): UnaryIsTrue, string(UnaryIsFalse): UnaryIsFalse,
	string(UnaryIsNotNull): UnaryIsNotNull, string(UnaryIsNotTrue): UnaryIsNotTrue, string(UnaryIsNotFalse): UnaryIsNotFalse,
	string(UnarySqrt): UnarySqrt, string(UnaryCubeRoot): UnaryCubeRoot,
}

var binaryOperators = map[string]BinaryOperator{
	string(BinaryLike): BinaryLike, string(BinaryILike): BinaryILike,
	string(BinaryRegexMatch): BinaryRegexMatch, string(BinaryRegexMatchCI): BinaryRegexMatchCI,
	string(BinaryCast): BinaryCast, string(BinaryArrayOverlap): BinaryArrayOverlap,
	string(BinaryDistance): BinaryDistance, string(BinaryRangeAdjacent): BinaryRangeAdjacent,
	string(BinaryShiftLeft): BinaryShiftLeft, string(BinaryShiftRight): BinaryShiftRight,
	string(BinaryContains): BinaryContains, string(BinaryContainedBy): BinaryContainedBy,
	string(BinaryJsonHasKey): BinaryJsonHasKey, string(BinaryJsonHasAnyKey): BinaryJsonHasAnyKey,
	string(BinaryNotExtendsRight): BinaryNotExtendsRight, string(BinaryNotExtendsLeft): BinaryNotExtendsLeft,
	string(BinaryJsonHasAllKeys): BinaryJsonHasAllKeys, string(BinaryQuestionColon): BinaryQuestionColon,
	string(BinaryFullTextSearch): BinaryFullTextSearch,
}

// foldedOperators maps every query.ts tag, including JS-alias spellings
// ("!=", "!==", "==="), onto its canonical FoldedOperator constant.
var foldedOperators = map[string]FoldedOperator{
	string(FoldAnd): FoldAnd, string(FoldOr): FoldOr,
	string(FoldAdd): FoldAdd, string(FoldSub): FoldSub, string(FoldMul): FoldMul, string(FoldDiv): FoldDiv,
	string(FoldPow): FoldPow, string(FoldMod): FoldMod, string(FoldBitOr): FoldBitOr, string(FoldBitAnd): FoldBitAnd,
	string(FoldJsonGet): FoldJsonGet, string(FoldJsonGetText): FoldJsonGetText,
	string(FoldJsonPathGet): FoldJsonPathGet, string(FoldJsonPathGetText): FoldJsonPathGetText,
	string(FoldDot): FoldDot, string(FoldConcat): FoldConcat,
	string(FoldConcatCoalescing): FoldConcatCoalescing, string(FoldCoalesceAlias): FoldCoalesceAlias,
	string(FoldLte): FoldLte, string(FoldGte): FoldGte, string(FoldLt): FoldLt, string(FoldGt): FoldGt,
	string(FoldEq):  FoldEq,
	string(FoldNeq): FoldNeq, "!=": FoldNeq,
	string(FoldIsDistinctFrom): FoldIsDistinctFrom, "!==": FoldIsDistinctFrom,
	string(FoldIsNotDistinctFrom): FoldIsNotDistinctFrom, "===": FoldIsNotDistinctFrom,
}

// comparisonFoldOps fold into pairwise-ANDed comparisons
// (["<",1,2,3] -> ["and",["<",1,2],["<",2,3]]) ; everything else is ordinary left-associative folding.
var comparisonFoldOps = map[FoldedOperator]bool{
	FoldLte: true, FoldGte: true, FoldLt: true, FoldGt: true, FoldEq: true,
	FoldNeq: true, FoldIsDistinctFrom: true, FoldIsNotDistinctFrom: true,
}

func parseArrayExpression(n *ast.Node) (Expression, error) {
	items, err := n.ArrayUseNode()
	if err != nil {
		return nil, fmt.Errorf("query: invalid array expression: %w", err)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("query: empty array is not a valid expression")
	}

	// [string] : the one tag-less array form, tried first. Excludes
	// "own"/"full" — the only zero-argument tags, else unreachable.
	if len(items) == 1 {
		if s, err := items[0].StrictString(); err == nil && s != "own" && s != "full" {
			return StringLiteral{Value: s}, nil
		}
	}

	tag, err := items[0].StrictString()
	if err != nil {
		return nil, fmt.Errorf("query: expression array must start with a string tag, or contain exactly one string (a literal): %w", err)
	}
	// Word-form synonyms (specs/query-json.md) normalize to their canonical
	// tag here, before anything downstream sees it — see OperatorWords.
	if canonical, ok := OperatorWords[tag]; ok {
		tag = canonical
	}
	rest := items[1:]

	switch tag {
	case "between", "not_between":
		if len(rest) != 3 {
			return nil, fmt.Errorf("query: %q needs exactly 3 operands, got %d", tag, len(rest))
		}
		minE, err := parseNode(&rest[0])
		if err != nil {
			return nil, err
		}
		expE, err := parseNode(&rest[1])
		if err != nil {
			return nil, err
		}
		maxE, err := parseNode(&rest[2])
		if err != nil {
			return nil, err
		}
		return BetweenExpr{Negate: tag == "not_between", Min: minE, Exp: expE, Max: maxE}, nil

	case "bigint":
		return parseValidatedStringArgLiteral(tag, rest, validBigIntLiteral, func(v string) Expression { return BigIntLiteral{Value: v} })
	case "numeric":
		return parseValidatedStringArgLiteral(tag, rest, validNumericLiteral, func(v string) Expression { return NumericLiteral{Value: v} })

	case "in", "not_in":
		if len(rest) < 1 {
			return nil, fmt.Errorf("query: %q needs a subject", tag)
		}
		subject, err := parseNode(&rest[0])
		if err != nil {
			return nil, err
		}
		candidates := make([]InCandidate, 0, len(rest)-1)
		for i := range rest[1:] {
			c := &rest[1+i]
			if s, err := c.StrictString(); err == nil {
				candidates = append(candidates, InCandidate{IsLiteral: true, Literal: s})
				continue
			}
			expr, err := parseNode(c)
			if err != nil {
				return nil, err
			}
			candidates = append(candidates, InCandidate{Expr: expr})
		}
		return InExpr{Negate: tag == "not_in", Subject: subject, Candidates: candidates}, nil

	case "any", "all":
		if len(rest) != 3 {
			return nil, fmt.Errorf("query: %q needs exactly 3 operands, got %d", tag, len(rest))
		}
		op, err := rest[0].StrictString()
		if err != nil {
			return nil, fmt.Errorf("query: %q operator must be a string: %w", tag, err)
		}
		if canonical, ok := OperatorWords[op]; ok {
			op = canonical
		}
		if _, isBinary := binaryOperators[op]; !isBinary {
			if _, isFolded := foldedOperators[op]; !isFolded {
				return nil, fmt.Errorf("query: %q operator %q is not a known operator", tag, op)
			}
		}
		subject, err := parseNode(&rest[1])
		if err != nil {
			return nil, err
		}
		arr, err := parseNode(&rest[2])
		if err != nil {
			return nil, err
		}
		return AnyAllExpr{All: tag == "all", Op: op, Subject: subject, Array: arr}, nil

	case "concat_ws":
		if len(rest) < 1 {
			return nil, fmt.Errorf("query: %q needs a separator", tag)
		}
		sep, err := parseNode(&rest[0])
		if err != nil {
			return nil, err
		}
		args, err := parseExpressionList(rest[1:])
		if err != nil {
			return nil, err
		}
		return ConcatWsExpr{Separator: sep, Args: args}, nil

	case "coalesce":
		args, err := parseExpressionList(rest)
		if err != nil {
			return nil, err
		}
		return CoalesceExpr{Args: args}, nil

	case "format":
		if len(rest) < 1 {
			return nil, fmt.Errorf("query: %q needs a format string", tag)
		}
		format, err := rest[0].StrictString()
		if err != nil {
			return nil, fmt.Errorf("query: %q format must be a string: %w", tag, err)
		}
		args, err := parseExpressionList(rest[1:])
		if err != nil {
			return nil, err
		}
		return FormatExpr{Format: format, Args: args}, nil

	case "agg", "aggregate":
		if len(rest) < 2 || len(rest) > 3 {
			return nil, fmt.Errorf("query: %q needs 2 or 3 operands, got %d", tag, len(rest))
		}
		ident, err := parseFunctionRef(&rest[0])
		if err != nil {
			return nil, fmt.Errorf("query: %q identifier: %w", tag, err)
		}
		argItems, err := rest[1].ArrayUseNode()
		if err != nil {
			return nil, fmt.Errorf("query: %q arguments must be an array: %w", tag, err)
		}
		args, err := parseExpressionList(argItems)
		if err != nil {
			return nil, err
		}
		var filter Expression
		if len(rest) == 3 {
			filter, err = parseNode(&rest[2])
			if err != nil {
				return nil, err
			}
		}
		return &AggExpr{Identifier: ident, Arguments: args, Filter: filter}, nil

	case "call":
		if len(rest) < 1 {
			return nil, fmt.Errorf("query: %q needs an identifier", tag)
		}
		ident, err := parseFunctionRef(&rest[0])
		if err != nil {
			return nil, fmt.Errorf("query: %q identifier: %w", tag, err)
		}
		args, err := parseExpressionList(rest[1:])
		if err != nil {
			return nil, err
		}
		return &CallExpr{Identifier: ident, Arguments: args}, nil

	case "own":
		return OwnExpr{}, nil
	case "full":
		return FullExpr{}, nil

	case "own_except", "full_except":
		except, err := parseStringListArg(tag, rest)
		if err != nil {
			return nil, err
		}
		if tag == "own_except" {
			return OwnExceptExpr{Except: except}, nil
		}
		return FullExceptExpr{Except: except}, nil

	case "own_and", "full_and":
		and, err := parseExpressionObjectArg(tag, rest)
		if err != nil {
			return nil, err
		}
		if tag == "own_and" {
			return OwnAndExpr{And: and}, nil
		}
		return FullAndExpr{And: and}, nil

	case "own_except_and", "full_except_and":
		if len(rest) != 2 {
			return nil, fmt.Errorf("query: %q needs exactly [except, and]", tag)
		}
		except, err := parseStringListValue(&rest[0])
		if err != nil {
			return nil, err
		}
		and, err := parseObjectFields(&rest[1])
		if err != nil {
			return nil, err
		}
		if tag == "own_except_and" {
			return OwnExceptAndExpr{Except: except, And: and}, nil
		}
		return FullExceptAndExpr{Except: except, And: and}, nil

	case "arr", "array":
		items, err := parseExpressionList(rest)
		if err != nil {
			return nil, err
		}
		return ArrExpr{Items: items}, nil

	case "lst", "list":
		items, err := parseExpressionList(rest)
		if err != nil {
			return nil, err
		}
		return LstExpr{Items: items}, nil

	case "index":
		if len(rest) != 2 {
			return nil, fmt.Errorf("query: %q needs exactly [array, index]", tag)
		}
		arr, err := parseNode(&rest[0])
		if err != nil {
			return nil, err
		}
		idx, err := parseNode(&rest[1])
		if err != nil {
			return nil, err
		}
		return IndexExpr{Array: arr, Index: idx}, nil

	case "slice":
		if len(rest) != 3 {
			return nil, fmt.Errorf("query: %q needs exactly [array, from, to]", tag)
		}
		arr, err := parseNode(&rest[0])
		if err != nil {
			return nil, err
		}
		from, err := parseNode(&rest[1])
		if err != nil {
			return nil, err
		}
		to, err := parseNode(&rest[2])
		if err != nil {
			return nil, err
		}
		return SliceExpr{Array: arr, From: from, To: to}, nil

	case "get-set":
		return parseGetSet(rest)
	case "get", "set":
		return parseGetOrSet(tag, rest)

	case "$param":
		if len(rest) < 1 || len(rest) > 2 {
			return nil, fmt.Errorf("query: %q needs [name] or [name, cast]", tag)
		}
		name, err := rest[0].StrictString()
		if err != nil {
			return nil, fmt.Errorf("query: %q name must be a string: %w", tag, err)
		}
		var cast string
		if len(rest) == 2 {
			cast, err = rest[1].StrictString()
			if err != nil {
				return nil, fmt.Errorf("query: %q cast must be a string: %w", tag, err)
			}
		}
		return ParamExpr{Name: name, Cast: cast}, nil
	}

	// "-"/"~" are members of two vocabularies ; checking unary (fixed
	// arity 1) before folded/binary resolves both without ambiguity.
	if op, ok := unaryOperators[tag]; ok && len(rest) == 1 {
		expr, err := parseNode(&rest[0])
		if err != nil {
			return nil, err
		}
		return UnaryExpr{Op: op, Expr: expr}, nil
	}
	if op, ok := binaryOperators[tag]; ok && len(rest) == 2 {
		left, err := parseNode(&rest[0])
		if err != nil {
			return nil, err
		}
		right, err := parseNode(&rest[1])
		if err != nil {
			return nil, err
		}
		return BinaryExpr{Op: op, Left: left, Right: right}, nil
	}
	if op, ok := foldedOperators[tag]; ok && len(rest) >= 1 {
		operands, err := parseExpressionList(rest)
		if err != nil {
			return nil, err
		}
		return foldExpression(op, operands)
	}

	return nil, fmt.Errorf("query: unrecognized expression tag %q (array length %d)", tag, len(items))
}

// foldExpression collapses an N-ary folded-operator array into nested,
// always-binary FoldedExpr nodes — see FoldedExpr's doc comment.
func foldExpression(op FoldedOperator, operands []Expression) (Expression, error) {
	if len(operands) == 0 {
		return nil, fmt.Errorf("query: folded operator %q needs at least one operand", op)
	}
	if len(operands) == 1 {
		return operands[0], nil
	}
	if comparisonFoldOps[op] {
		pairs := make([]Expression, 0, len(operands)-1)
		for i := 0; i+1 < len(operands); i++ {
			pairs = append(pairs, FoldedExpr{Op: op, Left: operands[i], Right: operands[i+1]})
		}
		return foldExpression(FoldAnd, pairs)
	}
	result := operands[0]
	for _, next := range operands[1:] {
		result = FoldedExpr{Op: op, Left: result, Right: next}
	}
	return result, nil
}

// parseDefaultPosition parses a default-value slot, where a bare "default"
// string means DefaultKeyword rather than an Identifier.
func parseDefaultPosition(n *ast.Node) (Expression, error) {
	if s, err := n.StrictString(); err == nil && s == "default" {
		return DefaultKeyword{}, nil
	}
	return parseNode(n)
}

func parseGetSet(rest []ast.Node) (Expression, error) {
	if len(rest) < 1 || len(rest) > 3 {
		return nil, fmt.Errorf("query: %q needs [column, default_get?, default_set?]", "get-set")
	}
	column, err := rest[0].StrictString()
	if err != nil {
		return nil, fmt.Errorf("query: get-set column must be a string: %w", err)
	}
	var defaultGet, defaultSet Expression
	if len(rest) >= 2 {
		defaultGet, err = parseDefaultPosition(&rest[1])
		if err != nil {
			return nil, err
		}
	}
	if len(rest) == 3 {
		defaultSet, err = parseDefaultPosition(&rest[2])
		if err != nil {
			return nil, err
		}
	}
	return &GetSetExpr{Column: column, DefaultGet: defaultGet, DefaultSet: defaultSet}, nil
}

func parseGetOrSet(tag string, rest []ast.Node) (Expression, error) {
	if len(rest) < 1 || len(rest) > 2 {
		return nil, fmt.Errorf("query: %q needs [column, default_value?]", tag)
	}
	column, err := rest[0].StrictString()
	if err != nil {
		return nil, fmt.Errorf("query: %q column must be a string: %w", tag, err)
	}
	var def Expression
	if len(rest) == 2 {
		def, err = parseDefaultPosition(&rest[1])
		if err != nil {
			return nil, err
		}
	}
	if tag == "get" {
		return &GetExpr{Column: column, DefaultValue: def}, nil
	}
	return &SetExpr{Column: column, DefaultValue: def}, nil
}

// ---- small shared decode helpers -------------------------------------------------

func parseExpressionList(items []ast.Node) ([]Expression, error) {
	out := make([]Expression, len(items))
	for i := range items {
		expr, err := parseNode(&items[i])
		if err != nil {
			return nil, fmt.Errorf("query: item %d: %w", i, err)
		}
		out[i] = expr
	}
	return out, nil
}

func parseStringListValue(n *ast.Node) ([]string, error) {
	items, err := n.ArrayUseNode()
	if err != nil {
		return nil, fmt.Errorf("query: expected a string array: %w", err)
	}
	out := make([]string, len(items))
	for i := range items {
		s, err := items[i].StrictString()
		if err != nil {
			return nil, fmt.Errorf("query: expected a string array, item %d: %w", i, err)
		}
		out[i] = s
	}
	return out, nil
}

func parseStringListArg(tag string, rest []ast.Node) ([]string, error) {
	if len(rest) != 1 {
		return nil, fmt.Errorf("query: %q needs exactly one array argument, got %d", tag, len(rest))
	}
	return parseStringListValue(&rest[0])
}

func parseObjectFields(n *ast.Node) (map[string]Expression, error) {
	raw, err := n.MapUseNode()
	if err != nil {
		return nil, fmt.Errorf("query: expected an object: %w", err)
	}
	out := make(map[string]Expression, len(raw))
	for k, v := range raw {
		expr, err := parseNode(&v)
		if err != nil {
			return nil, fmt.Errorf("query: field %q: %w", k, err)
		}
		out[k] = expr
	}
	return out, nil
}

func parseExpressionObjectArg(tag string, rest []ast.Node) (map[string]Expression, error) {
	if len(rest) != 1 {
		return nil, fmt.Errorf("query: %q needs exactly one object argument, got %d", tag, len(rest))
	}
	return parseObjectFields(&rest[0])
}

// parseFunctionRef parses AggExpr/CallExpr's identifier : a bare string
// (unqualified, never split on ".") or an explicit {schema, name} object.
func parseFunctionRef(n *ast.Node) (FunctionRef, error) {
	switch n.TypeSafe() {
	case ast.V_STRING:
		name, err := n.StrictString()
		if err != nil {
			return FunctionRef{}, fmt.Errorf("must be a string or {schema, name} object: %w", err)
		}
		return FunctionRef{Name: name}, nil

	case ast.V_OBJECT:
		nameNode := n.Get("name")
		if !nameNode.Exists() {
			return FunctionRef{}, fmt.Errorf(`object form needs a "name"`)
		}
		name, err := nameNode.StrictString()
		if err != nil {
			return FunctionRef{}, fmt.Errorf(`"name" must be a string: %w`, err)
		}
		var schema string
		if schemaNode := n.Get("schema"); schemaNode.Exists() {
			schema, err = schemaNode.StrictString()
			if err != nil {
				return FunctionRef{}, fmt.Errorf(`"schema" must be a string: %w`, err)
			}
		}
		return FunctionRef{Schema: schema, Name: name}, nil

	default:
		return FunctionRef{}, fmt.Errorf("must be a string or {schema, name} object, got type %d", n.TypeSafe())
	}
}

// validBigIntLiteral/validNumericLiteral gate v so a malformed value is a
// clean 400 at parse time, not a raw Postgres "invalid input syntax" error.
var (
	validBigIntLiteral  = regexp.MustCompile(`^-?[0-9]+$`)
	validNumericLiteral = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)
)

// parseValidatedStringArgLiteral is parseStringArgLiteral plus a format
// gate, for literal kinds written straight into SQL text, not bound.
func parseValidatedStringArgLiteral(tag string, rest []ast.Node, valid *regexp.Regexp, build func(string) Expression) (Expression, error) {
	if len(rest) != 1 {
		return nil, fmt.Errorf("query: %q expects exactly one string argument, got %d", tag, len(rest))
	}
	v, err := rest[0].StrictString()
	if err != nil {
		return nil, fmt.Errorf("query: %q expects a string argument: %w", tag, err)
	}
	if !valid.MatchString(v) {
		return nil, fmt.Errorf("query: %q: %q is not a valid %s literal", tag, v, tag)
	}
	return build(v), nil
}
