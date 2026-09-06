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
	// the key name of the join ; accessible from the parent. Named OuterAlias
	// (not just "alias") to match specs/query-engine.md's "## Scoping ###
	// Self-reference and child scope" section,
	// which distinguishes this (usable by the parent) from InnerName
	// (usable by this node and its own descendants).
	OuterAlias string

	// Populated from "join" in the JSON, then classified per child via
	// pg.Relation.ResolveJoin's isToOne result — outgoing when the child's own
	// on-columns are unique (this node's on-columns point at a unique set),
	// incoming when this node's own on-columns are the unique side (the
	// child's on-columns, which must be indexed, point at them). This is
	// cardinality-based, not FK-based : a join is classified this way whether
	// or not a real foreign key backs it. See query-engine.md ### Definitions.
	OutgoingNodes []*QueryNode
	IncomingNodes []*QueryNode

	// Relation is what this node's children/on_conflict/insert_columns/
	// update_columns resolve against. Function != nil is what decides
	// whether this node IS a function call (arguments was present in the
	// JSON) — Relation is populated for a function node too whenever its
	// return type maps to a known relation (via pg.DbInfos.GetRelationByType),
	// since a table-valued function is joinable/writable exactly like the
	// type it returns (query.ts's own note). Relation is nil only for a
	// function node whose return type isn't a relation at all (e.g. a
	// scalar-returning function used as a query root, which then can't be
	// joined into or have on_conflict/insert_columns/update_columns —
	// resolution rejects those as a hard error rather than nil-panicking).
	Relation *pg.Relation

	// Function is non-nil iff this node is a function call — check this,
	// not Relation, to tell a function node from a plain relation node.
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

	// Shape is pass 2's shape/writability derivation output for this node,
	// computed from the already-resolved Select — see DeriveShapes
	// (shape.go). nil until pass 2's second step has run.
	Shape *NodeShape
}

func (n *QueryNode) IsFunction() bool { return n.Function != nil }
func (n *QueryNode) IsRelation() bool { return n.Relation != nil }
