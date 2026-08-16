package query

type Expression interface {
	// Validation pass for scoping / blacklist checking
	Validate( /* may need arguments ? */ ) error
}

// Adds asc/desc/asc nulls first/desc nulls first
type OrderByExpression interface {
	Expression
}
