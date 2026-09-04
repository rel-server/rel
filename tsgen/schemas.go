package tsgen

import "strings"

// ParseSchemaList splits a comma-separated schema whitelist (http.typescript.
// schemas, or the `schemas` query param — config can't hold arrays, so both
// use the same comma-separated-string convention as e.g. http.cors.
// allowed_origins) into its individual schema names ; "" yields nil ("every
// schema").
func ParseSchemaList(s string) []string {
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
