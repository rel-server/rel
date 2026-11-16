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

// FillComputedFields populates every Relation's ComputedFields. Must run after Functions, Relations and Types
// are resolved.
//
// A function is registered on relation r when it is a plain function in r's schema, callable with one argument
// (the others have defaults), whose first input argument is r's row type. One named like a column of r is
// never registered : the column wins.
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
