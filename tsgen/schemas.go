package tsgen

import "strings"

// parseSchemaList splits a comma-separated schema whitelist (typescript.schemas or the `schemas` query param) ;
// "" yields nil, every schema.
func parseSchemaList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
