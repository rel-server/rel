// Package rpc implements specs/jwt-roles-and-http.md's "# HTTP" route
// functions : Postgres functions directly callable at
// /rpc/{schema}/{function}, gated by the full JWT lifecycle (Verify/
// Check/Renew/Apply-role from the jwt package, mint/logout from a route
// function's own RelHttpResponse.jwt).
package rpc

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/pg"
	"github.com/jackc/pgx/v5"
)

// Route is one discovered route function : Function is the underlying
// Postgres function ; MimeType is non-empty only when this route's return
// type is a mimetype domain (a domain over bytea or text whose own name
// contains "/"), in which case that name (route.MimeType) IS the
// Content-Type a call to this route responds with. AcceptsFiles/
// AcceptsPartsHeaders record which of ## Request bodies' four signature
// shapes this route matched — (), (req), (req, files bytea[]), or
// (req, files bytea[], parts_headers jsonb) — driving how invokeRoute
// calls it and how the handler must parse the incoming request body.
type Route struct {
	Function            *pg.Function
	MimeType            string
	AcceptsFiles        bool
	AcceptsPartsHeaders bool

	// AnonymousAuthorized is specs/jwt-roles-and-http.md "# HTTP ##
	// Anonymous route authorization" : true iff, at the time the registry
	// was built, the anonymous role could actually reach this route —
	// has_schema_privilege(anon, schema, 'USAGE') AND
	// has_function_privilege(anon, function, 'EXECUTE'), both conjuncts.
	// Meaningless when anonymous access is disabled altogether (db.
	// AnonymousRoleExists false) — callers must check that first, since an
	// anonymous role that doesn't exist was never queried against here.
	AnonymousAuthorized bool
}

// Registry is every discovered route function, keyed schema → base
// function name → HTTP verb (uppercased, "" for an unsuffixed function
// answering every verb with no more specific match).
type Registry struct {
	routes map[string]map[string]map[string]Route
}

// Lookup finds the Route for schema/function/method, falling back to the
// unsuffixed ("") entry when no verb-specific one matches — Lifecycle's own
// "If a verb-suffixed function is defined alongside an unsuffixed one, the
// unsuffixed function is the fallback for verbs with no specific match."
// The second return is false if nothing matches either way.
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

// BuildRegistry scans db.Functions for route functions per ## HTTP's own
// signature rule : name doesn't start with "_", takes 0 arguments or one
// argument of the RequestDomainName domain, and returns either the
// ResponseDomainName domain or a mimetype domain (bytea-based, name
// contains "/"). http.functions.allowed_routes additionally restricts.
//
// A domain name that doesn't resolve to exactly one type is NOT fatal for
// ResponseDomainName specifically — spec : "Not an error if it doesn't
// exist, but rel will warn, since without it no authentication flow can
// work" — mimetype-domain routes can still be discovered even without it.
// The same non-fatal treatment is extended to RequestDomainName (an unset/
// unresolvable request domain just means no 1-argument route functions can
// ever match, 0-argument ones are unaffected). An AMBIGUOUS domain name
// (matches more than one schema, when configured without its own schema
// qualifier) IS a real, fatal build error — there's nothing sensible to
// default to.
//
// Once every route function is discovered, BuildRegistry also runs
// specs/jwt-roles-and-http.md "# HTTP ## Anonymous route authorization" :
// one combined query against db.Pool computing, per route function, both
// whether cfg.Pg.Query.AnonymousRole can reach it (schema USAGE + function
// EXECUTE, both required — see Route.AnonymousAuthorized) and whether
// PUBLIC can (same two-conjunct check, logged as a non-fatal warning per
// route it's true for — Postgres grants EXECUTE to PUBLIC by default on
// CREATE FUNCTION, a well-known footgun this surfaces rather than silently
// ignores). The anonymous half is only trusted when db.AnonymousRoleExists
// ; the PUBLIC-warning half always runs, independent of that.
func BuildRegistry(db *pg.DbInfos, cfg *config.Config) (*Registry, error) {
	reqType, err := resolveDomainByName(db, cfg.Http.RequestDomainName)
	if err != nil {
		return nil, err
	}
	if reqType == nil {
		slog.Default().Warn("rpc: request domain not found, no argument-taking route functions will be discovered", "name", cfg.Http.RequestDomainName)
	}
	respType, err := resolveDomainByName(db, cfg.Http.ResponseDomainName)
	if err != nil {
		return nil, err
	}
	if respType == nil {
		slog.Default().Warn("rpc: response domain not found, no RelHttpResponse route functions will be discovered", "name", cfg.Http.ResponseDomainName)
	}

	var allowedRoutes *regexp.Regexp
	if cfg.Http.Functions.AllowedRoutes != "" {
		allowedRoutes, err = regexp.Compile(cfg.Http.Functions.AllowedRoutes)
		if err != nil {
			return nil, fmt.Errorf("rpc: http.functions.allowed_routes: invalid regexp: %w", err)
		}
	}

	reg := &Registry{routes: map[string]map[string]map[string]Route{}}
	for _, fn := range db.Functions {
		if !fn.IsPlainFunction() {
			continue
		}
		if strings.HasPrefix(fn.Identifier.Name, "_") {
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
		if existing, dup := reg.routes[schema][base][verb]; dup {
			slog.Default().Error("rpc: ambiguous route, skipping both", "schema", schema, "function", base, "verb", verb,
				"first", existing.Function.Identifier.String(), "second", fn.Identifier.String())
			continue
		}
		reg.routes[schema][base][verb] = Route{
			Function:            fn,
			MimeType:            mimeType,
			AcceptsFiles:        acceptsFiles,
			AcceptsPartsHeaders: acceptsPartsHeaders,
		}
	}

	if err := applyAnonymousAuthorization(db, cfg, reg); err != nil {
		return nil, err
	}

	return reg, nil
}

// anonPublicPriv is one route function's cached anon/PUBLIC reachability,
// per specs/jwt-roles-and-http.md "# HTTP ## Anonymous route
// authorization" — both fields are the same two-conjunct check
// (has_schema_privilege(role, schema, 'USAGE') AND
// has_function_privilege(role, function, 'EXECUTE')), against the
// anonymous role and PUBLIC respectively.
type anonPublicPriv struct {
	anonOK   bool
	publicOK bool
}

// applyAnonymousAuthorization runs ONE combined query (not one per route)
// against every discovered route function's PgOid, then sets each Route's
// AnonymousAuthorized and warns for every route reachable by PUBLIC — see
// BuildRegistry's own doc comment. Mutates reg.routes in place.
func applyAnonymousAuthorization(db *pg.DbInfos, cfg *config.Config, reg *Registry) error {
	var oids []int
	for _, byFunc := range reg.routes {
		for _, byVerb := range byFunc {
			for _, route := range byVerb {
				oids = append(oids, route.Function.PgOid)
			}
		}
	}
	if len(oids) == 0 {
		return nil
	}

	// has_schema_privilege/has_function_privilege ERROR outright if handed a
	// role name that doesn't exist in pg_roles — so when db.AnonymousRoleExists
	// is false (unconfigured, or configured to a name nothing created), the
	// anon-role half of the query must not run against that name at all ;
	// anon_ok is hardcoded false in that branch instead (AnonymousAuthorized
	// is meaningless with anonymous access disabled anyway — see Route's own
	// doc comment). The PUBLIC half always runs regardless — "PUBLIC" is
	// always a valid pseudo-role for these functions, independent of any
	// real role's existence.
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
		return fmt.Errorf("rpc: querying anonymous/PUBLIC route authorization: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var oid int
		var priv anonPublicPriv
		if err := rows.Scan(&oid, &priv.anonOK, &priv.publicOK); err != nil {
			return fmt.Errorf("rpc: scanning anonymous/PUBLIC route authorization: %w", err)
		}
		privByOid[oid] = priv
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rpc: reading anonymous/PUBLIC route authorization: %w", err)
	}

	for schema, byFunc := range reg.routes {
		for base, byVerb := range byFunc {
			for verb, route := range byVerb {
				priv := privByOid[route.Function.PgOid]
				route.AnonymousAuthorized = db.AnonymousRoleExists && priv.anonOK
				byVerb[verb] = route
				if priv.publicOK {
					slog.Default().Warn("rpc: route is executable by PUBLIC", "schema", schema, "function", base, "verb", verb,
						"target", route.Function.Identifier.String())
				}
			}
		}
	}
	return nil
}

// matchesRouteShape checks fn's arguments/return type against ## HTTP's and
// ## Request bodies' signature rule, returning (mimeTypeName, acceptsFiles,
// acceptsPartsHeaders, true) on a match — mimeTypeName is "" for a
// RelHttpResponse route, non-empty for a mimetype-domain route. Only four
// argument shapes are recognized, matched by TYPE SEQUENCE, never parameter
// name : (), (req), (req, files bytea[]), (req, files bytea[],
// parts_headers jsonb). Anything else — extra/reordered/differently-typed
// parameters — is simply not discovered as a route, same as any other
// signature mismatch.
func matchesRouteShape(fn *pg.Function, reqType, respType *pg.Type) (string, bool, bool, bool) {
	inTypes := inputArgumentTypes(fn)
	// inputArgumentTypes only collects IsIn() arguments ; a function with
	// an INOUT/VARIADIC parameter would inflate PgNargs beyond len(inTypes)
	// without matching any of the shapes below, so guard on the two
	// staying equal — otherwise e.g. a (req, INOUT x) function's single
	// collected IN type could false-match the one-argument (req) shape.
	if len(inTypes) != fn.PgNargs {
		return "", false, false, false
	}

	var acceptsFiles, acceptsPartsHeaders bool
	switch len(inTypes) {
	case 0:
		// fine, 0-arg route functions are always shape-eligible on the
		// argument side.
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

// isMimeTypeUnderlying is # HTTP's mimetype-domain rule, generalized to
// both underlying types it now recognizes : a domain over bytea (raw bytes
// ARE the body) or over text (the string IS the body directly, no base64).
func isMimeTypeUnderlying(underlying *pg.Type) bool {
	name := underlying.PgIdentifier.String()
	return name == "pg_catalog.bytea" || name == "pg_catalog.text"
}

// isBytesArrayType reports whether t is exactly bytea[] — an array type
// whose element is pg_catalog.bytea, matched by TYPE, never through a
// domain wrapper (## Request bodies' four shapes require the EXACT type).
func isBytesArrayType(t *pg.Type) bool {
	return t != nil && t.IsArray() && t.ElementType != nil && t.ElementType.PgIdentifier.String() == "pg_catalog.bytea"
}

// isJsonbType reports whether t is exactly pg_catalog.jsonb.
func isJsonbType(t *pg.Type) bool {
	return t != nil && t.PgIdentifier.String() == "pg_catalog.jsonb"
}

// inputArgumentTypes returns the Type of every one of fn's true INPUT
// arguments (IsIn() — OUT-only arguments never appear here), in positional
// order — the generalized form of the old singleInputArgument, needed now
// that route functions can take up to three arguments (## Request bodies).
func inputArgumentTypes(fn *pg.Function) []*pg.Type {
	types := make([]*pg.Type, 0, fn.PgNargs)
	for i := range fn.Arguments {
		if fn.Arguments[i].IsIn() {
			types = append(types, fn.Arguments[i].Type)
		}
	}
	return types
}

// splitVerb splits a Postgres function name's optional "__VERB" suffix
// (case-insensitive per spec) into (base name, uppercased verb) — verb is
// "" when unsuffixed.
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

// resolveDomainByName finds the *pg.Type named name : if name contains a
// ".", it's treated as an explicit "schema.name" qualifier ; otherwise
// every schema is searched by bare name, and more than one match is a
// fatal ambiguity (nothing sensible to default to). nil, nil (not an
// error) if nothing matches at all — see BuildRegistry's own doc comment
// for why an absent domain isn't fatal.
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
		return nil, fmt.Errorf("rpc: domain name %q is ambiguous across schemas %v — configure it schema-qualified", name, schemas)
	}
}
