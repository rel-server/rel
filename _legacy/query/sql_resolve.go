/**
  For all our Ast components, visit them and resolve the symbols they use.
*/

package query

import (
	"fmt"
	"reflect"
	"strings"

	"gitlab.com/tozd/go/errors"

	"sales-way.com/server/pg"
)

type ResolveContext struct {
	Tables    map[string]*pg.DBTable
	Functions map[string]*pg.Function

	scope *Scope
}

func (ctx *ResolveContext) Child() *ResolveContext {
	return &ResolveContext{
		Tables:    ctx.Tables,
		Functions: ctx.Functions,
		scope:     ctx.scope.Child(),
	}
}

func NewResolveContext(tables map[string]*pg.DBTable, functions map[string]*pg.Function, root *Scope) *ResolveContext {

	return &ResolveContext{
		Tables:    tables,
		Functions: functions,
		scope:     root,
	}
}

func (ctx *ResolveContext) AddBody() {
	ctx.scope.AddRewrittenSymbol("$body", "($1::jsonb)")
}

func (ops AstOperations) Resolve(ctx *ResolveContext) error {
	for _, op := range ops {
		if err := op.Resolve(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (op *AstOperation) Resolve(ctx *ResolveContext) error {

	if op.Operand != nil {
		if rewritten, err := op.Operand.Rewrite(ctx.scope); err != nil {
			return err
		} else {
			op.Operand = rewritten
		}
	}

	if op.Selection != nil {
		if err := op.Selection.Resolve(ctx); err != nil {
			return err
		}
	}

	return nil
}

func (sel *AstSelection) Resolve(ctx *ResolveContext) error {

	if sel.FunctionCallArguments != nil {
		function_name, err := ctx.scope.ResolveExpression(sel.NameExpression)
		if err != nil {
			return err
		}

		// This is a function call.
		if fn, ok := ctx.Functions[function_name.Repr()]; ok {
			sel.ResolvedFunction = fn
		} else {
			return errors.New("function '" + function_name.Name() + "' not found")
		}
	}

	table_name := ""
	if sel.ResolvedFunction != nil {
		if sel.ResolvedFunction.ReturnsComposite {
			table_name = sel.ResolvedFunction.Schema + "." + sel.ResolvedFunction.ReturnType
		}
	} else if sel.ResolvedTable == nil {
		resolved_table_name, err := ctx.scope.ResolveExpression(sel.NameExpression)
		if err != nil {
			return err
		}
		table_name = resolved_table_name.Repr()
	}

	if table_name != "" && sel.ResolvedTable == nil {
		if table, ok := ctx.Tables[table_name]; ok {
			sel.ResolvedTable = table
		} else {
			return errors.New("table '" + table_name + "' not found")
		}
	}

	child_ctx := ctx.Child()

	if sel.ResolvedTable != nil {

		for _, field := range sel.ResolvedTable.ComputedColumns {
			child_ctx.scope.AddSymbol(NewRewrittenName(field.Name, field.Schema+"."+field.Name+"(ROW(\""+sel.Alias+"\".*)::"+sel.ResolvedTable.DbTableName()+")"))
		}

		// ctx.root.AddSymbol(sel.ResolvedTable.Name)
		for _, field := range sel.ResolvedTable.Columns {
			child_ctx.scope.AddSimpleSymbol(field.Name)
		}
	}

	// If we have a table and a function that returns a set and no selected fields, add a star selector.
	if sel.ResolvedTable != nil &&
		(sel.ResolvedFunction == nil || sel.ResolvedFunction.ReturnsSet) &&
		len(sel.Fields) == 0 {
		sel.Fields = append(sel.Fields, &AstStarSelector{})
	}

	if sel.ResolvedTable == nil && sel.ResolvedFunction == nil {
		return errors.New("table or function not found")
	}

	if sel.ResolvedTable != nil {
		// Resolve the relationships.
		for _, field := range sel.Fields {
			if err := sel.resolveField(child_ctx, field, ""); err != nil {
				return err
			}
		}
	} else {
		if len(sel.Fields) > 0 {
			return errors.New("function '" + sel.ResolvedFunction.Name + "' does not return a composite type")
		}
	}

	if sel.WhereClause != nil {
		if rewritten_expr, err := sel.WhereClause.Rewrite(child_ctx.scope); err != nil {
			return err
		} else {
			sel.WhereClause = rewritten_expr
		}
	}

	// Is this where we add fields

	return nil
}

func (sel *AstSelection) resolveField(ctx *ResolveContext, field IAstField, json_path string) error {
	if relationship_field, ok := field.(*AstRelationshipField); ok {

		// Necessary to know whether it's a top level selection
		relationship_field.ParentSelection = sel
		relationship_field.Selection.ParentRelation = relationship_field

		relationship_name, err := ctx.scope.ResolveExpression(relationship_field.Name)
		if err != nil {
			return err
		}

		rel_name := relationship_field.Operator + relationship_name.Repr()
		if relationship_field.FieldNames != nil {
			rel_name = rel_name + "(" + strings.Join(relationship_field.FieldNames, ",") + ")"
		}

		rsp, ok := sel.ResolvedTable.RelationshipsMap[rel_name]
		if !ok {
			return errors.Errorf("relationship %s not found", rel_name)
		} else if rsp == nil {
			return errors.Errorf("relationship %s is ambiguous, specify the columns or the constraint name", rel_name)
		}

		relationship_field.Selection.Alias = relationship_field.Alias

		relationship_field.ResolvedRelationShip = rsp
		relationship_field.Selection.ResolvedTable = rsp.DistantRelation
		if err := relationship_field.Selection.Resolve(ctx); err != nil {
			return err
		}

		if !relationship_field.IsReadOnly {
			if !rsp.IsReverse {
				sel.ResolvedOutgoingRelationships = append(sel.ResolvedOutgoingRelationships, ResolvedRelationship{
					Relationship:   relationship_field,
					JsonPathPrefix: json_path,
				})
			} else {
				sel.ResolvedIncomingRelationships = append(sel.ResolvedIncomingRelationships, ResolvedRelationship{
					Relationship:   relationship_field,
					JsonPathPrefix: json_path,
				})
			}
		}
	}

	if group, ok := field.(*AstFieldGroup); ok {
		new_prefix := json_path + "->'" + group.Name + "'"
		for _, field := range group.Fields {
			if err := sel.resolveField(ctx, field, new_prefix); err != nil {
				return err
			}
		}
	}

	if star, ok := field.(*AstStarSelector); ok {
		new_prefix := json_path
		if star.Name != "" {
			new_prefix = new_prefix + "->'" + star.Name + "'"
		}
		for _, col := range sel.ResolvedTable.ColumnsInOrder {
			if star.Exclusions.Has(col.Name) {
				continue
			}
			sel.ResolvedSimpleFields = append(sel.ResolvedSimpleFields, ResolvedSimpleField{
				Name:           col.Name,
				JsonPathPrefix: new_prefix,
			})
		}
	}

	if simple_field, ok := field.(*AstSimpleField); ok {
		rewritten_expr, err := simple_field.Expression.Rewrite(ctx.scope)
		if err != nil {
			return err
		}
		simple_field.Expression = rewritten_expr

		if colname, ok := simple_field.Expression.(*Identifier); ok {
			if col, ok := sel.ResolvedTable.Columns[colname.Identifier]; ok {
				sel.ResolvedSimpleFields = append(sel.ResolvedSimpleFields, ResolvedSimpleField{
					Name:           col.Name,
					JsonPathPrefix: json_path,
				})
			}
		} else if colname, ok := simple_field.Expression.(AstRawSql); ok {
			sel.ResolvedSimpleFields = append(sel.ResolvedSimpleFields, ResolvedSimpleField{
				Name:           colname.String(),
				JsonPathPrefix: json_path,
			})
		} else {
			printType(reflect.ValueOf(simple_field), "")
			return errors.New("cannot resolve expression " + simple_field.Expression.String())
		}

	}

	return nil
}

func (raw_sql AstRawSql) Rewrite(scope *Scope) (IAstExpression, error) {
	return raw_sql, nil
}

func (ident *Identifier) Rewrite(scope *Scope) (IAstExpression, error) {

	if sym, ok := scope.GetSymbol(ident.String()); ok {
		return AstRawSql(sym.Repr()), nil
	} else {
		return nil, errors.New("cannot resolve expression " + ident.String())
	}
}

func (binop *AstBinOp) Rewrite(scope *Scope) (IAstExpression, error) {
	if binop.Op.String() == "." {
		// resolve the expression
		if sym, err := scope.ResolveExpression(binop); err == nil {
			return AstRawSql(sym.Repr()), nil
		} else {
			return nil, err
		}
	}

	if left, err := binop.Left.Rewrite(scope); err == nil {
		binop.Left = left
	} else {
		return nil, err
	}

	if right, err := binop.Right.Rewrite(scope); err == nil {
		binop.Right = right
	} else {
		return nil, err
	}

	return binop, nil
}

func (arr AstArray) Rewrite(scope *Scope) (IAstExpression, error) {
	for i, element := range arr {
		if rewritten, err := element.Rewrite(scope); err == nil {
			arr[i] = rewritten
		}
	}
	return arr, nil
}

func (name *AstNamedFunctionArgument) Rewrite(scope *Scope) (IAstExpression, error) {
	if rewritten, err := name.Expression.Rewrite(scope); err == nil {
		name.Expression = rewritten
	}
	return name, nil
}

func (call *AstFunctionCall) Rewrite(scope *Scope) (IAstExpression, error) {
	// Maybe should check with fn( syntax ?
	// Anyways right now tables will shadow functions.
	if rewritten, err := call.Left.Rewrite(scope); err == nil {
		call.Left = rewritten
	}

	for i, arg := range call.Arguments {
		if rewritten, err := arg.Rewrite(scope); err == nil {
			call.Arguments[i] = rewritten
		}
	}

	return call, nil
}

func (list AstExpressionList) Rewrite(scope *Scope) (IAstExpression, error) {
	for i, expr := range list {
		if rewritten, err := expr.Rewrite(scope); err == nil {
			list[i] = rewritten
		}
	}
	return list, nil
}

func (prefix *AstPrefixOp) Rewrite(scope *Scope) (IAstExpression, error) {
	if rewritten, err := prefix.Expression.Rewrite(scope); err == nil {
		prefix.Expression = rewritten
	}
	return prefix, nil
}

func printType(v reflect.Value, indent string) {
	if !v.IsValid() {
		fmt.Println(indent + "nil")
		return
	}

	// Unwrap pointers/interfaces
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			fmt.Println(indent + "nil")
			return
		}
		v = v.Elem()
	}

	t := v.Type()

	if t.Kind() == reflect.Struct {
		name := t.Name()
		if name == "" {
			name = "anonymous struct"
		}
		fmt.Println(indent+"Struct:", name)
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			fmt.Printf("%s  Field: %s (%s)\n", indent, field.Name, field.Type)
			printType(v.Field(i), indent+"    ")
		}
	} else if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		fmt.Println(indent+"Slice/Array:", t)
		for i := 0; i < v.Len(); i++ {
			printType(v.Index(i), indent+"  ")
		}
	} else if t.Kind() == reflect.Map {
		fmt.Println(indent+"Map:", t)
		for _, key := range v.MapKeys() {
			fmt.Printf("%s  Key: %v\n", indent, key)
			printType(v.MapIndex(key), indent+"    ")
		}
	} else {
		fmt.Printf("%s%s = %v\n", indent, t, v)
	}
}
