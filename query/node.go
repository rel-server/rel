package query

import "github.com/ceymard/rel/pg"

type WriteMode int

const (
	READONLY WriteMode = iota
	MERGE
	MERGE_NEW
	MERGE_UPDATE
	INSERT
	UPSERT
	UPDATE
	DELETE_ONLY
)

type QueryJoinColumn struct {
	Local   *pg.Column
	Distant *pg.Column
}

type QueryNode struct {
	Parent *QueryNode

	// If parent is non-nil, how it's being joined to it
	JoinColumns []QueryJoinColumn
	// the key name of the join ; accessible from the parent
	JoinAlias string

	// Populated from "join" in the JSON, then classified per child via
	// pg.Relation.ResolveJoin's isToOne result — outgoing when the child's own
	// on-columns are unique (this node's on-columns point at a unique set),
	// incoming when this node's own on-columns are the unique side (the
	// child's on-columns, which must be indexed, point at them). This is
	// cardinality-based, not FK-based : a join is classified this way whether
	// or not a real foreign key backs it. See querying.md ### Definitions.
	OutgoingNodes []*QueryNode
	IncomingNodes []*QueryNode

	// Either a Relation or a Function, check for nil
	Relation *pg.Relation

	// Table-Valued Function as a Relation root can be used in joins ; their return type indicates what we can join with
	Function *pg.Function

	// One or the other
	FunctionArguments   []Expression
	FunctionArgumentMap map[string]Expression

	// `alias` in query.ts ; accessible in itself and its children
	InnerName string

	Where Expression

	WriteMode WriteMode

	// One or the other, check for emptiness
	OnConflictConstraintName string
	OnConflictColumns        []string

	InsertColumns []string
	UpdateColumns []string

	Select Expression

	Distinct   bool
	DistinctOn []Expression

	OrderBy []OrderByTerm

	// nil indicates absence
	Offset *int
	Limit  *int
}

func (n *QueryNode) IsFunction() bool { return n.Function != nil }
func (n *QueryNode) IsRelation() bool { return n.Relation != nil }
