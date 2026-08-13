package rel

import (
	"sort"
	"strings"

	"gitlab.com/tozd/go/errors"
	"sales-way.com/server/pg"
	"sales-way.com/server/query"
)

func (ops ServerOperations) Resolve(ctx *ResolveContext) error {
	for _, op := range ops {
		if err := op.Query.Resolve(ctx, nil); err != nil {
			return err
		}
	}
	return nil
}

func (q *Query) Resolve(ctx *ResolveContext, parent *Query) error {
	q.ParentQuery = parent

	if err := q.resolveRelation(ctx); err != nil {
		return err
	}

	childCtx := ctx.Child()
	if err := q.populateScope(childCtx); err != nil {
		return err
	}

	for alias, rel := range q.Rels {
		rel.ParentQuery = q
		rel.Alias = alias
		if err := rel.resolveAsChild(childCtx, q, alias); err != nil {
			return err
		}
		q.Rels[alias] = rel
	}

	if err := q.resolveSelect(childCtx); err != nil {
		return err
	}

	if q.Where != nil {
		rewritten, err := q.Where.Rewrite(childCtx.scope)
		if err != nil {
			return err
		}
		q.Where = rewritten
	}

	if q.Arguments != nil {
		for i, arg := range q.Arguments {
			rewritten, err := arg.Rewrite(childCtx.scope)
			if err != nil {
				return err
			}
			q.Arguments[i] = rewritten
		}
	}

	return nil
}

func (q *Query) resolveRelation(ctx *ResolveContext) error {
	tableKey := q.Relation.TableKey()

	if q.Arguments != nil {
		if fn, ok := ctx.Functions[tableKey]; ok {
			q.ResolvedFunction = fn
			if fn.ReturnsComposite {
				if table, ok := ctx.Tables[fn.Schema+"."+fn.ReturnType]; ok {
					q.ResolvedTable = table
				}
			}
		} else {
			return errors.New("function " + tableKey + " not found")
		}
	} else {
		if table, ok := ctx.Tables[tableKey]; ok {
			q.ResolvedTable = table
		} else {
			return errors.New("table " + tableKey + " not found")
		}
	}

	if q.ResolvedTable == nil && q.ResolvedFunction == nil {
		return errors.New("table or function not found for " + tableKey)
	}

	return nil
}

func (q *Query) populateScope(ctx *ResolveContext) error {
	if q.ResolvedTable == nil {
		return nil
	}

	for _, field := range q.ResolvedTable.ComputedColumns {
		ctx.scope.AddSymbol(query.NewRewrittenName(
			field.Name,
			field.Schema+"."+field.Name+"(ROW(\""+q.alias()+"\".*)::"+q.ResolvedTable.DbTableName()+")",
		))
	}

	for _, field := range q.ResolvedTable.Columns {
		ctx.scope.AddSimpleSymbol(field.Name)
	}

	for relAlias := range q.Rels {
		ctx.scope.AddSimpleSymbol(relAlias)
	}

	return nil
}

func (q *Query) alias() string {
	if q.Alias != "" {
		return q.Alias
	}
	if q.ResolvedTable != nil {
		return q.ResolvedTable.Name
	}
	return "q"
}

func (rel *Query) resolveAsChild(ctx *ResolveContext, parent *Query, alias string) error {
	if !rel.IsRel {
		return errors.New("expected rel query")
	}
	if parent.ResolvedTable == nil {
		return errors.New("parent query has no resolved table")
	}

	tableKey := rel.Relation.TableKey()
	if table, ok := ctx.Tables[tableKey]; ok {
		rel.ResolvedTable = table
	} else {
		return errors.New("table " + tableKey + " not found for rel " + alias)
	}

	relationship, err := findRelationship(parent.ResolvedTable, rel.ResolvedTable, rel.On)
	if err != nil {
		return errors.WithMessage(err, "in rel "+alias)
	}
	rel.ResolvedRelationship = relationship

	if err := rel.Resolve(ctx, parent); err != nil {
		return err
	}

	if !relationship.IsReverse {
		parent.OutgoingsRels = append(parent.OutgoingsRels, rel)
	} else {
		parent.IncomingsRels = append(parent.IncomingsRels, rel)
	}

	return nil
}

func findRelationship(parent *pg.DBTable, child *pg.DBTable, on map[string]string) (*pg.RelationShip, error) {
	if len(on) == 0 {
		return nil, errors.New("on is required for rel")
	}

	var matches []*pg.RelationShip
	for _, rel := range parent.Relationships {
		if rel.DistantRelation.DbTableName() != child.DbTableName() {
			continue
		}
		if relationshipMatchesOn(rel, on) {
			matches = append(matches, rel)
		}
	}

	switch len(matches) {
	case 0:
		return nil, errors.New("no relationship matches on for " + child.DbTableName())
	case 1:
		return matches[0], nil
	default:
		return nil, errors.New("ambiguous relationship for " + child.DbTableName() + ", specify all columns in on")
	}
}

func relationshipMatchesOn(rel *pg.RelationShip, on map[string]string) bool {
	if len(on) != len(rel.ColumnsNames) {
		return false
	}
	for i, localCol := range rel.ColumnsNames {
		distantCol, ok := on[localCol]
		if !ok || distantCol != rel.DistantColumnsNames[i] {
			return false
		}
	}
	return true
}

func (q *Query) resolveSelect(ctx *ResolveContext) error {
	if q.ResolvedTable == nil {
		if len(q.Fields) > 0 {
			return errors.New("function does not return a composite type")
		}
		return nil
	}

	if len(q.Fields) == 0 {
		q.Fields = []IAstField{&AstStarSelector{}}
		for alias := range q.Rels {
			q.Fields = append(q.Fields, &AstRelationshipField{
				Alias:    alias,
				RelAlias: alias,
			})
		}
		sort.Slice(q.Fields, func(i, j int) bool {
			_, iIsRel := q.Fields[i].(*AstRelationshipField)
			_, jIsRel := q.Fields[j].(*AstRelationshipField)
			return !iIsRel && jIsRel
		})
	}

	for i, field := range q.Fields {
		resolved, err := q.resolveField(ctx, field, "")
		if err != nil {
			return err
		}
		if resolved != nil {
			q.Fields[i] = resolved
		}
	}

	return nil
}

func (q *Query) resolveField(ctx *ResolveContext, field IAstField, jsonPath string) (IAstField, error) {
	switch f := field.(type) {
	case *AstRelationshipField:
		rel, ok := q.Rels[f.RelAlias]
		if !ok {
			return nil, errors.New("unknown rel alias " + f.RelAlias)
		}
		f.Query = &rel
		f.ResolvedRelationShip = rel.ResolvedRelationship
		f.IsReadOnly = rel.IsReadOnly()

	case *AstFieldGroup:
		newPrefix := jsonPath + "->'" + f.Name + "'"
		for i, child := range f.Fields {
			resolved, err := q.resolveField(ctx, child, newPrefix)
			if err != nil {
				return nil, err
			}
			if resolved != nil {
				f.Fields[i] = resolved
			}
		}

	case *AstStarSelector:
		newPrefix := jsonPath
		if f.Name != "" {
			newPrefix = newPrefix + "->'" + f.Name + "'"
		}
		for _, col := range q.ResolvedTable.ColumnsInOrder {
			if f.Exclusions.Has(col.Name) {
				continue
			}
			q.ResolvedSimpleFields = append(q.ResolvedSimpleFields, ResolvedSimpleField{
				Name:           col.Name,
				JsonPathPrefix: newPrefix,
			})
		}

	case *AstSimpleField:
		if colname, ok := f.Expression.(*query.Identifier); ok {
			if rel, exists := q.Rels[colname.Identifier]; exists {
				relField := &AstRelationshipField{
					Alias:                f.Name,
					RelAlias:             colname.Identifier,
					Query:                &rel,
					ResolvedRelationShip: rel.ResolvedRelationship,
					IsReadOnly:           rel.IsReadOnly(),
				}
				return relField, nil
			}
		}

		rewritten, err := f.Expression.Rewrite(ctx.scope)
		if err != nil {
			return nil, err
		}
		f.Expression = rewritten

		if colname, ok := f.Expression.(*query.Identifier); ok {
			if col, ok := q.ResolvedTable.Columns[colname.Identifier]; ok {
				q.ResolvedSimpleFields = append(q.ResolvedSimpleFields, ResolvedSimpleField{
					Name:           col.Name,
					JsonPathPrefix: jsonPath,
				})
			}
		} else if colname, ok := f.Expression.(query.AstRawSql); ok {
			q.ResolvedSimpleFields = append(q.ResolvedSimpleFields, ResolvedSimpleField{
				Name:           colname.String(),
				JsonPathPrefix: jsonPath,
			})
		} else if call, ok := f.Expression.(*query.AstFunctionCall); ok {
			if col := columnFromColOp(call); col != "" {
				q.ResolvedSimpleFields = append(q.ResolvedSimpleFields, ResolvedSimpleField{
					Name:           col,
					JsonPathPrefix: jsonPath,
				})
			}
		}
	}

	return nil, nil
}

func columnFromColOp(call *query.AstFunctionCall) string {
	if left, ok := call.Left.(*query.Identifier); ok {
		switch strings.ToLower(left.Identifier) {
		case "col", "get", "set":
			if len(call.Arguments) > 0 {
				if col, ok := call.Arguments[0].(*query.Identifier); ok {
					return col.Identifier
				}
			}
		}
	}
	return ""
}
