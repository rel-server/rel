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
	// ---- dev : top-level, ungrouped ----
	{"dev", "false", "Development mode : shows the real Postgres error text/stack trace on an otherwise-generic 5xx, and the real message on a permission-denied error, instead of a safe generic one. See error-handling.md ## Postgres error detail / ## Stack traces."},

	// ---- pg.* : Postgres connection ----
	{"pg.uri", "", "Full \"postgres://user:pass@host:port/db\" connection string. Takes precedence when set, populating pg.host/pg.port/pg.database itself — setting them alongside pg.uri is an error. --pg.uri alone is enough to run rel."},
	{"pg.user", "", "Primary login username, used when pg.uri is unset — for introspection, reload.cmd, and serving requests alike."},
	{"pg.password", "", "Password for pg.user."},
	{"pg.host", DefaultPgHost, "Postgres host, used when pg.uri is unset."},
	{"pg.port", fmt.Sprint(DefaultPgPort), "Postgres port, used when pg.uri is unset."},
	{"pg.database", "", "Database name, used when pg.uri is unset. Required one way or the other."},
	{"pg.pool_size", fmt.Sprint(DefaultPgPoolSize), "Max connections in the pool that serves requests. Never affects startup introspection, which uses one short-lived connection regardless."},
	{"pg.query.anonymous_role", DefaultPgQueryAnonymousRole, "Role used for requests without a valid session."},
	{"pg.query.max_depth", fmt.Sprint(DefaultMaxDepth), "Maximum nesting depth a query may specify."},
	{"pg.query.wellknown_path", DefaultPgQueryWellKnownPath, "Colon-separated directories searched for well-known queries."},

	// ---- http.* : HTTP server + declared-route discovery ----
	{"http.host", "(all interfaces)", "HTTP listen address."},
	{"http.public_host", "(disabled)", "This deployment's own externally-reachable domain (a bare host, e.g. app.example.com — no scheme/port/path ; rel always builds https://<host>/... from it). An openid.<name>/saml.<name> entry with no effective host (its own public_host override, or this one) is skipped — see docs/content/http/authentication.md ## OpenID Connect and SAML."},
	{"http.port", fmt.Sprint(DefaultHttpPort), "HTTP listen port."},
	{"http.cookies_max_age", fmt.Sprint(DefaultHttpCookiesMaxAge) + " (seconds)", "Default max-age for cookies set via a route response, when unspecified. Doesn't apply to the JWT cookie — see jwt.max_age."},
	{"http.max_body_size", fmt.Sprint(DefaultHttpMaxBodySize) + " (bytes)", "Hard cap on a declared route request's entire body (for multipart, the whole envelope — boundaries and part headers included, not just part payload bytes). Rejected with 413 before any of it is buffered in memory."},
	{"http.max_upload_size", "= http.max_body_size", "Hard cap, in bytes, on a stream_upload route's streamed payload (see specs/new-routes.md ## `stream_upload`). Since that payload streams to a temp file rather than being buffered in memory, this can be set much higher than http.max_body_size. A stream_upload function's first call may return a smaller upload.max_size to tighten this per-request ; it can never raise it."},
	{"http.max_part_count", fmt.Sprint(DefaultHttpMaxPartCount), "Max number of multipart/form-data parts a single declared-route request may contain, independent of their total byte size."},
	{"http.functions.allowed_auth", "(unrestricted)", "Regexp restricting which route/middleware functions may mint or clear a session."},
	{"http.functions.sso_callback", "(disabled)", "Fallback Postgres function an openid.<name>/saml.<name> entry's own callback_function calls when it doesn't set one itself (docs/content/http/authentication.md ## OpenID Connect and SAML)."},
	{"http.static.path", DefaultHttpStaticPath, "Colon-separated list of filesystem directories served as the root-level static fallback for any path no declared route claims."},
	{"http.templates.path", DefaultHttpTemplatesPath, "Filesystem directory Jet templates (a route's or middleware's own \"template\") are loaded from."},
	{"http.typescript.enable", "false (true if dev)", "Serve GET /rel/database.ts — the introspected schema as TypeScript types plus a few query-building helpers."},
	{"http.typescript.schemas", "(every schema, aside from pg_catalog)", "Comma-separated whitelist of schemas GET /rel/database.ts may export ; intersected with the endpoint's own ?schemas= query param."},
	{"http.cors.allowed_origins", "(empty — CORS closed)", "Comma-separated list of exact origins allowed to make cross-origin requests, or the literal \"*\"."},
	{"http.cors.allowed_methods", DefaultHttpCorsAllowedMethods, "Methods a CORS preflight may approve."},
	{"http.cors.allowed_headers", DefaultHttpCorsAllowedHeaders, "Request headers a CORS preflight may approve."},
	{"http.cors.max_age", fmt.Sprint(DefaultHttpCorsMaxAge) + " (seconds)", "How long a browser may cache one CORS preflight response."},
	{"http.csp.default_src", DefaultHttpCspDefaultSrc, "CSP default-src directive value."},
	{"http.csp.script_src", "(unset — falls back to default-src)", "CSP script-src directive value."},
	{"http.csp.style_src", "(unset — falls back to default-src)", "CSP style-src directive value."},
	{"http.csp.img_src", "(unset — falls back to default-src)", "CSP img-src directive value."},
	{"http.csp.font_src", "(unset — falls back to default-src)", "CSP font-src directive value."},
	{"http.csp.connect_src", "(unset — falls back to default-src)", "CSP connect-src directive value."},
	{"http.csp.object_src", "(unset — falls back to default-src)", "CSP object-src directive value."},
	{"http.csp.frame_ancestors", "(unset — falls back to default-src)", "CSP frame-ancestors directive value."},
	{"http.csp.base_uri", "(unset — falls back to default-src)", "CSP base-uri directive value."},
	{"http.csp.form_action", "(unset — falls back to default-src)", "CSP form-action directive value."},
	{"http.csp.policy", "(unset)", "Full, raw Content-Security-Policy header value ; replaces every individual http.csp.* directive above entirely when set."},

	// ---- jwt.* : session lifecycle ----
	{"jwt.secret", "(auto-generated)", "HMAC signing secret for JWTs. Auto-generates a 32-character secret into /secrets/jwt-secret (falling back to ./jwt-secret) on first run and reuses it after — see $FILE$/$GEN$ below."},
	{"jwt.cookie_name", DefaultJwtCookieName, "Name of the cookie carrying the JWT."},
	{"jwt.algorithm", DefaultJwtAlgorithm, "Signing algorithm : HS256, HS384, or HS512."},
	{"jwt.same_site", DefaultJwtSameSite, "SameSite attribute of the JWT cookie : Strict, Lax, or None."},
	{"jwt.max_age", fmt.Sprint(DefaultJwtMaxAge) + " (seconds)", "How long a freshly-minted token stays valid."},
	{"jwt.renew_after", fmt.Sprint(DefaultJwtRenewAfter), "Fraction of a token's lifespan after which it's renewed on next use."},
	{"jwt.max_session_age", fmt.Sprint(DefaultJwtMaxSessionAge) + " (seconds)", "Hard ceiling on a session's total lifetime, from first auth — survives renewal."},

	// ---- saml.* : the shared SP identity across every saml.<name> IdP ;
	// per-name saml.<name>.*/openid.<name>.* entries are prose-documented
	// below (## Dynamic namespaces), not table rows, same as
	// route.<schema>.<function>.* ----
	{"saml.certificate_path", DefaultSamlCertificatePath, "Colon-separated search list for this deployment's SAML SP certificate. Loaded if found ; generated and persisted to the first candidate whose parent directory exists, otherwise."},
	{"saml.private_key_path", DefaultSamlPrivateKeyPath, "Colon-separated search list for the SP certificate's private key — see saml.certificate_path."},

	// ---- logging.* ----
	{"logging.handler", DefaultLoggingHandler, "Log output format : pretty or JSON."},
	{"logging.level", DefaultLoggingLevel, "Minimum log level : debug, info, warn, or error."},

	// ---- reload.* ----
	{"reload.cmd", "", "Command line rel runs before every reload (SIGUSR1 or startup). Empty skips this step entirely. Interpolated ({name}/{name:default} against configuration keys and environment variables), then split with shell-style word-splitting/quoting rules."},
	{"reload.timeout", fmt.Sprint(DefaultReloadTimeout) + " (seconds)", "reload.cmd is aborted and considered failed once this elapses."},
	{"reload.drain_timeout", fmt.Sprint(DefaultReloadDrainTimeout) + " (seconds)", "How long a reload waits for in-flight requests to finish, before reload.cmd runs, before cancelling their contexts and proceeding anyway."},

	// ---- typescript.* ----
	{"typescript.helper_path", "(disabled)", "Filesystem path rel (re)writes database.ts's content to directly, at startup and on every SIGUSR1 reload — for an editor/LSP watching a real file on disk, without a request round-trip."},
}

// sections groups Options for --help's rendering ; checked in order, first
// match wins, with an unmatched key falling into a catch-all "Other".
var sections = []struct {
	Heading string
	Prefix  string
}{
	{"Postgres connection", "pg."},
	{"HTTP server / /route functions", "http."},
	{"JWT sessions", "jwt."},
	{"Logging", "logging."},
	{"Reload", "reload."},
	{"TypeScript export", "typescript."},
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
Postgres, and serves POST /rel plus every declared route (route.<schema>.
<function>.path, or a "route::"/"route:{...}" comment on the function
itself — see specs/new-routes.md) until an interrupt/terminate signal
requests a graceful shutdown.

Flags:
  -c, --config <path>          Load exactly this config file (same as REL_CONFIG).
  -h, --help                   Show this help and exit.
  --typescript-out <path>      Introspect, write database.ts to <path> (or "-" for stdout), and exit —
                                no reload.cmd, no /route or well-known registries, no HTTP listener. Honors
                                pg.*/http.typescript.schemas/blacklist.* from the loaded configuration.

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
  route.<schema>.<function>.path=<chi-path>    declare <schema>.<function> routable at <chi-path>
  route.<schema>.<function>.method=<verbs>     comma-separated accepted methods ; inferred if unset
  route.<schema>.<function>.template=<path>    default Jet template, used when the response doesn't set its own
  route.<schema>.<function>.stream_upload=y|n  disk-streamed single-upload flow, called twice (see specs/new-routes.md)
  route.<schema>.<function>.middleware=y|n     runs ahead of every route, static file, or /rel request under its own path prefix
  openid.<name>.issuer=<url>                   OIDC issuer URL ; /.well-known/openid-configuration is discovered from it
  openid.<name>.client_id=<id>                 default $FILE$/secrets/openid-<name>.id:./openid-<name>.id
  openid.<name>.client_secret=<secret>         default $FILE$/secrets/openid-<name>.secret:./openid-<name>.secret
  openid.<name>.scopes=<comma-separated>       default openid,email,profile
  openid.<name>.fetch_userinfo=y|n             also call the userinfo endpoint, merging its claims over the ID token's
  openid.<name>.callback_function=<fqname>     falls back to http.functions.sso_callback when unset
  openid.<name>.public_host=<host>             overrides http.public_host for this entry alone
  saml.<name>.idp_metadata_url=<url>           fetched once at startup, retried lazily on the next login attempt if it fails
  saml.<name>.force_signed_requests=y|n        default y : sign the outgoing AuthnRequest with the SP key
  saml.<name>.callback_function=<fqname>       falls back to http.functions.sso_callback when unset
  saml.<name>.public_host=<host>               overrides http.public_host for this entry alone
  See docs/content/http/authentication.md ## OpenID Connect and SAML for the full
  /auth/oidc/<name>/*, /auth/saml/<name>/* contract.
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

// wrapIndented word-wraps s to a fixed 78-column budget (fits an 80-column
// terminal after indent), continuation lines aligned under the description.
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
