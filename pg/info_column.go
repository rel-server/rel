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

type Column struct {
	Name string

	// Index is the column's attnum (information_schema's ordinal_position).
	Index int

	// Source is the column a view column is a plain pass-through of, one hop (a view over a view points at the
	// inner view's column). nil for a table column or an expression. Filled by FillViewProvenance.
	Source *Column `json:"-"`
	// SourceRelation is the relation Source belongs to.
	SourceRelation *Relation `json:"-"`

	PgTypeOid int

	// DeclaredType is the column's type as format_type prints it, modifiers included ("numeric(10,2)", a domain's
	// name), for a temp table to repeat. Schema-qualified only where the introspecting search_path does not see it.
	DeclaredType string

	// PgDomainIdentifier is set only when the column's type is a domain, since PgTypeOid names the domain's base
	// type. FillTypeInformations swaps it into Type.
	PgDomainIdentifier *SqlIdentifier `json:"DomainIdentifier"`

	Type *Type

	// The column's COMMENT ON, empty if unset.
	Comment string

	DefaultExpression string

	// IsPrimaryKey/IsParOfUnique are filled later, by FillConstraintInformations.
	IsPrimaryKey  bool
	IsIdentity    bool
	IsGenerated   bool // GENERATED ALWAYS AS ... STORED
	IsParOfUnique bool
	IsNullable    bool
	IsUpdatable   bool
}

// IsReallyNotNull reports !IsNullable or a not-null constraint on the column's domain, which IsNullable
// does not show.
func (c *Column) IsReallyNotNull() bool {
	return !c.IsNullable || c.Type.PgDomainNotNull
}
