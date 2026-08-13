package pg

import (
  "io"
  "fmt"
  "bytes"
  "testing"
)


import "strings"

func joiner(s []string) string {
  return "\"" + strings.Join(s, "\",\"") + "\""
}


func OutputTypescriptSchema(ø *TypescriptWriter, schema string, tables []*DBTable, functions []*Function) {
  ø.Write([]byte("import * as s from \"@salesway/scotty\"\nimport { Rel "))
  ø.Write([]byte("}"))
  ø.Write([]byte(" from \"./model\"\n"))
  
var needed_schemas = make(map[string]bool)
for _, table := range tables {
  for _, rel := range table.Relationships {
    if rel.DistantRelation.Schema != table.Schema {
      needed_schemas[rel.DistantRelation.Schema] = true
    }
  }
}


  for schema := range needed_schemas {
    ø.Write([]byte("import * as "))
    ø.Write([]byte(schema))
    ø.Write([]byte(" from \"./"))
    ø.Write([]byte(schema))
    ø.Write([]byte("\"\n"))
  }
  ø.Write([]byte("const Multiple = true as const\nconst SingleRow = false as const\n\n"))

  for _, table := range tables {

    if table.Schema != schema {
      continue
      ø.Write([]byte("\n"))
    }
    ø.Write([]byte("export const "))
    ø.Write([]byte(table.Name))
    ø.Write([]byte(" = {\n  name: \""))
    ø.Write([]byte(table.DbTableName()))
    ø.Write([]byte("\",\n  fields: {\n"))

    for _, column := range table.ColumnsInOrder {

      if column.IsExported {
        ø.Write([]byte("    "))
        ø.Write([]byte(column.Name))
        ø.Write([]byte(": "))
        ø.Write([]byte(ø.SerializerFor(column.TypeSchema, column.Type, column.NotNull)))
        ø.Write([]byte(",\n"))
      }
    }
    ø.Write([]byte("  "))
    ø.Write([]byte("}"))
    ø.Write([]byte(",\n"))

    if len(table.ComputedColumns) > 0 {
      ø.Write([]byte("  computed_fields: {\n"))

      for _, column := range table.ComputedColumns {

        if column.IsExported {
          ø.Write([]byte("    "))
          ø.Write([]byte(column.Name))
          ø.Write([]byte(": "))
          ø.Write([]byte(ø.SerializerFor(column.TypeSchema, column.Type, false)))
          ø.Write([]byte(".ro,\n"))
        }
      }
      ø.Write([]byte("  "))
      ø.Write([]byte("}"))
      ø.Write([]byte(",\n"))
    }
    ø.Write([]byte("  relations: () => ({\n"))

    for _, rel := range table.Relationships {
      ø.Write([]byte("    \""))
      ø.Write([]byte(rel.PreferredForm()))
      ø.Write([]byte("\": Rel("))

      if rel.DistantRelation.Schema != table.Schema {
        ø.Write([]byte(rel.DistantRelation.Schema))
        ø.Write([]byte("."))
      }
      ø.Write([]byte(rel.DistantRelation.Name))
      ø.Write([]byte(", "))

      if rel.IsMultiple {
        ø.Write([]byte("Multiple"))
      } else {
        ø.Write([]byte("SingleRow"))
      }
      ø.Write([]byte(", ["))
      ø.Write([]byte(joiner(rel.ColumnsNames)))
      ø.Write([]byte("], ["))
      ø.Write([]byte(joiner(rel.DistantColumnsNames)))
      ø.Write([]byte("]),\n"))
    }
    ø.Write([]byte("  "))
    ø.Write([]byte("}"))
    ø.Write([]byte("),\n  pk: ["))

    for i, col := range table.Pk {

      if i > 0 {
        ø.Write([]byte(", "))
      }
      ø.Write([]byte("\""))
      ø.Write([]byte(col.Name))
      ø.Write([]byte("\""))
    }
    ø.Write([]byte("],\n"))
    ø.Write([]byte("}"))
    ø.Write([]byte("\n\n"))
  }
  ø.Write([]byte("\n"))

  for _, function := range functions {

    if function.Schema != schema {
      continue
      ø.Write([]byte("\n"))
    }
    
// FIXME : on les désactive pour l'instant
// function.WriteFunctionBody(ø)

  }
}


// just so that imports are not removed
func TestInclude0(t *testing.T) {
  var buf bytes.Buffer
  var st fmt.Stringer = &buf
  var w io.Writer = &buf
  _, _ = w.Write([]byte(st.String()))
}
