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

// buildLookupIndices populates the by-schema-then-name indices below from
// infos.Relations/Functions. Pure Go, no DB round-trip ; must run after both
// are filled. Built once, at introspection time, not re-derived per request
// — same convention as Relation's own byName/uniqueColumnGroups/
// byOtherRelation, built once in FillConstraintInformations.
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

// ResolveRelation resolves schema.name against introspection : looked up
// directly when schema is given, or via infos.SearchPath (first schema
// where the name exists wins — standard Postgres search_path semantics)
// when schema is "". Returns nil, not an error, when nothing matches — the
// caller (the query package) decides how "not found" versus "blacklisted"
// map to their respective HTTP statuses ; pg has no config dependency and
// performs no blacklist check itself.
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

// ResolveFunctionCandidates is ResolveRelation for functions, returning the
// full overload set for schema.name (schema resolved via search path the
// same way when "") — disambiguating by call shape (arity, argument names)
// is the caller's job ; this only answers "what's named this."
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
