package config

// Config is rel's runtime configuration, as described in specs/querying.md
// ## Configuration and ## Scoping. This is only the shape ; nothing loads it
// from a file/env yet (config.tempindexthreshold, mentioned in querying.md's
// Writing Algorithm as a tentative option the redactor was "torn" on, isn't
// here either — deliberately, since it was never actually settled).
type Config struct {
	Pg    Pg
	Query Query

	Blacklist Blacklist
}

type Query struct {
	// MaxDepth is query.maxdepth : the maximum depth a query can specify.
	// querying.md's default is 6.
	MaxDepth int

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

	// Anonymous is the role rel switches to, from Querier, for requests
	// with no credentials of their own.
	Anonymous string

	Host string
	Port int
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
