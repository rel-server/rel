package pg

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// FillViewProvenance sets Column.Source on every view/materialized view column that is a plain pass-through of
// another relation's column (`select id from t`, `select t.id as other_name from t join u ...`), with no
// SQL parsing of our own : Postgres reports, for every column of a query's result, the table and column number
// it came from (the wire protocol's RowDescription ; zero for an expression, a function call, a cast...). So each
// view's definition is prepared — parsed and analyzed by the server, never executed — and that description read.
//
// A view over another view points at the inner view's column (the FROM item is the view itself) ; follow Source
// repeatedly to get to a table.
//
// Best effort, never an error : a view that can't be prepared (a definition that doesn't re-parse under the
// introspection connection's search path, say) is logged and left with no Source, which only means its columns
// are treated as plain values.
func FillViewProvenance(db *DbInfos, conn *pgx.Conn) {
	ctx := context.Background()
	for _, view := range db.Relations {
		if !view.IsViewLike() {
			continue
		}
		// Nothing outside the user's own schemas can usefully be a source of table-local ids, and the system
		// views are numerous.
		if s := view.Identifier.Schema; s == "pg_catalog" || s == "information_schema" || s == "pg_toast" {
			continue
		}

		var def string
		if err := conn.QueryRow(ctx, "select pg_get_viewdef($1::oid)", view.PgRelId).Scan(&def); err != nil {
			log.Warn("view provenance: could not read the view definition", "schema", view.Identifier.Schema, "view", view.Identifier.Name, "error", err)
			continue
		}
		desc, err := conn.PgConn().Prepare(ctx, "", def, nil)
		if err != nil {
			log.Warn("view provenance: could not prepare the view definition", "schema", view.Identifier.Schema, "view", view.Identifier.Name, "error", err)
			continue
		}
		if len(desc.Fields) != len(view.Columns) {
			continue
		}
		for i, f := range desc.Fields {
			if f.TableOID == 0 || f.TableAttributeNumber == 0 {
				continue
			}
			src := db.GetRelation(int(f.TableOID))
			if src == nil {
				continue
			}
			for _, c := range src.Columns {
				if c.Index == int(f.TableAttributeNumber) {
					view.Columns[i].Source = c
					break
				}
			}
		}
	}
}
