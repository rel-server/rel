// Package tsgen implements specs/typescript.md ## database.ts's "Section 2"
// — generating Relations/Relationships/Functions/Wellknowns and their
// Table__/View__/Type__ interfaces from a *pg.DbInfos — plus the final
// concatenation into a single self-sufficient database.ts (## database.ts
// ### File layout).
package tsgen

import "strings"

// pascalCase is specs/typescript.md ## Schema interfaces' own naming rule :
// PascalCase at each "_" boundary ("room_types" -> "RoomTypes").
func pascalCase(s string) string {
	parts := strings.Split(s, "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteByte(byte(strings.ToUpper(p[:1])[0]))
		b.WriteString(p[1:])
	}
	return b.String()
}

func relationKey(schema, name string) string { return schema + "." + name }

func tableInterfaceName(schema, name string) string {
	return "Table__" + pascalCase(schema) + "__" + pascalCase(name)
}

func viewInterfaceName(schema, name string) string {
	return "View__" + pascalCase(schema) + "__" + pascalCase(name)
}

// relationInterfaceName picks Table__/View__ per specs/typescript.md ##
// Schema interfaces' "Naming" rule (same column rules either way).
func relationInterfaceName(schema, name string, isView bool) string {
	if isView {
		return viewInterfaceName(schema, name)
	}
	return tableInterfaceName(schema, name)
}

func typeInterfaceName(schema, name string) string {
	return "Type__" + pascalCase(schema) + "__" + pascalCase(name)
}
