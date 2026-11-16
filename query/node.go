package query

import "github.com/rel-server/rel/pg"

type WriteMode int

const (
	READONLY WriteMode = iota
	MERGE
	MERGE_NEW
	MERGE_UPDATE
	INSERT
	UPSERT
	// UPDATE_EXISTING updates the rows whose identity is in the payload, anywhere in the tree.
	UPDATE_EXISTING
	DELETE_ONLY
	// UPDATE applies one patch object to every row matching the root's where.
	UPDATE
	// DELETE removes every row matching the root's where.
	DELETE
)

type QueryJoinColumn struct {
	Local   *pg.Column
	Distant *pg.Column
}

type QueryNode struct {
	Parent *QueryNode

	// If parent is non-nil, how it's being joined to it
	JoinColumns []QueryJoinColumn
	// OuterAlias is the join's key name, usable by the parent (InnerName is usable by this node and below).
	OuterAlias string

	// The "join" children : outgoing when the child's on-columns are unique, incoming when this node's are.
	// Classified by uniqueness, whether or not a foreign key backs the join.
	OutgoingNodes []*QueryNode
	IncomingNodes []*QueryNode

	// Relation is what children/on_conflict/insert_columns/update_columns resolve against. A function node has
	// one too when its return type is a relation, and is then joinable/writable like it ; it is nil only for a
	// function returning a non-relation, which resolution then refuses to join into or write.
	Relation *pg.Relation

	// Function is non-nil iff this node is a function call ; check this, not Relation.
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

	// Shape is derived by pass 2 from the resolved Select ; nil until then.
	Shape *NodeShape
}

func (n *QueryNode) IsFunction() bool { return n.Function != nil }
func (n *QueryNode) IsRelation() bool { return n.Relation != nil }
