
@{
  import (
    "sales-way.com/server/query"
  )
}

@func OutputDeleteStatement(w io.Writer, sel *query.AstSelection, do_delete bool, do_upsert bool) {

  @{
    rel := sel.ParentRelation
    for _, r := range sel.ResolvedOutgoingRelationships {
      if !r.Relationship.IsReadOnly {
        // Outgoing relationships are never deleted, no matter what was specified.
        OutputDeleteStatement(w, r.Relationship.Selection, false, do_upsert)
      }
    }
  }

  @{
    // DELETE is processed first for incoming relationships.
    for _, r := range sel.ResolvedIncomingRelationships {
      if !r.Relationship.IsReadOnly {
        // DELETE is added for upsert for incoming relationships.
        OutputDeleteStatement(w, r.Relationship.Selection, true, do_upsert)
      }
    }
  }

  @if do_delete {
    , @sel.UniqueTableNameWith("delete") AS (
      DELETE FROM @sel.DbTableName()
      WHERE
        @if sel.WhereClause != nil { (@sel.WhereClause.String()) AND }
        @if rel != nil && rel.ResolvedRelationShip.IsReverse {
          (@...`"`rel.ResolvedRelationShip.DistantColumnsNames`"`|`, `)
          IN (SELECT DISTINCT @...`"rev".`rel.ParentSelection.PrimaryKeyColumns()|`, `
          FROM @rel.ParentSelection.UniqueTableNameWith("insert") as "rev")
          AND
        }
        (@sel.PrefixPrimaryKeyWith("")) NOT IN (SELECT DISTINCT @sel.PrefixPrimaryKeyWith("dep.") FROM @sel.UniqueTableNameWith("insert") as "dep")

      RETURNING *
    )
  }

}

@func OutputInsertStatement(w io.Writer, sel *query.AstSelection, do_upsert bool) {
  @{
    for _, r := range sel.ResolvedOutgoingRelationships {
      if !r.Relationship.IsReadOnly {
        // Outgoing relationships are never deleted, no matter what was specified.
        OutputInsertStatement(w, r.Relationship.Selection, do_upsert)
      }
    }
  }

  , @sel.UniqueTableNameWith("insert_for_real") AS (
    INSERT INTO @(sel.DbTableName())(@...sel.ResolvedTable.AllColumnsNames()|`, `)
    SELECT @for|", " _, col := range sel.ResolvedTable.ColumnsInOrder {
      @col.Name
    }
    FROM @sel.UniqueTableNameWith("insert")
    @if do_upsert {
      ON CONFLICT (@sel.PrefixPrimaryKeyWith("")) DO UPDATE SET
        @for|", " _, col := range sel.ResolvedTable.ColumnsInOrder {
          @col.Name = excluded.@col.Name
        }
    }
    RETURNING *
  )

  @{
    for _, r := range sel.ResolvedIncomingRelationships {
      if !r.Relationship.IsReadOnly {
        OutputInsertStatement(w, r.Relationship.Selection, do_upsert)
      }
    }
  }
}

@func OutputDmlTable2(w io.Writer, sel *query.AstSelection, do_delete bool, do_upsert bool) {
  @{
    rel := sel.ParentRelation

    for _, r := range sel.ResolvedOutgoingRelationships {
      if !r.Relationship.IsReadOnly {
        // Outgoing relationships are never deleted, no matter what was specified.
        OutputDmlTable2(w, r.Relationship.Selection, false, do_upsert)
      }
    }
  }

  , @sel.UniqueTableNameWith("insert") AS (

    SELECT
      jt.___node_id,
      jt.___parent_id,
      @for|", " _, col := range sel.ResolvedTable.ColumnsInOrder {
        @if other_column := rel.OtherColumn(col.Name); rel != nil && !rel.IsReadOnly && other_column != "" {
          @(rel.ParentSelection.UniqueTableNameWith("insert"))."@other_column"
        } @elseif distant_column, rel_out := sel.OutgoingColumn(col.Name); rel_out != nil {
          @(rel_out.Selection.UniqueTableNameWith("insert"))."@distant_column"
        } @elseif col.Default != nil && col.NotNull && (rel == nil || !rel.IsReadOnly) {
          CASE WHEN obj."@col.Name" IS NULL THEN @(*col.Default) ELSE obj."@col.Name" END
        } @else {
          obj.@col.Name
        }
        as "@col.Name"
      }
    FROM __flat_json_temp as jt
      @for _, r := range sel.ResolvedOutgoingRelationships {
        @if !r.Relationship.IsReadOnly {
          INNER JOIN @r.Relationship.Selection.UniqueTableNameWith("insert") ON jt.___node_id = @(r.Relationship.Selection.UniqueTableNameWith("insert")).___parent_id
        }
      }
      @if rel != nil && rel.ResolvedRelationShip.IsReverse && !rel.IsReadOnly {
        INNER JOIN @rel.ParentSelection.UniqueTableNameWith("insert") ON jt.___parent_id = @(rel.ParentSelection.UniqueTableNameWith("insert")).___node_id
      }
      , jsonb_populate_record(NULL::@sel.DbTableName(), jt.json) as obj

    WHERE
      jt.___selection_index = @sel.SelectionIndex%d
  )

  @{
    for _, r := range sel.ResolvedIncomingRelationships {
      if !r.Relationship.IsReadOnly {
        // DELETE is added for upsert for incoming relationships.
        OutputDmlTable2(w, r.Relationship.Selection, do_upsert || do_delete, do_upsert)
      }
    }
  }
}

@func GenerateFlatQuery(w io.Writer, op *query.AstSelection, do_delete bool, do_upsert bool) {
  CREATE TEMP TABLE @op.TempDMLTableName() ON COMMIT DROP AS
  WITH __empty AS (SELECT true as empty)
  @{
    OutputDmlTable2(w, op, do_delete, do_upsert)
    OutputDeleteStatement(w, op, do_delete, do_upsert)
    OutputInsertStatement(w, op, do_upsert)

  }
  SELECT * FROM @(op.UniqueTableNameWith("insert_for_real"))
}
