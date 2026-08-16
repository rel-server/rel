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

package pg

import (
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/samber/oops"
)

// Raw constraint row as returned by introspection, before resolution into
// Constraint objects with real *Column/*Relation pointers. Transient : never
// stored on Relation, only threaded through FillConstraintInformations.
type dbConstraint struct {
	Name  string
	Type  string // pg_constraint.contype : "p", "u", "f"
	RelId int

	// True declared order (pg_constraint.conkey), NOT alphabetical — required to
	// pair correctly with TargetColumns for composite foreign keys. Never sort
	// this independently of TargetColumns ; use sortedColumnKey for lookup keys
	// instead, computed once each side is resolved.
	Columns []string

	TargetRelId   int      // 0 unless Type == "f"
	TargetColumns []string // true declared order (pg_constraint.confkey), positionally paired with Columns
}

type ConstraintType int

const (
	ConstraintTypePrimaryKey ConstraintType = iota
	ConstraintTypeUnique
	ConstraintTypeOutgoingForeignKey
	ConstraintTypeIncomingForeignKey
)

func (c ConstraintType) String() string {
	switch c {
	case ConstraintTypePrimaryKey:
		return "PRIMARY KEY"
	case ConstraintTypeUnique:
		return "UNIQUE"
	case ConstraintTypeOutgoingForeignKey:
		return "FOREIGN KEY"
	case ConstraintTypeIncomingForeignKey:
		return "INCOMING FOREIGN KEY"
	default:
		return "UNKNOWN"
	}
}

func (c ConstraintType) IsOutgoingForeignKey() bool { return c == ConstraintTypeOutgoingForeignKey }
func (c ConstraintType) IsIncomingForeignKey() bool { return c == ConstraintTypeIncomingForeignKey }
func (c ConstraintType) IsPrimaryKey() bool         { return c == ConstraintTypePrimaryKey }
func (c ConstraintType) IsUnique() bool             { return c == ConstraintTypeUnique }

// A resolved constraint : a PRIMARY KEY/UNIQUE, or one side of a foreign key.
// For a foreign key, Target is the reciprocal Constraint living on the other
// relation — either side reaches the other via .Target, and .Target.Relation
// is always "the other relation", regardless of direction.
type Constraint struct {
	Relation *Relation
	Name     string
	Type     ConstraintType

	// True declared order, positionally paired with Target.Columns for a
	// foreign key (Columns[i] references Target.Columns[i]).
	Columns []*Column

	Target *Constraint // nil unless Type is one of the foreign key types
}

// OtherRelation returns the relation on the other side of a foreign key.
func (c *Constraint) OtherRelation() *Relation {
	if c.Target == nil {
		return nil
	}
	return c.Target.Relation
}

// sortedColumnKey canonicalizes a column set into a map key : sorted, since
// map lookups only ever need to answer a set-membership question (do these
// exact columns, in any order, back a constraint), never a positional one.
// The one place order matters — pairing a foreign key's two sides together —
// is handled separately, from the true declared order, never from this key.
func sortedColumnKey(columns []string) string {
	sorted := append([]string(nil), columns...)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

func resolveColumns(relation *Relation, names []string) []*Column {
	cols := make([]*Column, len(names))
	for i, name := range names {
		cols[i] = relation.ColumnsMap[name]
	}
	return cols
}

// ---- introspection ----------------------------------------------------------

func FillConstraintInformations(infos *DbInfos, conn *pgx.Conn) error {
	var raw []dbConstraint
	if err := scanIntoThroughJsonAgg(conn, INFO_QUERY_CONSTRAINTS, &raw); err != nil {
		return err
	}

	for _, dbc := range raw {
		relation := infos.GetRelation(dbc.RelId)
		if relation == nil {
			return oops.With("constraint", dbc.Name).With("relId", dbc.RelId).Errorf("constraint references unknown relation (this should not happen)")
		}

		c := &Constraint{
			Relation: relation,
			Name:     dbc.Name,
			Columns:  resolveColumns(relation, dbc.Columns),
		}
		relation.byName[c.Name] = c

		switch dbc.Type {
		case "p":
			c.Type = ConstraintTypePrimaryKey
			relation.PrimaryKey = c
			relation.uniqueColumnGroups[sortedColumnKey(dbc.Columns)] = c

		case "u":
			c.Type = ConstraintTypeUnique
			relation.uniqueColumnGroups[sortedColumnKey(dbc.Columns)] = c

		case "f":
			c.Type = ConstraintTypeOutgoingForeignKey
			relation.OutgoingForeignKeys = append(relation.OutgoingForeignKeys, c)

			target := infos.GetRelation(dbc.TargetRelId)
			if target == nil {
				return oops.With("constraint", dbc.Name).With("targetRelId", dbc.TargetRelId).Errorf("foreign key references unknown target relation (this should not happen)")
			}

			incoming := &Constraint{
				Relation: target,
				Name:     dbc.Name,
				Type:     ConstraintTypeIncomingForeignKey,
				Columns:  resolveColumns(target, dbc.TargetColumns),
				Target:   c,
			}
			c.Target = incoming

			target.IncomingForeignKeys = append(target.IncomingForeignKeys, incoming)

			relation.byOtherRelation[target.PgRelId] = append(relation.byOtherRelation[target.PgRelId], c)
			target.byOtherRelation[relation.PgRelId] = append(target.byOtherRelation[relation.PgRelId], incoming)

		default:
			return oops.With("constraint", dbc.Name).With("type", dbc.Type).Errorf("unexpected constraint type (this should not happen)")
		}
	}

	return nil
}

var INFO_QUERY_CONSTRAINTS = /* sql */ `
SELECT json_agg(C) FROM (SELECT
	c.contype::text AS "Type",
	c.conname AS "Name",
	c.conrelid::integer AS "RelId",
	(
		SELECT array_agg(a.attname ORDER BY k.ord)
		FROM unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
	) AS "Columns",
	c.confrelid::integer AS "TargetRelId",
	(
		SELECT array_agg(a.attname ORDER BY k.ord)
		FROM unnest(c.confkey) WITH ORDINALITY AS k(attnum, ord)
		JOIN pg_attribute a ON a.attrelid = c.confrelid AND a.attnum = k.attnum
	) AS "TargetColumns"
FROM pg_constraint c
JOIN pg_class cl ON cl.oid = c.conrelid
JOIN pg_namespace n ON n.oid = cl.relnamespace
WHERE c.contype IN ('p', 'u', 'f')
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
) C;`

// ---- lookup API ---------------------------------------------------------------
//
// This is the only public surface for consulting a relation's constraints —
// canonicalization (sorting, joining, prefix expansion) happens once, in here,
// never in calling code.

// FindConstraintByName looks up a constraint by its real, database-assigned
// name (e.g. resolving a query's on_conflict target).
func (r *Relation) FindConstraintByName(name string) *Constraint {
	return r.byName[name]
}

// FindUniqueConstraint looks up a PRIMARY KEY/UNIQUE constraint whose columns
// exactly match the given set (order-independent).
func (r *Relation) FindUniqueConstraint(columns []string) *Constraint {
	return r.uniqueColumnGroups[sortedColumnKey(columns)]
}

// RelationshipsTo returns every foreign key relationship (either direction)
// between r and other. A table can legitimately have more than one distinct
// relationship to the same other table (e.g. orders.customer_id and
// orders.billing_customer_id both -> customers) ; len() > 1 is not itself an
// error; ResolveJoin disambiguates using the query's actual column pairing.
func (r *Relation) RelationshipsTo(other *Relation) []*Constraint {
	return r.byOtherRelation[other.PgRelId]
}

// pairingMatches checks the full ordered correspondence between a foreign key
// constraint and a client-supplied `on` mapping — not just that the column
// sets independently match, which would also silently accept an inverted
// pairing (see querying.md ### Insertion / Updates for the analogous point on
// the write side).
func pairingMatches(c *Constraint, on map[string]string) bool {
	if c.Target == nil || len(c.Columns) != len(on) {
		return false
	}
	for i, col := range c.Columns {
		want, ok := on[col.Name]
		if !ok || want != c.Target.Columns[i].Name {
			return false
		}
	}
	return true
}

func splitOn(on map[string]string) (local, parent []string) {
	local = make([]string, 0, len(on))
	parent = make([]string, 0, len(on))
	for l, p := range on {
		local = append(local, l)
		parent = append(parent, p)
	}
	return local, parent
}

// ResolveJoin validates that `on` (local column -> parent column, as in a
// query's `on` field) is backed by a real relationship between r (the relation
// being described — the child) and parent (the enclosing relation), and
// reports whether the embed is to-one.
//
// Cardinality is decided purely by whether r's own `on` columns are unique on
// r (querying.md ## Reading Algorithm : "unique on the joined side -> object")
// — independent of whether the relationship is foreign-key-backed, and
// independent of which side satisfies the eligibility check below.
//
// Eligibility (querying.md ### Scoping) requires either a foreign key whose
// exact column pairing matches `on`, or a unique constraint on r's columns or
// on parent's columns — and, unconditionally, an index on r's own `on`
// columns : the generated query always scans r filtered by them, once per
// parent row, regardless of whether the embed turns out to be to-one or
// to-many. This is a hard error, not a warning.
func (r *Relation) ResolveJoin(parent *Relation, on map[string]string) (constraint *Constraint, isToOne bool, err error) {
	oc := oops.With("relation", r.Identifier.String()).With("parent", parent.Identifier.String()).With("on", on)

	if len(on) == 0 {
		return nil, false, oc.Errorf("on must not be empty")
	}

	localCols, parentCols := splitOn(on)
	isToOne = r.FindUniqueConstraint(localCols) != nil

	for _, c := range r.RelationshipsTo(parent) {
		if pairingMatches(c, on) {
			constraint = c
			break
		}
	}

	if constraint == nil {
		// Open question (see querying.md ### Scoping, join eligibility) : this
		// checks column SETS independently on each side, with no correspondence
		// between them. A composite FK's target is required to be backed by a
		// unique constraint on exactly its column set, so any permutation of a
		// pairing over that same set passes here even when it contradicts the
		// FK's actual, declared correspondence (pairingMatches above already
		// rejected it for the FK itself). Unresolved whether that should also be
		// disallowed here when such an FK exists between r and parent.
		if u := r.FindUniqueConstraint(localCols); u != nil {
			constraint = u
		} else if u := parent.FindUniqueConstraint(parentCols); u != nil {
			constraint = u
		}
	}

	if constraint == nil {
		return nil, false, oc.Errorf("no foreign key or unique constraint backs this join")
	}

	if !r.IsIndexed(localCols) {
		return nil, false, oc.With("columns", localCols).Errorf("join is not indexed on the local relation's columns")
	}

	return constraint, isToOne, nil
}
