package route

// Route discovery from config and comment declarations.

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/samber/oops"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
)

// Route is one discovered route or middleware function.
type Route struct {
	Function *pg.Function

	// Path is exactly as declared, chi syntax. AnonPath replaces every placeholder with "{}" ; it is the key for
	// sorting, collisions and middleware matching.
	Path     string
	AnonPath string

	// Methods is the resolved, uppercased, deduplicated method set ; never empty.
	Methods []string

	// HasRequestArg is true when the function's first argument is json/jsonb, which receives the request object.
	HasRequestArg bool

	// PathArgs is the function's named `text` IN arguments, in declared order ; each is a placeholder name in Path.
	PathArgs []string

	// AcceptsBytes/AcceptsBytesArray record which upload bytes argument (if any) the function takes ; mutually
	// exclusive.
	AcceptsBytes      bool
	AcceptsBytesArray bool

	// ContentType is the default Content-Type, from the single return type or, when FullControl, the second OUT
	// column (whose response "content_type" can still override it). ContentIsBinary flags a bytea-based return,
	// which cannot be used with a template.
	ContentType     string
	ContentIsBinary bool

	// ContentNative marks a value of a type with no mimetype of its own (an int, a timestamp, an array, a
	// composite...) : it is sent as application/json through to_jsonb, or as CBOR in its own types when that is
	// what the request Accepts.
	ContentNative bool

	// AnonymousAuthorized reports whether the anonymous role has an explicit grant on this function, as of
	// registry build. Always false when there is no anonymous role.
	AnonymousAuthorized bool

	// FullControl is true for the "..., OUT json/jsonb, OUT some_type) returns record" shape ; middleware always
	// has it.
	FullControl bool

	Template     string
	StreamUpload bool
	IsMiddleware bool

	// Source records where this entry's declaration came from.
	Source string // "config" or "comment"
}

// Registry holds Routes sorted longest AnonPath first and Middleware shortest AnonPath first, ties broken by
// schema.function.
type Registry struct {
	Routes     []Route
	Middleware []Route
}

// MiddlewareChain returns, shortest prefix first, the middleware whose prefix matches targetPath : a declared
// route's AnonPath, or a concrete request path for /rel or the static fallback.
func (s *Registry) MiddlewareChain(targetPath string) []Route {
	var chain []Route
	for _, mw := range s.Middleware {
		if middlewarePrefixMatches(mw.AnonPath, targetPath) {
			chain = append(chain, mw)
		}
	}
	return chain
}

// middlewarePrefixMatches matches mwAnonPath against targetPath's leading segments, a "{}" segment matching any
// one segment.
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

// find is a test helper ; dispatch goes through chi.
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

// reservedPrefixes are rel's own endpoints, which no declared route may use.
var reservedPrefixes = []string{"/auth", "/rel"}

func isReservedPath(anonPath string) bool {
	for _, p := range reservedPrefixes {
		if anonPath == p || strings.HasPrefix(anonPath, p+"/") {
			return true
		}
	}
	return false
}

// placeholderPattern extracts a chi path placeholder's name, ignoring any ":regexp" : "{id:[0-9]+}" -> "id".
var placeholderPattern = regexp.MustCompile(`\{([^:}]+)(?::[^}]*)?\}`)

// anonymizePath replaces every placeholder with "{}".
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

// BuildRegistry resolves every route and middleware declared in config or a function comment. An invalid
// declaration only disables its function, with a logged error ; the returned error is a database failure.
func BuildRegistry(db *pg.DbInfos, cfg *config.Config) (*Registry, error) {
	byKey := map[string]resolvedDeclaration{}

	// Comment declarations first ; config ones override them with a warning.
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

	log.Info("route registry built", "route_count", len(set.Routes), "middleware_count", len(set.Middleware))
	for _, entry := range set.Routes {
		log.Debug("route registered",
			"methods", entry.Methods, "path", entry.AnonPath,
			"function", entry.Function.Identifier.String(), "anonymous_authorized", entry.AnonymousAuthorized)
	}
	for _, entry := range set.Middleware {
		log.Debug("middleware registered",
			"path", entry.AnonPath,
			"function", entry.Function.Identifier.String(), "anonymous_authorized", entry.AnonymousAuthorized)
	}

	return set, nil
}

// applyAnonymousAuthorization sets each entry's AnonymousAuthorized, counting only an explicit grant (not the
// default EXECUTE to PUBLIC), and warns for every function PUBLIC can execute.
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
		return oops.Wrapf(err, "route: querying anonymous/PUBLIC route authorization")
	}
	defer rows.Close()
	for rows.Next() {
		var oid int
		var priv anonPublicPriv
		if err := rows.Scan(&oid, &priv.anonOK, &priv.publicOK); err != nil {
			return oops.Wrapf(err, "route: scanning anonymous/PUBLIC route authorization")
		}
		privByOid[oid] = priv
	}
	if err := rows.Err(); err != nil {
		return oops.Wrapf(err, "route: reading anonymous/PUBLIC route authorization")
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

// excludeCollisions disables both routes of any pair with the same AnonPath and an overlapping method. Disjoint
// methods at the same AnonPath coexist.
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
