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
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/pg"
	"github.com/samber/oops"
)

// LookupInScope resolves a first-hop name against n's Scope : its relation's columns and computed fields, its own
// InnerName (n itself) and its children's join aliases, never a parent's or sibling's. A name matching more than
// one is an ambiguity error, not resolved by precedence, so a query never silently reads the wrong thing.
// Introspection already keeps computed fields from shadowing columns.
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

	var computed ResolvedField
	if n.Relation != nil {
		if fn := n.Relation.ComputedFields[name]; fn != nil {
			computed = ComputedFieldRef{Node: n, Function: fn}
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
	if computed != nil {
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
		case computed != nil:
			return computed, nil
		case alias != nil:
			return alias, nil
		default:
			return n, nil
		}
	default:
		return nil, oc.Errorf("identifier %q is ambiguous : it matches more than one of a column, a computed field, a join alias, and this node's own alias", name)
	}
}
