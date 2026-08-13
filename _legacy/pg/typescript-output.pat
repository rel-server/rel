
@{
  import "strings"

  func joiner(s []string) string {
    return "\"" + strings.Join(s, "\",\"") + "\""
  }
}

@func OutputTypescriptSchema(ø *TypescriptWriter, schema string, tables []*DBTable, functions []*Function) {
  import * as s from "@@salesway/scotty"
  import { Rel @"}" from "./model"
  @{
    var needed_schemas = make(map[string]bool)
    for _, table := range tables {
      for _, rel := range table.Relationships {
        if rel.DistantRelation.Schema != table.Schema {
          needed_schemas[rel.DistantRelation.Schema] = true
        }
      }
    }
  }
  @for schema := range needed_schemas {
    import * as @schema from "./@schema"
  }
  const Multiple = true as const
  const SingleRow = false as const

  @for _, table := range tables {
    @if table.Schema != schema {
      @continue
    }
    export const @table.Name = {
      name: "@table.DbTableName()",
      fields: {
        @for _, column := range table.ColumnsInOrder {
          @if column.IsExported {
            @column.Name: @ø.SerializerFor(column.TypeSchema, column.Type, column.NotNull),
          }
        }
      @"}",
      @if len(table.ComputedColumns) > 0 {
        computed_fields: {
          @for _, column := range table.ComputedColumns {
            @if column.IsExported {
              @column.Name: @(ø.SerializerFor(column.TypeSchema, column.Type, false)).ro,
            }
          }
        @"}",
      }
      relations: () => ({
        @for _, rel := range table.Relationships {
          "@rel.PreferredForm()": Rel(@if rel.DistantRelation.Schema != table.Schema { @(rel.DistantRelation.Schema). } @rel.DistantRelation.Name, @if rel.IsMultiple { Multiple } @else { SingleRow }, [@joiner(rel.ColumnsNames)], [@joiner(rel.DistantColumnsNames)]),
        }
      @"}"),
      pk: [@for i, col := range table.Pk { @if i > 0 { @", " } "@col.Name" }],
    @"}"

  }

  @for _, function := range functions {
    @if function.Schema != schema {
      @continue
    }
    @{
      // FIXME : on les désactive pour l'instant
      // function.WriteFunctionBody(ø)
    }
  }
}
