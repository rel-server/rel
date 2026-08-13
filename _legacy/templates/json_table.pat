
@{
  import (
    "sales-way.com/server/query"
  )
}

@func GenerateDeleteQuery(w io.Writer, sel *query.AstSelection) {
  CREATE TEMP TABLE @sel.TempDMLTableName() ON COMMIT DROP AS
  WITH @sel.UniqueTableNameWith("delete") AS (
  DELETE FROM @sel.DbTableName()
  @if sel.WhereClause != nil {
    WHERE @sel.WhereClause.String()
  }
  RETURNING *
  )
  SELECT * FROM @sel.UniqueTableNameWith("delete")
}