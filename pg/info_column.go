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

/** Domains may define a not null constraint, which is of interest in the case of types or views that tend to say something is potentially null when it's really not. */
func (c *Column) IsReallyNotNull() bool {
	return c.IsNotNull || c.Type.PgDomainNotNull
}
