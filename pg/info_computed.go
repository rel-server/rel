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

// FillComputedFields populates every Relation's ComputedFields — the
// Postgres "functional column" convention (a function taking a table's own
// row type as its argument, callable as `alias.func_name`/`func_name(alias)`
// in ordinary SQL). Must run after Functions, Relations, and Types are all
// resolved (a function's argument types and a relation's own composite Type
// both have to be filled in already), and before anything reads
// Relation.ComputedFields.
//
// A candidate function is registered on relation r when :
//   - it's a plain function (IsPlainFunction — never an aggregate/window/
//     procedure) ;
//   - it lives in r's own schema ;
//   - it's callable with exactly one argument (AcceptsArity(1) — every
//     argument after the first has a default) ;
//   - its first input argument's type is r's own composite row type.
//
// A candidate whose name collides with one of r's own physical columns is
// never registered at all — the real column wins by construction. This is a
// schema-authoring problem (two things in the same database legitimately
// wanting the same name), not a per-query ambiguity for the resolver to
// arbitrate — resolving it here means the query-time scope lookup never
// even sees a conflict.
func FillComputedFields(infos *DbInfos) {
	for _, r := range infos.Relations {
		r.ComputedFields = map[string]*Function{}
	}

	for _, f := range infos.Functions {
		if !f.IsPlainFunction() || !f.AcceptsArity(1) {
			continue
		}
		first := f.FirstInputArg()
		if first == nil || first.Type == nil {
			continue
		}
		rel := first.Type.CompositeRelation()
		if rel == nil || rel.IsStandaloneCompositeType || rel.IsSynthetic {
			continue
		}
		if rel.Identifier.Schema != f.Identifier.Schema {
			continue
		}
		if rel.ColumnsMap[f.Identifier.Name] != nil {
			continue
		}
		rel.ComputedFields[f.Identifier.Name] = f
	}
}
