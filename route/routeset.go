package route

// This file implements specs/new-routes.md's declaration-based discovery :
// BuildRegistry resolves every route.<schema>.<function> config entry and
// every "route" comment declaration to its actual *pg.Function, classifies
// its argument/return shape structurally, and sorts/excludes per
// ## How it works. It exists alongside the old domain-based BuildRegistry
// (registry.go) as a separate, not-yet-wired artifact — see
// specs/new-routes.md and the implementation plan's Stage 1/2 split ; the
// old registry keeps serving real traffic until the Stage 2 cutover
// deletes it and renames this one into its place.

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
)

// Route is one discovered route or middleware function.
type Route struct {
	Function *pg.Function

	// Path is exactly as declared, chi syntax. AnonPath replaces every
	// "{name}"/"{name:regexp}" placeholder with "{}" — the sort/collision/
	// prefix-matching key specs/new-routes.md ## How it works and
	// ## Middleware both use.
	Path     string
	AnonPath string

	// Methods is the resolved, uppercased, deduplicated method set — never
	// empty (## Method inference always produces at least one).
	Methods []string

	// HasRequestArg is true when the function's first argument is
	// json/jsonb (it always receives the request object) — false only for
	// a genuinely no-argument function.
	HasRequestArg bool

	// PathArgs is the function's own named `text` IN arguments, in
	// declared order — each must appear as a placeholder name in Path (see
	// classifyShape).
	PathArgs []string

	// AcceptsBytes/AcceptsBytesArray record which second-argument shape (if
	// any) the function declared — mutually exclusive, both false for a
	// function taking no upload bytes at all.
	AcceptsBytes      bool
	AcceptsBytesArray bool

	// ContentType is the default Content-Type ## Function prototype's
	// single-return-value table states, resolved from either the plain
	// single-return type (FullControl false) or the two-OUT shape's second
	// column (FullControl true, where the envelope's own "content_type"
	// can still override it at response time). ContentIsBinary additionally
	// flags a plain bytea/mimetype-domain-over-bytea return for
	// ## Templates' incompatibility check.
	ContentType     string
	ContentIsBinary bool

	// AnonymousAuthorized reports whether the anonymous role could reach
	// this route/middleware at registry-build time, via an EXPLICIT grant —
	// see applyAnonymousAuthorization. Meaningless when anonymous access is
	// disabled (db.AnonymousRoleExists false).
	AnonymousAuthorized bool

	// FullControl is true for the two-trailing-OUT-column
	// "..., OUT JSON/JSONB, OUT some_other_type) returns record" shape —
	// see ## Function prototype and ## Middleware (middleware functions are
	// always this shape).
	FullControl bool

	Template     string
	StreamUpload bool
	IsMiddleware bool

	// Source records where this entry's declaration came from, purely for
	// the config-overrides-comment-with-warning log line.
	Source string // "config" or "comment"
}

// Registry is BuildRegistry's full result : Routes sorted longest-
// AnonPath-first (then alphabetically by schema.function to break a tie),
// Middleware sorted shortest-AnonPath-first then alphabetically — see
// ## How it works and ## Middleware.
type Registry struct {
	Routes     []Route
	Middleware []Route
}

// MiddlewareChain returns every s.Middleware entry whose prefix applies to
// targetPath — a declared route's own AnonPath (a precomputed-at-dispatch-
// time chain) or a concrete request path for /rel or the static fallback,
// which have no declared route of their own. Already shortest-prefix-first,
// then alphabetical (s.Middleware's own sort order), since filtering
// preserves order.
func (s *Registry) MiddlewareChain(targetPath string) []Route {
	var chain []Route
	for _, mw := range s.Middleware {
		if middlewarePrefixMatches(mw.AnonPath, targetPath) {
			chain = append(chain, mw)
		}
	}
	return chain
}

// middlewarePrefixMatches implements ## Middleware's "prefix match ...
// segment by segment" rule : mwAnonPath's segments must all match
// targetPath's corresponding leading segments, where a "{}" segment
// matches any single segment (targetPath's own placeholder segments are
// already anonymized to "{}" too when it's a declared route's AnonPath ;
// for a concrete request path — /rel or a static file — targetPath carries
// real segment values instead, which a middleware's own "{}" placeholder
// still matches).
func middlewarePrefixMatches(mwAnonPath, targetPath string) bool {
	if mwAnonPath == "/" {
		return true
	}
	mwSegs := strings.Split(strings.Trim(mwAnonPath, "/"), "/")
	targetSegs := strings.Split(strings.Trim(targetPath, "/"), "/")
	if len(mwSegs) > len(targetSegs) {
		return false
	}
	for i, seg := range mwSegs {
		if seg == "{}" {
			continue
		}
		if seg != targetSegs[i] {
			return false
		}
	}
	return true
}

// find is a test helper — production dispatch (Stage 2) matches a real
// request path against Path/AnonPath through chi itself, not by exact
// schema/function lookup.
func (s *Registry) find(schema, function string) (Route, bool) {
	for _, e := range s.Routes {
		if e.Function.Identifier.Schema == schema && e.Function.Identifier.Name == function {
			return e, true
		}
	}
	for _, e := range s.Middleware {
		if e.Function.Identifier.Schema == schema && e.Function.Identifier.Name == function {
			return e, true
		}
	}
	return Route{}, false
}

// reservedPrefixes is specs/new-routes.md ## How it works' "path under a
// reserved prefix" rule : rel's own endpoints, always excluded from user
// declaration regardless of source, at a higher priority than the
// ordinary collision rule below.
var reservedPrefixes = []string{"/auth", "/rel"}

func isReservedPath(anonPath string) bool {
	for _, p := range reservedPrefixes {
		if anonPath == p || strings.HasPrefix(anonPath, p+"/") {
			return true
		}
	}
	return false
}

// placeholderPattern extracts a chi path placeholder's name, ignoring any
// ":regexp" constraint — "{id:[0-9]+}" -> "id".
var placeholderPattern = regexp.MustCompile(`\{([^:}]+)(?::[^}]*)?\}`)

// anonymizePath is ## How it works' "anonymizing" rule : every
// "{name}"/"{name:regexp}" placeholder becomes the literal "{}" sort/
// prefix-matching key.
func anonymizePath(path string) string {
	return placeholderPattern.ReplaceAllString(path, "{}")
}

// pathPlaceholderNames returns every placeholder name declared in path, in
// order.
func pathPlaceholderNames(path string) []string {
	matches := placeholderPattern.FindAllStringSubmatch(path, -1)
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m[1]
	}
	return names
}

// resolvedDeclaration pairs a Declaration with the *pg.Function it names
// and where it came from, before shape classification.
type resolvedDeclaration struct {
	fn   *pg.Function
	decl Declaration
	src  string
}

// BuildRegistry resolves every declared route/middleware — config
// (route.<schema>.<function>.*) and database comment alike — to its
// function, classifies its shape, and applies ## Method inference,
// ## How it works' sorting/collision/reserved-path rules, and
// ## Templates' template+binary incompatibility check.
func BuildRegistry(db *pg.DbInfos, cfg *config.Config) (*Registry, error) {
	byKey := map[string]resolvedDeclaration{}

	// Comment-declared, first — config overrides these below, per
	// ## In the configuration : "Routes in configuration override routes
	// defined in the database with a warning."
	fnByKey := map[string]*pg.Function{}
	for i := range db.Functions {
		fn := db.Functions[i]
		if !fn.IsPlainFunction() {
			continue
		}
		fnByKey[fn.Identifier.Schema+"."+fn.Identifier.Name] = fn

		decl, err := parseCommentDeclaration(fn.Comment)
		if err != nil {
			log.Error("route: invalid route declaration in comment, function disabled",
				"schema", fn.Identifier.Schema, "function", fn.Identifier.Name, "error", err.Error())
			continue
		}
		if decl == nil {
			continue
		}
		key := fn.Identifier.Schema + "." + fn.Identifier.Name
		byKey[key] = resolvedDeclaration{fn: fn, decl: *decl, src: "comment"}
	}

	for schema, byFunc := range cfg.Route {
		for function, cfgDecl := range byFunc {
			key := schema + "." + function
			fn, ok := fnByKey[key]
			if !ok {
				log.Warn("route: config-declared route names a function that doesn't exist, ignored",
					"schema", schema, "function", function)
				continue
			}
			if _, hadComment := byKey[key]; hadComment {
				log.Warn("route: config route declaration overrides the database comment declaration",
					"schema", schema, "function", function)
			}
			byKey[key] = resolvedDeclaration{fn: fn, decl: declFromConfig(cfgDecl), src: "config"}
		}
	}

	var routes, middleware []Route

	for _, rd := range byKey {
		entry, ok := classifyShape(rd)
		if !ok {
			continue // classifyShape already logged why
		}
		if isReservedPath(entry.AnonPath) {
			log.Error("route: reserved path, function disabled",
				"schema", rd.fn.Identifier.Schema, "function", rd.fn.Identifier.Name, "path", entry.Path)
			continue
		}
		if entry.IsMiddleware {
			middleware = append(middleware, entry)
		} else {
			routes = append(routes, entry)
		}
	}

	routes = excludeCollisions(routes)

	sort.SliceStable(routes, func(i, j int) bool {
		if len(routes[i].AnonPath) != len(routes[j].AnonPath) {
			return len(routes[i].AnonPath) > len(routes[j].AnonPath)
		}
		return routeSortKey(routes[i]) < routeSortKey(routes[j])
	})
	sort.SliceStable(middleware, func(i, j int) bool {
		if len(middleware[i].AnonPath) != len(middleware[j].AnonPath) {
			return len(middleware[i].AnonPath) < len(middleware[j].AnonPath)
		}
		return routeSortKey(middleware[i]) < routeSortKey(middleware[j])
	})

	set := &Registry{Routes: routes, Middleware: middleware}
	if err := applyAnonymousAuthorization(db, cfg, set); err != nil {
		return nil, err
	}
	return set, nil
}

// applyAnonymousAuthorization sets each entry's AnonymousAuthorized (an
// EXPLICIT grant only — Postgres's own CREATE FUNCTION default EXECUTE-to-
// PUBLIC never counts, so a route nobody explicitly decided the anonymous
// role should reach isn't credited just because PUBLIC can call it) and
// warns for every PUBLIC-reachable route/middleware. Ported from the old
// domain-based registry's own version of this query — same aclexplode-
// based check, walking Registry's entries instead.
func applyAnonymousAuthorization(db *pg.DbInfos, cfg *config.Config, set *Registry) error {
	var oids []int
	for _, e := range set.Routes {
		oids = append(oids, e.Function.PgOid)
	}
	for _, e := range set.Middleware {
		oids = append(oids, e.Function.PgOid)
	}
	if len(oids) == 0 {
		return nil
	}

	query := `
		select f.oid::integer as fn_oid,
		       has_schema_privilege($1, f.pronamespace, 'USAGE')
		         and exists (
		           select 1
		           from pg_roles r,
		                aclexplode(coalesce(f.proacl, acldefault('f', f.proowner))) as a(grantor, grantee, privilege_type, is_grantable)
		           where r.rolname = $1
		             and a.privilege_type = 'EXECUTE'
		             and a.grantee <> 0
		             and pg_has_role(r.oid, a.grantee, 'USAGE')
		         ) as anon_ok,
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

	applyPriv := func(entries []Route) {
		for i := range entries {
			priv := privByOid[entries[i].Function.PgOid]
			entries[i].AnonymousAuthorized = db.AnonymousRoleExists && priv.anonOK
			if priv.publicOK {
				log.Warn("route: route is executable by PUBLIC",
					"schema", entries[i].Function.Identifier.Schema, "function", entries[i].Function.Identifier.Name,
					"target", entries[i].Function.Identifier.String())
			}
		}
	}
	applyPriv(set.Routes)
	applyPriv(set.Middleware)

	return nil
}

// anonPublicPriv is one function's cached anon/PUBLIC reachability.
type anonPublicPriv struct {
	anonOK   bool
	publicOK bool
}

func routeSortKey(e Route) string {
	return e.Function.Identifier.Schema + "." + e.Function.Identifier.Name
}

// excludeCollisions implements ## How it works' "same simplified name AND
// an overlapping resolved method -> warning, neither enabled" rule.
// Disjoint methods at the same AnonPath coexist, dispatched by method.
func excludeCollisions(routes []Route) []Route {
	byAnonPath := map[string][]int{}
	for i, e := range routes {
		byAnonPath[e.AnonPath] = append(byAnonPath[e.AnonPath], i)
	}

	excluded := map[int]bool{}
	for _, idxs := range byAnonPath {
		if len(idxs) < 2 {
			continue
		}
		for a := 0; a < len(idxs); a++ {
			for b := a + 1; b < len(idxs); b++ {
				i, j := idxs[a], idxs[b]
				if methodsOverlap(routes[i].Methods, routes[j].Methods) {
					if !excluded[i] {
						log.Error("route: colliding routes, both disabled",
							"path", routes[i].AnonPath,
							"first", routeSortKey(routes[i]), "second", routeSortKey(routes[j]))
					}
					excluded[i] = true
					excluded[j] = true
				}
			}
		}
	}

	out := make([]Route, 0, len(routes))
	for i, e := range routes {
		if !excluded[i] {
			out = append(out, e)
		}
	}
	return out
}

func methodsOverlap(a, b []string) bool {
	set := make(map[string]bool, len(a))
	for _, m := range a {
		set[m] = true
	}
	for _, m := range b {
		if set[m] {
			return true
		}
	}
	return false
}
