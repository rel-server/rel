package config

// Config is rel's runtime configuration — connection settings are
// docs/content/configuration/index.md ### Postgres connection, blacklist shape
// is specs/query-engine.md ## Scoping. This is only the shape ; nothing loads
// it from a file/env yet (config.tempindexthreshold, mentioned in
// query-engine.md's Writing Algorithm as a tentative option the redactor was
// "torn" on, isn't here either — deliberately, since it was never actually
// settled).
type Config struct {
	Pg      Pg
	Logging Logging
	Http    Http
	Jwt     Jwt
	Dmut    Dmut

	// Openid/Saml are specs/oauth-saml.md's openid.<name>.*/saml.* :
	// the /auth/oidc/{name}/* and /auth/saml/{name}/* endpoints.
	Openid map[string]OpenidProvider
	Saml   Saml

	Blacklist Blacklist

	// Route is route.<schema>.<function>.* — specs/new-routes.md
	// ## In the configuration : config-declared routes, keyed schema then
	// function name.
	Route map[string]map[string]RouteDecl

	// TypeScript is specs/typescript.md ## Configuration's typescript.* :
	// typescript.helper_path only, as of now — the endpoints themselves are
	// gated by Http.TypeScript/Http.Json below.
	TypeScript TypeScript

	// Dev is docs/content/configuration/index.md ## Development mode's `dev`
	// key, default false : gates the extra detail error-handling.md ##
	// Postgres error detail and ## Stack traces add to error responses.
	Dev bool
}

// TypeScript is specs/typescript.md ## Configuration's typescript.* namespace.
type TypeScript struct {
	// HelperPath is typescript.helper_path, default "" (disabled) : a
	// filesystem path rel (re)writes database.ts's content to directly, at
	// startup and on every SIGUSR1 reload (specs/typescript.md ## Reloading
	// `helper_path`), on top of serving it over HTTP.
	HelperPath string
}

// Dmut is specs/migrations.md ## Configuration : dmut.path/reload_drain_timeout.
type Dmut struct {
	// Path is dmut.path, default "/dmut" : directory containing the
	// mutation files dmut reads recursively. Missing directory means dmut
	// is skipped entirely, not an error — see specs/migrations.md ## Execution.
	Path string
	// ReloadDrainTimeout is dmut.reload_drain_timeout, default 30
	// (seconds) : how long a SIGUSR1 reload waits for in-flight requests
	// to finish before cancelling their contexts and proceeding anyway —
	// see specs/migrations.md ## Reloading.
	ReloadDrainTimeout int
}

// DefaultDmutPath/DefaultDmutReloadDrainTimeout are specs/migrations.md ##
// Configuration's own stated defaults.
const (
	DefaultDmutPath               = "/dmut"
	DefaultDmutReloadDrainTimeout = 30
)

// Jwt is authentication.md ## Configuration : jwt.secret/cookie_name/
// algorithm/same_site/max_age/renew_after/max_session_age.
type Jwt struct {
	// Secret is jwt.secret, default DefaultJwtSecret — the JWT signing
	// secret. Its $GEN$ path is a colon-separated search list (##
	// $GEN$ multi-path resolution) : /secrets/jwt-secret, a flat filename
	// directly under a deployment's mounted /secrets/ volume — rel never
	// creates directories, only files, so a flat path only requires
	// /secrets/ itself to exist — falling back to ./jwt-secret (relative
	// to the process's cwd) when that isn't wired up — the previous,
	// single-path default, still exactly what a plain `go run`/`just run`
	// dev loop gets.
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

// DefaultJwt* are authentication.md ## Configuration's stated defaults.
const (
	// DefaultJwtSecret's $GEN$ path is a colon-separated search list, resolved
	// per specs/configuration.md ## $GEN$ multi-path resolution : a fixed
	// deployment mount point first, falling back to the process's own cwd.
	DefaultJwtSecret        = "$FILE$/secrets/jwt-secret:./jwt-secret$GEN$32"
	DefaultJwtCookieName    = "accesstoken"
	DefaultJwtAlgorithm     = "HS256"
	DefaultJwtSameSite      = "Lax"
	DefaultJwtMaxAge        = 1800
	DefaultJwtRenewAfter    = 0.5
	DefaultJwtMaxSessionAge = 604800
)

// Logging is docs/content/configuration/operations.md ## Logging :
// logging.handler, logging.level, logging.filter.*, logging.exclude.*.
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
// route.md ## HTTP ## Configuration's own http.* keys, which
// govern /route function discovery and dispatch.
type Http struct {
	Host string
	Port int

	// PublicHost is http.public_host, default "" : this deployment's own
	// externally-reachable domain (a bare host, e.g. "app.example.com" —
	// NO scheme, port, or path). rel always builds "https://<host>/..."
	// from it — see docs/content/http/authentication.md ## OpenID Connect
	// and SAML for why a bare host, not a full URL, and why the scheme is
	// never configurable.
	// Needed because both OIDC's redirect_uri and SAML's metadata/ACS URLs
	// require a STABLE, exactly-registered-with-the-IdP value, not one
	// derived per-request from the incoming Host header (most IdPs require
	// an exact, pre-registered redirect_uri, so a value that could vary by
	// request is a non-starter). An openid.<name>/saml.<name> entry with
	// no effective host (neither its own OpenidProvider.PublicHost/
	// SamlProvider.PublicHost override nor this one set) is skipped, not
	// fatal — see sso.Mount's own doc comment.
	PublicHost string

	// RequestDomainName is http.request_domain_name, default
	// "RelHttpRequest" : the fully qualified, unquoted name of the JSON
	// domain identifying a route function's single request-typed argument.
	RequestDomainName string
	// ResponseDomainName is http.response_domain_name, default
	// "RelHttpResponse" : the fully qualified, unquoted name of the JSON
	// domain identifying a route function's response return type.
	ResponseDomainName string
	// UploadDomainName is http.upload_domain_name, default "RelUpload" :
	// the unquoted name of the JSON domain used by
	// specs/http-content.md ## Static files ### Upload destinations'
	// two-function upload mechanism. Not an error if it doesn't resolve —
	// that mechanism simply isn't discovered, same non-fatal treatment as
	// RequestDomainName/ResponseDomainName above.
	UploadDomainName string
	// CookiesMaxAge is http.cookies_max_age, default 86400 : default max-age
	// for cookies set via the generic "cookies" field, when the response
	// doesn't specify one. Does not apply to the JWT cookie (see Jwt.MaxAge).
	CookiesMaxAge int
	// MaxBodySize is http.max_body_size, default 10485760 (10 MiB) : hard
	// cap, in bytes, on a /route request's ENTIRE body — for multipart, the
	// whole envelope (boundaries and part headers included, not just the
	// sum of part payload bytes). Enforced before any of it is buffered in
	// memory — see route.md ## Configuration. Scoped to /route
	// only, never /rel.
	MaxBodySize int
	// MaxUploadSize is http.max_upload_size, default equal to MaxBodySize :
	// hard cap, in bytes, on the streamed payload of a single-upload /route
	// request (specs/http-content.md ## Upload destinations) — replaces
	// MaxBodySize as the ceiling on that path, since the payload streams
	// straight to a temp file rather than being buffered in memory, so it
	// can reasonably be set much higher. A route's __prepare function may
	// return a smaller RelUpload.max_size to tighten this further for a
	// given request (e.g. a per-user quota) ; it can never raise it above
	// this configured ceiling.
	MaxUploadSize int
	// MaxPartCount is http.max_part_count, default 100 : max number of
	// multipart/form-data parts a single /route request may contain,
	// independent of their total byte size — see ## Request bodies
	// ### Limits.
	MaxPartCount int

	Functions  HttpFunctions
	Static     HttpStatic
	Templates  HttpTemplates
	Cors       HttpCors
	Csp        HttpCsp
	TypeScript HttpTypeScript
}

// HttpTypeScript is http.typescript.* — specs/typescript.md ##
// Configuration/## Endpoints : GET /rel/database.ts.
type HttpTypeScript struct {
	// Enable is http.typescript.enable, default false (true if Dev is
	// enabled) : serves GET /rel/database.ts.
	Enable bool
	// Schemas is http.typescript.schemas, default "" (every schema found,
	// aside from pg_catalog) : a comma-separated whitelist of schemas that
	// may be exported, matching every other comma-separated list convention
	// (e.g. http.cors.allowed_origins) since config can't hold arrays.
	// Intersected with the endpoint's own `schemas` query param, when given.
	Schemas string
}

// HttpTemplates is http.templates.* — specs/http-content.md ##
// Templates.
type HttpTemplates struct {
	// Path is http.templates.path, default "/template" (renamed from the
	// never-implemented http.templatesdir) : the filesystem directory Jet
	// templates are loaded from.
	Path string
}

// HttpCors is http.cors.* — specs/http-content.md ## CORS ##
// Configuration.
type HttpCors struct {
	// AllowedOrigins is http.cors.allowed_origins, default "" (CORS fully
	// closed) : comma-separated list of exact origins, or the literal "*"
	// (see ### `*` as an explicit value).
	AllowedOrigins string
	// AllowedMethods is http.cors.allowed_methods, default "GET, POST, PUT,
	// PATCH, DELETE, OPTIONS" : methods a preflight may approve.
	AllowedMethods string
	// AllowedHeaders is http.cors.allowed_headers, default "Content-Type" :
	// request headers a preflight may approve, beyond the browser's always-
	// allowed simple set.
	AllowedHeaders string
	// MaxAge is http.cors.max_age, default 600 (seconds) : how long a
	// browser may cache one preflight response.
	MaxAge int
}

// HttpCsp is http.csp.* — specs/http-content.md ## CSP ## Configuration
// : one config key per CSP directive, plus a raw full-policy override.
type HttpCsp struct {
	DefaultSrc     string
	ScriptSrc      string
	StyleSrc       string
	ImgSrc         string
	FontSrc        string
	ConnectSrc     string
	ObjectSrc      string
	FrameAncestors string
	BaseUri        string
	FormAction     string
	// Policy is http.csp.policy, default "" : a full, raw
	// Content-Security-Policy header value, semicolon-separated directives
	// exactly as the header itself is written. When set, REPLACES every
	// individual directive above entirely.
	Policy string
}

// RouteDecl is one route.<schema>.<function>.* config-declared route —
// specs/new-routes.md ## In the configuration. Schema/function are
// inferred from the config key path itself, not stored here. Mirrors the
// same fields a comment-embedded Route declaration carries (see
// route.Declaration), minus function/schema.
type RouteDecl struct {
	// Path is route.<schema>.<function>.path : a path in chi syntax.
	// Required for the function to become routable at all.
	Path string
	// Method is route.<schema>.<function>.method, default "" (inferred —
	// see specs/new-routes.md ## Method inference) : a comma-separated
	// list of accepted HTTP methods.
	Method string
	// Template is route.<schema>.<function>.template : a Jet template path,
	// used when the response doesn't set its own.
	Template string
	// StreamUpload is route.<schema>.<function>.stream_upload.
	StreamUpload bool
	// Middleware is route.<schema>.<function>.middleware.
	Middleware bool
}

// StaticAccessRule is one http.static.access.<name>.* entry —
// specs/http-content.md ## Static files ### Access control.
type StaticAccessRule struct {
	// Prefix is http.static.access.<name>.prefix : the subpath prefix this
	// rule gates.
	Prefix string
	// Function is http.static.access.<name>.function : unquoted, fully
	// qualified name of the Postgres function called for any request whose
	// path falls under Prefix.
	Function string
}

// HttpFunctions is route.md's http.functions.* namespace.
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
	// must additionally match to become a public /route function.
	AllowedRoutes string
	// CheckSession is http.functions.check_session, default "" (disabled)
	// : unquoted, fully qualified name of a Postgres function that lets the
	// database reject a session before exp/max_session_age would otherwise.
	CheckSession string
	// SsoCallback is http.functions.sso_callback, default "" (disabled) :
	// unquoted, fully qualified name of the Postgres function an
	// openid.<name>/saml.<name> entry's own callback_function falls back
	// to when unset — docs/content/http/authentication.md ## OpenID Connect
	// and SAML.
	SsoCallback string
}

// OpenidProvider is one openid.<name>.* entry — specs/oauth-saml.md
// ## Configuration — OIDC.
type OpenidProvider struct {
	// Issuer is openid.<name>.issuer (required) : /.well-known/openid-
	// configuration is fetched from it for discovery.
	Issuer string
	// ClientID/ClientSecret are openid.<name>.client_id/client_secret,
	// default DefaultOpenidClientIDPath/DefaultOpenidClientSecretPath with
	// "<name>" substituted — see those constants' own doc comment for why
	// there's no $GEN$ fallback the way jwt.secret/Saml's cert have one.
	ClientID     string
	ClientSecret string
	// Scopes is openid.<name>.scopes, default DefaultOpenidScopes —
	// comma-separated per configuration.md ## No arrays.
	Scopes []string
	// FetchUserinfo is openid.<name>.fetch_userinfo, default false : also
	// call the discovered userinfo endpoint after token exchange, merging
	// its claims over the ID token's own (userinfo wins on collision).
	FetchUserinfo bool
	// CallbackFunction is openid.<name>.callback_function, default "" :
	// falls back to HttpFunctions.SsoCallback when unset.
	CallbackFunction string
	// PublicHost is openid.<name>.public_host, default "" (empty) : an
	// optional override of Http.PublicHost for this entry alone — see
	// specs/oauth-saml.md ## Configuration — HTTP. Empty means "use
	// Http.PublicHost."
	PublicHost string
}

// Saml is saml.* : the default SP certificate/key (shared across every
// configured saml.<name> IdP that doesn't override it) plus the named
// saml.<name>.* entries themselves — specs/oauth-saml.md
// ## Configuration — SAML/## Certificate.
type Saml struct {
	// CertificatePath/PrivateKeyPath are saml.certificate_path/
	// saml.private_key_path, default DefaultSamlCertificatePath/
	// DefaultSamlPrivateKeyPath : colon-separated candidate search lists,
	// same shape jwt.secret's own default uses. Loaded if found (bring-
	// your-own-certificate) ; generated and persisted to the first
	// candidate whose parent directory exists, otherwise — see
	// ## Certificate. Used by any saml.<name> entry that doesn't set its
	// own SamlProvider.CertificatePath/PrivateKeyPath.
	CertificatePath string
	PrivateKeyPath  string
	// Providers is saml.<name>.* — named entries, same map-of-named-
	// sub-config shape as HttpStatic.Access.
	Providers map[string]SamlProvider
}

// SamlProvider is one saml.<name>.* entry.
type SamlProvider struct {
	// IdpMetadataUrl is saml.<name>.idp_metadata_url (required) : fetched
	// once at startup, lazily retried per ## Metadata fetch is lazy on
	// failure.
	IdpMetadataUrl string
	// ForceSignedRequests is saml.<name>.force_signed_requests, default
	// true : sign the outgoing AuthnRequest with the SP key. Defaults true
	// (not false) since the SP always has a key available and signing is
	// strictly more secure — the rare IdP that can't accept signed
	// requests is the actual exception case, and that's the one that
	// should opt out.
	ForceSignedRequests bool
	// CallbackFunction is saml.<name>.callback_function, default "" :
	// same fallback rule as OpenidProvider.CallbackFunction.
	CallbackFunction string
	// PublicHost is saml.<name>.public_host, default "" (empty) : same
	// per-entry override as OpenidProvider.PublicHost.
	PublicHost string
	// CertificatePath/PrivateKeyPath are saml.<name>.certificate_path/
	// saml.<name>.private_key_path, default "" (empty) : an optional
	// per-entry override of Saml.CertificatePath/PrivateKeyPath, same
	// resolveHost-style "own value wins when set, else the shared one"
	// rule as PublicHost. Same shape and same load-or-generate-and-persist
	// behavior as the shared pair when set — see ## Certificate.
	CertificatePath string
	PrivateKeyPath  string
}

// HttpStatic is http.static.* — static file serving, per
// specs/http-content.md ## Static files/### Access control : Path names
// a colon-separated list of FILESYSTEM directories (never the URL prefix,
// which is always the fixed "/static/"), Access is the named, prefix-scoped
// access-control rule set.
type HttpStatic struct {
	// Path is http.static.path, default "/static" : a COLON-SEPARATED list
	// of filesystem directories served at the fixed /static/ URL prefix
	// (search list, first match wins — see specs/http-content.md
	// ## Static files). NOT the URL prefix itself, which is always the
	// fixed, unconfigurable "/static/".
	Path string
	// Access is http.static.access.<name>.* — named, prefix-scoped access
	// control rules (specs/http-content.md ### Access control).
	Access map[string]StaticAccessRule
}

// DefaultLoggingHandler/DefaultLoggingLevel/DefaultHttpPort are
// docs/content/configuration/operations.md's own stated defaults
// (Handler/Level) and this package's own invented default (Port — see
// Http's doc comment). DefaultHttpRequestDomainName/
// DefaultHttpResponseDomainName are docs/content/http/requests-responses.md's
// stated defaults ; DefaultHttpCookiesMaxAge is route.md's own stated
// default. DefaultHttpStaticPath is this package's own invented default,
// matching HttpStatic's doc comment.
const (
	DefaultLoggingHandler         = "pretty"
	DefaultLoggingLevel           = "info"
	DefaultHttpPort               = 8080
	DefaultHttpRequestDomainName  = "RelHttpRequest"
	DefaultHttpResponseDomainName = "RelHttpResponse"
	DefaultHttpCookiesMaxAge      = 86400
	DefaultHttpStaticPath         = "/static"
	// DefaultHttpMaxBodySize/DefaultHttpMaxPartCount are jwt-roles-and-
	// http.md's own stated defaults for http.max_body_size (10 MiB) and
	// http.max_part_count (100).
	DefaultHttpMaxBodySize  = 10485760
	DefaultHttpMaxPartCount = 100
	// DefaultHttpUploadDomainName is docs/content/http/uploads.md's stated
	// default for http.upload_domain_name.
	DefaultHttpUploadDomainName = "RelUpload"
	// DefaultHttpTemplatesPath is specs/http-content.md ## Templates'
	// stated default for http.templates.path.
	DefaultHttpTemplatesPath = "/template"
	// DefaultHttpCorsAllowedMethods/DefaultHttpCorsAllowedHeaders/
	// DefaultHttpCorsMaxAge are specs/http-content.md ## CORS
	// ### Configuration's stated defaults. AllowedOrigins has no default
	// constant — its default is the empty string (CORS fully closed).
	DefaultHttpCorsAllowedMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
	DefaultHttpCorsAllowedHeaders = "Content-Type"
	DefaultHttpCorsMaxAge         = 600
	// DefaultHttpCspDefaultSrc is specs/http-content.md ## CSP
	// ### Configuration's stated default : only default-src has a value by
	// default, every other directive is unset.
	DefaultHttpCspDefaultSrc = "'self'"
)

// DefaultOpenidScopes/DefaultOpenidClientIDPath/DefaultOpenidClientSecretPath
// are specs/oauth-saml.md ## Configuration — OIDC's stated defaults.
// ClientID/ClientSecret have NO $GEN$ fallback the way jwt.secret does —
// unlike a JWT secret or the SAML SP certificate, a client id/secret is
// issued by the IdP when the app is registered there, so rel has no
// business fabricating one ; these are read-only $FILE$ paths, plain
// "required value missing" errors if absent everywhere. "<name>" is
// substituted for the configured openid.<name> entry at resolve time.
const (
	DefaultOpenidScopes            = "openid,email,profile"
	DefaultOpenidClientIDPath      = "$FILE$/secrets/openid-<name>.id:./openid-<name>.id"
	DefaultOpenidClientSecretPath  = "$FILE$/secrets/openid-<name>.secret:./openid-<name>.secret"
	DefaultSamlForceSignedRequests = true
)

// DefaultSamlCertificatePath/DefaultSamlPrivateKeyPath are specs/oauth-saml.md
// ## Configuration — SAML's stated defaults : flat filenames directly
// under /secrets/, not a subdirectory — see jwt.secret's own doc comment
// for why rel deliberately never needs to create a directory for these.
const (
	DefaultSamlCertificatePath = "/secrets/saml-cert.pem:./saml-cert.pem"
	DefaultSamlPrivateKeyPath  = "/secrets/saml-cert.key:./saml-cert.key"
)

// DefaultPgHost/DefaultPgPort/DefaultPgQueryAnonymousRole/
// DefaultPgQueryWellKnownPath are query-engine.md/well-known-queries.md's own
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
	// DefaultPgPoolSize is pg.pool_size's default : the max connections in
	// the pool that serves requests — a fixed, framework-agnostic starting
	// point, not pgx's own machine-CPU-scaled default.
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

	// AnonymousRole is pg.query.anonymous_role (default "~anonymous",
	// docs/content/configuration/index.md ### Postgres connection) : the
	// role rel switches to, from Login, for requests with no credentials of
	// their own — used both for /rel and JWT verification failures on
	// /route (authentication.md ## Roles, which used to name this same
	// setting jwt.anonrole ; reconciled onto query.anonymous_role, the name
	// query-engine.md already used, itself later moved under pg.query.* for
	// this same consistency pass).
	AnonymousRole string

	// MaxDepth is pg.query.max_depth : the maximum depth a query can
	// specify. Default 6 — docs/content/configuration/index.md ### Postgres
	// connection.
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
	// credentials narrow. Database is NOT in query-engine.md at all ; this
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

// Blacklist holds the function/relation blacklist from query-engine.md's
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

// IsTruthy interprets a raw config value the way query-engine.md's "`y` or
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
// exact name first, then the schema-wide "*" wildcard query-engine.md's key
// syntax allows.
func (b Blacklist) IsFunctionBlacklisted(schema, name string) bool {
	return IsTruthy(b.Functions[schema][name]) || IsTruthy(b.Functions[schema]["*"])
}

// IsRelationBlacklisted is IsFunctionBlacklisted for relations.
func (b Blacklist) IsRelationBlacklisted(schema, name string) bool {
	return IsTruthy(b.Relations[schema][name]) || IsTruthy(b.Relations[schema]["*"])
}

// DefaultMaxDepth is docs/content/configuration/index.md's pg.query.max_depth
// default.
const DefaultMaxDepth = 6

// DefaultBlacklist is docs/content/configuration/index.md ### Restricting
// what a query can reach's default blacklist : pg_catalog and
// information_schema wholesale for relations (wildcarded deliberately, not
// enumerated — see query-engine.md ## Scoping's "Why" on that), and the
// specific dangerous pg_catalog functions for functions, including the rest
// of the pg_advisory_*lock* family query-engine.md ## Scoping calls out by
// name pattern rather than listing individually (the _unlock variants are
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
