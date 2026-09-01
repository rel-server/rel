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

package query

import (
	"github.com/ceymard/rel/errcode"
	"github.com/ceymard/rel/pg"
	"github.com/samber/oops"
)

// LookupInScope resolves a first-hop bare name against n's own Scope : n's
// own relation columns, n's own declared alias (InnerName, self-referencing
// n itself), and n's visible children's join aliases (OuterAlias, both
// OutgoingNodes and IncomingNodes) — never a parent's or a sibling's, per
// specs/query-engine.md's "## Scoping ### Self-reference and child scope" section.
//
// A name matching more than one of these is a hard error, not silently
// resolved by precedence (decided this session) : picking one on a
// collision would let a query run and silently return data other than what
// the author meant, with no signal anything was ambiguous. The author
// controls the alias that collided ; renaming it is the fix.
func (n *QueryNode) LookupInScope(name string) (ResolvedField, error) {
	oc := oops.With("name", name).Code(errcode.UnknownIdentifier)
	if n.InnerName != "" {
		oc = oc.With("node", n.InnerName)
	}

	var col ResolvedField
	if n.Relation != nil {
		if c := n.Relation.ColumnsMap[name]; c != nil {
			col = ColumnPath{Node: n, Path: []*pg.Column{c}}
		}
	}

	var alias ResolvedField
	for _, c := range n.OutgoingNodes {
		if c.OuterAlias == name {
			alias = c
			break
		}
	}
	if alias == nil {
		for _, c := range n.IncomingNodes {
			if c.OuterAlias == name {
				alias = c
				break
			}
		}
	}

	self := name == n.InnerName && n.InnerName != ""

	matches := 0
	if col != nil {
		matches++
	}
	if alias != nil {
		matches++
	}
	if self {
		matches++
	}

	switch matches {
	case 0:
		return nil, oc.Errorf("unresolvable identifier %q", name)
	case 1:
		switch {
		case col != nil:
			return col, nil
		case alias != nil:
			return alias, nil
		default:
			return n, nil
		}
	default:
		return nil, oc.Errorf("identifier %q is ambiguous : it matches more than one of a column, a join alias, and this node's own alias", name)
	}
}
