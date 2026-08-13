package query

import (
	"github.com/valyala/bytebufferpool"
	"sales-way.com/server/pg"
)

type ParsingContext struct {
	Tables         pg.DBAllTables
	lexer          *Lexer
	RelationshipNb int
	DefaultVerb    AstOperationType
}

type JsonTableContext struct {
	sel    *AstSelection
	parent *JsonTableContext
	path   string
}

func NewJsonTableContext(sel *AstSelection) *JsonTableContext {
	return &JsonTableContext{
		sel:    sel,
		parent: nil,
		path:   "",
	}
}

func (ctx *JsonTableContext) NewChild(sel *AstSelection, path string) *JsonTableContext {
	return &JsonTableContext{
		sel:    sel,
		parent: ctx,
		path:   path,
	}
}

type SqlgenContext struct {
	Tables       pg.DBAllTables
	Functions    pg.DBFunctionMap
	DefaultVerb  string
	Buffer       *bytebufferpool.ByteBuffer
	IsInSubQuery bool
	HasBody      bool

	parentContext       *SqlgenContext
	currentOperation    *AstOperation
	currentTable        *pg.DBTable
	currentSelection    *AstSelection
	currentRelationship *pg.RelationShip

	selectedFieldsCount int
	subAliasCount       int
}

func NewContext(tables pg.DBAllTables, functions pg.DBFunctionMap, defaultVerb string) *SqlgenContext {
	return &SqlgenContext{
		Tables:      tables,
		Functions:   functions,
		DefaultVerb: defaultVerb,
		Buffer:      bytebufferpool.Get(),
	}
}

func (ctx *SqlgenContext) NewChildContext(tbl *pg.DBTable, selection *AstSelection) *SqlgenContext {
	ctx.subAliasCount++
	return &SqlgenContext{
		Tables:              ctx.Tables,
		DefaultVerb:         ctx.DefaultVerb,
		Buffer:              ctx.Buffer,
		Functions:           ctx.Functions,
		parentContext:       ctx,
		currentTable:        tbl,
		currentSelection:    selection,
		currentOperation:    ctx.currentOperation,
		selectedFieldsCount: 0,
		subAliasCount:       0,
	}
}

func (ctx *SqlgenContext) W(s ...string) {
	for _, s := range s {
		ctx.Buffer.WriteString(s)
	}
}
