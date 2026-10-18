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
	return parseNode(sonicNode{&root})
}

func parseNode(n Node) (Expression, error) {
	switch n.Kind() {
	case KindNull:
		return NullLiteral{}, nil

	case KindBool:
		b, err := n.StrictBool()
		if err != nil {
			return nil, fmt.Errorf("query: invalid boolean expression: %w", err)
		}
		return BoolLiteral{Value: b}, nil

	case KindNumber:
		f, err := n.StrictFloat64()
		if err != nil {
			return nil, fmt.Errorf("query: invalid number expression: %w", err)
		}
		return NumberLiteral{Value: f}, nil

	case KindString:
		s, err := n.StrictString()
		if err != nil {
			return nil, fmt.Errorf("query: invalid string expression: %w", err)
		}
		return StringLiteral{Value: s}, nil

	case KindObject:
		fields, err := parseObjectFields(n)
		if err != nil {
			return nil, err
		}
		return ObjectExpr{Fields: fields}, nil

	case KindArray:
		return parseArrayExpression(n)

	case KindInvalid:
		return nil, fmt.Errorf("query: invalid expression JSON")

	default:
		return nil, fmt.Errorf("query: unexpected JSON value in expression position (type %d)", n.Kind())
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

func parseArrayExpression(n Node) (Expression, error) {
	items, err := arrayItems(n)
	if err != nil {
		return nil, fmt.Errorf("query: invalid array expression: %w", err)
	}
	if items.len == 0 {
		return nil, fmt.Errorf("query: empty array is not a valid expression")
	}

	tag, err := items.at(0).StrictString()
	if err != nil {
		return nil, fmt.Errorf("query: expression array must start with a string tag: %w", err)
	}
	// Word-form synonyms (specs/query-json.md) normalize to their canonical
	// tag here, before anything downstream sees it — see OperatorWords.
	if canonical, ok := OperatorWords[tag]; ok {
		tag = canonical
	}
	rest := items.from(1)

	switch tag {
	case "between", "not_between":
		if rest.len != 3 {
			return nil, fmt.Errorf("query: %q needs exactly 3 operands, got %d", tag, rest.len)
		}
		minE, err := parseNode(rest.at(0))
		if err != nil {
			return nil, err
		}
		expE, err := parseNode(rest.at(1))
		if err != nil {
			return nil, err
		}
		maxE, err := parseNode(rest.at(2))
		if err != nil {
			return nil, err
		}
		return BetweenExpr{Negate: tag == "not_between", Min: minE, Exp: expE, Max: maxE}, nil

	case "bigint":
		return parseValidatedStringArgLiteral(tag, rest, validBigIntLiteral, func(v string) Expression { return BigIntLiteral{Value: v} })
	case "numeric":
		return parseValidatedStringArgLiteral(tag, rest, validNumericLiteral, func(v string) Expression { return NumericLiteral{Value: v} })

	case "in", "not_in":
		if rest.len < 1 {
			return nil, fmt.Errorf("query: %q needs a subject", tag)
		}
		subject, err := parseNode(rest.at(0))
		if err != nil {
			return nil, err
		}
		candidates, err := parseExpressionList(rest.from(1))
		if err != nil {
			return nil, err
		}
		return InExpr{Negate: tag == "not_in", Subject: subject, Candidates: candidates}, nil

	case "any", "all":
		if rest.len != 3 {
			return nil, fmt.Errorf("query: %q needs exactly 3 operands, got %d", tag, rest.len)
		}
		op, err := rest.at(0).StrictString()
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
		subject, err := parseNode(rest.at(1))
		if err != nil {
			return nil, err
		}
		arr, err := parseNode(rest.at(2))
		if err != nil {
			return nil, err
		}
		return AnyAllExpr{All: tag == "all", Op: op, Subject: subject, Array: arr}, nil

	case "concat_ws":
		if rest.len < 1 {
			return nil, fmt.Errorf("query: %q needs a separator", tag)
		}
		sep, err := parseNode(rest.at(0))
		if err != nil {
			return nil, err
		}
		args, err := parseExpressionList(rest.from(1))
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
		if rest.len < 1 {
			return nil, fmt.Errorf("query: %q needs a format string", tag)
		}
		format, err := rest.at(0).StrictString()
		if err != nil {
			return nil, fmt.Errorf("query: %q format must be a string: %w", tag, err)
		}
		args, err := parseExpressionList(rest.from(1))
		if err != nil {
			return nil, err
		}
		return FormatExpr{Format: format, Args: args}, nil

	case "agg", "aggregate":
		if rest.len < 2 || rest.len > 3 {
			return nil, fmt.Errorf("query: %q needs 2 or 3 operands, got %d", tag, rest.len)
		}
		ident, err := parseFunctionRef(rest.at(0))
		if err != nil {
			return nil, fmt.Errorf("query: %q identifier: %w", tag, err)
		}
		argItems, err := arrayItems(rest.at(1))
		if err != nil {
			return nil, fmt.Errorf("query: %q arguments must be an array: %w", tag, err)
		}
		args, err := parseExpressionList(argItems)
		if err != nil {
			return nil, err
		}
		agg := &AggExpr{Identifier: ident, Arguments: args}
		if rest.len == 3 {
			if err := parseAggOptions(rest.at(2), agg); err != nil {
				return nil, fmt.Errorf("query: %q options: %w", tag, err)
			}
		}
		return agg, nil

	case "call":
		if rest.len < 1 {
			return nil, fmt.Errorf("query: %q needs an identifier", tag)
		}
		ident, err := parseFunctionRef(rest.at(0))
		if err != nil {
			return nil, fmt.Errorf("query: %q identifier: %w", tag, err)
		}
		args, err := parseExpressionList(rest.from(1))
		if err != nil {
			return nil, err
		}
		return &CallExpr{Identifier: ident, Arguments: args}, nil

	case ".":
		// Unlike every other position, a "." hop name is always a bare name,
		// never a general Expression — otherwise it'd parse as a
		// StringLiteral like any other bare string now does. Left-associative
		// fold, same shape foldExpression would produce, but Right is always
		// an *Identifier (see resolveHopInto/compileFolded).
		if rest.len < 1 {
			return nil, fmt.Errorf("query: %q needs at least 1 operand, got %d", tag, rest.len)
		}
		// The chain's own starting point : a bare JSON string in this leading
		// position is ALWAYS a name to resolve against the current scope —
		// same rule the old degenerate one-hop case already applied to a
		// lone operand, now just the natural start of a longer chain too, so
		// [".", "chain", "name"] IS [".", [".", "chain"], "name"] — no need
		// to nest an inner "." just to get the base resolved as a lookup
		// rather than parsed as a StringLiteral. Only when the leading
		// operand ISN'T a bare name (["col", "home"], a nested [".", ...],
		// ["call", ...], ...) does it fall through to a genuine
		// sub-expression base, same as before ; that form still requires at
		// least one hop after it; there's nothing else it could mean.
		var result Expression
		hopsFrom := 1
		if name, err := rest.at(0).StrictString(); err == nil {
			result = &Identifier{Name: name}
		} else {
			if rest.len < 2 {
				return nil, fmt.Errorf("query: %q needs a hop name after a non-bare-name base", tag)
			}
			base, err := parseNode(rest.at(0))
			if err != nil {
				return nil, err
			}
			result = base
		}
		for i := hopsFrom; i < rest.len; i++ {
			name, err := rest.at(i).StrictString()
			if err != nil {
				return nil, fmt.Errorf("query: %q hop name must be a bare string: %w", tag, err)
			}
			result = FoldedExpr{Op: FoldDot, Left: result, Right: &Identifier{Name: name}}
		}
		return result, nil

	case "*", "*~":
		return parseStarTag(tag == "*~", rest)

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
		if rest.len != 2 {
			return nil, fmt.Errorf("query: %q needs exactly [array, index]", tag)
		}
		arr, err := parseNode(rest.at(0))
		if err != nil {
			return nil, err
		}
		idx, err := parseNode(rest.at(1))
		if err != nil {
			return nil, err
		}
		return IndexExpr{Array: arr, Index: idx}, nil

	case "slice":
		if rest.len != 3 {
			return nil, fmt.Errorf("query: %q needs exactly [array, from, to]", tag)
		}
		arr, err := parseNode(rest.at(0))
		if err != nil {
			return nil, err
		}
		from, err := parseNode(rest.at(1))
		if err != nil {
			return nil, err
		}
		to, err := parseNode(rest.at(2))
		if err != nil {
			return nil, err
		}
		return SliceExpr{Array: arr, From: from, To: to}, nil

	case "col":
		return parseCol(rest)
	case "get", "set":
		return parseGetOrSet(tag, rest)

	case "$param":
		if rest.len < 1 || rest.len > 2 {
			return nil, fmt.Errorf("query: %q needs [name] or [name, cast]", tag)
		}
		name, err := rest.at(0).StrictString()
		if err != nil {
			return nil, fmt.Errorf("query: %q name must be a string: %w", tag, err)
		}
		var cast string
		if rest.len == 2 {
			cast, err = rest.at(1).StrictString()
			if err != nil {
				return nil, fmt.Errorf("query: %q cast must be a string: %w", tag, err)
			}
		}
		return ParamExpr{Name: name, Cast: cast}, nil
	}

	// "-"/"~" are members of two vocabularies ; checking unary (fixed
	// arity 1) before folded/binary resolves both without ambiguity.
	if op, ok := unaryOperators[tag]; ok && rest.len == 1 {
		expr, err := parseNode(rest.at(0))
		if err != nil {
			return nil, err
		}
		return UnaryExpr{Op: op, Expr: expr}, nil
	}
	if op, ok := binaryOperators[tag]; ok && rest.len == 2 {
		left, err := parseNode(rest.at(0))
		if err != nil {
			return nil, err
		}
		right, err := parseNode(rest.at(1))
		if err != nil {
			return nil, err
		}
		return BinaryExpr{Op: op, Left: left, Right: right}, nil
	}
	if op, ok := foldedOperators[tag]; ok && rest.len >= 1 {
		operands, err := parseExpressionList(rest)
		if err != nil {
			return nil, err
		}
		return foldExpression(op, operands)
	}

	return nil, fmt.Errorf("query: unrecognized expression tag %q (array length %d)", tag, items.len)
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
func parseDefaultPosition(n Node) (Expression, error) {
	if s, err := n.StrictString(); err == nil && s == "default" {
		return DefaultKeyword{}, nil
	}
	return parseNode(n)
}

func parseCol(rest nodeList) (Expression, error) {
	if rest.len < 1 || rest.len > 3 {
		return nil, fmt.Errorf("query: %q needs [column, default_get?, default_set?]", "col")
	}
	column, err := rest.at(0).StrictString()
	if err != nil {
		return nil, fmt.Errorf("query: col column must be a string: %w", err)
	}
	var defaultGet, defaultSet Expression
	if rest.len >= 2 {
		defaultGet, err = parseDefaultPosition(rest.at(1))
		if err != nil {
			return nil, err
		}
	}
	if rest.len == 3 {
		defaultSet, err = parseDefaultPosition(rest.at(2))
		if err != nil {
			return nil, err
		}
	}
	return &ColExpr{Column: column, DefaultGet: defaultGet, DefaultSet: defaultSet}, nil
}

func parseGetOrSet(tag string, rest nodeList) (Expression, error) {
	if rest.len < 1 || rest.len > 2 {
		return nil, fmt.Errorf("query: %q needs [column, default_value?]", tag)
	}
	column, err := rest.at(0).StrictString()
	if err != nil {
		return nil, fmt.Errorf("query: %q column must be a string: %w", tag, err)
	}
	var def Expression
	if rest.len == 2 {
		def, err = parseDefaultPosition(rest.at(1))
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

func parseExpressionList(items nodeList) ([]Expression, error) {
	out := make([]Expression, items.len)
	for i := 0; i < items.len; i++ {
		expr, err := parseNode(items.at(i))
		if err != nil {
			return nil, fmt.Errorf("query: item %d: %w", i, err)
		}
		out[i] = expr
	}
	return out, nil
}

func parseStringListValue(n Node) ([]string, error) {
	items, err := arrayItems(n)
	if err != nil {
		return nil, fmt.Errorf("query: expected a string array: %w", err)
	}
	out := make([]string, items.len)
	for i := range items.len {
		s, err := items.at(i).StrictString()
		if err != nil {
			return nil, fmt.Errorf("query: expected a string array, item %d: %w", i, err)
		}
		out[i] = s
	}
	return out, nil
}

func parseObjectFields(n Node) (map[string]Expression, error) {
	out := map[string]Expression{}
	err := n.ObjectEach(func(k string, v Node) error {
		expr, err := parseNode(v)
		if err != nil {
			return fmt.Errorf("query: field %q: %w", k, err)
		}
		out[k] = expr
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("query: expected an object: %w", err)
	}
	return out, nil
}

// parseStarTag parses "*"/"*~"'s trailing arguments : any number of except
// []string arrays and/or and map[string]Expression objects, dispatched by
// JSON type rather than position or count — every array is concatenated
// into one except list, every object merged into one and map (later keys
// overriding earlier), in the order given.
func parseStarTag(own bool, rest nodeList) (Expression, error) {
	tagName := "*"
	if own {
		tagName = "*~"
	}
	var except []string
	var and map[string]Expression
	for i := range rest.len {
		switch rest.at(i).Kind() {
		case KindArray:
			e, err := parseStringListValue(rest.at(i))
			if err != nil {
				return nil, err
			}
			except = append(except, e...)
		case KindObject:
			a, err := parseObjectFields(rest.at(i))
			if err != nil {
				return nil, err
			}
			if and == nil {
				and = map[string]Expression{}
			}
			for k, v := range a {
				and[k] = v
			}
		default:
			return nil, fmt.Errorf("query: %q's trailing arguments must each be an except array or an and object", tagName)
		}
	}
	return StarExpr{Own: own, Except: except, And: and}, nil
}

// parseFunctionRef parses AggExpr/CallExpr's identifier : a bare string
// (unqualified, never split on ".") or an explicit {schema, name} object.
func parseFunctionRef(n Node) (FunctionRef, error) {
	switch n.Kind() {
	case KindString:
		name, err := n.StrictString()
		if err != nil {
			return FunctionRef{}, fmt.Errorf("must be a string or {schema, name} object: %w", err)
		}
		return FunctionRef{Name: name}, nil

	case KindObject:
		nameNode, ok := n.ObjectGet("name")
		if !ok {
			return FunctionRef{}, fmt.Errorf(`object form needs a "name"`)
		}
		name, err := nameNode.StrictString()
		if err != nil {
			return FunctionRef{}, fmt.Errorf(`"name" must be a string: %w`, err)
		}
		var schema string
		if schemaNode, ok := n.ObjectGet("schema"); ok {
			schema, err = schemaNode.StrictString()
			if err != nil {
				return FunctionRef{}, fmt.Errorf(`"schema" must be a string: %w`, err)
			}
		}
		return FunctionRef{Schema: schema, Name: name}, nil

	default:
		return FunctionRef{}, fmt.Errorf("must be a string or {schema, name} object, got type %d", n.Kind())
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
func parseValidatedStringArgLiteral(tag string, rest nodeList, valid *regexp.Regexp, build func(string) Expression) (Expression, error) {
	if rest.len != 1 {
		return nil, fmt.Errorf("query: %q expects exactly one string argument, got %d", tag, rest.len)
	}
	v, err := rest.at(0).StrictString()
	if err != nil {
		return nil, fmt.Errorf("query: %q expects a string argument: %w", tag, err)
	}
	if !valid.MatchString(v) {
		return nil, fmt.Errorf("query: %q: %q is not a valid %s literal", tag, v, tag)
	}
	return build(v), nil
}

// parseAggOptions reads an "agg"'s last operand, {filter?, distinct?, order_by?}.
func parseAggOptions(n Node, agg *AggExpr) error {
	return n.ObjectEach(func(key string, v Node) error {
		var err error
		switch key {
		case "filter":
			if agg.Filter, err = parseNode(v); err != nil {
				return err
			}
		case "distinct":
			if agg.Distinct, err = v.StrictBool(); err != nil {
				return fmt.Errorf(`"distinct" must be a boolean : %w`, err)
			}
		case "order_by":
			items, err := arrayItems(v)
			if err != nil {
				return fmt.Errorf(`"order_by" must be an array : %w`, err)
			}
			for i := range items.len {
				term, err := parseOrderByTerm(items.at(i))
				if err != nil {
					return fmt.Errorf(`"order_by" item %d : %w`, i, err)
				}
				agg.OrderBy = append(agg.OrderBy, term)
			}
		default:
			return fmt.Errorf("unknown option %q (expected filter, distinct or order_by)", key)
		}
		return nil
	})
}
