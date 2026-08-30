// --help support : a single declarative table of every dotted config key
// this package understands (Options below), rendered by Help(). Kept next
// to loader.go's assemble() deliberately — assemble() is the actual
// authority on which keys exist ; this table must be kept in sync with it
// by hand when a key is added/removed/renamed, but every DEFAULT value
// quoted here is the same exported constant assemble() itself passes to
// *OrDefault, so a default can never drift out of sync the way a
// hand-copied literal could.
package config

import (
	"fmt"
	"strings"
)

// Option documents one dotted config key for --help : Env and Flag are
// mechanically derived from Key (EnvVar/FlagOf below), never hand-typed,
// so they can't drift from Key itself.
type Option struct {
	Key     string
	Default string // human-readable ; "" means "no default — see Description"
	Desc    string
}

// EnvVar derives a dotted key's REL_ environment variable form —
// "pg.query.anonymous_role" -> "REL_PG__QUERY__ANONYMOUS_ROLE" — matching
// loader.go's own env.Provider TransformFunc exactly (uppercase, "." ->
// "__"), just run in reverse.
func EnvVar(key string) string {
	return "REL_" + strings.ToUpper(strings.ReplaceAll(key, ".", "__"))
}

// FlagOf derives a dotted key's CLI flag form — "query.host" ->
// "--query.host" — matching parseFlags' own dotted-key convention exactly.
func FlagOf(key string) string {
	return "--" + key
}

// Options is every dotted key assemble() (loader.go) reads, in the same
// order assemble() reads them, grouped by section for --help's own
// rendering (Sections below defines the group boundaries).
var Options = []Option{
	// ---- pg.* : Postgres connection ----
	{"pg.uri", "", "Full \"postgres://user:pass@host:port/db\" connection string. Authoritative when set — pg.user/password/host/port/database below are ignored. --pg.uri alone is enough to run rel."},
	{"pg.user", "", "Primary login username, used when pg.uri is unset. Falls back for pg.query.user."},
	{"pg.password", "", "Password for pg.user."},
	{"pg.host", DefaultPgHost, "Postgres host, used when pg.uri is unset."},
	{"pg.port", fmt.Sprint(DefaultPgPort), "Postgres port, used when pg.uri is unset."},
	{"pg.database", "", "Database name, used when pg.uri is unset. Required one way or the other."},
	{"pg.pool_size", fmt.Sprint(DefaultPgPoolSize), "Max connections in the pool that serves requests. Never affects startup introspection or dmut migrations, which use one short-lived connection regardless."},
	{"pg.query.user", "= pg.user", "Narrower-scoped login used to serve requests. Optional — introspection and migrations always use pg.user/pg.uri regardless."},
	{"pg.query.password", "= pg.password", "Password for pg.query.user."},
	{"pg.query.anonymous_role", DefaultPgQueryAnonymousRole, "Role used for requests without a valid session."},
	{"pg.query.max_depth", fmt.Sprint(DefaultMaxDepth), "Maximum nesting depth a query may specify."},
	{"pg.query.wellknown_path", DefaultPgQueryWellKnownPath, "Colon-separated directories searched for well-known queries."},

	// ---- http.* : HTTP server + /rpc route-function discovery ----
	{"http.host", "(all interfaces)", "HTTP listen address."},
	{"http.port", fmt.Sprint(DefaultHttpPort), "HTTP listen port."},
	{"http.request_domain_name", DefaultHttpRequestDomainName, "Name of the JSON domain identifying a route function's request argument type."},
	{"http.response_domain_name", DefaultHttpResponseDomainName, "Name of the JSON domain identifying a route function's response type."},
	{"http.cookies_max_age", fmt.Sprint(DefaultHttpCookiesMaxAge) + " (seconds)", "Default max-age for cookies set via a route response, when unspecified. Doesn't apply to the JWT cookie — see jwt.max_age."},
	{"http.functions.allowed_auth", "(unrestricted)", "Regexp restricting which route functions may mint or clear a session."},
	{"http.functions.allowed_routes", "(unrestricted)", "Regexp restricting which route functions are exposed as /rpc routes."},
	{"http.functions.check_session", "(disabled)", "Function called on every authenticated request, letting the database reject a session early."},
	{"http.static.path", DefaultHttpStaticPath, "Path prefix for static file serving (not yet implemented)."},

	// ---- jwt.* : session lifecycle ----
	{"jwt.secret", "(auto-generated)", "HMAC signing secret for JWTs. Auto-generates a 32-character secret into ./jwt-secret on first run and reuses it after — see $FILE$/$GEN$ below."},
	{"jwt.cookie_name", DefaultJwtCookieName, "Name of the cookie carrying the JWT."},
	{"jwt.algorithm", DefaultJwtAlgorithm, "Signing algorithm : HS256, HS384, or HS512."},
	{"jwt.same_site", DefaultJwtSameSite, "SameSite attribute of the JWT cookie : Strict, Lax, or None."},
	{"jwt.max_age", fmt.Sprint(DefaultJwtMaxAge) + " (seconds)", "How long a freshly-minted token stays valid."},
	{"jwt.renew_after", fmt.Sprint(DefaultJwtRenewAfter), "Fraction of a token's lifespan after which it's renewed on next use."},
	{"jwt.max_session_age", fmt.Sprint(DefaultJwtMaxSessionAge) + " (seconds)", "Hard ceiling on a session's total lifetime, from first auth — survives renewal."},

	// ---- logging.* ----
	{"logging.handler", DefaultLoggingHandler, "Log output format : pretty or JSON."},
	{"logging.level", DefaultLoggingLevel, "Minimum log level : debug, info, warn, or error."},
}

// Sections groups Options for --help's own rendering — {heading, key
// prefix}, checked in order, first match wins. Keep in sync with Options'
// own key set above (an unmatched key falls into a catch-all "Other"
// section rather than being silently dropped).
var sections = []struct {
	Heading string
	Prefix  string
}{
	{"Postgres connection", "pg."},
	{"HTTP server / /rpc route functions", "http."},
	{"JWT sessions", "jwt."},
	{"Logging", "logging."},
}

func sectionFor(key string) string {
	for _, s := range sections {
		if strings.HasPrefix(key, s.Prefix) {
			return s.Heading
		}
	}
	return "Other"
}

// Help renders the full --help text : usage, sources/precedence, config
// file discovery, then every Options entry grouped by section, then the
// dynamic namespaces Options can't enumerate individually
// (logging.filter.*/exclude.*, blacklist.*), then $FILE$ indirection.
func Help() string {
	var b strings.Builder

	b.WriteString(`rel — a bidirectional Postgres query engine over HTTP

Usage:
  rel [flags]

rel has no subcommands. It loads configuration (below), connects to
Postgres, and serves POST /rel and /rpc/{schema}/{function} until an
interrupt/terminate signal requests a graceful shutdown.

Flags:
  -c, --config <path>   Load exactly this config file (same as REL_CONFIG).
  -h, --help            Show this help and exit.

Any config key documented below can ALSO be set as a command-line flag :
  --<key>=<value>   or   --<key> <value>

Configuration sources, later overriding earlier for the same key :
  1. Config file (TOML, YAML, or HUML) — see "Config file discovery" below.
  2. Environment variables, prefixed REL_, "__" as the dot delimiter
     (REL_PG__HOST=... -> pg.host).
  3. Command-line flags (highest precedence), dotted form directly.

Config file discovery (only when -c/--config and REL_CONFIG are both unset),
first match wins, more than one recognized file at the same step is a fatal
"ambiguous config files" error :
  1. ./rel.toml, ./rel.yaml, ./rel.yml, ./rel.huml
  2. $XDG_CONFIG_HOME/rel/rel.{toml,yaml,yml,huml} (XDG_CONFIG_HOME defaults to ~/.config)
  3. /etc/rel/rel.{toml,yaml,yml,huml}
  No file at any step is not an error — rel proceeds on env vars, flags,
  and the built-in defaults below alone.

Configuration keys :
`)

	grouped := map[string][]Option{}
	var order []string
	for _, opt := range Options {
		s := sectionFor(opt.Key)
		if _, seen := grouped[s]; !seen {
			order = append(order, s)
		}
		grouped[s] = append(grouped[s], opt)
	}

	keyWidth := 0
	envWidth := 0
	for _, opt := range Options {
		if len(opt.Key) > keyWidth {
			keyWidth = len(opt.Key)
		}
		if e := len(EnvVar(opt.Key)); e > envWidth {
			envWidth = e
		}
	}

	for _, heading := range order {
		fmt.Fprintf(&b, "\n  %s\n", heading)
		for _, opt := range grouped[heading] {
			def := opt.Default
			if def == "" {
				def = "(none)"
			}
			fmt.Fprintf(&b, "    %-*s  %-*s  default %s\n", keyWidth, opt.Key, envWidth, EnvVar(opt.Key), def)
			fmt.Fprintf(&b, "        %s\n", wrapIndented(opt.Desc, 8))
		}
	}

	b.WriteString(`
Dynamic namespaces (not enumerated above — "*" matches an entire schema) :
  logging.filter.<attr>=<regexp>              only show log records whose <attr> matches <regexp>
  logging.exclude.<attr>=<regexp>              suppress records whose <attr> matches <regexp> (applied after filter)
  blacklist.relations.<schema>.<name|*>=y|n    blacklist a relation, or a whole schema, from being queried
  blacklist.functions.<schema>.<name|*>=y|n    blacklist a function from being called
  Both blacklists only ever ADD to the built-in defaults ; neither ever removes from them.
  As flags/config file keys these are dotted directly ; as environment variables, the
  same REL_/"__" rule as every other key applies to the placeholders too, e.g.
  blacklist.relations.public.orders=y -> REL_BLACKLIST__RELATIONS__PUBLIC__ORDERS=y.
  An underscore WITHIN a name (e.g. a schema called my_app) is indistinguishable from
  a "__" boundary in env-var form — prefer a config file or flag for such a name.

Any scalar value, from any source, may be written as a file reference
instead of literally :
  $FILE$/path                        the file's contents (one trailing newline trimmed)
  $FILE$/path$DEFAULT$fallback       the file's contents if it exists, else the literal fallback text
  $FILE$/path$GEN$<n>                the file's contents if it exists, else <n> random characters,
                                      generated once and written to path

Configuration MUST NOT contain arrays in any source : a set of named things
becomes named keys (REL_OPENID__GOOGLE__SECRET, not a list), and a list of
plain scalars is one comma-separated string.
`)

	return b.String()
}

// wrapIndented is a small, deliberately simple word-wrap : Desc strings
// here are one paragraph each, wrapped to a fixed 78-column budget (a
// conservative width that fits comfortably in a default 80-column
// terminal even after the indent), continuation lines indented to align
// under the description column rather than the key column.
func wrapIndented(s string, indent int) string {
	const width = 78
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	pad := strings.Repeat(" ", indent)
	var lines []string
	line := words[0]
	for _, w := range words[1:] {
		if len(line)+1+len(w) > width-indent {
			lines = append(lines, line)
			line = w
			continue
		}
		line += " " + w
	}
	lines = append(lines, line)
	return strings.Join(lines, "\n"+pad)
}
