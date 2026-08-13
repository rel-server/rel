package query

import (
	"sales-way.com/server/pg"
	"sales-way.com/server/utils"
)

type AstOperationType int

const (
	OP_GET AstOperationType = iota
	OP_UPDATE
	OP_DELETE
	OP_INSERT
	OP_UPSERT
	OP_MERGE
)

type AstOperation struct {
	Verb      AstOperationType
	Operand   IAstExpression // Typically the payload that will be passed to the verb
	Selection *AstSelection
	Rollback  bool // Whether this will be a dry run
}

type AstOperations []*AstOperation

type AstSelection struct {
	Alias          string
	NameExpression IAstExpression

	FunctionCallArguments FunctionCallArguments
	IsOperation           bool // Whether this selection is the top-level one

	ResolvedFunction *pg.Function
	ResolvedTable    *pg.DBTable

	ParentRelation *AstRelationshipField

	ResolvedOutgoingRelationships []ResolvedRelationship // Relationships that are coming from this selection (>>)
	ResolvedIncomingRelationships []ResolvedRelationship // Relationships that are going to this selection (<<) and are generally arrays
	ResolvedSimpleFields          []ResolvedSimpleField  // Simple fields that are coming from this selection

	Fields      []IAstField
	WhereClause IAstExpression

	OrderBy []*AstOrder
	Limit   int
	Offset  int

	SelectionIndex int // The index of this selection in the query
}

type ResolvedRelationship struct {
	Relationship   *AstRelationshipField
	JsonPathPrefix string // Generally empty, unless the field comes in a subfield
}

type ResolvedSimpleField struct {
	Name           string
	JsonPathPrefix string // Generally empty, unless the field comes in a subfield
}

type AstOrder struct {
	Asc        bool
	Expression IAstExpression
}

///// IAstField

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
	Expression IAstExpression
}

type AstRelationshipField struct {
	Alias    string
	Operator string // << or >>

	Name       IAstExpression // Alias, only with dots
	FieldNames []string

	IsReadOnly           bool // Whether we'll try to insert into the relationship
	ParentSelection      *AstSelection
	Selection            *AstSelection
	ResolvedRelationShip *pg.RelationShip
}

type AstRawSql string

///////////////////////

type AstExpressionList []IAstExpression
type FunctionCallArguments []IAstExpression

type AstNamedFunctionArgument struct {
	Alias      string
	Expression IAstExpression
}

type AstArray []IAstExpression

type AstLimitClause struct {
	Limit  int
	Offset int
}

var (
	_ IAstField = (*AstFieldGroup)(nil)
	_ IAstField = (*AstStarSelector)(nil)
	_ IAstField = (*AstSimpleField)(nil)
	_ IAstField = (*AstRelationshipField)(nil)
)

type IAstField interface {
	String() string
	sqlSelect(ctx *SqlgenContext) error
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

/////////////////////// Expression

type IAstExpression interface {
	Sql(ctx *SqlgenContext)
	String() string
	Rewrite(scope *Scope) (IAstExpression, error)
}

type BaseExpression struct{}

type AstFunctionCall struct {
	BaseExpression
	Left      IAstExpression
	Arguments FunctionCallArguments
}

type AstPrefixOp struct {
	BaseExpression
	Op         Token
	Expression IAstExpression
}

type AstSuffixOp struct {
	BaseExpression
	Op         Token
	Expression IAstExpression
}

type AstBinOp struct {
	BaseExpression
	Op    Token
	Left  IAstExpression
	Right IAstExpression
}

func makeBinOp(left IAstExpression, op string, right IAstExpression) *AstBinOp {
	return &AstBinOp{
		Op:    Token{Kind: TK_OPERATOR, Bytes: []byte(op)},
		Left:  left,
		Right: right,
	}
}

type Identifier struct {
	BaseExpression
	Identifier string
}

func (id Identifier) String() string {
	return id.Identifier
}
