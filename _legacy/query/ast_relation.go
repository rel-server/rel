package query

import (
	"bytes"
)

func (rel *AstRelationshipField) OutputDistantFkWithPrefix(prefix string) string {
	buf := bytes.Buffer{}
	for i, col := range rel.ResolvedRelationShip.DistantColumnsNames {
		if i > 0 {
			buf.WriteString(", ")
		}
		buf.WriteString(prefix)
		buf.WriteString("\"")
		buf.WriteString(col)
		buf.WriteString("\"")
	}
	return buf.String()
}

func (rel *AstRelationshipField) OutputIncomingFkWithPrefix(prefix string) string {
	buf := bytes.Buffer{}
	for i, col := range rel.ResolvedRelationShip.ColumnsNames {
		if i > 0 {
			buf.WriteString(", ")
		}
		buf.WriteString(prefix)
		buf.WriteString("\"")
		buf.WriteString(col)
		buf.WriteString("\"")
	}
	return buf.String()
}
