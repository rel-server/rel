package query

import (
	"strconv"
)

func (sel *AstSelection) JsonTableName() string {
	return "\"_json_" + sel.ResolvedTable.Name + strconv.Itoa(sel.SelectionIndex) + "\""
}

func (sel *AstSelection) DMLTableName() string {
	return "\"" + sel.ResolvedTable.Name + "_dml_" + strconv.Itoa(sel.SelectionIndex) + "\""
}

func (sel *AstSelection) DMLTableNameDelete() string {
	return "\"" + sel.ResolvedTable.Name + "_delete_" + strconv.Itoa(sel.SelectionIndex) + "\""
}

func (sel *AstSelection) TempDMLTableName() string {
	return "\"" + sel.ResolvedTable.Name + "_temp_dml_" + strconv.Itoa(sel.SelectionIndex) + "\""
}
