// specs/database-json.md's GET /rel/database.json : the same introspected
// structure GenerateSchema (schema.go) renders as TypeScript, exported as
// plain JSON instead — for a caller that isn't a TypeScript project, or that
// wants the raw introspected facts (nullability, domain identity, constraint
// names, foreign-key eligibility) database.ts's own collapsed TS type
// expressions don't preserve.
package tsgen

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/wellknown"
)

// databaseJSONVersion is specs/database-json.md ## Top-level shape's
// `version` : this JSON payload's own format version, bumped whenever a
// breaking shape change is made to any section below — unrelated to the
// database's own schema version (there isn't one).
const databaseJSONVersion = 1

// DatabaseJSON is specs/database-json.md ## Top-level shape.
type DatabaseJSON struct {
	Version            int                                         `json:"version"`
	SearchPath         []string                                    `json:"search_path"`
	Types              map[string]*TypeInfoJSON                    `json:"types"`
	Relations          map[string]*RelationInfoJSON                `json:"relations"`
	Relationships      map[string][]RelationshipVariantJSON        `json:"relationships"`
	Functions          map[string][]FunctionInfoJSON               `json:"functions"`
	FunctionsByName    map[string]string                           `json:"functions_by_name"`
	ComputedProperties map[string]map[string]ComputedFieldInfoJSON `json:"computed_properties"`
	Wellknowns         map[string]WellknownInfoJSON                `json:"wellknowns"`
}

// TypeInfoJSON is specs/database-json.md ## `types`' TypeInfo — one Go
// struct standing in for that section's discriminated union ; MarshalJSON
// below emits only the fields the "kind" it holds actually specifies.
type TypeInfoJSON struct {
	Schema   string
	Name     string
	Kind     string // "scalar" | "enum" | "domain" | "array" | "composite"
	Comment  string
	Labels   []string // enum
	Base     string   // domain
	NotNull  bool     // domain
	Element  string   // array
	Relation string   // composite
}

func (t *TypeInfoJSON) MarshalJSON() ([]byte, error) {
	m := map[string]any{"schema": t.Schema, "name": t.Name, "kind": t.Kind}
	// Postgres never comments an array's own auto-generated pseudo-type ;
	// omitted for that kind rather than always present-but-empty.
	if t.Comment != "" && t.Kind != "array" {
		m["comment"] = t.Comment
	}
	switch t.Kind {
	case "enum":
		m["labels"] = t.Labels
	case "domain":
		m["base"] = t.Base
		m["not_null"] = t.NotNull
	case "array":
		m["element"] = t.Element
	case "composite":
		m["relation"] = t.Relation
	}
	return sonic.Marshal(m)
}

// RelationInfoJSON is specs/database-json.md ## `relations`' RelationInfo.
type RelationInfoJSON struct {
	Schema            string              `json:"schema"`
	Name              string              `json:"name"`
	Kind              string              `json:"kind"` // "table" | "view" | "materialized_view" | "type"
	Comment           string              `json:"comment,omitempty"`
	Columns           []ColumnInfoJSON    `json:"columns"`
	PrimaryKey        *ConstraintRefJSON  `json:"primary_key"`
	UniqueConstraints []ConstraintRefJSON `json:"unique_constraints"`
	Indexes           []IndexInfoJSON     `json:"indexes"`
}

// ConstraintRefJSON is a named, ordered column list — RelationInfo's own
// `primary_key`/`unique_constraints` entries.
type ConstraintRefJSON struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
}

// ColumnInfoJSON is specs/database-json.md ## `relations`' ColumnInfo.
type ColumnInfoJSON struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Comment      string `json:"comment,omitempty"`
	IsNullable   bool   `json:"is_nullable"`
	IsPrimaryKey bool   `json:"is_primary_key"`
	IsIdentity   bool   `json:"is_identity"`
	IsGenerated  bool   `json:"is_generated"`
	IsUpdatable  bool   `json:"is_updatable"`
	HasDefault   bool   `json:"has_default"`
}

// IndexInfoJSON is specs/database-json.md ## `relations`' IndexInfo.
type IndexInfoJSON struct {
	Name    string   `json:"name"`
	Unique  bool     `json:"unique"`
	Columns []string `json:"columns"`
}

// RelationshipVariantJSON is specs/database-json.md ## `relationships`'
// RelationshipVariant.
type RelationshipVariantJSON struct {
	Target         string            `json:"target"`
	Direction      string            `json:"direction"` // "outgoing" | "incoming"
	On             map[string]string `json:"on"`
	Unique         bool              `json:"unique"`
	Eligible       bool              `json:"eligible"`
	Shortcut       string            `json:"shortcut"`
	ConstraintName string            `json:"constraint_name"`
}

// FunctionInfoJSON is specs/database-json.md ## `functions`' FunctionInfo.
type FunctionInfoJSON struct {
	Schema     string                `json:"schema"`
	Name       string                `json:"name"`
	Comment    string                `json:"comment,omitempty"`
	Volatility string                `json:"volatility"` // "immutable" | "stable" | "volatile"
	IsStrict   bool                  `json:"is_strict"`
	Args       []FunctionArgInfoJSON `json:"args"`
	ReturnsSet bool                  `json:"returns_set"`
	Returns    *string               `json:"returns"`
	Relation   *string               `json:"relation,omitempty"`
}

// FunctionArgInfoJSON is specs/database-json.md ## `functions`'
// FunctionArgInfo.
type FunctionArgInfoJSON struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Mode       string `json:"mode"` // "in" | "out" | "inout" | "variadic" | "table"
	HasDefault bool   `json:"has_default"`
}

// ComputedFieldInfoJSON is specs/database-json.md ## `computed_properties`'
// ComputedFieldInfo.
type ComputedFieldInfoJSON struct {
	Returns    string `json:"returns"`
	ReturnsSet bool   `json:"returns_set"`
}

// WellknownInfoJSON is specs/database-json.md ## `wellknowns`'
// WellknownInfo.
type WellknownInfoJSON struct {
	Name   string                   `json:"name"`
	Query  json.RawMessage          `json:"query"`
	Params []WellknownParamInfoJSON `json:"params"`
}

// WellknownParamInfoJSON is specs/database-json.md ## `wellknowns`'
// WellknownParamInfo.
type WellknownParamInfoJSON struct {
	Name       string `json:"name"`
	PgType     string `json:"pg_type"`
	Kind       string `json:"kind"` // "number" | "string" | "boolean" | "unknown"
	HasDefault bool   `json:"has_default"`
}

// GenerateDatabaseJSON is specs/database-json.md's full payload, marshaled.
func GenerateDatabaseJSON(db *pg.DbInfos, opts Options, wkReg *wellknown.Registry) ([]byte, error) {
	return sonic.Marshal(buildDatabaseJSON(db, opts, wkReg))
}

func buildDatabaseJSON(db *pg.DbInfos, opts Options, wkReg *wellknown.Registry) *DatabaseJSON {
	relations := targetRelations(db, opts)
	fns := targetFunctions(db, opts)
	allowed := map[*pg.Relation]bool{}
	for _, r := range relations {
		allowed[r] = true
	}

	tc := newJSONTypeCollector()

	relInfos := map[string]*RelationInfoJSON{}
	built := map[string]bool{}
	pending := append([]*pg.Relation(nil), relations...)
	for len(pending) > 0 {
		r := pending[0]
		pending = pending[1:]
		key := relationKey(r.Identifier.Schema, r.Identifier.Name)
		if built[key] {
			continue
		}
		built[key] = true
		relInfos[key] = buildRelationInfo(r, tc)

		// A composite type referenced by this relation's own columns (## types
		// ## Endpoint's transitive-closure rule) may have pulled in a backing
		// relation neither whitelisted nor yet queued — enqueue it too, so ITS
		// own columns get the same treatment (a composite-of-composite chain).
		for k, extra := range tc.extraRelations {
			if !built[k] {
				pending = append(pending, extra)
			}
		}
	}

	functionsByKey := map[string][]FunctionInfoJSON{}
	for _, f := range fns {
		key := relationKey(f.Identifier.Schema, f.Identifier.Name)
		functionsByKey[key] = append(functionsByKey[key], buildFunctionInfo(f, allowed, tc))
	}

	computed := map[string]map[string]ComputedFieldInfoJSON{}
	for _, r := range relations {
		if len(r.ComputedFields) == 0 {
			continue
		}
		key := relationKey(r.Identifier.Schema, r.Identifier.Name)
		names := make([]string, 0, len(r.ComputedFields))
		for name, f := range r.ComputedFields {
			if opts.Blacklist.IsFunctionBlacklisted(f.Identifier.Schema, f.Identifier.Name) {
				continue
			}
			names = append(names, name)
		}
		if len(names) == 0 {
			continue
		}
		sort.Strings(names)
		fields := make(map[string]ComputedFieldInfoJSON, len(names))
		for _, name := range names {
			f := r.ComputedFields[name]
			fields[name] = ComputedFieldInfoJSON{
				Returns:    tc.register(f.ReturnType),
				ReturnsSet: f.ReturnsSet,
			}
		}
		computed[key] = fields
	}

	searchPath := db.SearchPath
	if searchPath == nil {
		searchPath = []string{}
	}

	return &DatabaseJSON{
		Version:            databaseJSONVersion,
		SearchPath:         searchPath,
		Types:              tc.entries,
		Relations:          relInfos,
		Relationships:      buildRelationships(relations, allowed),
		Functions:          functionsByKey,
		FunctionsByName:    bareNameWinners(fns, db.SearchPath),
		ComputedProperties: computed,
		Wellknowns:         buildWellknowns(wkReg),
	}
}

// relationKind is RelationInfo's own "kind" discriminator.
func relationKind(r *pg.Relation) string {
	switch {
	case r.IsView:
		return "view"
	case r.IsMaterializedView:
		return "materialized_view"
	case r.IsStandaloneCompositeType:
		return "type"
	default:
		return "table"
	}
}

func buildRelationInfo(r *pg.Relation, tc *jsonTypeCollector) *RelationInfoJSON {
	columns := make([]ColumnInfoJSON, 0, len(r.Columns))
	for _, c := range r.Columns {
		columns = append(columns, ColumnInfoJSON{
			Name:         c.Name,
			Type:         tc.register(c.Type),
			Comment:      c.Comment,
			IsNullable:   c.IsNullable,
			IsPrimaryKey: c.IsPrimaryKey,
			IsIdentity:   c.IsIdentity,
			IsGenerated:  c.IsGenerated,
			IsUpdatable:  c.IsUpdatable,
			HasDefault:   c.DefaultExpression != "",
		})
	}

	var pk *ConstraintRefJSON
	if r.PrimaryKey != nil {
		pk = &ConstraintRefJSON{Name: r.PrimaryKey.Name, Columns: columnNames(r.PrimaryKey.Columns)}
	}

	unique := make([]ConstraintRefJSON, 0, len(r.UniqueConstraints))
	for _, c := range r.UniqueConstraints {
		unique = append(unique, ConstraintRefJSON{Name: c.Name, Columns: columnNames(c.Columns)})
	}

	indexes := make([]IndexInfoJSON, 0, len(r.Indexes))
	for _, idx := range r.Indexes {
		indexes = append(indexes, IndexInfoJSON{Name: idx.Name, Unique: idx.IsUnique, Columns: idx.Columns})
	}

	return &RelationInfoJSON{
		Schema:            r.Identifier.Schema,
		Name:              r.Identifier.Name,
		Kind:              relationKind(r),
		Comment:           r.Comment,
		Columns:           columns,
		PrimaryKey:        pk,
		UniqueConstraints: unique,
		Indexes:           indexes,
	}
}

// buildRelationships is specs/database-json.md ## `relationships` : every
// declared foreign key touching an exported relation, both directions,
// whether or not it's actually usable in a `join()` — unlike
// renderRelationships (schema.go), which drops an ineligible one outright.
func buildRelationships(relations []*pg.Relation, allowed map[*pg.Relation]bool) map[string][]RelationshipVariantJSON {
	out := map[string][]RelationshipVariantJSON{}

	for _, owner := range relations {
		for _, c := range owner.OutgoingForeignKeys {
			referenced := c.OtherRelation()
			if referenced == nil || !allowed[referenced] {
				continue
			}
			ownerCols := columnNames(c.Columns)
			eligible := owner.IsIndexed(ownerCols)

			pairs := make([]string, len(c.Columns))
			onOutgoing := make(map[string]string, len(c.Columns))
			onIncoming := make(map[string]string, len(c.Columns))
			for i := range c.Columns {
				pairs[i] = c.Target.Columns[i].Name + ":" + c.Columns[i].Name
				onOutgoing[c.Columns[i].Name] = c.Target.Columns[i].Name
				onIncoming[c.Target.Columns[i].Name] = c.Columns[i].Name
			}
			pairStr := strings.Join(pairs, ",")

			ownerKey := relationKey(owner.Identifier.Schema, owner.Identifier.Name)
			referencedKey := relationKey(referenced.Identifier.Schema, referenced.Identifier.Name)

			out[ownerKey] = append(out[ownerKey], RelationshipVariantJSON{
				Target:         referencedKey,
				Direction:      "outgoing",
				On:             onOutgoing,
				Unique:         true, // an FK's target columns are always PRIMARY KEY/UNIQUE-backed
				Eligible:       eligible,
				Shortcut:       referencedKey + ">;" + pairStr,
				ConstraintName: c.Name,
			})
			out[referencedKey] = append(out[referencedKey], RelationshipVariantJSON{
				Target:         ownerKey,
				Direction:      "incoming",
				On:             onIncoming,
				Unique:         owner.FindUniqueConstraint(ownerCols) != nil,
				Eligible:       eligible,
				Shortcut:       ownerKey + "<;" + pairStr,
				ConstraintName: c.Name,
			})
		}
	}

	for key, variants := range out {
		sort.Slice(variants, func(i, j int) bool {
			if variants[i].Target != variants[j].Target {
				return variants[i].Target < variants[j].Target
			}
			return variants[i].Direction < variants[j].Direction
		})
		out[key] = variants
	}
	return out
}

// argModeString is FunctionArgInfo's own "mode" : pg.FunctionArgument's
// PgMode constants, spelled out.
func argModeString(mode string) string {
	switch mode {
	case pg.MODE_OUT:
		return "out"
	case pg.MODE_INOUT:
		return "inout"
	case pg.MODE_VARIADIC:
		return "variadic"
	case pg.MODE_TABLE:
		return "table"
	default: // pg.MODE_IN, and INFO_QUERY_FUNCTIONS' own coalesce(..., 'i') default
		return "in"
	}
}

func functionVolatility(f *pg.Function) string {
	switch {
	case f.IsImmutable:
		return "immutable"
	case f.IsStable:
		return "stable"
	default:
		return "volatile"
	}
}

func buildFunctionInfo(f *pg.Function, allowed map[*pg.Relation]bool, tc *jsonTypeCollector) FunctionInfoJSON {
	args := make([]FunctionArgInfoJSON, len(f.Arguments))
	var inputIdx []int
	for i := range f.Arguments {
		a := &f.Arguments[i]
		args[i] = FunctionArgInfoJSON{
			Name: a.Name,
			Type: tc.register(a.Type),
			Mode: argModeString(a.PgMode),
		}
		if a.IsIn() || a.IsInOut() || a.IsVariadic() {
			inputIdx = append(inputIdx, i)
		}
	}
	// has_default only ever applies to a trailing IN/INOUT/VARIADIC input
	// argument — PgNargsDefaults' own trailing count, mirrored from
	// schema.go's renderFunctionArgs/inputArgs.
	firstOptional := len(inputIdx) - f.PgNargsDefaults
	for pos, idx := range inputIdx {
		if pos >= firstOptional {
			args[idx].HasDefault = true
		}
	}

	info := FunctionInfoJSON{
		Schema:     f.Identifier.Schema,
		Name:       f.Identifier.Name,
		Comment:    f.Comment,
		Volatility: functionVolatility(f),
		IsStrict:   f.IsStrict,
		Args:       args,
		ReturnsSet: f.ReturnsSet,
	}

	if f.RecordRelation == nil {
		key := tc.register(f.ReturnType)
		info.Returns = &key
	}
	if f.ReturnsSet {
		if rel := f.ReturnType.CompositeRelation(); rel != nil && allowed[rel] {
			key := relationKey(rel.Identifier.Schema, rel.Identifier.Name)
			info.Relation = &key
		}
	}

	return info
}

func buildWellknowns(wkReg *wellknown.Registry) map[string]WellknownInfoJSON {
	out := map[string]WellknownInfoJSON{}
	for _, c := range wkReg.All() {
		names := make([]string, 0, len(c.Params))
		for name := range c.Params {
			names = append(names, name)
		}
		sort.Strings(names)

		params := make([]WellknownParamInfoJSON, 0, len(names))
		for _, name := range names {
			p := c.Params[name]
			params = append(params, WellknownParamInfoJSON{
				Name:       name,
				PgType:     p.Type,
				Kind:       wellknown.TSTypeForParam(p.Type),
				HasDefault: p.HasDefault,
			})
		}

		out[c.Name] = WellknownInfoJSON{
			Name:   c.Name,
			Query:  json.RawMessage(c.QueryRaw),
			Params: params,
		}
	}
	return out
}

// jsonTypeCollector accumulates TypeInfoJSON entries discovered while
// walking column/argument/return types, de-duplicated by "schema.name" —
// tsgen's own typeCollector (types.go), keyed by type identity instead of
// rendering a TS expression. Also records every composite type's own
// backing relation (extraRelations) — specs/database-json.md ## `relations`
// ## Endpoint's transitive-closure rule : that relation must resolve in
// `relations` regardless of the schema whitelist/blacklist.
type jsonTypeCollector struct {
	seen           map[string]bool
	entries        map[string]*TypeInfoJSON
	extraRelations map[string]*pg.Relation
}

func newJSONTypeCollector() *jsonTypeCollector {
	return &jsonTypeCollector{
		seen:           map[string]bool{},
		entries:        map[string]*TypeInfoJSON{},
		extraRelations: map[string]*pg.Relation{},
	}
}

// register resolves t (and, transitively, whatever it references) into tc,
// returning its own "schema.name" key — tsTypeExpr's (types.go) same
// array/domain/enum/composite/scalar order, but recording identity rather
// than rendering a TS type expression.
func (tc *jsonTypeCollector) register(t *pg.Type) string {
	if t == nil {
		return ""
	}
	key := t.PgIdentifier.String()
	if tc.seen[key] {
		return key
	}
	// Marked seen BEFORE recursing : a composite type whose own columns
	// reference itself would otherwise recurse forever (typeCollector's own
	// guard, types.go).
	tc.seen[key] = true

	entry := &TypeInfoJSON{
		Schema:  t.PgIdentifier.Schema,
		Name:    t.PgIdentifier.Name,
		Comment: t.Comment,
	}
	tc.entries[key] = entry

	switch {
	case t.IsArray():
		entry.Kind = "array"
		entry.Element = tc.register(t.ElementType)
	case t.IsDomain():
		entry.Kind = "domain"
		entry.NotNull = t.PgDomainNotNull
		entry.Base = tc.register(t.BaseType)
	case t.IsEnum():
		entry.Kind = "enum"
		entry.Labels = append([]string(nil), t.EnumLabels...)
	case t.IsComposite():
		entry.Kind = "composite"
		relKey := relationKey(t.Relation.Identifier.Schema, t.Relation.Identifier.Name)
		entry.Relation = relKey
		tc.extraRelations[relKey] = t.Relation
	default:
		entry.Kind = "scalar"
	}
	return key
}
