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

// buildLookupIndices populates the by-schema-then-name indices.
func buildLookupIndices(infos *DbInfos) {
	infos.RelationMapBySchemaName = make(map[string]map[string]*Relation)
	for _, r := range infos.Relations {
		bySchema := infos.RelationMapBySchemaName[r.Identifier.Schema]
		if bySchema == nil {
			bySchema = make(map[string]*Relation)
			infos.RelationMapBySchemaName[r.Identifier.Schema] = bySchema
		}
		bySchema[r.Identifier.Name] = r
	}

	infos.FunctionsBySchemaName = make(map[string]map[string][]*Function)
	for _, f := range infos.Functions {
		bySchema := infos.FunctionsBySchemaName[f.Identifier.Schema]
		if bySchema == nil {
			bySchema = make(map[string][]*Function)
			infos.FunctionsBySchemaName[f.Identifier.Schema] = bySchema
		}
		bySchema[f.Identifier.Name] = append(bySchema[f.Identifier.Name], f)
	}
}

// ResolveRelation resolves schema.name, through SearchPath when schema is "". Returns nil when nothing
// matches ; it does no blacklist check.
func (d *DbInfos) ResolveRelation(schema, name string) *Relation {
	if schema != "" {
		return d.RelationMapBySchemaName[schema][name]
	}
	for _, s := range d.SearchPath {
		if r := d.RelationMapBySchemaName[s][name]; r != nil {
			return r
		}
	}
	return nil
}

// ResolveFunctionCandidates is ResolveRelation for functions, returning every overload ; the caller picks one.
func (d *DbInfos) ResolveFunctionCandidates(schema, name string) []*Function {
	if schema != "" {
		return d.FunctionsBySchemaName[schema][name]
	}
	for _, s := range d.SearchPath {
		if fns := d.FunctionsBySchemaName[s][name]; len(fns) > 0 {
			return fns
		}
	}
	return nil
}
