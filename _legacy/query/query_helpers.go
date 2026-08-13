package query

func (rel *AstRelationshipField) JsonName() string {
	if rel == nil {
		return ""
	}

	multiple := ""
	if rel.ResolvedRelationShip.IsMultiple {
		multiple = "[]"
	}
	return "\":" + rel.Alias + multiple + "\""
}
