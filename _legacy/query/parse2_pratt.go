package query

import "strings"

/*

Operator/Element	Associativity	Description
.	left	table/column name separator
::	left	PostgreSQL-style typecast
[ ]	left	array element selection
+ -	right	unary plus, unary minus
COLLATE	left	collation selection
AT	left	AT TIME ZONE, AT LOCAL
^	left	exponentiation
* / %	left	multiplication, division, modulo
+ -	left	addition, subtraction
(any other operator)	left	all other native and user-defined operators
BETWEEN IN LIKE ILIKE SIMILAR	 	range containment, set membership, string matching
< > = <= >= <>	 	comparison operators
IS ISNULL NOTNULL	 	IS TRUE, IS FALSE, IS NULL, IS DISTINCT FROM, etc.
NOT	right	logical negation
AND	left	logical conjunction
OR	left	logical disjunction

*/

func _parseExpressionListLike(ctx *ParsingContext, close string, fn func(expr IAstExpression)) error {
	if err := optParseUntilSeparatedBy(ctx, close, ",", func() error {
		if expr, err := parseExpression(ctx, 0); err != nil {
			return err
		} else {
			fn(expr)
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// Nud is we land on the token at the beginning of the expression
func (tok Token) Xp2Nud(ctx *ParsingContext) (IAstExpression, error) {
	switch tok.Kind {

	case TK_OPERATOR:
		switch strings.ToLower(tok.String()) {
		case "not", "-", "+", "~":
			right, err := parseExpression(ctx, 0)
			if err != nil {
				return nil, err
			}

			return &AstPrefixOp{
				Op:         tok,
				Expression: right,
			}, nil
		}

	case TK_LPAREN:
		// List (). If only one element, it's a parenthesized expression
		var list = make(AstExpressionList, 0)
		if err := _parseExpressionListLike(ctx, ")", func(expr IAstExpression) {
			list = append(list, expr)
		}); err != nil {
			return nil, err
		}
		return list, nil

	case TK_LBRACKET: // "["

		var arr = make(AstArray, 0)
		if err := _parseExpressionListLike(ctx, "]", func(expr IAstExpression) {
			arr = append(arr, expr)
		}); err != nil {
			return nil, err
		}
		return arr, nil

	case TK_IDENT:
		return &Identifier{
			Identifier: tok.String(),
		}, nil

	case TK_NUMBER, TK_STRING:
		return AstRawSql(tok.String()), nil
	}

	return nil, tok.ErrorMessage("unknown identifier")
}

var ABOVE_DOT = 16
var ABOVE_PAREN = 13

func (tok Token) Xp2Lbp() int {

	switch tok.Kind {

	case TK_OPERATOR:
		var str = strings.ToLower(tok.String())

		switch str {
		case "or":
			return 1
		case "and":
			return 2
		case "not":
			return 3
		case "is", "isnull", "notnull":
			return 4
		case "<", ">", "=", "!=", "<=", ">=":
			return 5
		case "between", "in", "like", "ilike", "similar", "similarto":
			return 7
		case "+", "-":
			return 9
		case "*", "/", "%":
			return 10
		case "^":
			return 11
		case "at":
			return 12
		case "collate", "(":
			return 13
			// unary + -
		case "[":
			return 14
		case "::":
			return 15
		case ".":
			return 16
		default:
			return 8 // any other operator
		}

	case TK_LPAREN:
		return 13
	default:
		return -1
	}
}

func (tok *Token) Xp2Led(ctx *ParsingContext, left IAstExpression) (IAstExpression, error) {
	// Binary operators
	var str = tok.String()
	switch str {
	case "(":
		if arguments, err := ParseFunctionCall(ctx); err != nil {
			return nil, err
		} else {
			return &AstFunctionCall{
				Left:      left,
				Arguments: arguments,
			}, nil
		}

	// case "::", ".", "=", "<>", "<", ">", ">=", "<=", "or", "and", "in", "+", "-", "*", "/", "%", "^", "is", "isnull", "notnull", "not", "between", "like", "ilike", "similar", "similarto", "||", "@>", "@<", "~", "~*", "~|", "~~", "~~*", "~~|":

	case "::" /* type cast */, "." /* field access */, "+" /* addition */, "–" /* subtraction */, "*" /* multiplication */, "/" /* division */, "%" /* modulo */, "^" /* exponentiation */, "&" /* bitwise AND */, "|" /* bitwise OR */, "#" /* bitwise XOR */, "<<" /* shift left */, ">>" /* shift right */, "=" /* equal */, "!=" /* not equal */, "<>" /* not equal (alternate) */, "<" /* less than */, "<=" /* less than or equal */, ">" /* greater than */, ">=" /* greater than or equal */, "||" /* string concatenation */, "~" /* regex match (case-sensitive) */, "~*" /* regex match (case-insensitive) */, "!~" /* regex not match (case-sensitive) */, "!~*" /* regex not match (case-insensitive) */, "&&" /* overlaps */, "@>" /* contains */, "<@" /* is contained by */, "-" /* remove elements */, "->" /* get field as JSON */, "->>" /* get field as text */, "#>" /* get path as JSON */, "#>>" /* get path as text */, "?" /* key exists */, "?|" /* any key exists */, "?&" /* all keys exist */, "@" /* point on line/circle/path */, "~=" /* same value (point/box/polygon) */, "&<" /* overlap or left of */, "&>" /* overlap or right of */, "<<|" /* strictly below */, "|>>" /* strictly above */, "|&<" /* overlap or below */, "|&>" /* overlap or above */, "and", "or", "is", "is not":
		if right, err := parseExpression(ctx, tok.Xp2Lbp()); err != nil {
			return nil, err
		} else {
			return &AstBinOp{
				Op:    *tok,
				Left:  left,
				Right: right,
			}, nil
		}
	}

	return nil, tok.ErrorMessage("unknown operator")
}

func parseExpression(ctx *ParsingContext, rbp int) (IAstExpression, error) {
	var tk = ctx.lexer.Next()
	if tk.IsUnexpected() {
		return nil, tk.Error()
	}

	left, err := tk.Xp2Nud(ctx)
	if err != nil {
		return nil, err
	}

	if left == nil {
		return nil, tk.ErrorMessage("expected expression")
	}

	for {
		tk = ctx.lexer.Peek()
		if tk.Xp2Lbp() < rbp {
			break
		}

		ctx.lexer.SetPosition(tk)

		if left2, err := tk.Xp2Led(ctx, left); err == nil {
			left = left2
		} else {
			return nil, err
		}
	}

	return left, nil
}
