// This file implements specs/http-content.md ### Upload destinations'
// discovery half : pairing a <name>__prepare/<name> function pair into
// one Route.
package route

import (
	"regexp"
	"strings"

	"github.com/ceymard/rel/pg"
)

const prepareSuffix = "__prepare"

// isPrepareSuffixed reports whether name ends with the reserved __prepare
// suffix, case-insensitively.
func isPrepareSuffixed(name string) bool {
	return len(name) > len(prepareSuffix) && strings.EqualFold(name[len(name)-len(prepareSuffix):], prepareSuffix)
}

// prepareBaseName strips the reserved __prepare suffix.
func prepareBaseName(name string) string {
	return name[:len(name)-len(prepareSuffix)]
}

// discoverUploadRoutes : both halves of a pair must be present to become
// a route ; an orphan either direction gets a non-fatal warning.
func discoverUploadRoutes(db *pg.DbInfos, reg *Registry, reqType, uploadType, respType *pg.Type, allowedRoutes *regexp.Regexp) {
	if reqType == nil || uploadType == nil || respType == nil {
		return
	}

	prepares := map[string]map[string]*pg.Function{}
	mandatories := map[string]map[string]*pg.Function{}

	for _, fn := range db.Functions {
		if !fn.IsPlainFunction() || strings.HasPrefix(fn.Identifier.Name, "_") {
			continue
		}
		if allowedRoutes != nil && !allowedRoutes.MatchString(fn.Identifier.String()) {
			continue
		}
		inTypes := inputArgumentTypes(fn)
		if len(inTypes) != fn.PgNargs || len(inTypes) != 2 {
			continue
		}
		schema := fn.Identifier.Schema

		if isPrepareSuffixed(fn.Identifier.Name) {
			// (req RelHttpRequest, part jsonb) returns RelUpload
			if inTypes[0] != reqType || !isJsonbType(inTypes[1]) || fn.ReturnType != uploadType {
				continue
			}
			base := prepareBaseName(fn.Identifier.Name)
			if prepares[schema] == nil {
				prepares[schema] = map[string]*pg.Function{}
			}
			prepares[schema][base] = fn
			continue
		}

		// (req RelHttpRequest, upload RelUpload) returns RelHttpResponse
		if inTypes[0] != reqType || inTypes[1] != uploadType || fn.ReturnType != respType {
			continue
		}
		if mandatories[schema] == nil {
			mandatories[schema] = map[string]*pg.Function{}
		}
		mandatories[schema][fn.Identifier.Name] = fn
	}

	// Union of every (schema, base) seen on either side, so an orphan in
	// EITHER direction gets its own warning.
	seen := map[string]map[string]bool{}
	for schema, byBase := range prepares {
		if seen[schema] == nil {
			seen[schema] = map[string]bool{}
		}
		for base := range byBase {
			seen[schema][base] = true
		}
	}
	for schema, byBase := range mandatories {
		if seen[schema] == nil {
			seen[schema] = map[string]bool{}
		}
		for base := range byBase {
			seen[schema][base] = true
		}
	}

	for schema, bases := range seen {
		for base := range bases {
			prep := prepares[schema][base]
			mand := mandatories[schema][base]
			switch {
			case prep != nil && mand != nil:
				if prep.IsVolatile {
					log.Warn("route: upload __prepare function is declared volatile, but rel runs it inside a real BEGIN READ ONLY transaction regardless",
						"schema", schema, "function", prep.Identifier.String())
				}
				if reg.routes[schema] == nil {
					reg.routes[schema] = map[string]map[string]Route{}
				}
				if reg.routes[schema][base] == nil {
					reg.routes[schema][base] = map[string]Route{}
				}
				if existing, dup := reg.routes[schema][base][""]; dup {
					log.Error("route: ambiguous route, skipping upload pair", "schema", schema, "function", base,
						"first", existing.Function.Identifier.String(), "second", mand.Identifier.String())
					continue
				}
				reg.routes[schema][base][""] = Route{
					Function:        mand,
					PrepareFunction: prep,
					IsUpload:        true,
				}
			case prep != nil && mand == nil:
				log.Warn("route: upload __prepare function has no matching mandatory sibling function, not a route",
					"schema", schema, "function", prep.Identifier.String())
			case prep == nil && mand != nil:
				log.Warn("route: upload mandatory function has no matching __prepare sibling function, not a route",
					"schema", schema, "function", mand.Identifier.String())
			}
		}
	}
}
