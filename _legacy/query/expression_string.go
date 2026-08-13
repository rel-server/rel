package query

import "strings"

func (expr FunctionCallArguments) String() string {
	expressions := make([]string, len(expr))
	for i, expression := range expr {
		expressions[i] = expression.String()
	}
	return "(" + strings.Join(expressions, ", ") + ")"
}

func (expr AstExpressionList) String() string {
	expressions := make([]string, len(expr))
	for i, expression := range expr {
		expressions[i] = expression.String()
	}
	return "(" + strings.Join(expressions, ", ") + ")"
}

func (expr *AstFunctionCall) String() string {
	return expr.Left.String() + "(" + expr.Arguments.String() + ")"
}

func (expr *AstPrefixOp) String() string {
	return expr.Op.String() + " " + expr.Expression.String()
}

func (expr *AstSuffixOp) String() string {
	return expr.Expression.String() + " " + expr.Op.String()
}

func (expr *AstBinOp) String() string {
	return expr.Left.String() + expr.Op.String() + expr.Right.String()
}

func (expr *AstNamedFunctionArgument) String() string {
	if len(expr.Alias) > 0 {
		return expr.Alias + " => " + expr.Expression.String()
	} else {
		return expr.Expression.String()
	}
}

func (expr AstArray) String() string {
	items := make([]string, len(expr))
	for i, item := range expr {
		items[i] = item.String()
	}
	return "ARRAY[" + strings.Join(items, ", ") + "]"
}

func (expr *AstSimpleField) String() string {
	return expr.Name
}

func (expr *AstRelationshipField) String() string {
	return expr.Name.String() + "(" + ")"
}

func (expr *AstFieldGroup) String() string {
	fields := make([]string, len(expr.Fields))
	for i, field := range expr.Fields {
		fields[i] = field.String()
	}
	return expr.Name + "(" + strings.Join(fields, ", ") + ")"
}

func (expr *AstStarSelector) String() string {
	return "*"
}

func (expr AstRawSql) String() string {
	return string(expr)
}
