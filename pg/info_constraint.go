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
	"github.com/rel-server/rel/errcode"
	"github.com/samber/oops"
)

// dbConstraint is introspection's raw row, before resolution into Constraint.
type dbConstraint struct {
	Name  string
	Type  string // pg_constraint.contype : "p", "u", "f"
	RelId int

	// True declared order (pg_constraint.conkey) — pairs positionally with
	// TargetColumns for composite FKs. Use sortedColumnKey for lookup keys.
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

// A resolved constraint : a PRIMARY KEY/UNIQUE, or one side of a foreign key, whose Target is then the
// reciprocal Constraint on the other relation.
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

// sortedColumnKey canonicalizes a column set into a map key, sorted : lookups only need set membership.
func sortedColumnKey(columns []string) string {
	sorted := append([]string(nil), columns...)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

func columnNames(columns []*Column) []string {
	names := make([]string, len(columns))
	for i, c := range columns {
		names[i] = c.Name
	}
	return names
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
		// pg_constraint is not filtered by role privilege : skip a relation the role cannot see.
		relation := infos.GetRelation(dbc.RelId)
		if relation == nil {
			continue
		}

		// Check the target side too, before any mutation, so a skip never leaves a half-built Constraint.
		var target *Relation
		if dbc.Type == "f" {
			target = infos.GetRelation(dbc.TargetRelId)
			if target == nil {
				continue
			}
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
			for _, col := range c.Columns {
				col.IsPrimaryKey = true
				col.IsParOfUnique = true // a primary key is inherently unique
			}

		case "u":
			c.Type = ConstraintTypeUnique
			relation.uniqueColumnGroups[sortedColumnKey(dbc.Columns)] = c
			relation.UniqueConstraints = append(relation.UniqueConstraints, c)
			for _, col := range c.Columns {
				col.IsParOfUnique = true
			}

		case "f":
			c.Type = ConstraintTypeOutgoingForeignKey
			relation.OutgoingForeignKeys = append(relation.OutgoingForeignKeys, c)

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
-- Deliberately unfiltered by schema — see info_relation.go's INFO_QUERY_RELATIONS
-- comment : introspection needs the complete picture, pg_catalog included.
FROM pg_constraint c
WHERE c.contype IN ('p', 'u', 'f')
) C;`

// ---- lookup API : column-set canonicalization happens once, here -------------

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

// RelationshipsTo returns every foreign key relationship (either direction) between r and other. There can be
// several (orders.customer_id and orders.billing_customer_id) ; ResolveJoin picks by column pairing.
func (r *Relation) RelationshipsTo(other *Relation) []*Constraint {
	return r.byOtherRelation[other.PgRelId]
}

// pairingMatches checks ordered correspondence, so an inverted pairing does not match.
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

// ResolveJoin checks that `on` (local column -> parent column) is backed by a relationship between r (the
// child) and parent, and reports whether the embed is to-one : whether r's `on` columns are unique on r.
//
// It needs a foreign key whose column pairing matches `on` or a unique constraint on either side's columns,
// and always an index on r's `on` columns, since the query scans r by them once per parent row.
func (r *Relation) ResolveJoin(parent *Relation, on map[string]string) (constraint *Constraint, isToOne bool, err error) {
	// JoinMissingIndex covers every join eligibility error, not only the missing index.
	oc := oops.With("relation", r.Identifier.String()).With("parent", parent.Identifier.String()).With("on", on).Code(errcode.JoinMissingIndex)

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
		// A foreign key on the same column sets with another pairing : most likely a swapped `on`.
		for _, c := range r.RelationshipsTo(parent) {
			if sortedColumnKey(columnNames(c.Columns)) == sortedColumnKey(localCols) &&
				sortedColumnKey(columnNames(c.Target.Columns)) == sortedColumnKey(parentCols) {
				return nil, false, oc.With("constraint", c.Name).Errorf("on's columns match foreign key %q but the pairing is inverted relative to its declared correspondence", c.Name)
			}
		}

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
