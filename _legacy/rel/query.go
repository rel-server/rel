package rel

import (
	"sales-way.com/server/pg"
	"sales-way.com/server/query"
)

type WriteMode int

const (
	MODE_READONLY WriteMode = iota
	MODE_INSERT
	MODE_UPSERT
	MODE_MERGE
	MODE_MERGENEW
	MODE_MERGEUPDATE
	MODE_UPDATE
	MODE_DELETEOTHER
)

type Identifier struct {
	Name   string
	Schema string
	Db     string
}

func (id *Identifier) TableKey() string {
	if id.Schema != "" {
		return id.Schema + "." + id.Name
	}
	if id.Db != "" {
		return id.Db + "." + id.Name
	}
	return id.Name
}

type Write struct {
	Relation Identifier
	Mode     WriteMode

	OnConflict []string
	InsertOnly []string
	UpdateOnly []string
}

type Query struct {
	Relation Identifier
	IsRel    bool
	Alias    string

	Write Write

	Arguments []query.IAstExpression

	Select query.IAstExpression
	Where  query.IAstExpression

	Rels map[string]Query
	On   map[string]string

	OnConflict []string
	InsertOnly []string
	UpdateOnly []string

	Offset int
	Limit  int

	Fields               []IAstField
	ResolvedSimpleFields []ResolvedSimpleField

	RelIndex    int
	ParentQuery *Query

	ResolvedTable        *pg.DBTable
	ResolvedFunction     *pg.Function
	ResolvedRelationship *pg.RelationShip

	OutgoingsRels []*Query
	IncomingsRels []*Query
}

type ResolvedSimpleField struct {
	Name           string
	JsonPathPrefix string
}
