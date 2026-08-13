package query

import (
	"slices"
)

// Whether a given column is in the relationship
func (rel *AstRelationshipField) IsColumnInRelationship(col string) bool {
	res := rel.ResolvedRelationShip
	if res == nil {
		return false
	}

	if res.IsReverse {
		return slices.Contains(res.DistantColumnsNames, col)
	}
	return slices.Contains(res.ColumnsNames, col)
}

func (rel *AstRelationshipField) OtherColumn(col string) string {
	if rel == nil {
		return ""
	}
	res := rel.ResolvedRelationShip
	if res == nil {
		return ""
	}

	if res.IsReverse {
		idx := slices.Index(res.DistantColumnsNames, col)
		if idx > -1 {
			return res.ColumnsNames[idx]
		}
		return ""
	}
	idx := slices.Index(res.ColumnsNames, col)
	if idx > -1 {
		return res.DistantColumnsNames[idx]
	}

	return ""
}

func (sel *AstSelection) OutgoingColumn(col string) (string, *AstRelationshipField) {
	for _, rel := range sel.ResolvedOutgoingRelationships {
		reso := rel.Relationship.ResolvedRelationShip
		for i, column := range reso.ColumnsNames {
			if column == col {
				return reso.DistantColumnsNames[i], rel.Relationship
			}
		}
	}
	return "", nil
}
