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
	Name      string
	PgTypeOid int

	// PgDomainIdentifier is set only when the column's declared type is a
	// domain — information_schema.columns' own udt_name/udt_schema (and so
	// PgTypeOid, resolved from them) name the domain's BASE type, not the
	// domain itself (SQL-standard information_schema behaviour) ; the
	// domain's real identity only survives via domain_schema/domain_name,
	// captured here instead. FillTypeInformations resolves this back into
	// Type once every *Type is known — see its own domain-column pass.
	PgDomainIdentifier *SqlIdentifier `json:"DomainIdentifier"`

	Type *Type

	// The column's own COMMENT ON, if any — meant primarily for the
	// TypeScript export to surface as a doc comment ; empty string if unset.
	Comment string

	DefaultExpression string

	IsPrimaryKey  bool
	IsIdentity    bool
	IsGenerated   bool
	IsParOfUnique bool
	IsNotNull     bool
	IsNullable    bool
	IsUpdatable   bool
}

// IsReallyNotNull reports IsNotNull OR a not-null constraint declared on
// the column's own domain type — a domain-backed column/view can read as
// nullable (IsNotNull false) while still never actually holding NULL.
func (c *Column) IsReallyNotNull() bool {
	return c.IsNotNull || c.Type.PgDomainNotNull
}
