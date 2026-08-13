package rel

import (
	"sales-way.com/server/pg"
	"sales-way.com/server/query"
)

type ResolveContext struct {
	Tables    pg.DBAllTables
	Functions pg.DBFunctionMap
	scope     *query.Scope
}

func NewResolveContext(tables pg.DBAllTables, functions pg.DBFunctionMap, root *query.Scope) *ResolveContext {
	return &ResolveContext{
		Tables:    tables,
		Functions: functions,
		scope:     root,
	}
}

func (ctx *ResolveContext) Child() *ResolveContext {
	return &ResolveContext{
		Tables:    ctx.Tables,
		Functions: ctx.Functions,
		scope:     ctx.scope.Child(),
	}
}

func (ctx *ResolveContext) AddBody() {
	ctx.scope.AddRewrittenSymbol("$body", "($1::jsonb)")
}
