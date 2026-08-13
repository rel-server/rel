package query

import (
	"strings"

	"gitlab.com/tozd/go/errors"
	"sales-way.com/server/pg"
	"sales-way.com/server/utils"
)

type Symbol interface {
	// Resolve(ctx *ResolveContext) (string, error)
	Name() string
	Repr() string
}

// A symbol that holds a scope.
type ScopeSymbol struct {
	Symbol string
	Scope  *Scope
}

func (ss *ScopeSymbol) Name() string {
	return ss.Symbol
}

func (ss *ScopeSymbol) Repr() string {
	return ss.Symbol
}

type SimpleSymbol struct {
	name string
	repr string
}

func NewRewrittenName(name string, repr string) Symbol {
	return &SimpleSymbol{
		name: name,
		repr: repr,
	}
}

func (rs *SimpleSymbol) Name() string {
	return rs.name
}

func (rs *SimpleSymbol) Repr() string {
	return rs.repr
}

//////////////////////////////////////////////////////////////////////////////////////////////////

func NewScopeSymbol(name string, scope *Scope) *ScopeSymbol {
	return &ScopeSymbol{
		Symbol: name,
		Scope:  NewScope(),
	}
}

type Scope struct {
	parent    *Scope
	symbols   map[string]Symbol
	functions map[string]*pg.Function
}

func NewScope() *Scope {
	return &Scope{
		symbols:   make(map[string]Symbol),
		functions: make(map[string]*pg.Function),
	}
}

func (scope *Scope) Child() *Scope {
	child := NewScope()
	child.parent = scope
	return child
}

func (scope *Scope) ResolveExpression(expr IAstExpression) (Symbol, error) {

	if binop, ok := expr.(*AstBinOp); ok {
		if binop.Op.String() != "." {
			return nil, errors.New("cannot resolve expression " + binop.String())
		}

		right, ok := binop.Right.(*Identifier)
		if !ok {
			return nil, errors.New("right side of . must be an identifier")
		}

		left, err := scope.ResolveExpression(binop.Left)
		if err != nil {
			return nil, err
		}

		left_scope, ok := left.(*ScopeSymbol)
		if !ok {
			return nil, errors.New("left side of . must be a scope symbol")
		}

		if sym, ok := left_scope.Scope.GetSymbol(right.Identifier); ok {
			return sym, nil
		}

		return nil, errors.New("no such symbol " + binop.String())
	}

	if ident, ok := expr.(*Identifier); ok {
		if symbol, ok := scope.GetSymbol(ident.String()); ok {
			return symbol, nil
		}
	}

	if scope.parent != nil {
		return scope.parent.ResolveExpression(expr)
	}

	return nil, errors.New("cannot resolve expression " + expr.String())
}

func (scope *Scope) AddSimpleSymbol(name string) {
	scope.AddSymbol(NewRewrittenName(name, name))
}

// Add a symbol to the scope.
func (scope *Scope) AddSymbol(symbol Symbol) {
	name := symbol.Name()

	// If it is a simple name, add it as an uppercase version as well.
	if len(name) == 0 {
		return
	}

	if name[0] != '"' {
		// Add an uppercase and a quoted version of the name.
		name2 := strings.ToUpper(name)
		scope.symbols[name2] = symbol

		name3 := "\"" + name + "\""
		scope.symbols[name3] = symbol
	} else {
		// Already "quoted" add it as is.
		scope.symbols[name] = symbol
	}
}

func (scope *Scope) AddRewrittenSymbol(symbol string, rewritten string) {
	scope.AddSymbol(NewRewrittenName(symbol, rewritten))
}

func (scope *Scope) HasSymbol(symbol string) bool {
	if len(symbol) > 0 && symbol[0] != '"' {
		symbol = strings.ToUpper(symbol)
	}
	if _, ok := scope.symbols[symbol]; !ok {
		return scope.parent != nil && scope.parent.HasSymbol(symbol)
	}
	return true
}

func (scope *Scope) GetSymbol(symbol string) (Symbol, bool) {
	if len(symbol) > 0 && symbol[0] != '"' {
		symbol = strings.ToUpper(symbol)
	}
	res, ok := scope.symbols[symbol]
	if !ok && scope.parent != nil {
		return scope.parent.GetSymbol(symbol)
	}
	return res, ok
}

// Add all columns in the current scope as well as the table alias if it was given
func (scope *Scope) AddSelection(sel *AstSelection) {
	// scope.AddSymbol(sel.SourceName.String())
	for _, field := range sel.ResolvedTable.ColumnsInOrder {
		// A simple name
		scope.AddSymbol(NewRewrittenName(field.Name, field.Name))
	}

	for _, field := range sel.ResolvedTable.ComputedColumns {
		scope.AddRewrittenSymbol(field.Name, "\""+field.Schema+"\".\""+field.Name+"\"("+sel.Alias+")")
	}
}

func (scope *Scope) AddFunction(fn *pg.Function) {
	if len(fn.Name) > 0 && fn.Name[0] != '"' {
		fn.Name = strings.ToUpper(fn.Name)
	}
	scope.functions[fn.Name] = fn
}

// Get a function by name from the current scope.
func (scope *Scope) GetFunction(symbol string) *pg.Function {
	if len(symbol) > 0 && symbol[0] != '"' {
		symbol = strings.ToUpper(symbol)
	}
	if fn, ok := scope.functions[symbol]; ok {
		return fn
	}
	if scope.parent != nil {
		return scope.parent.GetFunction(symbol)
	}
	return nil
}

func NewRootScope(tables map[string]*pg.DBTable, functions map[string]*pg.Function, schemas []string) *Scope {

	root := NewScope()
	var schemas_set utils.Set[string]
	for _, schema := range schemas {
		schemas_set.Add(schema)
	}

	for _, tbl := range tables {
		if schemas_set.Has(tbl.Schema) {
			root.AddSymbol(NewRewrittenName(tbl.Name, tbl.Schema+"."+tbl.Name))
		}
	}

	// exposing base functions but not those starting with pg_
	for _, fn := range functions {
		if fn.Schema == "pg_catalog" && !strings.HasPrefix(fn.Name, "pg_") {
			// pp.Println("adding function", fn.Name, fn.Schema+"."+fn.Name)
			root.AddSymbol(NewRewrittenName(fn.Name+"(", fn.Schema+"."+fn.Name))
			root.AddSymbol(NewRewrittenName(fn.Name, fn.Schema+"."+fn.Name))
		} else if schemas_set.Has(fn.Schema) {
			root.AddSymbol(NewRewrittenName(fn.Name, fn.Schema+"."+fn.Name))
		}
	}

	for _, schema := range schemas {
		schema_scope := NewScopeSymbol(schema, root)
		root.AddSymbol(schema_scope)

		for _, tbl := range tables {
			if tbl.Schema == schema {
				schema_scope.Scope.AddSymbol(NewRewrittenName(tbl.Name, tbl.Schema+"."+tbl.Name))
			}
		}

		for _, fn := range functions {
			if fn.Schema == schema {
				schema_scope.Scope.AddSymbol(NewRewrittenName(fn.Name+"(", fn.Schema+"."+fn.Name))
				schema_scope.Scope.AddSymbol(NewRewrittenName(fn.Name, fn.Schema+"."+fn.Name))
			}
		}
	}

	// Those are constants not in pg_catalog.
	root.AddSimpleSymbol("current_user")
	root.AddSimpleSymbol("session_user")
	root.AddSimpleSymbol("user")
	root.AddSimpleSymbol("current_timestamp")
	root.AddSimpleSymbol("current_date")
	root.AddSimpleSymbol("current_time")
	root.AddSimpleSymbol("current_catalog")
	root.AddSimpleSymbol("current_schema")
	root.AddSimpleSymbol("current_role")
	root.AddSimpleSymbol("true")
	root.AddSimpleSymbol("false")
	root.AddSimpleSymbol("null")

	return root
}
