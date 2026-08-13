package query

func (tk Token) Sql(ctx *SqlgenContext) {
	// maybe some handling ?
	ctx.W(tk.String())
}

/////////////////////////////////

func _ensureNotQuoted(identifier string) string {
	if len(identifier) > 0 && identifier[0] == '"' {
		return identifier[1 : len(identifier)-1]
	}
	return identifier
}

// NewSqlIdentifier creates a new SqlIdentifier from a string, since the lexer doesn't make the distinction between schema and identifier
func NewSqlIdentifier(identifier string) *Identifier {
	return &Identifier{
		Identifier: identifier,
	}
}

func (id *Identifier) Sql(ctx *SqlgenContext) {
	ctx.W(id.Identifier)
}

//////////////////////////////

// -2
func (expr *AstPrefixOp) Sql(ctx *SqlgenContext) {
	expr.Op.Sql(ctx)
	ctx.W(" ")
	expr.Expression.Sql(ctx)
}

// 2+
func (expr *AstSuffixOp) Sql(ctx *SqlgenContext) {
	expr.Expression.Sql(ctx)
	ctx.W(" ")
	expr.Op.Sql(ctx)
}

// 1 + 2
func (expr *AstBinOp) Sql(ctx *SqlgenContext) {
	ctx.W("(")
	expr.Left.Sql(ctx)
	ctx.W(" ")
	expr.Op.Sql(ctx)
	ctx.W(" ")
	expr.Right.Sql(ctx)
	ctx.W(")")
}

// raw
func (raw AstRawSql) Sql(ctx *SqlgenContext) {
	ctx.W(string(raw))
}

// ('1', '2', '3')
func (list AstExpressionList) Sql(ctx *SqlgenContext) {
	ctx.W("(")
	for i, expr := range list {
		if i > 0 {
			ctx.W(", ")
		}
		expr.Sql(ctx)
	}
	ctx.W(")")
}

func (args FunctionCallArguments) Sql(ctx *SqlgenContext) {

	// Do not output if empty
	if args == nil {
		return
	}

	ctx.W("(")
	for i, expr := range args {
		if i > 0 {
			ctx.W(", ")
		}
		expr.Sql(ctx)
	}
	ctx.W(")")
}

// [1, 2, 3]
func (arr AstArray) Sql(ctx *SqlgenContext) {
	ctx.W("ARRAY[")
	for i, expr := range arr {
		if i > 0 {
			ctx.W(", ")
		}
		expr.Sql(ctx)
	}
	ctx.W("]")
}

func (arg *AstNamedFunctionArgument) Sql(ctx *SqlgenContext) {
	ctx.W("\"" + arg.Alias + "\"")
	ctx.W(" => ")
	arg.Expression.Sql(ctx)
}

func (call *AstFunctionCall) Sql(ctx *SqlgenContext) {
	call.Left.Sql(ctx)
	call.Arguments.Sql(ctx)
}

// An expression is an identification chain if it is a sequence of .-separated identifiers.
func ExpressionIsIdentificationChain(expr IAstExpression) bool {
	if binop, ok := expr.(*AstBinOp); ok {
		if binop.Op.String() == "." {
			return ExpressionIsIdentificationChain(binop.Left) && ExpressionIsIdentificationChain(binop.Right)
		}
	}
	if _, ok := expr.(*Identifier); ok {
		return true
	}
	return false
}
