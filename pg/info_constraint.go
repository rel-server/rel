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

	"github.com/ceymard/rel/errcode"
	"github.com/jackc/pgx/v5"
	"github.com/samber/oops"
)

// dbConstraint is introspection's raw row, before resolution into Constraint.
// Transient — never stored, only threaded through FillConstraintInformations.
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
// a lookup only ever needs set membership, never positional order.
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
		// pg_constraint is unfiltered by role privilege, unlike GetRelation —
		// skip an invisible relation, same pattern as info_index.go's case.
		relation := infos.GetRelation(dbc.RelId)
		if relation == nil {
			continue
		}

		// The FK's target side needs the same check, before any mutation,
		// so a skip never leaves a half-built Constraint under byName.
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

// RelationshipsTo returns every foreign key relationship (either direction)
// between r and other. A table can legitimately have more than one distinct
// relationship to the same other table (e.g. orders.customer_id and
// orders.billing_customer_id both -> customers) ; len() > 1 is not itself an
// error; ResolveJoin disambiguates using the query's actual column pairing.
func (r *Relation) RelationshipsTo(other *Relation) []*Constraint {
	return r.byOtherRelation[other.PgRelId]
}

// pairingMatches checks full ordered correspondence, not just that column
// sets match — that would also accept an inverted pairing.
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
// r (query-engine.md ## Reading Algorithm : "unique on the joined side -> object")
// — independent of whether the relationship is foreign-key-backed, and
// independent of which side satisfies the eligibility check below.
//
// Eligibility (query-engine.md ### Scoping) requires either a foreign key whose
// exact column pairing matches `on`, or a unique constraint on r's columns or
// on parent's columns — and, unconditionally, an index on r's own `on`
// columns : the generated query always scans r filtered by them, once per
// parent row, regardless of whether the embed turns out to be to-one or
// to-many. This is a hard error, not a warning.
func (r *Relation) ResolveJoin(parent *Relation, on map[string]string) (constraint *Constraint, isToOne bool, err error) {
	// error-handling.md's whole "Join eligibility" family, not just the
	// literal missing-index case — distinct from errcode.UnknownIdentifier.
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
		// A same-column-set FK exists but its pairing didn't match `on` :
		// near-certainly a swapped/typo'd `on`, not a second relationship.
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
