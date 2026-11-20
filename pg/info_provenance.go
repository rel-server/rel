package pg

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// FillViewProvenance sets Column.Source on every view column that is a plain pass-through of another relation's
// column, by preparing (never executing) each view's definition and reading the source table and column the
// server reports for each result column.
//
// A view over another view points at the inner view's column.
//
// Best effort : a view that can't be prepared (its definition does not re-parse under the introspection
// search path, say) is logged and left with no Source.
func FillViewProvenance(db *DbInfos, conn *pgx.Conn) {
	ctx := context.Background()
	for _, view := range db.Relations {
		if !view.IsViewLike() {
			continue
		}
		// System views are numerous and never a source of table-local ids.
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
					view.Columns[i].SourceRelation = src
					break
				}
			}
		}
	}
}
