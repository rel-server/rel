// GET /rel/database.json : what GenerateSchema renders as TypeScript, as plain JSON, keeping the facts the
// TypeScript types collapse (nullability, domains, constraint names, join eligibility).
package tsgen

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/wellknown"
)

// databaseJSONVersion is the payload's format version, bumped on any breaking shape change.
const databaseJSONVersion = 1

// DatabaseJSON is the top-level shape of the database.json payload.
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

// TypeInfoJSON is a `types` entry ; MarshalJSON emits only the fields of its kind.
type TypeInfoJSON struct {
	Schema   string
	Name     string
	Kind     string // "scalar" | "enum" | "domain" | "array" | "composite"
	Comment  string
	TSType   string   // domain, declared
	TSFrom   string   // domain, declared
	Labels   []string // enum
	Base     string   // domain
	NotNull  bool     // domain
	Element  string   // array
	Relation string   // composite
}

func (t *TypeInfoJSON) MarshalJSON() ([]byte, error) {
	m := map[string]any{"schema": t.Schema, "name": t.Name, "kind": t.Kind}
	// An array type is auto-generated and never commented.
	if t.Comment != "" && t.Kind != "array" {
		m["comment"] = t.Comment
	}
	switch t.Kind {
	case "enum":
		m["labels"] = t.Labels
	case "domain":
		m["base"] = t.Base
		m["not_null"] = t.NotNull
		if t.TSType != "" {
			m["ts_type"] = t.TSType
		}
		if t.TSFrom != "" {
			m["ts_from"] = t.TSFrom
		}
	case "array":
		m["element"] = t.Element
	case "composite":
		m["relation"] = t.Relation
	}
	return sonic.Marshal(m)
}

// RelationInfoJSON is a `relations` entry.
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

// ConstraintRefJSON is a named, ordered column list : a `primary_key`/`unique_constraints` entry.
type ConstraintRefJSON struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
}

// ColumnInfoJSON is one column of a RelationInfoJSON.
type ColumnInfoJSON struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Comment      string `json:"comment,omitempty"`
	TSType       string `json:"ts_type,omitempty"`
	TSFrom       string `json:"ts_from,omitempty"`
	IsNullable   bool   `json:"is_nullable"`
	IsPrimaryKey bool   `json:"is_primary_key"`
	IsIdentity   bool   `json:"is_identity"`
	IsGenerated  bool   `json:"is_generated"`
	IsUpdatable  bool   `json:"is_updatable"`
	HasDefault   bool   `json:"has_default"`
}

// IndexInfoJSON is one index of a RelationInfoJSON.
type IndexInfoJSON struct {
	Name    string   `json:"name"`
	Unique  bool     `json:"unique"`
	Columns []string `json:"columns"`
}

// RelationshipVariantJSON is one `relationships` entry : a foreign key seen from one side.
type RelationshipVariantJSON struct {
	Target         string            `json:"target"`
	Direction      string            `json:"direction"` // "outgoing" | "incoming"
	On             map[string]string `json:"on"`
	Unique         bool              `json:"unique"`
	Eligible       bool              `json:"eligible"`
	Shortcut       string            `json:"shortcut"`
	ConstraintName string            `json:"constraint_name"`
}

// FunctionInfoJSON is a `functions` entry.
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

// FunctionArgInfoJSON is one argument of a FunctionInfoJSON.
type FunctionArgInfoJSON struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Mode       string `json:"mode"` // "in" | "out" | "inout" | "variadic" | "table"
	HasDefault bool   `json:"has_default"`
}

// ComputedFieldInfoJSON is a `computed_properties` entry.
type ComputedFieldInfoJSON struct {
	Returns    string `json:"returns"`
	ReturnsSet bool   `json:"returns_set"`
}

// WellknownInfoJSON is a `wellknowns` entry.
type WellknownInfoJSON struct {
	Name   string                   `json:"name"`
	Query  json.RawMessage          `json:"query"`
	Params []WellknownParamInfoJSON `json:"params"`
}

// WellknownParamInfoJSON is one parameter of a WellknownInfoJSON.
type WellknownParamInfoJSON struct {
	Name       string `json:"name"`
	PgType     string `json:"pg_type"`
	Kind       string `json:"kind"` // "number" | "string" | "boolean" | "unknown"
	HasDefault bool   `json:"has_default"`
	Comment    string `json:"comment,omitempty"`
	TSType     string `json:"ts_type,omitempty"`
	TSFrom     string `json:"ts_from,omitempty"`
}

// GenerateDatabaseJSON marshals the full database.json payload.
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
	tc.decls = declSource{config: opts.Types}

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

		// A composite column type pulls in its backing relation even outside the whitelist ; queue it so its own
		// columns are walked too.
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

// relationKind is RelationInfo's "kind".
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
		d, _ := tc.decls.column(r, c)
		columns = append(columns, ColumnInfoJSON{
			Name:         c.Name,
			Type:         tc.register(c.Type),
			Comment:      d.Comment,
			TSType:       d.Type,
			TSFrom:       d.From,
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

	comment, _ := tc.decls.relation(r)
	if r.IsStandaloneCompositeType {
		d, _ := parseDecl(r.Comment, "type", "")
		comment = d.Comment
	}
	return &RelationInfoJSON{
		Schema:            r.Identifier.Schema,
		Name:              r.Identifier.Name,
		Kind:              relationKind(r),
		Comment:           comment,
		Columns:           columns,
		PrimaryKey:        pk,
		UniqueConstraints: unique,
		Indexes:           indexes,
	}
}

// buildRelationships builds the `relationships` section : every foreign key touching an exported relation, both
// directions, usable in a `join()` or not.
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

			// The incoming marker is `<` when the foreign key columns are unique (one to one), `*` otherwise.
			incomingUnique := owner.FindUniqueConstraint(ownerCols) != nil
			incomingMarker := "*"
			if incomingUnique {
				incomingMarker = "<"
			}

			out[ownerKey] = append(out[ownerKey], RelationshipVariantJSON{
				Target:         referencedKey,
				Direction:      "outgoing",
				On:             onOutgoing,
				Unique:         true, // a foreign key always targets a PRIMARY KEY/UNIQUE
				Eligible:       eligible,
				Shortcut:       referencedKey + ">" + pairStr,
				ConstraintName: c.Name,
			})
			out[referencedKey] = append(out[referencedKey], RelationshipVariantJSON{
				Target:         ownerKey,
				Direction:      "incoming",
				On:             onIncoming,
				Unique:         incomingUnique,
				Eligible:       eligible,
				Shortcut:       ownerKey + incomingMarker + pairStr,
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

// argModeString spells out a pg.FunctionArgument PgMode.
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
	default: // pg.MODE_IN
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
	// PgNargsDefaults counts trailing input args with a default.
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
				Comment:    p.TS.Comment,
				TSType:     p.TS.Type,
				TSFrom:     p.TS.From,
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

// jsonTypeCollector accumulates the TypeInfoJSON entries met while walking types, de-duplicated by "schema.name".
// extraRelations holds each composite type's backing relation, which must appear in `relations` regardless of
// the whitelist/blacklist.
type jsonTypeCollector struct {
	decls          declSource
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

// register records t and whatever it references into tc, and returns its "schema.name" key.
func (tc *jsonTypeCollector) register(t *pg.Type) string {
	if t == nil {
		return ""
	}
	key := t.PgIdentifier.String()
	if tc.seen[key] {
		return key
	}
	// Marked seen before recursing : a composite type may reference itself.
	tc.seen[key] = true

	d, _ := tc.decls.typ(t)
	entry := &TypeInfoJSON{
		Schema:  t.PgIdentifier.Schema,
		Name:    t.PgIdentifier.Name,
		Comment: d.Comment,
		TSType:  d.Type,
		TSFrom:  d.From,
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
