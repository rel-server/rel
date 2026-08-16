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

//----------------------------------------------------------------------------------

// Query the database and fill the infos
func FillTypeInformations(infos *DbInfos, conn *pgx.Conn) error {
	var ok bool

	if err := scanIntoThroughJsonAgg(conn, INFO_QUERY_TYPES, &infos.Types); err != nil {
		return err
	}

	for _, t := range infos.Types {
		infos.TypeMapByOid[t.PgOid] = &t
	}

	var type_by_relid map[int]*Type = make(map[int]*Type)

	for _, t := range infos.Types {
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
			type_by_relid[t.PgRelId] = &t
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
	) as "PgIdentifier"
FROM
  pg_type t
  INNER JOIN pg_namespace n ON n.oid = t.typnamespace
) T;`
