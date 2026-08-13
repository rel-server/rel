package rel

import (
	"strings"

	"github.com/bytedance/sonic/ast"
	"gitlab.com/tozd/go/errors"
	"sales-way.com/server/query"
)

func operatorToken(op string) query.Token {
	return query.Token{Kind: query.TK_OPERATOR, Bytes: []byte(op)}
}

func makeQueryBinOp(left query.IAstExpression, op string, right query.IAstExpression) *query.AstBinOp {
	return &query.AstBinOp{
		Op:    operatorToken(op),
		Left:  left,
		Right: right,
	}
}

func ExpressionFromNode(ctx *ParsingContext, node *ast.Node) (query.IAstExpression, error) {
	switch node.TypeSafe() {
	case ast.V_NULL:
		return query.AstRawSql("null"), nil
	case ast.V_TRUE:
		return query.AstRawSql("true"), nil
	case ast.V_FALSE:
		return query.AstRawSql("false"), nil
	case ast.V_NUMBER:
		raw, err := node.Raw()
		if err != nil {
			return nil, errors.WithStack(err)
		}
		return query.AstRawSql(raw), nil
	case ast.V_STRING:
		str, err := node.StrictString()
		if err != nil {
			return nil, errors.WithStack(err)
		}
		return &query.Identifier{Identifier: str}, nil
	case ast.V_ARRAY:
		return expressionFromArray(ctx, node)
	case ast.V_OBJECT:
		return expressionFromObject(ctx, node)
	default:
		return nil, errors.New("invalid expression type")
	}
}

func expressionFromObject(ctx *ParsingContext, node *ast.Node) (query.IAstExpression, error) {
	args := make(query.FunctionCallArguments, 0)
	var err error

	node.ForEach(func(path ast.Sequence, child *ast.Node) bool {
		expr, innerErr := ExpressionFromNode(ctx, child)
		if innerErr != nil {
			err = innerErr
			return false
		}
		args = append(args, &query.AstNamedFunctionArgument{
			Alias:      *path.Key,
			Expression: expr,
		})
		return true
	})

	if err != nil {
		return nil, err
	}
	return query.AstExpressionList(args), nil
}

func expressionFromArray(ctx *ParsingContext, node *ast.Node) (query.IAstExpression, error) {
	elems, err := arrayElements(node)
	if err != nil {
		return nil, err
	}
	if len(elems) == 0 {
		return nil, errors.New("empty expression array")
	}

	first, err := nodeElementString(&elems[0])
	if err != nil {
		return nil, err
	}

	if first != "" {
		return expressionFromOperator(ctx, first, elems[1:])
	}

	if len(elems) == 1 {
		return ExpressionFromNode(ctx, &elems[0])
	}

	list := make(query.AstExpressionList, 0, len(elems))
	for i := range elems {
		expr, err := ExpressionFromNode(ctx, &elems[i])
		if err != nil {
			return nil, err
		}
		list = append(list, expr)
	}
	return list, nil
}

func expressionFromOperator(ctx *ParsingContext, op string, args []ast.Node) (query.IAstExpression, error) {
	op = strings.ToLower(op)

	switch op {
	case "-", "not", "~":
		if len(args) != 1 {
			return nil, errors.Errorf("operator %s expects 1 argument", op)
		}
		expr, err := ExpressionFromNode(ctx, &args[0])
		if err != nil {
			return nil, err
		}
		return &query.AstPrefixOp{Op: operatorToken(op), Expression: expr}, nil

	case "between", "not between":
		if len(args) != 3 {
			return nil, errors.Errorf("operator %s expects 3 arguments", op)
		}
		min, err := ExpressionFromNode(ctx, &args[0])
		if err != nil {
			return nil, err
		}
		exp, err := ExpressionFromNode(ctx, &args[1])
		if err != nil {
			return nil, err
		}
		max, err := ExpressionFromNode(ctx, &args[2])
		if err != nil {
			return nil, err
		}
		return makeQueryBinOp(min, op, makeQueryBinOp(exp, "and", max)), nil

	case "concat", "concat_ws", "coalesce", "arr", "array", "lst", "list":
		if len(args) == 0 {
			return nil, errors.Errorf("operator %s expects at least 1 argument", op)
		}
		fnArgs := make(query.FunctionCallArguments, 0, len(args))
		for i := range args {
			expr, err := ExpressionFromNode(ctx, &args[i])
			if err != nil {
				return nil, err
			}
			fnArgs = append(fnArgs, expr)
		}
		return &query.AstFunctionCall{
			Left:      &query.Identifier{Identifier: op},
			Arguments: fnArgs,
		}, nil

	case "format":
		if len(args) < 1 {
			return nil, errors.New("format expects at least a format string")
		}
		format, err := args[0].StrictString()
		if err != nil {
			return nil, errors.WithStack(err)
		}
		fnArgs := make(query.FunctionCallArguments, 0, len(args))
		fnArgs = append(fnArgs, query.AstRawSql(format))
		for i := 1; i < len(args); i++ {
			expr, err := ExpressionFromNode(ctx, &args[i])
			if err != nil {
				return nil, err
			}
			fnArgs = append(fnArgs, expr)
		}
		return &query.AstFunctionCall{
			Left:      &query.Identifier{Identifier: "format"},
			Arguments: fnArgs,
		}, nil

	case "call":
		if len(args) < 1 {
			return nil, errors.New("call expects at least a function identifier")
		}
		id, err := ExpressionFromNode(ctx, &args[0])
		if err != nil {
			return nil, err
		}
		fnArgs := make(query.FunctionCallArguments, 0, len(args)-1)
		for i := 1; i < len(args); i++ {
			expr, err := ExpressionFromNode(ctx, &args[i])
			if err != nil {
				return nil, err
			}
			fnArgs = append(fnArgs, expr)
		}
		return &query.AstFunctionCall{Left: id, Arguments: fnArgs}, nil

	case "agg", "aggregate":
		if len(args) < 2 {
			return nil, errors.New("aggregate expects an identifier and arguments")
		}
		id, err := ExpressionFromNode(ctx, &args[0])
		if err != nil {
			return nil, err
		}
		aggArgs, err := expressionArrayArg(ctx, &args[1])
		if err != nil {
			return nil, err
		}
		fnArgs := make(query.FunctionCallArguments, 0, len(aggArgs)+1)
		fnArgs = append(fnArgs, aggArgs...)
		if len(args) > 2 {
			filter, err := ExpressionFromNode(ctx, &args[2])
			if err != nil {
				return nil, err
			}
			fnArgs = append(fnArgs, filter)
		}
		return &query.AstFunctionCall{Left: id, Arguments: fnArgs}, nil

	case "col", "get", "set":
		// Field selection operators are valid in expressions but resolved in select context.
		fnArgs := make(query.FunctionCallArguments, 0, len(args))
		for i := range args {
			expr, err := ExpressionFromNode(ctx, &args[i])
			if err != nil {
				return nil, err
			}
			fnArgs = append(fnArgs, expr)
		}
		return &query.AstFunctionCall{
			Left:      &query.Identifier{Identifier: op},
			Arguments: fnArgs,
		}, nil

	case "*":
		// Star with exclusions is handled in select parsing.
		return &query.AstFunctionCall{
			Left:      &query.Identifier{Identifier: "*"},
			Arguments: parseStringArguments(args),
		}, nil
	}

	if len(args) != 2 {
		return nil, errors.Errorf("operator %s expects 2 arguments", op)
	}
	left, err := ExpressionFromNode(ctx, &args[0])
	if err != nil {
		return nil, err
	}
	right, err := ExpressionFromNode(ctx, &args[1])
	if err != nil {
		return nil, err
	}
	return makeQueryBinOp(left, op, right), nil
}

func expressionArrayArg(ctx *ParsingContext, node *ast.Node) (query.FunctionCallArguments, error) {
	if node.TypeSafe() != ast.V_ARRAY {
		expr, err := ExpressionFromNode(ctx, node)
		if err != nil {
			return nil, err
		}
		return query.FunctionCallArguments{expr}, nil
	}
	elems, err := arrayElements(node)
	if err != nil {
		return nil, err
	}
	args := make(query.FunctionCallArguments, 0, len(elems))
	for i := range elems {
		expr, err := ExpressionFromNode(ctx, &elems[i])
		if err != nil {
			return nil, err
		}
		args = append(args, expr)
	}
	return args, nil
}

func parseStringArguments(args []ast.Node) query.FunctionCallArguments {
	res := make(query.FunctionCallArguments, 0, len(args))
	for i := range args {
		if str, err := args[i].StrictString(); err == nil {
			res = append(res, &query.Identifier{Identifier: str})
		}
	}
	return res
}

func arrayElements(node *ast.Node) ([]ast.Node, error) {
	values, err := node.Values()
	if err != nil {
		return nil, errors.WithStack(err)
	}
	elems := make([]ast.Node, 0)
	var item ast.Node
	for values.Next(&item) {
		elems = append(elems, item)
	}
	return elems, nil
}

func nodeElementString(node *ast.Node) (string, error) {
	if node.TypeSafe() != ast.V_STRING {
		return "", nil
	}
	return node.StrictString()
}
