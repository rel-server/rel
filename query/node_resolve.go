// Copyright 2025 Christophe Eymard
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Pass 1, resolution step : turns a decoded rawRelation tree (node_parse.go)
// into a fully DB-resolved *QueryNode tree — see specs/query-compiler.md's
// "Pass 1" section. Expression-typed fields are copied through as-is
// (already parsed by node_parse.go via parseNode) ; nothing here resolves
// identifiers inside them, that's pass 2.
package query

import (
	"sort"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/pg"
	"github.com/samber/oops"
)

// ResolveContext carries what resolution needs beyond the raw tree itself.
type ResolveContext struct {
	Db     *pg.DbInfos
	Config *config.Config

	// shapeInProgress marks nodes whose selectShape (expression_resolve.go)
	// is currently being computed — detects a select that hops into its own
	// node's Shape via a self-reference (e.g. select: {"x": [".", "self",
	// "id"]}), which would otherwise recurse : selectShape -> resolveChain
	// (still unresolved) -> Identifier "self" -> *QueryNode landing on this
	// same node -> resolveExternalHop -> selectShape again, forever. Lazily
	// allocated ; nil is a valid, empty map to read from.
	shapeInProgress map[*QueryNode]bool

	// resolvingOwn marks nodes currently resolving their OWN where/select/
	// distinct_on/order_by (set for that block's duration in
	// ResolveExpressions) — resolveExternalHop consults this to refuse a
	// self-alias hop into a node's own Shape from within its own
	// expressions (only an *external* hop, from a parent, may see a target's
	// computed select keys). Distinct from shapeInProgress : that one guards
	// selectShape's own reentrancy (the "select" position specifically,
	// e.g. nested inside a literal) ; this one enforces the broader
	// no-self-shape-visibility rule across where/select/distinct_on/
	// order_by alike, including the case where selectShape has ALREADY
	// finished caching node.Shape by the time a later sibling field (e.g.
	// where, resolved before select) reaches the same hop. Lazily
	// allocated ; nil is a valid, empty map to read from.
	resolvingOwn map[*QueryNode]bool
}

// ResolveQuery resolves one decoded Relation (the root of a bare query or a
// WriteQuery) into a fully DB-resolved *QueryNode tree.
func (ctx *ResolveContext) ResolveQuery(raw *rawRelation) (*QueryNode, error) {
	return ctx.resolveNode(raw, nil, "", 1)
}

var writeModeByString = map[string]WriteMode{
	"merge":        MERGE,
	"readonly":     READONLY,
	"insert":       INSERT,
	"upsert":       UPSERT,
	"merge-new":    MERGE_NEW,
	"merge-update": MERGE_UPDATE,
	"update":       UPDATE,
	"deleteonly":   DELETE_ONLY,
}

// resolveNode resolves one node. parent is nil for the root. outerAlias is
// the key this node sits under in its parent's `join` map (irrelevant, left
// "" for the root). depth counts the root as 1 ; config.Query.MaxDepth
// bounds it (querying.md/query-compiler.md's "not yet wired" maxdepth item).
//
// Order matters here — see specs/query-compiler.md and this session's plan :
// the node's own relation/function must resolve before `on` can be checked
// against it ; cardinality (isToOne) must be known before write_mode can be
// defaulted/validated ; on_conflict/insert_columns/update_columns need
// node.Relation, which may be nil for a function node whose return type
// isn't a known relation.
func (ctx *ResolveContext) resolveNode(raw *rawRelation, parent *QueryNode, outerAlias string, depth int) (*QueryNode, error) {
	name := raw.Relation
	if raw.IsFunction {
		name = raw.Function
	}
	oc := oops.With("relation", name).With("schema", raw.Schema).With("alias", raw.Alias).With("depth", depth)

	if depth > ctx.Config.Pg.Query.MaxDepth {
		return nil, oc.Errorf("query exceeds the configured maximum depth (%d)", ctx.Config.Pg.Query.MaxDepth)
	}

	node := &QueryNode{
		Parent:     parent,
		InnerName:  raw.Alias,
		OuterAlias: outerAlias,
	}

	if raw.IsFunction {
		fn, err := ctx.resolveFunction(raw, oc)
		if err != nil {
			return nil, err
		}
		node.Function = fn
		node.FunctionArguments = raw.ArgumentsPositional
		node.FunctionArgumentMap = raw.ArgumentsNamed
		// A table-valued function is joinable/writable exactly like the type
		// it returns (query.ts's own note) ; nil here just means this
		// particular function's return type isn't a known relation, which
		// only matters once something below tries to join/write through it.
		node.Relation = ctx.Db.GetRelationByType(fn.PgReturnTypeOid)
		if node.Relation == nil {
			// RETURNS TABLE(...) / plain OUT-parameters : prorettype is the
			// one generic, shared pg_catalog.record pseudo-type, which never
			// resolves to a real relation via GetRelationByType above (see
			// pg.Function.RecordRelation's own doc comment) — fn.RecordRelation
			// is this function's OWN column list instead, built once at
			// introspection time from its OUT-mode arguments. Still nil for a
			// function with no OUT arguments at all (a bare scalar return),
			// same as before.
			node.Relation = fn.RecordRelation
		}
	} else {
		rel := ctx.Db.ResolveRelation(raw.Schema, raw.Relation)
		if rel == nil {
			return nil, oc.Errorf("unknown relation")
		}
		if ctx.Config.Blacklist.IsRelationBlacklisted(rel.Identifier.Schema, rel.Identifier.Name) {
			return nil, oc.Errorf("relation %q is blacklisted", rel.Identifier.String())
		}
		node.Relation = rel
	}

	isToOne := false
	if parent != nil {
		if node.Relation == nil {
			return nil, oc.Errorf("cannot join into this node : it has no resolvable relation (the function's return type isn't a known relation)")
		}
		if parent.Relation == nil {
			return nil, oc.Errorf("cannot join : the parent node has no resolvable relation")
		}

		var err error
		_, isToOne, err = node.Relation.ResolveJoin(parent.Relation, raw.On)
		if err != nil {
			return nil, oc.Wrapf(err, "resolving join")
		}

		// Built directly from raw.On against each side's ColumnsMap, not
		// from the *pg.Constraint ResolveJoin returns : that constraint's
		// Target is nil on the non-FK eligibility path, and even when set,
		// "which side" isn't reliably the child's — see query-compiler.md.
		localNames := make([]string, 0, len(raw.On))
		for local := range raw.On {
			localNames = append(localNames, local)
		}
		sort.Strings(localNames) // deterministic order, not Go map iteration order
		node.JoinColumns = make([]QueryJoinColumn, 0, len(localNames))
		for _, local := range localNames {
			parentCol := raw.On[local]
			localCol := node.Relation.ColumnsMap[local]
			if localCol == nil {
				return nil, oc.Errorf("on: unknown local column %q", local)
			}
			distCol := parent.Relation.ColumnsMap[parentCol]
			if distCol == nil {
				return nil, oc.Errorf("on: unknown parent column %q", parentCol)
			}
			node.JoinColumns = append(node.JoinColumns, QueryJoinColumn{Local: localCol, Distant: distCol})
		}

		if isToOne {
			parent.OutgoingNodes = append(parent.OutgoingNodes, node)
		} else {
			parent.IncomingNodes = append(parent.IncomingNodes, node)
		}
	}

	var writeMode WriteMode
	if raw.WriteMode == "" {
		switch {
		case parent == nil:
			writeMode = INSERT
		case isToOne:
			writeMode = UPSERT
		default:
			writeMode = MERGE
		}
	} else {
		wm, ok := writeModeByString[raw.WriteMode]
		if !ok {
			return nil, oc.Errorf("unknown write_mode %q", raw.WriteMode)
		}
		writeMode = wm
	}
	if parent != nil && isToOne {
		switch writeMode {
		case MERGE, MERGE_NEW, MERGE_UPDATE, DELETE_ONLY:
			return nil, oc.Errorf("write_mode %q is not valid on an outgoing (to-one) relation", raw.WriteMode)
		}
	}
	node.WriteMode = writeMode

	if raw.OnConflictConstraintName != "" || len(raw.OnConflictColumns) > 0 {
		if node.Relation == nil {
			return nil, oc.Errorf("on_conflict given but this node has no resolvable relation")
		}
		if raw.OnConflictConstraintName != "" {
			c := node.Relation.FindConstraintByName(raw.OnConflictConstraintName)
			if c == nil {
				return nil, oc.Errorf("unknown constraint %q", raw.OnConflictConstraintName)
			}
			node.OnConflictConstraintName = c.Name
		} else {
			c := node.Relation.FindUniqueConstraint(raw.OnConflictColumns)
			if c == nil {
				return nil, oc.Errorf("no unique/primary-key constraint matches on_conflict columns %v", raw.OnConflictColumns)
			}
			node.OnConflictConstraintName = c.Name
			node.OnConflictColumns = raw.OnConflictColumns
		}
	} else if node.Relation != nil && node.Relation.PrimaryKey != nil {
		// query.ts's documented default : PK when on_conflict is unspecified.
		node.OnConflictConstraintName = node.Relation.PrimaryKey.Name
	}

	for _, col := range raw.InsertColumns {
		if node.Relation == nil || node.Relation.ColumnsMap[col] == nil {
			return nil, oc.Errorf("insert_columns: unknown column %q", col)
		}
	}
	for _, col := range raw.UpdateColumns {
		if node.Relation == nil || node.Relation.ColumnsMap[col] == nil {
			return nil, oc.Errorf("update_columns: unknown column %q", col)
		}
	}
	node.InsertColumns = raw.InsertColumns
	node.UpdateColumns = raw.UpdateColumns

	if raw.Select != nil {
		node.Select = raw.Select
	} else {
		// query.ts's documented default when select is unspecified.
		node.Select = FullExpr{}
	}
	node.Where = raw.Where
	node.Distinct = raw.Distinct
	node.DistinctOn = raw.DistinctOn
	node.OrderBy = raw.OrderBy
	node.Offset = raw.Offset
	node.Limit = raw.Limit

	if raw.Join != nil {
		aliases := make([]string, 0, len(raw.Join))
		for alias := range raw.Join {
			aliases = append(aliases, alias)
		}
		sort.Strings(aliases) // deterministic order, not Go map iteration order
		for _, alias := range aliases {
			if _, err := ctx.resolveNode(raw.Join[alias], node, alias, depth+1); err != nil {
				return nil, err
			}
		}
	}

	return node, nil
}

// resolveFunction picks the single *pg.Function candidate matching raw's
// schema/name and call shape. Only plain functions (prokind 'f') are
// eligible here — aggregates/window functions/procedures are a different
// kind of call, not a relation/function-position root. Named-argument calls
// disambiguate by matching every given name against the candidate's
// IN/INOUT argument names ; positional calls disambiguate by arity via
// AcceptsArity. More than one surviving candidate is an ambiguity error,
// not a guess.
func (ctx *ResolveContext) resolveFunction(raw *rawRelation, oc oops.OopsErrorBuilder) (*pg.Function, error) {
	return resolveFunctionCandidate(ctx.Db, ctx.Config.Blacklist, raw.Schema, raw.Function,
		raw.ArgumentsPositional, raw.ArgumentsNamed, (*pg.Function).IsPlainFunction, oc)
}

// resolveFunctionCandidate picks the single *pg.Function candidate matching
// schema/name and call shape, filtered by kindOK — (*pg.Function).
// IsPlainFunction for a relation/function-position root or a "call"
// expression, (*pg.Function).IsAggregate for an "agg" expression. Shared
// core behind pass 1's resolveFunction (a node's own root) and pass 2's
// AggExpr/CallExpr resolution (a function reference embedded inside an
// expression) — same disambiguation rules either way : named-argument calls
// match every given name against the candidate's IN/INOUT argument names
// (functionAcceptsNames), positional calls match by arity (AcceptsArity).
// More than one surviving candidate is an ambiguity error, not a guess.
func resolveFunctionCandidate(
	db *pg.DbInfos, bl config.Blacklist,
	schema, name string,
	positional []Expression, named map[string]Expression,
	kindOK func(*pg.Function) bool,
	oc oops.OopsErrorBuilder,
) (*pg.Function, error) {
	candidates := db.ResolveFunctionCandidates(schema, name)

	filtered := make([]*pg.Function, 0, len(candidates))
	for _, f := range candidates {
		if kindOK(f) {
			filtered = append(filtered, f)
		}
	}
	if len(filtered) == 0 {
		return nil, oc.Errorf("unknown function")
	}

	var matches []*pg.Function
	if named != nil {
		for _, f := range filtered {
			if functionAcceptsNames(f, named) {
				matches = append(matches, f)
			}
		}
	} else {
		n := len(positional)
		for _, f := range filtered {
			if f.AcceptsArity(n) {
				matches = append(matches, f)
			}
		}
	}

	if len(matches) == 0 {
		return nil, oc.Errorf("no overload of %q matches the given arguments", name)
	}
	if len(matches) > 1 {
		return nil, oc.Errorf("ambiguous function %q : %d overloads match the given arguments", name, len(matches))
	}

	fn := matches[0]
	if bl.IsFunctionBlacklisted(fn.Identifier.Schema, fn.Identifier.Name) {
		return nil, oc.Errorf("function %q is blacklisted", fn.Identifier.String())
	}
	return fn, nil
}

// functionAcceptsNames reports whether a named-argument call matches f :
// every given name must be one of f's input parameter names, AND every
// *required* input parameter (Postgres only allows defaults on the trailing
// PgNargsDefaults input parameters, so the first PgNargs-PgNargsDefaults, in
// declared order, are the required ones) must be present among the given
// names. Checking only the first half (every given name is valid) isn't
// enough : {a: 1} against fn(a int, b int) — both required, no default —
// would wrongly count as a match if b's absence went unchecked.
func functionAcceptsNames(f *pg.Function, named map[string]Expression) bool {
	inputNames := make([]string, 0, len(f.Arguments))
	nameSet := make(map[string]bool, len(f.Arguments))
	for i := range f.Arguments {
		if f.Arguments[i].IsIn() || f.Arguments[i].IsInOut() {
			inputNames = append(inputNames, f.Arguments[i].Name)
			nameSet[f.Arguments[i].Name] = true
		}
	}
	for name := range named {
		if !nameSet[name] {
			return false
		}
	}
	required := max(len(inputNames)-f.PgNargsDefaults, 0)
	for i := range required {
		if _, ok := named[inputNames[i]]; !ok {
			return false
		}
	}
	return true
}
