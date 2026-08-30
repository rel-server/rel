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
	"github.com/jackc/pgx/v5"
	"github.com/samber/oops"
)

type Type struct {
	PgIdentifier SqlIdentifier

	// The type's own COMMENT ON, if any — meant primarily for the TypeScript
	// export to surface as a doc comment ; empty string if unset. Most useful
	// for domains and composite types ; built-in scalar types are never
	// commented in practice.
	Comment string

	ArrayType   *Type     // The array type of this type
	ElementType *Type     // The element type of this type, yielded by subscripting - does not implicate that this is an array
	BaseType    *Type     // only not nil if this is a domain
	Relation    *Relation // The relation that this type is a composite type of, nil otherwise

	PgDomainNotNull bool // Whether the domain is not null, only used for domains
	PgOid           int
	PgElemOid       int
	PgArrayOid      int // If IsArray, the oid of the array type
	PgRelId         int // When this type is a composite type
	PgRealTypeId    int // The oid of the real type, if this is a domain
}

// This is the only true test for array types
func (t *Type) IsArray() bool {
	return t != nil && t.ElementType != nil && t.ElementType.ArrayType == t
}

func (t *Type) IsComposite() bool {
	return t != nil && t.Relation != nil
}

func (t *Type) IsDomain() bool {
	return t != nil && t.BaseType != nil
}

// Underlying unwraps t through any chain of domains, returning the first
// non-domain type — t itself if t isn't a domain. A domain's own PgRelId is
// always 0 (only the base type carries one), so IsComposite()/IsArray()
// naively checked directly on a domain type report false even when the
// domain wraps a composite/array type ; callers that need to know the
// actual represented shape should check Underlying() instead of t directly.
func (t *Type) Underlying() *Type {
	for t != nil && t.IsDomain() {
		t = t.BaseType
	}
	return t
}

// CompositeRelation returns the *Relation t represents as a composite value,
// unwrapping any domain wrapping first — nil if t isn't composite even after
// unwrapping.
func (t *Type) CompositeRelation() *Relation {
	u := t.Underlying()
	if u == nil {
		return nil
	}
	return u.Relation
}

//----------------------------------------------------------------------------------

// Query the database and fill the infos
func FillTypeInformations(infos *DbInfos, conn *pgx.Conn) error {
	var ok bool

	if err := scanIntoThroughJsonAgg(conn, INFO_QUERY_TYPES, &infos.Types); err != nil {
		return err
	}

	// Index into infos.Types directly throughout, never range-by-value : a
	// fresh loop ranging over infos.Types by value would mutate a throwaway
	// copy on every assignment below, distinct from whatever infos.TypeMapByOid
	// points to, and none of it would stick (the same class of bug as the
	// f.Arguments fix above, just easier to miss because nothing failed loudly
	// — every type silently reported IsArray()/IsDomain()/IsComposite() false).
	for i := range infos.Types {
		infos.TypeMapByOid[infos.Types[i].PgOid] = &infos.Types[i]
	}

	var type_by_relid map[int]*Type = make(map[int]*Type)

	for i := range infos.Types {
		t := &infos.Types[i]

		if t.PgElemOid != 0 {
			if t.ElementType, ok = infos.TypeMapByOid[t.PgElemOid]; !ok {
				return oops.With("type", t.PgIdentifier.String()).With("elemOid", t.PgElemOid).Errorf("failed to find element type (this should not happen)")
			}
		}

		if t.PgArrayOid != 0 {
			if t.ArrayType, ok = infos.TypeMapByOid[t.PgArrayOid]; !ok {
				return oops.With("type", t.PgIdentifier.String()).With("arrayOid", t.PgArrayOid).Errorf("failed to find array type (this should not happen)")
			}
		}

		if t.PgRealTypeId != 0 {
			if t.BaseType, ok = infos.TypeMapByOid[t.PgRealTypeId]; !ok {
				return oops.With("type", t.PgIdentifier.String()).With("baseOid", t.PgRealTypeId).Errorf("failed to find base type (this should not happen)")
			}
		}

		// We'll use this when filling the relations
		if t.PgRelId > 0 {
			type_by_relid[t.PgRelId] = t
		}
	}

	// A composite type's backing relation, resolved now that both maps exist —
	// needs the relation to actually have been introspected, which is why
	// introspection must never exclude pg_catalog/information_schema : a
	// function returning a pg_catalog composite type (or SETOF a system view)
	// still needs IsComposite()/.Relation to resolve correctly.
	for i := range infos.Types {
		t := &infos.Types[i]
		if t.PgRelId > 0 {
			t.Relation = infos.GetRelation(t.PgRelId)
		}
	}

	// Fill the types for functions
	for _, f := range infos.Functions {
		// Errors should never happen here
		var ok bool
		if f.ReturnType, ok = infos.TypeMapByOid[f.PgReturnTypeOid]; !ok {
			return oops.With("function", f.Identifier.String()).With("returnTypeOid", f.PgReturnTypeOid).Errorf("failed to find return type (this should not happen)")
		}

		// Arguments is a value slice ; index into it directly rather than
		// ranging by value, or the assignment below would silently mutate a
		// throwaway copy and never stick on the real element.
		for i := range f.Arguments {
			a := &f.Arguments[i]
			if a.Type, ok = infos.TypeMapByOid[a.PgTypeOid]; !ok {
				return oops.With("function", f.Identifier.String()).With("argument", a.Name).With("typeOid", a.PgTypeOid).Errorf("failed to find argument type (this should not happen)")
			}
		}

		// RecordRelation : built directly from this function's own OUT-mode
		// arguments, independent of ReturnType — see Function.RecordRelation's
		// own doc comment for why ReturnType.Relation can never resolve this
		// (every record-returning function shares the one generic
		// pg_catalog.record pseudo-type, which has no backing pg_class row).
		// PrimaryKey/OutgoingForeignKeys/IncomingForeignKeys/Indexes and every
		// unexported lookup map are deliberately left at their zero value —
		// there is genuinely nothing here for them to describe : a function's
		// computed output is never constrained or indexed by Postgres, so
		// leaving them nil/empty is the accurate representation, not a
		// shortcut. This is also what makes the result automatically safe to
		// hand to the query engine unchanged : every write-eligibility check
		// (identityIsWritable, on_conflict's own FindUniqueConstraint lookup)
		// and every join-eligibility check (ResolveJoin's FindUniqueConstraint/
		// RelationshipsTo/IsIndexed) already reads from exactly these fields,
		// and a nil map read in Go safely reports "not found" rather than
		// panicking — so this relation is structurally unwritable and can
		// never be the covered/indexed (child) side of any join, with no
		// separate guard needed anywhere else.
		var outCols []*Column
		outColsMap := map[string]*Column{}
		for i := range f.Arguments {
			a := &f.Arguments[i]
			// RETURNS TABLE(...)'s own pseudo-columns are proargmode 't'
			// (MODE_TABLE), NOT 'o' (a plain OUT parameter, MODE_OUT) —
			// genuinely distinct Postgres concepts, both included here since
			// both represent "part of this function's own output."
			if !a.IsOut() && !a.IsTableColumn() {
				continue
			}
			col := &Column{Name: a.Name, PgTypeOid: a.PgTypeOid, Type: a.Type}
			outCols = append(outCols, col)
			outColsMap[a.Name] = col
		}
		if len(outCols) > 0 {
			f.RecordRelation = &Relation{
				Identifier:  f.Identifier,
				IsSynthetic: true,
				Columns:     outCols,
				ColumnsMap:  outColsMap,
				Type:        f.ReturnType,
			}
		}
	}

	for _, r := range infos.Relations {
		if r.Type, ok = type_by_relid[r.PgRelId]; !ok {
			return oops.With("relation", r.Identifier.String()).With("relId", r.PgRelId).Errorf("failed to find type for relation (this should not happen)")
		}

		for _, c := range r.Columns {
			if c.Type, ok = infos.TypeMapByOid[c.PgTypeOid]; !ok {
				return oops.With("relation", r.Identifier.String()).With("column", c.Name).With("typeOid", c.PgTypeOid).Errorf("failed to find type for column (this should not happen)")
			}
		}
	}

	// Now, do the relations

	return nil
}

// A query we will use to fetch complex type information
var INFO_QUERY_TYPES = /* sql */ `
SELECT json_agg(T) FROM (SELECT
  t.oid::integer AS "PgOid",
  t.typelem::integer AS "PgElemOid",
	t.typarray::integer AS "PgArrayOid",
	t.typrelid::integer AS "PgRelId",
	t.typbasetype::integer AS "PgRealTypeId",
	t.typnotnull::boolean AS "PgDomainNotNull",
	json_build_object(
		'Schema', n.nspname,
		'Name', t.typname
	) as "PgIdentifier",
	obj_description(t.oid, 'pg_type') AS "Comment"
FROM
  pg_type t
  INNER JOIN pg_namespace n ON n.oid = t.typnamespace
) T;`
