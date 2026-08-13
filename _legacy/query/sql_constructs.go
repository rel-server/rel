package query

import (
	"strconv"

	"gitlab.com/tozd/go/errors"
)

// func (ops AstOperations) SqlSelect(ctx *SqlgenContext) error {

// 	if len(ops) > 1 {
// 		ctx.W("SELECT json_agg(_inner.res) FROM (")
// 	}

// 	for i, op := range ops {
// 		if i > 0 {
// 			ctx.W(
// 				" UNION ALL ",
// 			)
// 		}

// 		ctx.currentOperation = op
// 		if err := op.SqlSelect(ctx); err != nil {
// 			return err
// 		}

// 	}
// 	ctx.currentOperation = nil

// 	if len(ops) > 1 {
// 		ctx.W(") _inner")
// 	}

// 	// ctx.W(") _outer")

// 	return nil
// }

func (op *AstOperation) SqlSelect(ctx *SqlgenContext) error {
	ctx.currentOperation = op

	ctx2 := ctx.NewChildContext(op.Selection.ResolvedTable, op.Selection)
	if err := op.Selection.sqlSelectQuery(ctx2); err != nil {
		return err
	}

	return nil
}

func (rel *AstRelationshipField) sqlSelect(ctx *SqlgenContext) error {

	// ctx2.currentTable = ctx.Tables[rel.Name]
	ctx2 := ctx.NewChildContext(rel.Selection.ResolvedTable, rel.Selection)
	ctx2.IsInSubQuery = true
	rls := rel.ResolvedRelationShip
	if rls == nil {
		return errors.New("no resolved relationship for " + rel.Name.String())
	}

	ctx2.currentRelationship = rls

	// rel.Selection.SourceName

	ctx2.W("(")

	// We will now add operators and ast expressions to have the sub-query follow the foreign key
	for i, local := range rls.ColumnsNames {

		// pp.Println(ctx.currentSelection.Alias, local, ctx2.currentSelection.Alias, rls.DistantColumnsNames[i])
		eq := makeBinOp(AstRawSql("\""+ctx.currentSelection.Alias+"\".\""+local+"\""), "=", AstRawSql("\""+ctx2.currentSelection.Alias+"\".\""+rls.DistantColumnsNames[i]+"\""))

		if rel.Selection.WhereClause == nil {
			rel.Selection.WhereClause = eq
		} else {
			rel.Selection.WhereClause = makeBinOp(rel.Selection.WhereClause, "AND", eq)
		}
	}

	// This is where we look for the actual related table
	if err := rel.Selection.sqlSelectQuery(ctx2); err != nil {
		return err
	}

	ctx.W(")")

	ctx.W(" AS \"", rel.Alias, "\"")
	return nil
}

func (field *AstSimpleField) sqlSelect(ctx *SqlgenContext) error {

	lookup := ""
	if name, ok := field.Expression.(AstRawSql); ok {
		lookup = string(name)
	} else if name, ok := field.Expression.(*Identifier); ok {
		lookup = name.String()
	}

	// This is not what should be done here !
	if computed, ok := ctx.currentTable.ComputedColumnsMap[lookup]; ok {
		ctx.W("\"", computed.Schema, "\".\"", computed.Name, "\"(\"", ctx.currentSelection.Alias, "\") AS ")
	} else if field.Expression != nil {
		field.Expression.Sql(ctx)
		ctx.W(" AS ")
	}

	ctx.W("\"", field.Name, "\"")
	return nil
}

func (group *AstFieldGroup) sqlSelect(ctx *SqlgenContext) error {

	ctx.W("(SELECT Q FROM (SELECT ")
	for i, field := range group.Fields {
		if i > 0 {
			ctx.W(", ")
		}
		if err := field.sqlSelect(ctx); err != nil {
			return err
		}
	}
	ctx.W(") Q) AS \"", group.Name, "\"")
	return nil
}

func (str *AstStarSelector) sqlSelect(ctx *SqlgenContext) error {
	// WRONG

	if ctx.currentTable == nil {
		return errors.New("no current table")
	}

	if str.Name != "" {
		ctx.W("(SELECT Q FROM (SELECT ")
	}

	for _, field := range ctx.currentTable.ColumnsInOrder {
		if str.Exclusions.Has(field.Name) {
			continue
		}
		if ctx.selectedFieldsCount > 0 {
			ctx.W(", ")
		}
		ctx.selectedFieldsCount++
		ctx.W("\"", field.Name, "\"")
	}

	if str.Name != "" {
		ctx.W(") Q) AS \"", str.Name, "\"")
	}

	return nil
}

// addField adds a field to the SELECT clause, and handles the comma separator
func (ctx *SqlgenContext) addField(fn func() error) error {
	if ctx.selectedFieldsCount > 0 {
		ctx.W(", ")
	}
	if err := fn(); err != nil {
		return err
	}
	ctx.selectedFieldsCount++
	return nil
}

/*
*

*
 */
func (sel *AstSelection) sqlSelectQuery(ctx *SqlgenContext) error {

	queried_object := ""
	if sel.ResolvedFunction != nil {
		queried_object = "\"" + sel.ResolvedFunction.Schema + "\".\"" + sel.ResolvedFunction.Name + "\""
	} else {
		queried_object = "\"" + sel.ResolvedTable.Schema + "\".\"" + sel.ResolvedTable.Name + "\""

		op := ctx.currentOperation
		if sel.ParentRelation == nil && (op.Verb != OP_GET) {
			// All other operations use RETURNING * to get the affected rows
			queried_object = sel.TempDMLTableName()
		}
	}

	returns_set := sel.ResolvedFunction != nil && sel.ResolvedFunction.ReturnsSet || (sel.ResolvedFunction == nil && (ctx.currentRelationship == nil || ctx.currentRelationship.IsMultiple))
	if returns_set {
		ctx.W("SELECT coalesce(json_agg(_R), '[]'::json) as res FROM (")
	} else {
		if len(sel.Fields) > 0 {
			ctx.W("SELECT coalesce(row_to_json(_R), 'null'::json) as res FROM (")
		} else {
			ctx.W("SELECT coalesce(row_to_json(_R)->'", sel.Alias, "', 'null'::json) as res FROM (")
		}
	}

	ctx.W("SELECT ")

	if len(sel.Fields) > 0 {
		for _, field := range sel.Fields {
			if err := ctx.addField(func() error {
				if err := field.sqlSelect(ctx); err != nil {
					return err
				}
				return nil
			}); err != nil {
				return err
			}
		}
		ctx.W(" FROM ")
	}
	ctx.W(queried_object, " ")

	if sel.ResolvedFunction != nil {
		sel.FunctionCallArguments.Sql(ctx)
	}

	ctx.W(" \"", sel.Alias, "\"")

	if sel.WhereClause != nil {
		ctx.W(" WHERE (")
		sel.WhereClause.Sql(ctx)
		ctx.W(") ")
	}

	if returns_set && len(sel.OrderBy) > 0 {
		ctx.W(" ORDER BY ")
		for i, order := range sel.OrderBy {
			if i > 0 {
				ctx.W(", ")
			}
			order.Expression.Sql(ctx)
			if !order.Asc {
				ctx.W(" DESC")
			}
		}
	}

	if returns_set && sel.Limit > 0 {
		ctx.W(" LIMIT ", strconv.Itoa(sel.Limit))
	}
	if returns_set && sel.Offset > 0 {
		ctx.W(" OFFSET ", strconv.Itoa(sel.Offset))
	}

	ctx.W(") _R")

	return nil
}
