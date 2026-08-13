package pg

import (
	_ "embed"
	"io"
	"sort"
	"strings"
)

type TypescriptWriter struct {
	io.Writer
	CurrentSchema string
	indentation   int

	IncludeDayJs  bool
	atStartOfLine bool
}

func joinWith[T any](sep string, slice []T, mapper func(T) string) string {
	mapped := make([]string, len(slice))
	for i, v := range slice {
		mapped[i] = mapper(v)
	}
	return strings.Join(mapped, sep)
}

func (w *TypescriptWriter) W(str ...string) (n int, err error) {
	n = 0

	var c byte

	for _, s := range str {
		s_ := []byte(s)

		if w.atStartOfLine && len(s_) > 0 && s_[0] != '\n' {
			w.atStartOfLine = false
			w.W(strings.Repeat(" ", w.indentation))
		}

		if n2, err := w.Writer.Write(s_); err != nil {
			return n + n2, err
		} else {
			n += n2
			if len(s_) > 0 {
				c = s_[len(s_)-1]
				if c == '\n' {
					w.atStartOfLine = true
				}
			}
		}

	}
	return n, nil
}

func (w *TypescriptWriter) Indent(s ...string) {
	w.W(s...)
	w.indentation += 2
}

func (w *TypescriptWriter) Dedent(s ...string) {
	w.indentation -= 2
	w.W(s...)
}

func (w *TypescriptWriter) BaseSerializerFor(schema string, typ string) string {
	if schema == "pg_catalog" {
		switch typ {
		case "text":
			return "str"
		case "int", "int4", "int8", "bigint", "float", "float4", "float8", "double", "decimal", "numeric":
			return "num"
		case "bool":
			return "bool"
		case "date":
			w.IncludeDayJs = true
			return "date"
		case "timestamp":
			return "timestamp"
		case "timestamptz":
			w.IncludeDayJs = true
			return "timestamptz"
		case "json", "jsonb":
			return "as_is"
		}
		return "pgTypeMap[\"" + schema + "." + typ + "\"] /* unknwown type */"
	}

	// most likely a composite
	//
	return "forward(() => s.obj(" + typ + ".fields))"
	// return "wellknown(\"" + schema + "." + typ + "\")"
}

func (w *TypescriptWriter) SerializerFor(schema, typ string, NotNull bool) string {
	is_array_type := typ[0] == '_'
	if is_array_type {
		typ = typ[1:]
	}

	var n = "s." + w.BaseSerializerFor(schema, typ)

	if is_array_type {
		n = n + ".array"
	}
	if NotNull {
		n = n + ".notnull"
	}

	return n
}

func (w *TypescriptWriter) baseTypeTypescriptName(schema string, t string) string {

	if schema == "pg_catalog" {
		switch t {
		case "text":
			return "string"
		case "int", "int4", "int8", "bigint", "float", "float4", "float8", "double", "decimal", "numeric":
			return "number"
		case "bool":
			return "boolean"
		case "date", "timestamp", "timestamptz":
			w.IncludeDayJs = true
			return "Dayjs.Dayjs"
		case "json", "jsonb":
			return "unknown"
		}

		return "s.pgTypeMap[\"" + schema + "." + t + "\"]"
	}

	return SnakeCaseClassName(t)
	// FIXME
	// return "s.pgTypeMap[\"" + schema + "." + t + "\"]"
}

func (w *TypescriptWriter) TypescriptName(schema string, t string, notNull bool) string {
	is_array_type := t[0] == '_'
	if is_array_type {
		t = t[1:]
	}

	var n = w.baseTypeTypescriptName(schema, t)
	if is_array_type {
		n = n + "[]"
	}
	if !notNull {
		n = n + " | null"
	}
	return n
}

func (c *DBColumn) TypescriptDefault(w *TypescriptWriter) string {

	if c.Default != nil {
		def := *c.Default

		if strings.HasSuffix(def, "::text") {
			return " = " + strings.Replace(def, "::text", "", 1)
		} else if def == "CURRENT_TIMESTAMP" {
			return " = Dayjs()"
		} else if def == "true" || def == "false" {
			return " = " + def
		} else if c.NotNull {
			return " = undefined! /* = " + def + " */"
		}

	}

	if !c.NotNull {
		return " = null"
	}
	return " = undefined!"
}

func (table *DBTable) WriteTableSerializer(w *TypescriptWriter) {
	w.Indent("static serializer = s.register(\"", table.DbTableName(), "\", () => s.instanceOf(", SnakeCaseClassName(table.Name), ", {\n")

	for _, fld := range table.ColumnsInOrder {
		if !fld.IsExported {
			continue
		}
		w.W(fld.Name, ": ", w.SerializerFor(fld.TypeSchema, fld.Type, fld.NotNull), ",\n")
	}

	if len(table.ComputedColumns) > 0 {
		w.W("\n")
		w.W("/* Computed columns */\n")
		for _, fld := range table.ComputedColumns {
			if !fld.IsExported {
				continue
			}
			w.W(fld.Name, ": ", w.SerializerFor(fld.TypeSchema, fld.Type, false), ".ro,\n")
		}
	}

	w.Dedent("}))\n\n")
}

func (p *DBTable) WriteTypescriptModelClass(w *TypescriptWriter) {
	base_class := "Model"
	if p.Pk != nil {
		base_class = "ModelWithPk"
	}

	w.Indent("\n\n", "export class ", SnakeCaseClassName(p.Name), " extends ", base_class, " {\n\n")
	w.W("static pg_name = \"", p.DbTableName(), "\"\n\n")

	p.WriteTableSerializer(w)

	if len(p.Relationships) > 0 {
		p.WriteTypescriptRelations(w)
	}

	if p.Pk != nil {
		w.W(
			"static pk = [",
			joinWith(", ", p.Pk, func(fld *DBColumn) string { return "\"" + fld.Name + "\"" }),
			"]\n\n",
		)

		w.W("get __pk_str() { return ")
		if len(p.Pk) > 1 {
			w.W("`", joinWith("␟", p.Pk, func(fld *DBColumn) string { return "${this." + fld.Name + "}" }), "`")
		} else {
			w.W("\"\" + this.", p.Pk[0].Name)
		}
		w.W(" }\n")

		w.W(
			"get __pk() { return {",
			joinWith(",", p.Pk, func(fld *DBColumn) string { return fld.Name + ": this." + fld.Name }),
			"} }",
			"\n\n",
		)
	}

	w.W(
		"static columns = [",
		joinWith(", ", p.ColumnsInOrder, func(fld *DBColumn) string { return "\"" + fld.Name + "\"" }),
		"] as (",
		joinWith(" | ", p.ColumnsInOrder, func(fld *DBColumn) string { return "\"" + fld.Name + "\"" }),
		")[]\n\n",
	)

	for _, fld := range p.ColumnsInOrder {
		w.W(fld.Name, ": ", w.TypescriptName(fld.TypeSchema, fld.Type, fld.NotNull), fld.TypescriptDefault(w), "\n")
	}

	if len(p.ComputedColumns) > 0 {
		w.W(
			"\n",
			"static computed_columns = [",
			joinWith(", ", p.ComputedColumns, func(fld *ComputedColumn) string { return "\"" + fld.Name + "\"" }),
			"] as (",
			joinWith(" | ", p.ComputedColumns, func(fld *ComputedColumn) string { return "\"" + fld.Name + "\"" }),
			")[]\n\n",
			"/* Computed columns */\n",
		)

		for _, fld2 := range p.ComputedColumns {
			w.W(fld2.Name, "!: ", w.TypescriptName(fld2.TypeSchema, fld2.Type, false), "\n")
		}
	}

	w.Dedent("}\n\n")
}

func (f *Function) WriteFunctionBody(w *TypescriptWriter) {
	// Do not export functions with unnamed arguments
	for _, arg := range f.Arguments {
		if arg.Name == "" {
			return
		}
	}

	w.W("export async function ", f.Name, "(args?: {")
	w.W(joinWith(",", f.Arguments, func(arg *FunctionArgument) string {
		return arg.Name + ": " + w.TypescriptName(arg.Schema, arg.Type, true)
	}))
	w.W("})")

	ser := w.SerializerFor(f.ReturnSchema, f.ReturnType, false)
	array_append := ""
	if f.ReturnsSet {
		array_append = "[]"
		ser = ser + ".array"
	}
	w.W(" : Promise<", w.TypescriptName(f.ReturnSchema, f.ReturnType, false), array_append, "> ")
	w.Indent("{\n")
	w.W("return call(\"", f.Schema, ".", f.Name, "\", ", ser, ", args)\n")

	w.Dedent("}\n\n")
}

func (table *DBTable) WriteTypescriptRelations(w *TypescriptWriter) {
	w.Indent("static relations = () => ({\n")
	for _, rel := range table.Relationships {

		is_multiple := "true as const"
		if !rel.IsMultiple {
			is_multiple = "false as const"
		}
		is_reverse := "true as const"
		if !rel.IsReverse {
			is_reverse = "false as const"
		}

		w.W(
			"\""+rel.PreferredForm()+"\": Rel(",
			SnakeCaseClassName(rel.DistantRelation.Name), ", ",
			is_multiple, ", ",
			is_reverse, ", ",
		)

		if !rel.IsReverse {
			w.W(
				"[",
				joinWith(", ", rel.Columns, func(col *DBColumn) string { return "\"" + col.Name + "\"" }),
				"], [",
				joinWith(", ", rel.DistantColumns, func(col *DBColumn) string { return "\"" + col.Name + "\"" }),
				"]",
			)
		} else {
			w.W(
				"[",
				joinWith(", ", rel.DistantColumns, func(col *DBColumn) string { return "\"" + col.Name + "\"" }),
				"], [",
				joinWith(", ", rel.Columns, func(col *DBColumn) string { return "\"" + col.Name + "\"" }),
				"]",
			)
		}

		w.W("),\n")
	}
	w.Dedent("})\n\n")
}

func WriteTypescriptFile(tables DBAllTables, schema string, functions map[string]*Function, writer io.Writer) error {
	w := TypescriptWriter{Writer: writer, atStartOfLine: true, CurrentSchema: schema}

	tbls := tables.InOrder()

	w.W(
		"import { Model, ModelWithPk, Rel, call } from \"./model\"\n",
		"import * as s from \"@salesway/scotty\"\n",
		"import Dayjs from \"dayjs\"\n",
	)
	// w.W("import { s } from \"@salesway/pgts\"\n")

	w.Indent("\n", "export interface Pg {\n")

	for _, table := range tbls {
		if table.Schema != schema {
			continue
		}
		w.W("\"", table.DbTableName(), "\": ")
		w.W(SnakeCaseClassName(table.Name), "\n")
	}
	w.Dedent("}\n")

	// Write the Models
	for _, table := range tbls {
		if table.Schema != schema {
			continue
		}
		table.WriteTypescriptModelClass(&w)
	}

	functions_in_order := make([]*Function, 0)
	for _, function := range functions {
		if function.IsExported {
			functions_in_order = append(functions_in_order, function)
		}
	}
	sort.Slice(functions_in_order, func(i, j int) bool {
		return functions_in_order[i].Name < functions_in_order[j].Name
	})

	for _, function := range functions_in_order {
		if function.Schema != schema {
			continue
		}
		// FIXME : on les désactive pour l'instant
		// function.WriteFunctionBody(&w)
	}
	return nil
}
