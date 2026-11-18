package config

// Config is rel's runtime configuration, built by Load.
type Config struct {
	Pg      Pg
	Logging Logging
	Http    Http
	Jwt     Jwt
	Reload  Reload
	Dmut    Dmut

	// Openid/Saml back the /auth/oidc/{name}/* and /auth/saml/{name}/* endpoints.
	Openid map[string]OpenidProvider
	Saml   Saml

	Blacklist Blacklist

	// Route is route.<schema>.<function>.*, keyed by schema then function name.
	Route map[string]map[string]RouteDecl

	// TypeScript is what the generated client exports ; whether it is served is Http.TypeScript.
	TypeScript TypeScript

	// Dev is `dev`, default false : adds Postgres error detail and stack traces to error responses.
	Dev bool

	// Raw is every dotted key set by file/env/flag, for reload.cmd's `{name}` interpolation. pg.uri and the granular
	// pg.* keys are always present, defaulted or derived ; other defaults are not.
	Raw map[string]any
}

// TypeScript is the typescript.* namespace.
type TypeScript struct {
	// HelperPath is typescript.helper_path, default "" (disabled) : a file rel rewrites database.ts to at startup and
	// on every reload.
	HelperPath string
	// Schemas is typescript.schemas, default "" (every schema but pg_catalog) : a comma-separated whitelist,
	// intersected with the endpoint's `schemas` param when given.
	Schemas string
	// Codec is typescript.codec, default "" (plain JSON) : comma-separated codec options (`json`|`cbor`,
	// `date`|`temporal`, `bigint`) for the endpoint, helper_path and --typescript-out alike ; the endpoint's `codec`
	// param replaces it for one request. Validated by tsgen.ParseCodec.
	Codec string
}

// Reload is reload.* : the external command run before every reload, startup included.
type Reload struct {
	// Cmd is reload.cmd, default "" (skipped) : interpolated, then split with shell-style quoting, never run by a shell.
	Cmd string
	// Timeout is reload.timeout, default 120 seconds, past which Cmd fails.
	Timeout int
	// DrainTimeout is reload.drain_timeout, default 30 seconds : how long a reload waits for in-flight requests before
	// cancelling them.
	DrainTimeout int
}

const (
	DefaultReloadTimeout      = 120
	DefaultReloadDrainTimeout = 30
)

// Dmut is dmut.* : the in-process dmut migration step, run just before reload.cmd.
type Dmut struct {
	// Path is dmut.path, default "" (skipped) : colon-separated directories or files to read mutations from.
	Path string
	// Timeout is dmut.timeout, default 120 seconds, past which the run fails.
	Timeout int
	// User/Password are dmut.user/dmut.password, default "" : each falls back on its own to Pg.User/Pg.Password.
	User     string
	Password string
	// Mode is dmut.mode : DmutModeDefault, DmutModeSimple or DmutModeFull.
	Mode string
}

// dmut.mode values, and dmut.timeout's default.
const (
	DmutModeDefault = "default"
	DmutModeSimple  = "simple"
	DmutModeFull    = "full"

	DefaultDmutTimeout = 120
)

// Jwt is jwt.* : session signing and lifetime.
type Jwt struct {
	// Secret is jwt.secret, default DefaultJwtSecret : /secrets/jwt-secret, flat so only /secrets/ must exist since rel
	// never creates directories, else ./jwt-secret for a dev run.
	Secret string
	// CookieName is jwt.cookie_name, default "accesstoken" : the cookie carrying the session.
	CookieName string
	// Algorithm is jwt.algorithm, default "HS256" (HS256/HS384/HS512) : enforced exactly on verification, so "none"
	// is rejected.
	Algorithm string
	// SameSite is jwt.same_site, default "Lax".
	SameSite string
	// MaxAge is jwt.max_age, default 1800 seconds : a new token's lifetime.
	MaxAge int
	// RenewAfter is jwt.renew_after, default 0.5 : fraction of a token's lifetime after which it is renewed.
	RenewAfter float64
	// MaxSessionAge is jwt.max_session_age, default 604800 seconds (7 days) : hard ceiling on a session, from
	// auth_time.
	MaxSessionAge int
}

const (
	DefaultJwtSecret        = "$FILE$/secrets/jwt-secret:./jwt-secret$GEN$32"
	DefaultJwtCookieName    = "accesstoken"
	DefaultJwtAlgorithm     = "HS256"
	DefaultJwtSameSite      = "Lax"
	DefaultJwtMaxAge        = 1800
	DefaultJwtRenewAfter    = 0.5
	DefaultJwtMaxSessionAge = 604800
)

// Logging is logging.*.
type Logging struct {
	// Handler is logging.handler : "JSON" or "pretty" (default "pretty").
	Handler string
	// Level is logging.level : debug/info/warn/error (default "info").
	Level string
	// Filter is logging.filter.* : per-key regexp a record's attribute must match to be logged.
	Filter map[string]string
	// Exclude is logging.exclude.* : per-key regexp that suppresses a matching record, applied after Filter.
	Exclude map[string]string
}

// Http is the listen address plus the http.* keys.
type Http struct {
	Host string
	Port int

	// PublicHost is http.public_host, default "" : this deployment's bare host (no scheme, port or path), from which
	// "https://<host>/..." is built for the IdP-registered OIDC and SAML URLs, which can't come from the request's Host
	// header. An SSO entry with no effective host is skipped.
	PublicHost string

	// CookiesMaxAge is http.cookies_max_age, default 86400 : max-age of a cookie set through the "cookies" field when
	// it has none. Not the session cookie's.
	CookiesMaxAge int
	// MaxBodySize is http.max_body_size, default 10 MiB : cap on a /rel POST body and on a /route request's whole
	// body, multipart envelope included, enforced before buffering.
	MaxBodySize int
	// MaxPartCount is http.max_part_count, default 100 : max multipart parts per /route request, whatever their size.
	MaxPartCount int
	// ResponseBuffer is http.response_buffer, default 65536 : bytes of a /rel response held back until commit, so a
	// response that small is delivered whole or replaced by an error. 0 streams from the first byte.
	ResponseBuffer int

	Functions  HttpFunctions
	Static     HttpStatic
	Upload     HttpUpload
	Templates  HttpTemplates
	Cors       HttpCors
	Csp        HttpCsp
	TypeScript HttpTypeScript
}

// HttpUpload is http.upload.*.
type HttpUpload struct {
	// Dir is http.upload.dir, default "" (uploads disabled) : subpath of the static write directory (first
	// http.static.path entry) uploads go under. Files there are served as static content but never run as jet.
	Dir string
	// MaxSize is http.upload.max_size, default MaxBodySize : cap on a single-upload /route request's streamed payload,
	// replacing MaxBodySize there. A route's __prepare may lower it per request, never raise it.
	MaxSize int
}

// HttpTypeScript is http.typescript.* : whether the generated client is served ; what it exports is TypeScript.
type HttpTypeScript struct {
	// Enable is http.typescript.enable, default Dev : serves GET /rel/database.ts and /rel/database.json.
	Enable bool
}

// HttpTemplates is http.templates.*.
type HttpTemplates struct {
	// Path is http.templates.path, default "/template" : the directory Jet templates are loaded from.
	Path string
}

// HttpCors is http.cors.*.
type HttpCors struct {
	// AllowedOrigins is http.cors.allowed_origins, default "" (CORS closed) : comma-separated exact origins, or "*".
	AllowedOrigins string
	// AllowedMethods is http.cors.allowed_methods : methods a preflight may approve.
	AllowedMethods string
	// AllowedHeaders is http.cors.allowed_headers : request headers a preflight may approve beyond the simple set.
	AllowedHeaders string
	// MaxAge is http.cors.max_age, default 600 seconds : how long a browser may cache a preflight response.
	MaxAge int
}

// HttpCsp is http.csp.* : one key per CSP directive, plus a raw full-policy override.
type HttpCsp struct {
	DefaultSrc     string
	ScriptSrc      string
	StyleSrc       string
	ImgSrc         string
	FontSrc        string
	ConnectSrc     string
	WorkerSrc      string
	ObjectSrc      string
	FrameAncestors string
	BaseUri        string
	FormAction     string
	// Policy is http.csp.policy, default "" : a raw Content-Security-Policy value that, when set, replaces every
	// directive above.
	Policy string
}

// RouteDecl is one route.<schema>.<function>.* route ; schema and function come from the key path.
type RouteDecl struct {
	// Path is the route's path in chi syntax ; without it the function is not routable.
	Path string
	// Method is a comma-separated list of accepted HTTP methods, default "" (inferred).
	Method string
	// Template is a Jet template path, used when the response doesn't set its own.
	Template     string
	StreamUpload bool
	Middleware   bool
}

// HttpFunctions is http.functions.*.
type HttpFunctions struct {
	// AllowedAuth is http.functions.allowed_auth, default "" (unrestricted) : regexp on the qualified, unquoted function
	// name restricting whose "jwt" response field rel honors.
	AllowedAuth string
	// SsoCallback is http.functions.sso_callback, default "" (disabled) : qualified, unquoted name of the function an
	// SSO entry's callback_function falls back to.
	SsoCallback string
}

// OpenidProvider is one openid.<name>.* entry.
type OpenidProvider struct {
	// Issuer is openid.<name>.issuer (required), the discovery base URL.
	Issuer string
	// ClientID/ClientSecret are openid.<name>.client_id/client_secret, default DefaultOpenidClientIDPath/
	// DefaultOpenidClientSecretPath with <name> substituted.
	ClientID     string
	ClientSecret string
	// Scopes is openid.<name>.scopes, default DefaultOpenidScopes, comma-separated.
	Scopes []string
	// FetchUserinfo is openid.<name>.fetch_userinfo, default false : also fetch userinfo after the token exchange, its
	// claims winning over the ID token's.
	FetchUserinfo bool
	// CallbackFunction is openid.<name>.callback_function ; "" falls back to HttpFunctions.SsoCallback.
	CallbackFunction string
	// PublicHost is openid.<name>.public_host ; "" falls back to Http.PublicHost.
	PublicHost string
}

// Saml is saml.* : the shared SP certificate/key plus the saml.<name>.* entries.
type Saml struct {
	// CertificatePath/PrivateKeyPath are saml.certificate_path/private_key_path : colon-separated search lists, loaded
	// if found, else generated and persisted at the first candidate whose parent directory exists. Used by every entry
	// that doesn't set its own.
	CertificatePath string
	PrivateKeyPath  string
	Providers       map[string]SamlProvider
}

// SamlProvider is one saml.<name>.* entry.
type SamlProvider struct {
	// IdpMetadataUrl is saml.<name>.idp_metadata_url (required) : fetched at startup, retried lazily on failure.
	IdpMetadataUrl string
	// ForceSignedRequests is saml.<name>.force_signed_requests, default true : sign the AuthnRequest with the SP key,
	// which always exists ; only an IdP that rejects signed requests needs it off.
	ForceSignedRequests bool
	// CallbackFunction is saml.<name>.callback_function ; "" falls back to HttpFunctions.SsoCallback.
	CallbackFunction string
	// PublicHost is saml.<name>.public_host ; "" falls back to Http.PublicHost.
	PublicHost string
	// CertificatePath/PrivateKeyPath are saml.<name>.certificate_path/private_key_path ; "" falls back to the shared
	// Saml pair. Same load-or-generate behaviour.
	CertificatePath string
	PrivateKeyPath  string
}

// HttpStatic is http.static.*.
type HttpStatic struct {
	// Path is http.static.path, default "/static" : colon-separated directories served at the router root for any
	// path no declared route claims, first match wins.
	Path string
}

const (
	DefaultLoggingHandler     = "pretty"
	DefaultLoggingLevel       = "info"
	DefaultHttpPort           = 8080
	DefaultHttpCookiesMaxAge  = 86400
	DefaultHttpStaticPath     = "/static"
	DefaultHttpMaxBodySize    = 10485760
	DefaultHttpMaxPartCount   = 100
	DefaultHttpResponseBuffer = 65536
	DefaultHttpTemplatesPath  = "/template"
	// AllowedOrigins has no default constant : it defaults to "" (CORS closed).
	DefaultHttpCorsAllowedMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
	DefaultHttpCorsAllowedHeaders = "Content-Type"
	DefaultHttpCorsMaxAge         = 600
	// Only default-src has a default ; every other directive is unset.
	DefaultHttpCspDefaultSrc = "'self'"
)

// ClientID/ClientSecret are read-only $FILE$ paths with no $GEN$ fallback : the IdP issues them, so rel must not
// make one up. <name> is substituted at resolve time.
const (
	DefaultOpenidScopes            = "openid,email,profile"
	DefaultOpenidClientIDPath      = "$FILE$/secrets/openid-<name>.id:./openid-<name>.id"
	DefaultOpenidClientSecretPath  = "$FILE$/secrets/openid-<name>.secret:./openid-<name>.secret"
	DefaultSamlForceSignedRequests = true
)

// Flat file names under /secrets/, so rel never needs to create a directory.
const (
	DefaultSamlCertificatePath = "/secrets/saml-cert.pem:./saml-cert.pem"
	DefaultSamlPrivateKeyPath  = "/secrets/saml-cert.key:./saml-cert.key"
)

const (
	DefaultPgHost               = "localhost"
	DefaultPgPort               = 5432
	DefaultPgQueryAnonymousRole = "~anonymous"
	DefaultPgQueryWellKnownPath = "/wellknown"
	// DefaultPgPoolSize is a fixed starting point, not pgx's CPU-scaled default.
	DefaultPgPoolSize = 10

	DefaultPgConnectTimeout = 30
)

// PgQuery is pg.query.*. Requests use Pg's login ; `set role` is what restricts them.
type PgQuery struct {
	// AnonymousRole is pg.query.anonymous_role, default "~anonymous" : the role of an anonymous user, on /rel and on
	// /route when session verification fails.
	AnonymousRole string

	// MaxDepth is pg.query.max_depth, default 6 : the maximum depth a query can specify.
	MaxDepth int

	// WellKnownDirs is pg.query.wellknown_path, default "/wellknown" : a colon-separated list of directories.
	WellKnownDirs string

	// AllowCount/AllowStats/AllowQueryPlan/AllowSql/AllowRollback are pg.query.allow_* : server-wide switches for the
	// matching ComplexQuery flags, each defaulting to Dev.
	AllowCount     bool
	AllowStats     bool
	AllowQueryPlan bool
	AllowSql       bool
	AllowRollback  bool
}

// Pg is the Postgres connection, used for introspection, reload.cmd and requests alike.
type Pg struct {
	// URI is pg.uri. When set, the granular fields are derived from it and setting any of them too is an error ;
	// otherwise it is derived from them. Always populated.
	URI string

	// User/Password are pg.user/pg.password ; always populated.
	User     string
	Password string

	// Host/Port/Database are pg.host/pg.port/pg.database ; always populated.
	Host     string
	Port     int
	Database string

	// PoolSize is pg.pool_size : max connections in the request-serving pool, not the introspection one.
	PoolSize int

	// NoticesLevel is pg.notices.level, default "warning" ("notice" if Dev) : the least severe Postgres notice rel logs
	// (off, warning, notice, info or debug). dmut's connection always logs its notices.
	NoticesLevel string

	// ConnectTimeout is pg.connect_timeout, in seconds : how long startup waits for Postgres. 0 means a single attempt.
	ConnectTimeout int

	Query PgQuery
}

// Blacklist is blacklist.functions.<schema>.<name or *> and blacklist.relations.<schema>.<name or *>, keyed schema
// then name. Values are kept raw ; see IsTruthy.
type Blacklist struct {
	Functions map[string]map[string]string
	Relations map[string]map[string]string
}

// IsTruthy accepts the common truthy spellings ; anything else, empty included, is false.
func IsTruthy(v string) bool {
	switch v {
	case "y", "Y", "yes", "true", "1":
		return true
	default:
		return false
	}
}

// IsFunctionBlacklisted reports whether schema.name is blacklisted, by exact name or by the schema's "*".
func (b Blacklist) IsFunctionBlacklisted(schema, name string) bool {
	return IsTruthy(b.Functions[schema][name]) || IsTruthy(b.Functions[schema]["*"])
}

// IsRelationBlacklisted is IsFunctionBlacklisted for relations.
func (b Blacklist) IsRelationBlacklisted(schema, name string) bool {
	return IsTruthy(b.Relations[schema][name]) || IsTruthy(b.Relations[schema]["*"])
}

const DefaultMaxDepth = 6

// DefaultBlacklist covers pg_catalog and information_schema wholesale for relations, and the dangerous pg_catalog
// functions, including every advisory lock but not the harmless _unlock ones.
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
