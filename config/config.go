package config

// Config is rel's runtime configuration, as described in specs/querying.md
// ## Configuration and ## Scoping. This is only the shape ; nothing loads it
// from a file/env yet (config.tempindexthreshold, mentioned in querying.md's
// Writing Algorithm as a tentative option the redactor was "torn" on, isn't
// here either — deliberately, since it was never actually settled).
type Config struct {
	Pg      Pg
	Logging Logging
	Http    Http
	Jwt     Jwt

	Blacklist Blacklist
}

// Jwt is jwt-roles-and-http.md ## Configuration : jwt.secret/cookie_name/
// algorithm/same_site/max_age/renew_after/max_session_age.
type Jwt struct {
	// Secret is jwt.secret, default "$FILE$jwt-secret$GEN$32" — the JWT
	// signing secret.
	Secret string
	// CookieName is jwt.cookie_name, default "accesstoken" : the cookie
	// scanned/set by rel to carry the JWT.
	CookieName string
	// Algorithm is jwt.algorithm, default "HS256" (one of HS256/HS384/
	// HS512) : enforced exactly on verification, including rejecting "none".
	Algorithm string
	// SameSite is jwt.same_site, default "Lax" : SameSite attribute of the
	// JWT cookie.
	SameSite string
	// MaxAge is jwt.max_age, default 1800 (30 minutes) : how long a
	// freshly-minted token is valid for (exp = iat + MaxAge).
	MaxAge int
	// RenewAfter is jwt.renew_after, default 0.5 : fraction of a token's own
	// exp-iat after which it's due for renewal.
	RenewAfter float64
	// MaxSessionAge is jwt.max_session_age, default 604800 (7 days) : hard
	// ceiling on a session's total lifetime, measured from auth_time.
	MaxSessionAge int
}

// DefaultJwt* are jwt-roles-and-http.md ## Configuration's stated defaults.
const (
	DefaultJwtSecret        = "$FILE$jwt-secret$GEN$32"
	DefaultJwtCookieName    = "accesstoken"
	DefaultJwtAlgorithm     = "HS256"
	DefaultJwtSameSite      = "Lax"
	DefaultJwtMaxAge        = 1800
	DefaultJwtRenewAfter    = 0.5
	DefaultJwtMaxSessionAge = 604800
)

// Logging is specs/01-logging.md ## Configuration : logging.handler,
// logging.level, logging.filter.*, logging.exclude.*.
type Logging struct {
	// Handler is logging.handler : "JSON" or "pretty" (default "pretty").
	Handler string
	// Level is logging.level : debug/info/warn/error (default "info").
	Level string
	// Filter is logging.filter.* : per-key regexp a log record's matching
	// attribute value must contain to be displayed.
	Filter map[string]string
	// Exclude is logging.exclude.* : same shape as Filter, but suppresses a
	// matching record instead, applied after Filter.
	Exclude map[string]string
}

// Http is the HTTP listen address (Host/Port — invented for cmd/rel, since
// no spec under specs/ defines the bind host/port) plus
// jwt-roles-and-http.md ## HTTP ## Configuration's own http.* keys, which
// govern /rpc route-function discovery and dispatch.
type Http struct {
	Host string
	Port int

	// RequestDomainName is http.request_domain_name, default
	// "RelHttpRequest" : the fully qualified, unquoted name of the JSON
	// domain identifying a route function's single request-typed argument.
	RequestDomainName string
	// ResponseDomainName is http.response_domain_name, default
	// "RelHttpResponse" : the fully qualified, unquoted name of the JSON
	// domain identifying a route function's response return type.
	ResponseDomainName string
	// CookiesMaxAge is http.cookies_max_age, default 86400 : default max-age
	// for cookies set via the generic "cookies" field, when the response
	// doesn't specify one. Does not apply to the JWT cookie (see Jwt.MaxAge).
	CookiesMaxAge int

	Functions HttpFunctions
	Static    HttpStatic
}

// HttpFunctions is jwt-roles-and-http.md's http.functions.* namespace.
type HttpFunctions struct {
	// AllowedAuth is http.functions.allowed_auth, default "" (unrestricted)
	// : regexp restricting which functions' responses rel will honor a
	// "jwt" field from, matched against the fully qualified, unquoted
	// function name. Named to match AllowedRoutes below — both are
	// restriction regexps over "which functions may do X" ; CheckSession
	// deliberately isn't (it names a function, not a restriction).
	AllowedAuth string
	// AllowedRoutes is http.functions.allowed_routes, default ""
	// (unrestricted) : regexp a function's fully qualified, unquoted name
	// must additionally match to become a public /rpc route.
	AllowedRoutes string
	// CheckSession is http.functions.check_session, default "" (disabled)
	// : unquoted, fully qualified name of a Postgres function that lets the
	// database reject a session before exp/max_session_age would otherwise.
	CheckSession string
}

// HttpStatic is http.static.* — static file serving. Only Path exists so
// far ; specs/TODO.md's own "Named but empty" note on static file serving
// ("configurable file access control based on path and database queries")
// implies more keys will likely join this namespace later (path-based
// access rules), which is why this is its own nested struct/key prefix
// rather than a single flat http.static_path — avoids a breaking rename
// when that happens. Not yet served by cmd/rel — config only for now.
type HttpStatic struct {
	// Path is http.static.path, default "/static".
	Path string
}

// DefaultLoggingHandler/DefaultLoggingLevel/DefaultHttpPort are
// specs/01-logging.md's own stated defaults (Handler/Level) and this
// package's own invented default (Port — see Http's doc comment).
// DefaultHttpRequestDomainName/DefaultHttpResponseDomainName/
// DefaultHttpCookiesMaxAge are jwt-roles-and-http.md's own stated defaults.
// DefaultHttpStaticPath is this package's own invented default, matching
// HttpStatic's doc comment.
const (
	DefaultLoggingHandler         = "pretty"
	DefaultLoggingLevel           = "info"
	DefaultHttpPort               = 8080
	DefaultHttpRequestDomainName  = "RelHttpRequest"
	DefaultHttpResponseDomainName = "RelHttpResponse"
	DefaultHttpCookiesMaxAge      = 86400
	DefaultHttpStaticPath         = "/static"
)

// DefaultPgHost/DefaultPgPort/DefaultPgQueryAnonymousRole/
// DefaultPgQueryWellKnownPath are querying.md/well-known-queries.md's own
// stated defaults for pg.host/pg.port/pg.query.anonymous_role/
// pg.query.wellknown_path — named here (rather than left as inline
// literals in loader.go's assemble()) so config/help.go's --help output
// can quote the exact same value assemble() actually uses, with no risk of
// the two drifting apart.
const (
	DefaultPgHost               = "localhost"
	DefaultPgPort               = 5432
	DefaultPgQueryAnonymousRole = "~anonymous"
	DefaultPgQueryWellKnownPath = "/wellknown"
	// DefaultPgPoolSize is pg.pool_size's default : the max number of
	// connections in the pool that actually serves requests. pgx's own
	// unconfigured default (max(4, runtime.NumCPU())) scales with the
	// machine rel happens to run on, not with what the database can
	// actually sustain — 10 is a small, common, framework-agnostic
	// starting point (Node's node-postgres and Java's HikariCP both
	// default here too) rather than a value tied to host CPU count.
	DefaultPgPoolSize = 10
)

type Login struct {
	User     string
	Password string
}

// PgQuery is pg.query.* : an OPTIONAL, narrower-scoped login for the
// request-serving connection specifically (the base identity every
// request's SET ROLE switches away from), plus the query-engine behavior
// settings that only make sense in terms of that connection. Documented
// and encouraged for a hardened deployment, never required : Pg's own
// primary login already works for serving requests too (SET ROLE is what
// actually restricts a request's data access, not the connecting login's
// own privileges), so the simplest possible setup is just Pg.User/
// Pg.Password/Pg.URI with PgQuery.Login left unset.
type PgQuery struct {
	// Login is pg.query.user / pg.query.password : if set, the login role
	// rel connects with to serve requests (introspection and dmut
	// migrations always use Pg's own primary login, never this one) — the
	// role `set role` switches are executed from at request time. Falls
	// back to Pg's own User/Password when unset. Embedded (not a named
	// field) so cfg.Pg.Query.User/.Password read directly, matching the
	// dotted key.
	Login

	// AnonymousRole is pg.query.anonymous_role (default "~anonymous") :
	// the role rel switches to, from Login, for requests with no
	// credentials of their own — used both for /rel (querying.md) and JWT
	// verification failures on /rpc (jwt-roles-and-http.md ## Roles, which
	// used to name this same setting jwt.anonrole ; reconciled onto
	// query.anonymous_role, the name querying.md already used, itself
	// later moved under pg.query.* for this same consistency pass).
	AnonymousRole string

	// MaxDepth is pg.query.max_depth : the maximum depth a query can
	// specify. querying.md's default is 6.
	MaxDepth int

	// WellKnownDirs is pg.query.wellknown_path (well-known-queries.md
	// ## Configuration), default "/wellknown" : a colon-separated list of
	// directories, kept as the raw string here — splitting happens wherever
	// well-known loading itself gets built (not yet).
	WellKnownDirs string
}

// Pg is the primary Postgres connection — the one thing every deployment
// MUST configure, deliberately kept as the simplest possible surface
// (pg.uri alone, or the granular fields below) rather than requiring a
// separate "admin" login. Used directly for dmut migrations and startup
// introspection, and as the request-serving connection's own fallback
// (see PgQuery's doc comment for why a second, narrower login is optional,
// not required).
type Pg struct {
	// URI is pg.uri : a full "postgres://user:pass@host:port/db"
	// connection string. When set, it is AUTHORITATIVE — User/Password/
	// Host/Port/Database below are ignored entirely, not merged with it ;
	// a partial URI plus partial granular fields has no clean precedence
	// rule, so this is deliberately all-or-nothing. Composes naturally
	// with $FILE$ (a single secrets file holding the whole URI, rather
	// than five separate keys each behind their own $FILE$ reference).
	URI string

	// User/Password are pg.user/pg.password — the primary login, used
	// when URI is unset. Also PgQuery.Login's own fallback.
	User     string
	Password string

	// Host/Port/Database are pg.host/pg.port/pg.database, used when URI
	// is unset — shared with PgQuery's own login when that's set, since
	// it's always the same Postgres instance/database, only the
	// credentials narrow. Database is NOT in querying.md at all ; this
	// struct had no field naming which database to connect to at all
	// before this was added (see specs/TODO.md's own note on this
	// invented key).
	Host     string
	Port     int
	Database string

	// PoolSize is pg.pool_size (default DefaultPgPoolSize) : the max number
	// of connections in the pool that serves requests (pg.NewInfosAdminQuery's
	// queryURI pool) — never the short-lived, single-connection introspection
	// pool used once at startup for dmut/schema reading.
	PoolSize int

	// Query is pg.query.* — see PgQuery's own doc comment.
	Query PgQuery
}

// Blacklist holds the function/relation blacklist from querying.md's
// ## Scoping : `blacklist.functions.<schema>.<function_name_or_operator or
// *>` and `blacklist.relations.<schema>.<relation_name or *>`. Each is a
// two-level map — outer key schema, inner key name-or-"*" — rather than a
// single map flat-keyed by "<schema>.<name_or_*>" : that would need string
// interpolation ("schema"+"."+"name") at every lookup and construction site,
// where nesting matches the config key's own structure directly. Values are
// kept as the raw config string ("y"/"true"/... or "n"/"false"/...) rather
// than a bool, matching every other rel config key's convention — see
// IsTruthy.
type Blacklist struct {
	Functions map[string]map[string]string
	Relations map[string]map[string]string
}

// IsTruthy interprets a raw config value the way querying.md's "`y` or
// `true`" phrasing implies : accept the common truthy spellings, treat
// everything else (including an absent/empty entry) as false.
func IsTruthy(v string) bool {
	switch v {
	case "y", "Y", "yes", "true", "1":
		return true
	default:
		return false
	}
}

// IsFunctionBlacklisted reports whether schema.name is blacklisted : the
// exact name first, then the schema-wide "*" wildcard querying.md's key
// syntax allows.
func (b Blacklist) IsFunctionBlacklisted(schema, name string) bool {
	return IsTruthy(b.Functions[schema][name]) || IsTruthy(b.Functions[schema]["*"])
}

// IsRelationBlacklisted is IsFunctionBlacklisted for relations.
func (b Blacklist) IsRelationBlacklisted(schema, name string) bool {
	return IsTruthy(b.Relations[schema][name]) || IsTruthy(b.Relations[schema]["*"])
}

// DefaultMaxDepth is querying.md's pg.query.max_depth default.
const DefaultMaxDepth = 6

// DefaultBlacklist is querying.md ## Scoping's default blacklist, verbatim :
// pg_catalog and information_schema wholesale for relations (wildcarded
// deliberately, not enumerated — see querying.md's own "Why" on that), and
// the specific dangerous pg_catalog functions for functions, including the
// rest of the pg_advisory_*lock* family the spec's parenthetical calls out
// by name pattern rather than listing individually (the _unlock variants are
// deliberately excluded, per that same note : "which are harmless").
func DefaultBlacklist() Blacklist {
	return Blacklist{
		Relations: map[string]map[string]string{
			"pg_catalog":         {"*": "y"},
			"information_schema": {"*": "y"},
		},
		Functions: map[string]map[string]string{
			"pg_catalog": {
				"set_config":                       "y",
				"pg_sleep":                         "y",
				"pg_terminate_backend":             "y",
				"pg_cancel_backend":                "y",
				"pg_advisory_lock":                 "y",
				"pg_advisory_lock_shared":          "y",
				"pg_advisory_xact_lock":            "y",
				"pg_advisory_xact_lock_shared":     "y",
				"pg_try_advisory_lock":             "y",
				"pg_try_advisory_lock_shared":      "y",
				"pg_try_advisory_xact_lock":        "y",
				"pg_try_advisory_xact_lock_shared": "y",
			},
		},
	}
}
