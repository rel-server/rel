// Package rpc implements specs/jwt-roles-and-http.md's "# HTTP" route
// functions : Postgres functions directly callable at
// /rpc/{schema}/{function}, gated by the full JWT lifecycle (Verify/
// Check/Renew/Apply-role from the jwt package, mint/logout from a route
// function's own RelHttpResponse.jwt).
package rpc

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/pg"
)

// Route is one discovered route function : Function is the underlying
// Postgres function ; MimeType is non-empty only when this route's return
// type is a mimetype domain (a domain over bytea whose own name contains
// "/"), in which case that name (route.MimeType) IS the Content-Type a
// call to this route responds with.
type Route struct {
	Function *pg.Function
	MimeType string
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

		mimeType, ok := matchesRouteShape(fn, reqType, respType)
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
		reg.routes[schema][base][verb] = Route{Function: fn, MimeType: mimeType}
	}

	return reg, nil
}

// matchesRouteShape checks fn's arguments/return type against ## HTTP's
// signature rule, returning ("", true) for a RelHttpResponse route or
// (mimeTypeName, true) for a mimetype-domain route.
func matchesRouteShape(fn *pg.Function, reqType, respType *pg.Type) (string, bool) {
	inCount, singleInType := singleInputArgument(fn)
	switch inCount {
	case 0:
		// fine, 0-arg route functions are always shape-eligible on the
		// argument side.
	case 1:
		if reqType == nil || singleInType != reqType {
			return "", false
		}
	default:
		return "", false
	}

	if respType != nil && fn.ReturnType == respType {
		return "", true
	}
	if fn.ReturnType.IsDomain() && fn.ReturnType.Underlying() != nil &&
		fn.ReturnType.Underlying().PgIdentifier.String() == "pg_catalog.bytea" &&
		strings.Contains(fn.ReturnType.PgIdentifier.Name, "/") {
		return fn.ReturnType.PgIdentifier.Name, true
	}
	return "", false
}

// singleInputArgument counts fn's true INPUT arity (PgNargs — OUT-only
// arguments never inflate this, matching AcceptsArity's own convention)
// and, when there's exactly one, returns its Type.
func singleInputArgument(fn *pg.Function) (int, *pg.Type) {
	if fn.PgNargs != 1 {
		return fn.PgNargs, nil
	}
	for i := range fn.Arguments {
		if fn.Arguments[i].IsIn() {
			return 1, fn.Arguments[i].Type
		}
	}
	return fn.PgNargs, nil
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
