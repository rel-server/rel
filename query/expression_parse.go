package query

import (
	"bytes"
	"encoding/binary"
	"regexp"

	"github.com/samber/oops"
)

// ParseExpression decodes one query.ts Expression from raw JSON, without any scope lookup. Empty input is
// NullLiteral. Values are read with the Strict* accessors only, since the lenient ones coerce across JSON types.
func ParseExpression(data []byte) (Expression, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return NullLiteral{}, nil
	}
	root, err := parseJSON(trimmed)
	if err != nil {
		return nil, oops.Wrapf(err, "query: invalid expression JSON")
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
			return nil, oops.Wrapf(err, "query: invalid boolean expression")
		}
		return BoolLiteral{Value: b}, nil

	case KindNumber:
		f, err := n.StrictFloat64()
		if err != nil {
			return nil, oops.Wrapf(err, "query: invalid number expression")
		}
		return NumberLiteral{Value: f}, nil

	case KindString:
		s, err := n.StrictString()
		if err != nil {
			return nil, oops.Wrapf(err, "query: invalid string expression")
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

	case KindInstant, KindDate, KindUUID, KindBytes:
		return parseTypedLiteral(n)

	case KindInvalid:
		return nil, oops.Errorf("query: invalid expression JSON")

	default:
		return nil, oops.Errorf("query: unexpected JSON value in expression position (type %d)", n.Kind())
	}
}

// parseTypedLiteral reads a CBOR value that carries its type through the write encoder of that type, so a
// filter accepts and refuses exactly what a write's data does.
func parseTypedLiteral(n Node) (Expression, error) {
	var b []byte
	var err error
	switch n.Kind() {
	case KindInstant:
		if b, err = encodeTimestamptz(nil, n, nil); err == nil {
			return InstantLiteral{Micros: int64(binary.BigEndian.Uint64(b))}, nil
		}
	case KindDate:
		if b, err = encodeDate(nil, n, nil); err == nil {
			return DateLiteral{Days: int32(binary.BigEndian.Uint32(b))}, nil
		}
	case KindUUID:
		if b, err = encodeUUID(nil, n, nil); err == nil {
			return UUIDLiteral{Value: [16]byte(b)}, nil
		}
	case KindBytes:
		if b, err = encodeBytea(nil, n, nil); err == nil {
			return BytesLiteral{Value: b}, nil
		}
	}
	return nil, oops.Wrapf(err, "query: invalid %s literal", kindName(n.Kind()))
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
		return nil, oops.Wrapf(err, "query: invalid array expression")
	}
	if items.len == 0 {
		return nil, oops.Errorf("query: empty array is not a valid expression")
	}

	tag, err := items.at(0).StrictString()
	if err != nil {
		return nil, oops.Wrapf(err, "query: expression array must start with a string tag")
	}
	// Word-form synonyms normalize to their canonical tag before anything downstream sees it.
	if canonical, ok := OperatorWords[tag]; ok {
		tag = canonical
	}
	rest := items.from(1)

	switch tag {
	case "between", "not_between":
		if rest.len != 3 {
			return nil, oops.With("tag", tag).Errorf("query: %q needs exactly 3 operands, got %d", tag, rest.len)
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
			return nil, oops.With("tag", tag).Errorf("query: %q needs a subject", tag)
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
			return nil, oops.With("tag", tag).Errorf("query: %q needs exactly 3 operands, got %d", tag, rest.len)
		}
		op, err := rest.at(0).StrictString()
		if err != nil {
			return nil, oops.With("tag", tag).Wrapf(err, "query: %q operator must be a string", tag)
		}
		if canonical, ok := OperatorWords[op]; ok {
			op = canonical
		}
		if _, isBinary := binaryOperators[op]; !isBinary {
			if _, isFolded := foldedOperators[op]; !isFolded {
				return nil, oops.With("tag", tag, "operator", op).Errorf("query: %q operator %q is not a known operator", tag, op)
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
			return nil, oops.With("tag", tag).Errorf("query: %q needs a separator", tag)
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
			return nil, oops.With("tag", tag).Errorf("query: %q needs a format string", tag)
		}
		format, err := rest.at(0).StrictString()
		if err != nil {
			return nil, oops.With("tag", tag).Wrapf(err, "query: %q format must be a string", tag)
		}
		args, err := parseExpressionList(rest.from(1))
		if err != nil {
			return nil, err
		}
		return FormatExpr{Format: format, Args: args}, nil

	case "agg", "aggregate":
		if rest.len < 2 || rest.len > 3 {
			return nil, oops.With("tag", tag).Errorf("query: %q needs 2 or 3 operands, got %d", tag, rest.len)
		}
		ident, err := parseFunctionRef(rest.at(0))
		if err != nil {
			return nil, oops.With("tag", tag).Wrapf(err, "query: %q identifier", tag)
		}
		argItems, err := arrayItems(rest.at(1))
		if err != nil {
			return nil, oops.With("tag", tag).Wrapf(err, "query: %q arguments must be an array", tag)
		}
		args, err := parseExpressionList(argItems)
		if err != nil {
			return nil, err
		}
		agg := &AggExpr{Identifier: ident, Arguments: args}
		if rest.len == 3 {
			if err := parseAggOptions(rest.at(2), agg); err != nil {
				return nil, oops.With("tag", tag).Wrapf(err, "query: %q options", tag)
			}
		}
		return agg, nil

	case "call":
		if rest.len < 1 {
			return nil, oops.With("tag", tag).Errorf("query: %q needs an identifier", tag)
		}
		ident, err := parseFunctionRef(rest.at(0))
		if err != nil {
			return nil, oops.With("tag", tag).Wrapf(err, "query: %q identifier", tag)
		}
		args, err := parseExpressionList(rest.from(1))
		if err != nil {
			return nil, err
		}
		return &CallExpr{Identifier: ident, Arguments: args}, nil

	case ".":
		// A "." hop name is always a bare name, never an Expression. Folds left-associatively, with Right always
		// an *Identifier.
		if rest.len < 1 {
			return nil, oops.With("tag", tag).Errorf("query: %q needs at least 1 operand, got %d", tag, rest.len)
		}
		// A bare string base is a name in the current scope, so [".", "a", "b"] is [".", [".", "a"], "b"]. Any
		// other base is a sub-expression and needs at least one hop after it.
		var result Expression
		hopsFrom := 1
		if name, err := rest.at(0).StrictString(); err == nil {
			result = &Identifier{Name: name}
		} else {
			if rest.len < 2 {
				return nil, oops.With("tag", tag).Errorf("query: %q needs a hop name after a non-bare-name base", tag)
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
				return nil, oops.With("tag", tag).Wrapf(err, "query: %q hop name must be a bare string", tag)
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
			return nil, oops.With("tag", tag).Errorf("query: %q needs exactly [array, index]", tag)
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
			return nil, oops.With("tag", tag).Errorf("query: %q needs exactly [array, from, to]", tag)
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
			return nil, oops.With("tag", tag).Errorf("query: %q needs [name] or [name, cast]", tag)
		}
		name, err := rest.at(0).StrictString()
		if err != nil {
			return nil, oops.With("tag", tag).Wrapf(err, "query: %q name must be a string", tag)
		}
		var cast string
		if rest.len == 2 {
			cast, err = rest.at(1).StrictString()
			if err != nil {
				return nil, oops.With("tag", tag).Wrapf(err, "query: %q cast must be a string", tag)
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

	return nil, oops.With("tag", tag).Errorf("query: unrecognized expression tag %q (array length %d)", tag, items.len)
}

// foldExpression collapses an N-ary folded-operator array into nested binary FoldedExpr nodes.
func foldExpression(op FoldedOperator, operands []Expression) (Expression, error) {
	if len(operands) == 0 {
		return nil, oops.With("operator", op).Errorf("query: folded operator %q needs at least one operand", op)
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

// parseDefaultPosition parses a default-value slot, where a bare "default" string means DefaultKeyword.
func parseDefaultPosition(n Node) (Expression, error) {
	if s, err := n.StrictString(); err == nil && s == "default" {
		return DefaultKeyword{}, nil
	}
	return parseNode(n)
}

func parseCol(rest nodeList) (Expression, error) {
	if rest.len < 1 || rest.len > 3 {
		return nil, oops.Errorf("query: %q needs [column, default_get?, default_set?]", "col")
	}
	column, err := rest.at(0).StrictString()
	if err != nil {
		return nil, oops.Wrapf(err, "query: col column must be a string")
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
		return nil, oops.With("tag", tag).Errorf("query: %q needs [column, default_value?]", tag)
	}
	column, err := rest.at(0).StrictString()
	if err != nil {
		return nil, oops.With("tag", tag).Wrapf(err, "query: %q column must be a string", tag)
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
			return nil, oops.With("index", i).Wrapf(err, "query: item %d", i)
		}
		out[i] = expr
	}
	return out, nil
}

func parseStringListValue(n Node) ([]string, error) {
	items, err := arrayItems(n)
	if err != nil {
		return nil, oops.Wrapf(err, "query: expected a string array")
	}
	out := make([]string, items.len)
	for i := range items.len {
		s, err := items.at(i).StrictString()
		if err != nil {
			return nil, oops.With("index", i).Wrapf(err, "query: expected a string array, item %d", i)
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
			return oops.With("field", k).Wrapf(err, "query: field %q", k)
		}
		out[k] = expr
		return nil
	})
	if err != nil {
		return nil, oops.Wrapf(err, "query: expected an object")
	}
	return out, nil
}

// parseStarTag parses "*"/"*~"'s trailing arguments : arrays are concatenated into the except list, objects
// merged into the and map with later keys winning.
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
			return nil, oops.With("tag", tagName).Errorf("query: %q's trailing arguments must each be an except array or an and object", tagName)
		}
	}
	return StarExpr{Own: own, Except: except, And: and}, nil
}

// parseFunctionRef parses a bare string (unqualified, never split on ".") or a {schema, name} object.
func parseFunctionRef(n Node) (FunctionRef, error) {
	switch n.Kind() {
	case KindString:
		name, err := n.StrictString()
		if err != nil {
			return FunctionRef{}, oops.Wrapf(err, "must be a string or {schema, name} object")
		}
		return FunctionRef{Name: name}, nil

	case KindObject:
		nameNode, ok := n.ObjectGet("name")
		if !ok {
			return FunctionRef{}, oops.Errorf(`object form needs a "name"`)
		}
		name, err := nameNode.StrictString()
		if err != nil {
			return FunctionRef{}, oops.Wrapf(err, `"name" must be a string`)
		}
		var schema string
		if schemaNode, ok := n.ObjectGet("schema"); ok {
			schema, err = schemaNode.StrictString()
			if err != nil {
				return FunctionRef{}, oops.Wrapf(err, `"schema" must be a string`)
			}
		}
		return FunctionRef{Schema: schema, Name: name}, nil

	default:
		return FunctionRef{}, oops.Errorf("must be a string or {schema, name} object, got type %d", n.Kind())
	}
}

// validBigIntLiteral/validNumericLiteral make a malformed value a 400 at parse time, not a Postgres error.
var (
	validBigIntLiteral  = regexp.MustCompile(`^-?[0-9]+$`)
	validNumericLiteral = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)
)

// parseValidatedStringArgLiteral is parseStringArgLiteral plus a format
// gate, for literal kinds written straight into SQL text, not bound.
func parseValidatedStringArgLiteral(tag string, rest nodeList, valid *regexp.Regexp, build func(string) Expression) (Expression, error) {
	if rest.len != 1 {
		return nil, oops.With("tag", tag).Errorf("query: %q expects exactly one string argument, got %d", tag, rest.len)
	}
	v, err := rest.at(0).StrictString()
	if err != nil {
		return nil, oops.With("tag", tag).Wrapf(err, "query: %q expects a string argument", tag)
	}
	if !valid.MatchString(v) {
		return nil, oops.With("tag", tag).Errorf("query: %q: %q is not a valid %s literal", tag, v, tag)
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
				return oops.Wrapf(err, `"distinct" must be a boolean `)
			}
		case "order_by":
			items, err := arrayItems(v)
			if err != nil {
				return oops.Wrapf(err, `"order_by" must be an array `)
			}
			for i := range items.len {
				term, err := parseOrderByTerm(items.at(i))
				if err != nil {
					return oops.With("index", i).Wrapf(err, `"order_by" item %d `, i)
				}
				agg.OrderBy = append(agg.OrderBy, term)
			}
		default:
			return oops.With("option", key).Errorf("unknown option %q (expected filter, distinct or order_by)", key)
		}
		return nil
	})
}
