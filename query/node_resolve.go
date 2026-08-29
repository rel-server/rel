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
	oc := oops.With("relation", raw.Relation).With("schema", raw.Schema).With("alias", raw.Alias).With("depth", depth)

	if depth > ctx.Config.Query.MaxDepth {
		return nil, oc.Errorf("query exceeds the configured maximum depth (%d)", ctx.Config.Query.MaxDepth)
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
	candidates := ctx.Db.ResolveFunctionCandidates(raw.Schema, raw.Relation)

	plain := make([]*pg.Function, 0, len(candidates))
	for _, f := range candidates {
		if f.IsPlainFunction() {
			plain = append(plain, f)
		}
	}
	if len(plain) == 0 {
		return nil, oc.Errorf("unknown function")
	}

	var matches []*pg.Function
	if raw.ArgumentsNamed != nil {
		for _, f := range plain {
			if functionAcceptsNames(f, raw.ArgumentsNamed) {
				matches = append(matches, f)
			}
		}
	} else {
		n := len(raw.ArgumentsPositional)
		for _, f := range plain {
			if f.AcceptsArity(n) {
				matches = append(matches, f)
			}
		}
	}

	if len(matches) == 0 {
		return nil, oc.Errorf("no overload of %q matches the given arguments", raw.Relation)
	}
	if len(matches) > 1 {
		return nil, oc.Errorf("ambiguous function %q : %d overloads match the given arguments", raw.Relation, len(matches))
	}

	fn := matches[0]
	if ctx.Config.Blacklist.IsFunctionBlacklisted(fn.Identifier.Schema, fn.Identifier.Name) {
		return nil, oc.Errorf("function %q is blacklisted", fn.Identifier.String())
	}
	return fn, nil
}

func functionAcceptsNames(f *pg.Function, named map[string]Expression) bool {
	argNames := make(map[string]bool, len(f.Arguments))
	for i := range f.Arguments {
		if f.Arguments[i].IsIn() || f.Arguments[i].IsInOut() {
			argNames[f.Arguments[i].Name] = true
		}
	}
	for name := range named {
		if !argNames[name] {
			return false
		}
	}
	return true
}
