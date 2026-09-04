// Package route implements specs/route.md's "# HTTP" route
// functions : Postgres functions directly callable at
// /route/{schema}/{function}, gated by the full JWT lifecycle (Verify/
// Check/Renew/Apply-role from the jwt package, mint/logout from a route
// function's own RelHttpResponse.jwt).
package route

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/logging"
	"github.com/ceymard/rel/pg"
	"github.com/jackc/pgx/v5"
)

// log is this package's module-tagged logger, per specs/logging.md
// ## Domain scoping.
var log = logging.For("route")

// Route is one discovered route function. Function is the underlying
// Postgres function. MimeType is non-empty only when this route's return
// type is a mimetype domain (a domain over bytea or text whose own name
// contains "/"), in which case that name is the Content-Type a call to
// this route responds with. AcceptsFiles/AcceptsPartsHeaders record which
// of specs/route.md ## Request bodies' four signature shapes this route
// matched — (), (req), (req, files bytea[]), or (req, files bytea[],
// parts_headers jsonb).
type Route struct {
	Function            *pg.Function
	MimeType            string
	AcceptsFiles        bool
	AcceptsPartsHeaders bool

	// AnonymousAuthorized reports whether the anonymous role could reach
	// this route at the time the registry was built (specs/route.md
	// ## Anonymous route authorization). Meaningless when anonymous access
	// is disabled (db.AnonymousRoleExists false) — callers must check that
	// first. For an upload route (IsUpload), this is true only when BOTH
	// halves are reachable (specs/http-content.md ### Upload destinations).
	AnonymousAuthorized bool

	// IsUpload is true for a specs/http-content.md ### Upload destinations
	// route : a discovered <name>__prepare/<name> pair. Function is the
	// mandatory (unsuffixed) half ; PrepareFunction is the __prepare half.
	// MimeType/AcceptsFiles/AcceptsPartsHeaders are always zero-valued here.
	IsUpload        bool
	PrepareFunction *pg.Function
}

// Registry is every discovered route function, keyed schema → base
// function name → HTTP verb (uppercased, "" for an unsuffixed function
// answering every verb with no more specific match).
type Registry struct {
	routes map[string]map[string]map[string]Route
}

// Lookup finds the Route for schema/function/method, falling back to the
// unsuffixed ("") entry when no verb-specific one matches (specs/route.md
// ## HTTP). The second return is false if nothing matches either way.
func (r *Registry) Lookup(schema, function, method string) (Route, bool) {
	byFunc := r.routes[schema]
	if byFunc == nil {
		return Route{}, false
	}
	byVerb := byFunc[function]
	if byVerb == nil {
		return Route{}, false
	}
	if route, ok := byVerb[strings.ToUpper(method)]; ok {
		return route, true
	}
	route, ok := byVerb[""]
	return route, ok
}

// BuildRegistry scans db.Functions for route functions per specs/route.md
// ## HTTP's signature rule, then runs ## Anonymous route authorization
// against db.Pool to populate each Route.AnonymousAuthorized and to warn
// about any route reachable by PUBLIC. http.functions.allowed_routes
// additionally restricts which functions are discovered.
//
// RequestDomainName/ResponseDomainName not resolving to a type is not
// fatal — it only narrows which routes can be discovered (specs/route.md
// ## Configuration). A domain name ambiguous across schemas IS fatal, and
// is the only error this function returns.
func BuildRegistry(db *pg.DbInfos, cfg *config.Config) (*Registry, error) {
	reqType, err := resolveDomainByName(db, cfg.Http.RequestDomainName)
	if err != nil {
		return nil, err
	}
	if reqType == nil {
		log.Warn("route: request domain not found, no argument-taking route functions will be discovered", "name", cfg.Http.RequestDomainName)
	}
	respType, err := resolveDomainByName(db, cfg.Http.ResponseDomainName)
	if err != nil {
		return nil, err
	}
	if respType == nil {
		log.Warn("route: response domain not found, no RelHttpResponse route functions will be discovered", "name", cfg.Http.ResponseDomainName)
	}
	uploadType, err := resolveDomainByName(db, cfg.Http.UploadDomainName)
	if err != nil {
		return nil, err
	}
	if uploadType == nil {
		log.Warn("route: upload domain not found, no upload-destination route functions will be discovered", "name", cfg.Http.UploadDomainName)
	}

	var allowedRoutes *regexp.Regexp
	if cfg.Http.Functions.AllowedRoutes != "" {
		allowedRoutes, err = regexp.Compile(cfg.Http.Functions.AllowedRoutes)
		if err != nil {
			return nil, fmt.Errorf("route: http.functions.allowed_routes: invalid regexp: %w", err)
		}
	}

	reg := &Registry{routes: map[string]map[string]map[string]Route{}}
	// Tracks a key once conflicted so a third+ duplicate can't silently
	// become sole owner after the second conflict clears the map entry.
	ambiguous := map[string]bool{}
	for _, fn := range db.Functions {
		if !fn.IsPlainFunction() {
			continue
		}
		if strings.HasPrefix(fn.Identifier.Name, "_") {
			continue
		}
		if isPrepareSuffixed(fn.Identifier.Name) {
			// specs/http-content.md ### Upload destinations : __prepare is
			// reserved, handled only by discoverUploadRoutes below.
			continue
		}
		if allowedRoutes != nil && !allowedRoutes.MatchString(fn.Identifier.String()) {
			continue
		}

		mimeType, acceptsFiles, acceptsPartsHeaders, ok := matchesRouteShape(fn, reqType, respType)
		if !ok {
			continue
		}

		base, verb := splitVerb(fn.Identifier.Name)
		schema := fn.Identifier.Schema
		if reg.routes[schema] == nil {
			reg.routes[schema] = map[string]map[string]Route{}
		}
		if reg.routes[schema][base] == nil {
			reg.routes[schema][base] = map[string]Route{}
		}
		key := schema + "\x00" + base + "\x00" + verb
		if ambiguous[key] {
			log.Error("route: ambiguous route, skipping", "schema", schema, "function", base, "verb", verb,
				"target", fn.Identifier.String())
			continue
		}
		if existing, dup := reg.routes[schema][base][verb]; dup {
			log.Error("route: ambiguous route, skipping both", "schema", schema, "function", base, "verb", verb,
				"first", existing.Function.Identifier.String(), "second", fn.Identifier.String())
			// Delete the first entry too, not just skip the second — the log
			// line says "skipping both", so both must actually be excluded.
			delete(reg.routes[schema][base], verb)
			ambiguous[key] = true
			continue
		}
		reg.routes[schema][base][verb] = Route{
			Function:            fn,
			MimeType:            mimeType,
			AcceptsFiles:        acceptsFiles,
			AcceptsPartsHeaders: acceptsPartsHeaders,
		}
	}

	discoverUploadRoutes(db, reg, reqType, uploadType, respType, allowedRoutes)

	if err := applyAnonymousAuthorization(db, cfg, reg); err != nil {
		return nil, err
	}

	return reg, nil
}

// anonPublicPriv is one route function's cached anon/PUBLIC reachability,
// per specs/route.md ## Anonymous route authorization.
type anonPublicPriv struct {
	anonOK   bool
	publicOK bool
}

// applyAnonymousAuthorization sets each Route's AnonymousAuthorized and
// warns for every PUBLIC-reachable route ; mutates reg.routes in place.
func applyAnonymousAuthorization(db *pg.DbInfos, cfg *config.Config, reg *Registry) error {
	var oids []int
	for _, byFunc := range reg.routes {
		for _, byVerb := range byFunc {
			for _, route := range byVerb {
				oids = append(oids, route.Function.PgOid)
				if route.IsUpload {
					oids = append(oids, route.PrepareFunction.PgOid)
				}
			}
		}
	}
	if len(oids) == 0 {
		return nil
	}

	// has_*_privilege errors on a role absent from pg_roles, so the
	// anon-role half is skipped (hardcoded) when AnonymousRoleExists is false.
	query := `
		select f.oid::integer as fn_oid,
		       has_schema_privilege($1, f.pronamespace, 'USAGE') and has_function_privilege($1, f.oid, 'EXECUTE') as anon_ok,
		       has_schema_privilege('public', f.pronamespace, 'USAGE') and has_function_privilege('public', f.oid, 'EXECUTE') as public_ok
		from pg_proc f
		where f.oid = any($2::oid[])
	`
	anonArg := cfg.Pg.Query.AnonymousRole
	if !db.AnonymousRoleExists {
		query = `
			select f.oid::integer as fn_oid,
			       false as anon_ok,
			       has_schema_privilege('public', f.pronamespace, 'USAGE') and has_function_privilege('public', f.oid, 'EXECUTE') as public_ok
			from pg_proc f
			where f.oid = any($1::oid[])
		`
	}

	privByOid := make(map[int]anonPublicPriv, len(oids))
	var rows pgx.Rows
	var err error
	if db.AnonymousRoleExists {
		rows, err = db.Pool.Query(context.Background(), query, anonArg, oids)
	} else {
		rows, err = db.Pool.Query(context.Background(), query, oids)
	}
	if err != nil {
		return fmt.Errorf("route: querying anonymous/PUBLIC route authorization: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var oid int
		var priv anonPublicPriv
		if err := rows.Scan(&oid, &priv.anonOK, &priv.publicOK); err != nil {
			return fmt.Errorf("route: scanning anonymous/PUBLIC route authorization: %w", err)
		}
		privByOid[oid] = priv
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("route: reading anonymous/PUBLIC route authorization: %w", err)
	}

	for schema, byFunc := range reg.routes {
		for base, byVerb := range byFunc {
			for verb, route := range byVerb {
				priv := privByOid[route.Function.PgOid]
				anonOK, publicOK := priv.anonOK, priv.publicOK
				if route.IsUpload {
					// specs/http-content.md ### Upload destinations : must
					// pass for both functions, fail-closed on either.
					prepPriv := privByOid[route.PrepareFunction.PgOid]
					anonOK = anonOK && prepPriv.anonOK
					publicOK = publicOK && prepPriv.publicOK
				}
				route.AnonymousAuthorized = db.AnonymousRoleExists && anonOK
				byVerb[verb] = route
				if publicOK {
					log.Warn("route: route is executable by PUBLIC", "schema", schema, "function", base, "verb", verb,
						"target", route.Function.Identifier.String())
				}
			}
		}
	}
	return nil
}

// matchesRouteShape checks fn against route.md's four argument shapes,
// matched by type sequence not parameter name.
func matchesRouteShape(fn *pg.Function, reqType, respType *pg.Type) (string, bool, bool, bool) {
	inTypes := inputArgumentTypes(fn)
	// An INOUT/VARIADIC parameter inflates PgNargs beyond len(inTypes)
	// without matching any shape below, so the two must stay equal.
	if len(inTypes) != fn.PgNargs {
		return "", false, false, false
	}

	var acceptsFiles, acceptsPartsHeaders bool
	switch len(inTypes) {
	case 0:
	case 1:
		if reqType == nil || inTypes[0] != reqType {
			return "", false, false, false
		}
	case 2:
		if reqType == nil || inTypes[0] != reqType || !isBytesArrayType(inTypes[1]) {
			return "", false, false, false
		}
		acceptsFiles = true
	case 3:
		if reqType == nil || inTypes[0] != reqType || !isBytesArrayType(inTypes[1]) || !isJsonbType(inTypes[2]) {
			return "", false, false, false
		}
		acceptsFiles = true
		acceptsPartsHeaders = true
	default:
		return "", false, false, false
	}

	if respType != nil && fn.ReturnType == respType {
		return "", acceptsFiles, acceptsPartsHeaders, true
	}
	if fn.ReturnType.IsDomain() && fn.ReturnType.Underlying() != nil &&
		isMimeTypeUnderlying(fn.ReturnType.Underlying()) &&
		strings.Contains(fn.ReturnType.PgIdentifier.Name, "/") {
		return fn.ReturnType.PgIdentifier.Name, acceptsFiles, acceptsPartsHeaders, true
	}
	return "", false, false, false
}

// isMimeTypeUnderlying is specs/route.md's mimetype-domain rule : a domain
// over bytea or over text.
func isMimeTypeUnderlying(underlying *pg.Type) bool {
	name := underlying.PgIdentifier.String()
	return name == "pg_catalog.bytea" || name == "pg_catalog.text"
}

// isBytesArrayType reports whether t is exactly bytea[], matched by type
// rather than through a domain wrapper.
func isBytesArrayType(t *pg.Type) bool {
	return t != nil && t.IsArray() && t.ElementType != nil && t.ElementType.PgIdentifier.String() == "pg_catalog.bytea"
}

// isJsonbType reports whether t is exactly pg_catalog.jsonb.
func isJsonbType(t *pg.Type) bool {
	return t != nil && t.PgIdentifier.String() == "pg_catalog.jsonb"
}

// inputArgumentTypes returns the Type of each of fn's IsIn() arguments, in
// positional order.
func inputArgumentTypes(fn *pg.Function) []*pg.Type {
	types := make([]*pg.Type, 0, fn.PgNargs)
	for i := range fn.Arguments {
		if fn.Arguments[i].IsIn() {
			types = append(types, fn.Arguments[i].Type)
		}
	}
	return types
}

// splitVerb splits a function name's optional "__VERB" suffix into (base,
// uppercased verb) ; verb is "" when unsuffixed.
func splitVerb(name string) (string, string) {
	idx := strings.LastIndex(name, "__")
	if idx < 0 {
		return name, ""
	}
	verb := name[idx+2:]
	if verb == "" {
		return name, ""
	}
	return name[:idx], strings.ToUpper(verb)
}

// resolveDomainByName : "schema.name" is explicit ; a bare name searches
// every schema, and more than one match is a fatal ambiguity.
func resolveDomainByName(db *pg.DbInfos, name string) (*pg.Type, error) {
	if name == "" {
		return nil, nil
	}
	if schema, bare, ok := strings.Cut(name, "."); ok {
		for i := range db.Types {
			t := &db.Types[i]
			if t.PgIdentifier.Schema == schema && t.PgIdentifier.Name == bare {
				return t, nil
			}
		}
		return nil, nil
	}

	var matches []*pg.Type
	for i := range db.Types {
		t := &db.Types[i]
		if t.PgIdentifier.Name == name {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return matches[0], nil
	default:
		schemas := make([]string, len(matches))
		for i, t := range matches {
			schemas[i] = t.PgIdentifier.Schema
		}
		return nil, fmt.Errorf("route: domain name %q is ambiguous across schemas %v — configure it schema-qualified", name, schemas)
	}
}
