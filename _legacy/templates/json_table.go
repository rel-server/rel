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


func GenerateDeleteQuery(w io.Writer, sel *query.AstSelection) {
  w.Write([]byte("CREATE TEMP TABLE "))
  w.Write([]byte(sel.TempDMLTableName()))
  w.Write([]byte(" ON COMMIT DROP AS\nWITH "))
  w.Write([]byte(sel.UniqueTableNameWith("delete")))
  w.Write([]byte(" AS (\nDELETE FROM "))
  w.Write([]byte(sel.DbTableName()))
  w.Write([]byte("\n"))

  if sel.WhereClause != nil {
    w.Write([]byte("WHERE "))
    w.Write([]byte(sel.WhereClause.String()))
    w.Write([]byte("\n"))
  }
  w.Write([]byte("RETURNING *\n)\nSELECT * FROM "))
  w.Write([]byte(sel.UniqueTableNameWith("delete")))
  w.Write([]byte("\n"))
}

// just so that imports are not removed
func TestInclude2(t *testing.T) {
  var buf bytes.Buffer
  var st fmt.Stringer = &buf
  var w io.Writer = &buf
  _, _ = w.Write([]byte(st.String()))
}
