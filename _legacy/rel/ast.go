package rel

import (
	"sales-way.com/server/pg"
	"sales-way.com/server/query"
	"sales-way.com/server/utils"
)

type AstFieldGroup struct {
	Name   string
	Fields []IAstField
}

type AstStarSelector struct {
	Name       string
	Exclusions utils.Set[string]
}

type AstSimpleField struct {
	Name       string
	Expression query.IAstExpression
}

type AstRelationshipField struct {
	Alias    string
	RelAlias string

	IsReadOnly           bool
	Query                *Query
	ResolvedRelationShip *pg.RelationShip
}

var (
	_ IAstField = (*AstFieldGroup)(nil)
	_ IAstField = (*AstStarSelector)(nil)
	_ IAstField = (*AstSimpleField)(nil)
	_ IAstField = (*AstRelationshipField)(nil)
)

type IAstField interface {
	Visit(visitor IAstFieldVisitor) error
}

type IAstFieldVisitor interface {
	VisitFieldGroup(field *AstFieldGroup) error
	VisitStarSelector(field *AstStarSelector) error
	VisitSimpleField(field *AstSimpleField) error
	VisitRelationshipField(field *AstRelationshipField) error
}

func (field *AstFieldGroup) Visit(visitor IAstFieldVisitor) error {
	return visitor.VisitFieldGroup(field)
}

func (field *AstStarSelector) Visit(visitor IAstFieldVisitor) error {
	return visitor.VisitStarSelector(field)
}

func (field *AstSimpleField) Visit(visitor IAstFieldVisitor) error {
	return visitor.VisitSimpleField(field)
}

func (field *AstRelationshipField) Visit(visitor IAstFieldVisitor) error {
	return visitor.VisitRelationshipField(field)
}
