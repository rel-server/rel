package config

// Config is rel's runtime configuration, as described in specs/querying.md
// ## Configuration and ## Scoping. This is only the shape ; nothing loads it
// from a file/env yet (config.tempindexthreshold, mentioned in querying.md's
// Writing Algorithm as a tentative option the redactor was "torn" on, isn't
// here either — deliberately, since it was never actually settled).
type Config struct {
	Pg      Pg
	Query   Query
	Logging Logging
	Http    Http

	Blacklist Blacklist
}

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

// Http is the HTTP listen address — invented for cmd/rel, since no spec
// under specs/ defines the bind host/port. http.host / http.port.
type Http struct {
	Host string
	Port int
}

// DefaultLoggingHandler/DefaultLoggingLevel/DefaultHttpPort are
// specs/01-logging.md's own stated defaults (Handler/Level) and this
// package's own invented default (Port — see Http's doc comment).
const (
	DefaultLoggingHandler = "pretty"
	DefaultLoggingLevel   = "info"
	DefaultHttpPort       = 8080
)

type Query struct {
	// MaxDepth is query.maxdepth : the maximum depth a query can specify.
	// querying.md's default is 6.
	MaxDepth int

	// WellKnownDirs is query.wellknown.path (well-known-queries.md
	// ## Configuration), default "/wellknown" : a colon-separated list of
	// directories, kept as the raw string here — splitting happens wherever
	// well-known loading itself gets built (not yet).
	WellKnownDirs string
}

type Login struct {
	User     string
	Password string
}

type Pg struct {
	// Querier is query.user / query.password : the login role rel connects
	// with, and the role `set role` switches are executed from. Login
	// groups the pair under one field per role (Querier vs. Dmut) rather
	// than flat QueryUser/QueryPassword strings, so the two roles are
	// disambiguated structurally (Querier.User vs. Dmut.User) instead of by
	// a naming convention a comment has to explain.
	Querier Login

	// Admin is dmut.user / dmut.password : the login rel will use to connect to the database to perform migrations with dmut, but also Querier's ident if not supplied.
	Admin Login

	// Anonymous is query.anonymous_role (default "~anonymous") : the role
	// rel switches to, from Querier, for requests with no credentials of
	// their own — used both for /rel (querying.md) and JWT verification
	// failures on /rpc (jwt-roles-and-http.md ## Roles, which used to name
	// this same setting jwt.anonrole ; reconciled onto query.anonymous_role,
	// the name querying.md already used).
	Anonymous string

	// Host/Port are query.host/query.port.
	Host string
	Port int

	// Database is query.database — NOT in querying.md at all ; this
	// struct had no field naming which database to connect to at all
	// before this was added (see specs/TODO.md's own note on this
	// invented key).
	Database string
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

// DefaultMaxDepth is querying.md's query.maxdepth default.
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
