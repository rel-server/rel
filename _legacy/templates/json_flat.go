package templates

import (
  "io"
  "fmt"
  "bytes"
  "testing"
)


import (
  "sales-way.com/server/query"
)


func OutputDeleteStatement(w io.Writer, sel *query.AstSelection, do_delete bool, do_upsert bool) {
  w.Write([]byte("\n"))
  
rel := sel.ParentRelation
for _, r := range sel.ResolvedOutgoingRelationships {
  if !r.Relationship.IsReadOnly {
    // Outgoing relationships are never deleted, no matter what was specified.
    OutputDeleteStatement(w, r.Relationship.Selection, false, do_upsert)
  }
}

  w.Write([]byte("\n"))
  
// DELETE is processed first for incoming relationships.
for _, r := range sel.ResolvedIncomingRelationships {
  if !r.Relationship.IsReadOnly {
    // DELETE is added for upsert for incoming relationships.
    OutputDeleteStatement(w, r.Relationship.Selection, true, do_upsert)
  }
}

  w.Write([]byte("\n"))

  if do_delete {
    w.Write([]byte(", "))
    w.Write([]byte(sel.UniqueTableNameWith("delete")))
    w.Write([]byte(" AS (\n  DELETE FROM "))
    w.Write([]byte(sel.DbTableName()))
    w.Write([]byte("\n  WHERE\n    "))

    if sel.WhereClause != nil {
      w.Write([]byte("("))
      w.Write([]byte(sel.WhereClause.String()))
      w.Write([]byte(") AND"))
    }
    w.Write([]byte("\n"))

    if rel != nil && rel.ResolvedRelationShip.IsReverse {
      w.Write([]byte("    ("))
      øsep5 := false
      for _, øiter4 := range rel.ResolvedRelationShip.DistantColumnsNames {
        if øsep5 {
          w.Write([]byte(`, `))
        } else {
          øsep5 = true
        }
        w.Write([]byte(`"`))
        w.Write([]byte(øiter4))
        w.Write([]byte(`"`))
      }
      w.Write([]byte(")\n    IN (SELECT DISTINCT "))
      øsep7 := false
      for _, øiter6 := range rel.ParentSelection.PrimaryKeyColumns() {
        if øsep7 {
          w.Write([]byte(`, `))
        } else {
          øsep7 = true
        }
        w.Write([]byte(`"rev".`))
        w.Write([]byte(øiter6))
      }
      w.Write([]byte("\n    FROM "))
      w.Write([]byte(rel.ParentSelection.UniqueTableNameWith("insert")))
      w.Write([]byte(" as \"rev\")\n    AND\n"))
    }
    w.Write([]byte("    ("))
    w.Write([]byte(sel.PrefixPrimaryKeyWith("")))
    w.Write([]byte(") NOT IN (SELECT DISTINCT "))
    w.Write([]byte(sel.PrefixPrimaryKeyWith("dep.")))
    w.Write([]byte(" FROM "))
    w.Write([]byte(sel.UniqueTableNameWith("insert")))
    w.Write([]byte(" as \"dep\")\n\n  RETURNING *\n)\n"))
  }
  w.Write([]byte("\n"))
}

func OutputInsertStatement(w io.Writer, sel *query.AstSelection, do_upsert bool) {
  
for _, r := range sel.ResolvedOutgoingRelationships {
  if !r.Relationship.IsReadOnly {
    // Outgoing relationships are never deleted, no matter what was specified.
    OutputInsertStatement(w, r.Relationship.Selection, do_upsert)
  }
}

  w.Write([]byte("\n, "))
  w.Write([]byte(sel.UniqueTableNameWith("insert_for_real")))
  w.Write([]byte(" AS (\n  INSERT INTO "))
  w.Write([]byte(sel.DbTableName()))
  w.Write([]byte("("))
  øsep10 := false
  for _, øiter9 := range sel.ResolvedTable.AllColumnsNames() {
    if øsep10 {
      w.Write([]byte(`, `))
    } else {
      øsep10 = true
    }
    w.Write([]byte(øiter9))
  }
  w.Write([]byte(")\n  SELECT"))

  øsep12 := false
  for _, col := range sel.ResolvedTable.ColumnsInOrder {
    if øsep12 {
      w.Write([]byte(", "))
    } else {
      øsep12 = true
    }
    w.Write([]byte("  "))
    w.Write([]byte(col.Name))
    w.Write([]byte("\n"))
  }
  w.Write([]byte("  FROM "))
  w.Write([]byte(sel.UniqueTableNameWith("insert")))
  w.Write([]byte("\n"))

  if do_upsert {
    w.Write([]byte("  ON CONFLICT ("))
    w.Write([]byte(sel.PrefixPrimaryKeyWith("")))
    w.Write([]byte(") DO UPDATE SET\n"))

    øsep14 := false
    for _, col := range sel.ResolvedTable.ColumnsInOrder {
      if øsep14 {
        w.Write([]byte(", "))
      } else {
        øsep14 = true
      }
      w.Write([]byte("    "))
      w.Write([]byte(col.Name))
      w.Write([]byte(" = excluded."))
      w.Write([]byte(col.Name))
      w.Write([]byte("\n"))
    }
  }
  w.Write([]byte("  RETURNING *\n)\n\n"))
  
for _, r := range sel.ResolvedIncomingRelationships {
  if !r.Relationship.IsReadOnly {
    OutputInsertStatement(w, r.Relationship.Selection, do_upsert)
  }
}

}

func OutputDmlTable2(w io.Writer, sel *query.AstSelection, do_delete bool, do_upsert bool) {
  
rel := sel.ParentRelation

for _, r := range sel.ResolvedOutgoingRelationships {
  if !r.Relationship.IsReadOnly {
    // Outgoing relationships are never deleted, no matter what was specified.
    OutputDmlTable2(w, r.Relationship.Selection, false, do_upsert)
  }
}

  w.Write([]byte("\n, "))
  w.Write([]byte(sel.UniqueTableNameWith("insert")))
  w.Write([]byte(" AS (\n\n  SELECT\n    jt.___node_id,\n    jt.___parent_id,\n"))

  øsep16 := false
  for _, col := range sel.ResolvedTable.ColumnsInOrder {
    if øsep16 {
      w.Write([]byte(", "))
    } else {
      øsep16 = true
    }

    if other_column := rel.OtherColumn(col.Name); rel != nil && !rel.IsReadOnly && other_column != "" {
      w.Write([]byte("    "))
      w.Write([]byte(rel.ParentSelection.UniqueTableNameWith("insert")))
      w.Write([]byte(".\""))
      w.Write([]byte(other_column))
      w.Write([]byte("\"\n"))
    } else if distant_column, rel_out := sel.OutgoingColumn(col.Name); rel_out != nil {
      w.Write([]byte("    "))
      w.Write([]byte(rel_out.Selection.UniqueTableNameWith("insert")))
      w.Write([]byte(".\""))
      w.Write([]byte(distant_column))
      w.Write([]byte("\"\n"))
    } else if col.Default != nil && col.NotNull && (rel == nil || !rel.IsReadOnly) {
      w.Write([]byte("    CASE WHEN obj.\""))
      w.Write([]byte(col.Name))
      w.Write([]byte("\" IS NULL THEN "))
      w.Write([]byte(*col.Default))
      w.Write([]byte(" ELSE obj.\""))
      w.Write([]byte(col.Name))
      w.Write([]byte("\" END\n"))
    } else {
      w.Write([]byte("    obj."))
      w.Write([]byte(col.Name))
      w.Write([]byte("\n"))
    }
    w.Write([]byte("    as \""))
    w.Write([]byte(col.Name))
    w.Write([]byte("\"\n"))
  }
  w.Write([]byte("  FROM __flat_json_temp as jt\n"))

  for _, r := range sel.ResolvedOutgoingRelationships {

    if !r.Relationship.IsReadOnly {
      w.Write([]byte("    INNER JOIN "))
      w.Write([]byte(r.Relationship.Selection.UniqueTableNameWith("insert")))
      w.Write([]byte(" ON jt.___node_id = "))
      w.Write([]byte(r.Relationship.Selection.UniqueTableNameWith("insert")))
      w.Write([]byte(".___parent_id\n"))
    }
  }

  if rel != nil && rel.ResolvedRelationShip.IsReverse && !rel.IsReadOnly {
    w.Write([]byte("    INNER JOIN "))
    w.Write([]byte(rel.ParentSelection.UniqueTableNameWith("insert")))
    w.Write([]byte(" ON jt.___parent_id = "))
    w.Write([]byte(rel.ParentSelection.UniqueTableNameWith("insert")))
    w.Write([]byte(".___node_id\n"))
  }
  w.Write([]byte("    , jsonb_populate_record(NULL::"))
  w.Write([]byte(sel.DbTableName()))
  w.Write([]byte(", jt.json) as obj\n\n  WHERE\n    jt.___selection_index = "))
  w.Write([]byte(fmt.Sprintf("%d", sel.SelectionIndex)))
  w.Write([]byte("\n)\n\n"))
  
for _, r := range sel.ResolvedIncomingRelationships {
  if !r.Relationship.IsReadOnly {
    // DELETE is added for upsert for incoming relationships.
    OutputDmlTable2(w, r.Relationship.Selection, do_upsert || do_delete, do_upsert)
  }
}

}

func GenerateFlatQuery(w io.Writer, op *query.AstSelection, do_delete bool, do_upsert bool) {
  w.Write([]byte("CREATE TEMP TABLE "))
  w.Write([]byte(op.TempDMLTableName()))
  w.Write([]byte(" ON COMMIT DROP AS\nWITH __empty AS (SELECT true as empty)\n"))
  
OutputDmlTable2(w, op, do_delete, do_upsert)
OutputDeleteStatement(w, op, do_delete, do_upsert)
OutputInsertStatement(w, op, do_upsert)


  w.Write([]byte("SELECT * FROM "))
  w.Write([]byte(op.UniqueTableNameWith("insert_for_real")))
  w.Write([]byte("\n"))
}


// just so that imports are not removed
func TestInclude1(t *testing.T) {
  var buf bytes.Buffer
  var st fmt.Stringer = &buf
  var w io.Writer = &buf
  _, _ = w.Write([]byte(st.String()))
}
