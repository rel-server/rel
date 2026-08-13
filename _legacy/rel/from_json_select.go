package rel

import (
	"strings"

	"github.com/bytedance/sonic/ast"
	"gitlab.com/tozd/go/errors"
	"sales-way.com/server/query"
	"sales-way.com/server/utils"
)

func selectFieldsFromNode(ctx *ParsingContext, node *ast.Node) ([]IAstField, error) {
	switch node.TypeSafe() {
	case ast.V_OBJECT:
		return selectFieldsFromObject(ctx, node)
	case ast.V_ARRAY, ast.V_STRING, ast.V_NUMBER, ast.V_TRUE, ast.V_FALSE, ast.V_NULL:
		field, err := selectFieldFromExpression(ctx, "", node)
		if err != nil {
			return nil, err
		}
		return []IAstField{field}, nil
	default:
		return nil, errors.New("invalid select expression")
	}
}

func selectFieldsFromObject(ctx *ParsingContext, node *ast.Node) ([]IAstField, error) {
	fields := make([]IAstField, 0)
	var err error

	node.ForEach(func(path ast.Sequence, child *ast.Node) bool {
		alias := *path.Key
		field, innerErr := selectFieldFromNode(ctx, alias, child)
		if innerErr != nil {
			err = innerErr
			return false
		}
		fields = append(fields, field)
		return true
	})

	return fields, err
}

func selectFieldFromNode(ctx *ParsingContext, alias string, node *ast.Node) (IAstField, error) {
	switch node.TypeSafe() {
	case ast.V_OBJECT:
		nested, err := selectFieldsFromObject(ctx, node)
		if err != nil {
			return nil, err
		}
		return &AstFieldGroup{Name: alias, Fields: nested}, nil
	default:
		return selectFieldFromExpression(ctx, alias, node)
	}
}

func selectFieldFromExpression(ctx *ParsingContext, alias string, node *ast.Node) (IAstField, error) {
	if node.TypeSafe() == ast.V_ARRAY {
		elems, err := arrayElements(node)
		if err != nil {
			return nil, err
		}
		if len(elems) == 0 {
			if alias == "*" || alias == "" {
				return &AstStarSelector{Exclusions: utils.Set[string]{}}, nil
			}
			return nil, errors.New("empty select array")
		}
	}

	if node.TypeSafe() == ast.V_STRING {
		name, err := node.StrictString()
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if alias != "" {
			return &AstSimpleField{
				Name: alias,
				Expression: &query.Identifier{
					Identifier: name,
				},
			}, nil
		}
		return &AstSimpleField{
			Name: name,
			Expression: &query.Identifier{
				Identifier: name,
			},
		}, nil
	}

	if node.TypeSafe() == ast.V_ARRAY {
		elems, err := arrayElements(node)
		if err != nil {
			return nil, err
		}
		if len(elems) == 0 {
			return nil, errors.New("empty select array")
		}

		op, err := nodeElementString(&elems[0])
		if err != nil {
			return nil, err
		}

		switch strings.ToLower(op) {
		case "*":
			star := &AstStarSelector{Name: alias, Exclusions: utils.Set[string]{}}
			for i := 1; i < len(elems); i++ {
				excl, err := elems[i].StrictString()
				if err != nil {
					return nil, errors.WithStack(err)
				}
				star.Exclusions.Add(excl)
			}
			return star, nil

		case "col", "get", "set":
			return selectFieldFromColOp(alias, op, elems[1:])

		default:
			expr, err := expressionFromArray(ctx, node)
			if err != nil {
				return nil, err
			}
			if alias == "" {
				return nil, errors.New("expression field requires an alias")
			}
			return &AstSimpleField{Name: alias, Expression: expr}, nil
		}
	}

	expr, err := ExpressionFromNode(ctx, node)
	if err != nil {
		return nil, err
	}
	if alias == "" {
		return nil, errors.New("expression field requires an alias")
	}
	return &AstSimpleField{Name: alias, Expression: expr}, nil
}

func selectFieldFromColOp(alias string, op string, args []ast.Node) (IAstField, error) {
	if len(args) < 1 {
		return nil, errors.Errorf("%s expects a column name", op)
	}
	column, err := args[0].StrictString()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	exprArgs := make(query.FunctionCallArguments, 0, len(args))
	exprArgs = append(exprArgs, &query.Identifier{Identifier: column})
	for i := 1; i < len(args); i++ {
		expr, err := ExpressionFromNode(nil, &args[i])
		if err != nil {
			return nil, err
		}
		exprArgs = append(exprArgs, expr)
	}

	field := &AstSimpleField{
		Name: alias,
		Expression: &query.AstFunctionCall{
			Left:      &query.Identifier{Identifier: op},
			Arguments: exprArgs,
		},
	}
	if alias == "" {
		field.Name = column
	}
	if op == "get" {
		field = &AstSimpleField{
			Name:       aliasOr(column, alias),
			Expression: &query.Identifier{Identifier: column},
		}
	}
	return field, nil
}

func aliasOr(column string, alias string) string {
	if alias != "" {
		return alias
	}
	return column
}
