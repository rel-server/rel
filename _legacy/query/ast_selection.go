package query

import (
	"bytes"
	"iter"
	"slices"
	"strconv"
)

func (sel *AstSelection) OutgoingRelationshipForColumn(col string) *ResolvedRelationship {
	for _, rel := range sel.ResolvedOutgoingRelationships {
		if slices.Contains(rel.Relationship.ResolvedRelationShip.ColumnsNames, col) {
			return &rel
		}
	}
	return nil
}

func (sel *AstSelection) DbTableName() string {
	return sel.ResolvedTable.DbTableName()
}

func (sel *AstSelection) UniqueTableNameWith(str string) string {
	return "\"" + sel.ResolvedTable.Name + "_" + str + "_" + strconv.Itoa(sel.SelectionIndex) + "\""
}

func (sel *AstSelection) Relations() iter.Seq[ResolvedRelationship] {
	return func(yield func(ResolvedRelationship) bool) {
		for _, rel := range sel.ResolvedOutgoingRelationships {
			if !yield(rel) {
				return
			}
		}
		for _, rel := range sel.ResolvedIncomingRelationships {
			if !yield(rel) {
				return
			}
		}
	}
}

func (sel *AstSelection) PrimaryKeyColumns() []string {
	result := make([]string, 0)
	for _, col := range sel.ResolvedTable.Pk {
		result = append(result, col.Name)
	}
	return result
}

func (sel *AstSelection) PrefixPrimaryKeyWith(prefix string) string {
	buf := bytes.Buffer{}
	for i, col := range sel.ResolvedTable.Pk {
		if i > 0 {
			buf.WriteString(", ")
		}
		buf.WriteString(prefix)
		buf.WriteString("\"")
		buf.WriteString(col.Name)
		buf.WriteString("\"")
	}
	return buf.String()
}

func (sel *AstSelection) ComparePrimaryKeyWith(prefix string) string {
	buf := bytes.Buffer{}
	for i, col := range sel.ResolvedTable.Pk {
		if i > 0 {
			buf.WriteString(", ")
		}
		buf.WriteString(prefix)
		buf.WriteString(".\"")
		buf.WriteString(col.Name)
		buf.WriteString("\" = " + sel.DbTableName() + ".\"" + col.Name + "\"")
	}
	return buf.String()
}
