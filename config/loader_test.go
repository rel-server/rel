package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knadh/koanf/providers/confmap"
	koanf "github.com/knadh/koanf/v2"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", p, err)
	}
	return p
}

func TestResolveConfigFilePath_ExplicitFlagWins(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "custom.toml", "")
	got, err := resolveConfigFilePath([]string{"--config=" + p}, "/nonexistent/env-config.toml", []string{dir})
	if err != nil {
		t.Fatalf("resolveConfigFilePath: %v", err)
	}
	if got != p {
		t.Errorf("expected explicit --config to win, got %q", got)
	}
}

func TestResolveConfigFilePath_EnvVarUsedWhenNoFlag(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "env.toml", "")
	got, err := resolveConfigFilePath(nil, p, []string{"/wont/be/used"})
	if err != nil {
		t.Fatalf("resolveConfigFilePath: %v", err)
	}
	if got != p {
		t.Errorf("expected REL_CONFIG path, got %q", got)
	}
}

func TestResolveConfigFilePath_DiscoveryTakesFirstMatchingRoot(t *testing.T) {
	root1 := t.TempDir()
	root2 := t.TempDir()
	p2 := writeFile(t, root2, "rel.toml", "")
	got, err := resolveConfigFilePath(nil, "", []string{root1, root2})
	if err != nil {
		t.Fatalf("resolveConfigFilePath: %v", err)
	}
	if got != p2 {
		t.Errorf("expected root2's file since root1 has none, got %q want %q", got, p2)
	}
}

func TestResolveConfigFilePath_AmbiguousFileAtSameStepIsFatal(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "rel.toml", "")
	writeFile(t, dir, "rel.yaml", "")
	_, err := resolveConfigFilePath(nil, "", []string{dir})
	if err == nil {
		t.Fatalf("expected an ambiguity error, got nil")
	}
}

func TestResolveConfigFilePath_NoMatchIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	got, err := resolveConfigFilePath(nil, "", []string{dir})
	if err != nil {
		t.Fatalf("expected no error when nothing found, got %v", err)
	}
	if got != "" {
		t.Errorf("expected empty path, got %q", got)
	}
}

func TestLoad_PrecedenceFileEnvFlag(t *testing.T) {
	// Isolate cwd : jwt.secret's unset $GEN$ default would otherwise write
	// a "jwt-secret" file wherever `go test` runs from.
	t.Chdir(t.TempDir())
	dir := t.TempDir()
	// docs/content/configuration/index.md ### Postgres connection's real keys : pg.host/pg.port.
	p := writeFile(t, dir, "rel.toml", `
[pg]
host = "file-host"
port = 1111
`)

	// File alone.
	cfg, err := Load([]string{"--config=" + p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Pg.Host != "file-host" || cfg.Pg.Port != 1111 {
		t.Fatalf("expected file values, got %+v", cfg.Pg)
	}

	// Env overrides file.
	t.Setenv("REL_PG__HOST", "env-host")
	cfg, err = Load([]string{"--config=" + p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Pg.Host != "env-host" {
		t.Errorf("expected env to override file host, got %q", cfg.Pg.Host)
	}
	if cfg.Pg.Port != 1111 {
		t.Errorf("expected file port to survive (env didn't set it), got %d", cfg.Pg.Port)
	}

	// Flag overrides both.
	cfg, err = Load([]string{"--config=" + p, "--pg.host=flag-host"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Pg.Host != "flag-host" {
		t.Errorf("expected flag to override env+file host, got %q", cfg.Pg.Host)
	}
}

// Covers pg.query.user's cross-default onto pg.user — the default's VALUE
// is another config key, not a constant, unlike ordinary *OrDefault keys.
func TestLoad_QueryUserDefaultsToPgUser(t *testing.T) {
	t.Chdir(t.TempDir()) // see TestLoad_PrecedenceFileEnvFlag's own note on why
	p := writeFile(t, t.TempDir(), "rel.toml", `
[pg]
user = "pg_user"
password = "pg_pass"
`)
	cfg, err := Load([]string{"--config=" + p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Pg.User != "pg_user" || cfg.Pg.Password != "pg_pass" {
		t.Fatalf("expected pg.user/password read directly, got %+v", cfg.Pg)
	}
	if cfg.Pg.Query.User != "pg_user" || cfg.Pg.Query.Password != "pg_pass" {
		t.Errorf("expected pg.query.user/password to default to pg.user/password, got %+v", cfg.Pg.Query.Login)
	}

	// pg.query.user, when explicitly set, must NOT be overridden by
	// pg.user.
	p2 := writeFile(t, t.TempDir(), "rel.toml", `
[pg]
user = "pg_user"

[pg.query]
user = "query_user"
`)
	cfg2, err := Load([]string{"--config=" + p2})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg2.Pg.Query.User != "query_user" {
		t.Errorf("expected explicit pg.query.user to win over pg.user's default, got %q", cfg2.Pg.Query.User)
	}
}

// pg.uri, when set, takes precedence and populates pg.host/pg.port/
// pg.database itself (specs/pg-uri-precedence.md) ; pg.query.user (an
// independent optional override) must still apply on top.
func TestLoad_PgURI_TakesPrecedence(t *testing.T) {
	t.Chdir(t.TempDir()) // see TestLoad_PrecedenceFileEnvFlag's own note on why
	p := writeFile(t, t.TempDir(), "rel.toml", `
[pg]
uri = "postgres://u:p@db.internal:5433/mydb"
user = "should-be-ignored"

[pg.query]
user = "query_user"
`)
	cfg, err := Load([]string{"--config=" + p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Pg.URI != "postgres://u:p@db.internal:5433/mydb" {
		t.Errorf("expected pg.uri read directly, got %q", cfg.Pg.URI)
	}
	if cfg.Pg.Host != "db.internal" || cfg.Pg.Port != 5433 || cfg.Pg.Database != "mydb" {
		t.Errorf("expected pg.host/pg.port/pg.database derived from pg.uri, got %+v", cfg.Pg)
	}
	if cfg.Pg.Query.User != "query_user" {
		t.Errorf("expected pg.query.user to still apply on top of pg.uri, got %q", cfg.Pg.Query.User)
	}
}

// specs/pg-uri-precedence.md : pg.host/pg.port/pg.database set alongside
// pg.uri is a configuration error, not a silently-ignored value.
func TestLoad_PgURI_WithGranularFieldsIsFatal(t *testing.T) {
	t.Chdir(t.TempDir())
	p := writeFile(t, t.TempDir(), "rel.toml", `
[pg]
uri = "postgres://u:p@db.internal:5432/mydb"
host = "conflicting-host"
`)
	if _, err := Load([]string{"--config=" + p}); err == nil {
		t.Fatal("expected an error when pg.host is set alongside pg.uri")
	}
}

func TestLoad_DefaultsApplyWhenNothingSet(t *testing.T) {
	// jwt.secret is fixed explicitly here to avoid the real $GEN$ default
	// writing a stray "jwt-secret" file ; TestLoad_JwtSecretDefault_GenAndResolve below exercises that behavior, isolated via t.Chdir.
	dir := t.TempDir()
	p := writeFile(t, dir, "rel.toml", "[jwt]\nsecret = \"fixed-test-secret\"\n")
	cfg, err := Load([]string{"--config=" + p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Logging.Handler != "pretty" {
		t.Errorf("expected default handler pretty, got %q", cfg.Logging.Handler)
	}
	if cfg.Logging.Level != "info" {
		t.Errorf("expected default level info, got %q", cfg.Logging.Level)
	}
	if cfg.Http.Port != DefaultHttpPort {
		t.Errorf("expected default http port %d, got %d", DefaultHttpPort, cfg.Http.Port)
	}
	if cfg.Pg.Query.MaxDepth != DefaultMaxDepth {
		t.Errorf("expected default max depth %d, got %d", DefaultMaxDepth, cfg.Pg.Query.MaxDepth)
	}
	if cfg.Pg.Query.WellKnownDirs != "/wellknown" {
		t.Errorf("expected default pg.query.wellknown_path=/wellknown, got %q", cfg.Pg.Query.WellKnownDirs)
	}
	if cfg.Pg.Query.AnonymousRole != "~anonymous" {
		t.Errorf("expected default pg.query.anonymous_role=~anonymous, got %q", cfg.Pg.Query.AnonymousRole)
	}
	if cfg.Pg.PoolSize != DefaultPgPoolSize {
		t.Errorf("expected default pg.pool_size=%d, got %d", DefaultPgPoolSize, cfg.Pg.PoolSize)
	}
	if cfg.Http.CookiesMaxAge != DefaultHttpCookiesMaxAge {
		t.Errorf("expected default http.cookies_max_age=%d, got %d", DefaultHttpCookiesMaxAge, cfg.Http.CookiesMaxAge)
	}
	if cfg.Http.Static.Path != DefaultHttpStaticPath {
		t.Errorf("expected default http.static.path=%q, got %q", DefaultHttpStaticPath, cfg.Http.Static.Path)
	}
	if cfg.Http.MaxBodySize != DefaultHttpMaxBodySize {
		t.Errorf("expected default http.max_body_size=%d, got %d", DefaultHttpMaxBodySize, cfg.Http.MaxBodySize)
	}
	if cfg.Http.MaxPartCount != DefaultHttpMaxPartCount {
		t.Errorf("expected default http.max_part_count=%d, got %d", DefaultHttpMaxPartCount, cfg.Http.MaxPartCount)
	}
	if cfg.Http.Templates.Path != DefaultHttpTemplatesPath {
		t.Errorf("expected default http.templates.path=%q, got %q", DefaultHttpTemplatesPath, cfg.Http.Templates.Path)
	}
	if cfg.Http.Cors.AllowedOrigins != "" {
		t.Errorf("expected default http.cors.allowed_origins empty (CORS closed), got %q", cfg.Http.Cors.AllowedOrigins)
	}
	if cfg.Http.Cors.AllowedMethods != DefaultHttpCorsAllowedMethods {
		t.Errorf("expected default http.cors.allowed_methods=%q, got %q", DefaultHttpCorsAllowedMethods, cfg.Http.Cors.AllowedMethods)
	}
	if cfg.Http.Cors.AllowedHeaders != DefaultHttpCorsAllowedHeaders {
		t.Errorf("expected default http.cors.allowed_headers=%q, got %q", DefaultHttpCorsAllowedHeaders, cfg.Http.Cors.AllowedHeaders)
	}
	if cfg.Http.Cors.MaxAge != DefaultHttpCorsMaxAge {
		t.Errorf("expected default http.cors.max_age=%d, got %d", DefaultHttpCorsMaxAge, cfg.Http.Cors.MaxAge)
	}
	if cfg.Http.Csp.DefaultSrc != DefaultHttpCspDefaultSrc {
		t.Errorf("expected default http.csp.default_src=%q, got %q", DefaultHttpCspDefaultSrc, cfg.Http.Csp.DefaultSrc)
	}
	if cfg.Http.Csp.ScriptSrc != "" || cfg.Http.Csp.Policy != "" {
		t.Errorf("expected every other http.csp.* directive unset by default, got %+v", cfg.Http.Csp)
	}
	if cfg.Jwt.Secret != "fixed-test-secret" {
		t.Errorf("expected the explicitly-set jwt.secret, got %q", cfg.Jwt.Secret)
	}
	if cfg.Jwt.CookieName != "accesstoken" || cfg.Jwt.Algorithm != "HS256" || cfg.Jwt.SameSite != "Lax" {
		t.Errorf("expected default jwt cookie/algorithm/samesite, got %+v", cfg.Jwt)
	}
	if cfg.Jwt.MaxAge != 1800 || cfg.Jwt.RenewAfter != 0.5 || cfg.Jwt.MaxSessionAge != 604800 {
		t.Errorf("expected default jwt maxage/renewafter/maxsessionage, got %+v", cfg.Jwt)
	}
	if cfg.Reload.Cmd != "" {
		t.Errorf("expected default reload.cmd empty, got %q", cfg.Reload.Cmd)
	}
	if cfg.Reload.Timeout != DefaultReloadTimeout {
		t.Errorf("expected default reload.timeout=%d, got %d", DefaultReloadTimeout, cfg.Reload.Timeout)
	}
	if cfg.Reload.DrainTimeout != DefaultReloadDrainTimeout {
		t.Errorf("expected default reload.drain_timeout=%d, got %d", DefaultReloadDrainTimeout, cfg.Reload.DrainTimeout)
	}
	// Default blacklist must still be present when config doesn't touch it.
	if !cfg.Blacklist.IsRelationBlacklisted("pg_catalog", "anything") {
		t.Errorf("expected DefaultBlacklist to still apply")
	}
}

// Covers http-content.md's CORS/CSP scalars.
func TestLoad_HttpContentKeys(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "rel.toml", `
[jwt]
secret = "fixed-test-secret"

[http.cors]
allowed_origins = "https://example.com,https://other.example.com"
allowed_methods = "GET, POST"
allowed_headers = "Content-Type, X-Custom"
max_age = 120

[http.csp]
default_src = "'self'"
script_src = "'self' https://cdn.example.com"
policy = ""

[http.templates]
path = "/my/templates"
`)
	cfg, err := Load([]string{"--config=" + p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Http.Cors.AllowedOrigins != "https://example.com,https://other.example.com" {
		t.Errorf("unexpected http.cors.allowed_origins: %q", cfg.Http.Cors.AllowedOrigins)
	}
	if cfg.Http.Cors.AllowedMethods != "GET, POST" {
		t.Errorf("unexpected http.cors.allowed_methods: %q", cfg.Http.Cors.AllowedMethods)
	}
	if cfg.Http.Cors.AllowedHeaders != "Content-Type, X-Custom" {
		t.Errorf("unexpected http.cors.allowed_headers: %q", cfg.Http.Cors.AllowedHeaders)
	}
	if cfg.Http.Cors.MaxAge != 120 {
		t.Errorf("unexpected http.cors.max_age: %d", cfg.Http.Cors.MaxAge)
	}
	if cfg.Http.Csp.ScriptSrc != "'self' https://cdn.example.com" {
		t.Errorf("unexpected http.csp.script_src: %q", cfg.Http.Csp.ScriptSrc)
	}
	if cfg.Http.Templates.Path != "/my/templates" {
		t.Errorf("unexpected http.templates.path: %q", cfg.Http.Templates.Path)
	}
}

// TestLoad_RouteDeclarations proves route.<schema>.<function>.* — the
// entire routing config surface added in Stage 5 — actually parses through
// the real Load() -> assemble() -> readRoutes() pipeline, not just via a
// hand-built RouteDecl injected directly into cfg.Route by route package
// tests (route_test.go's TestMain does that, but never exercises the TOML
// loader path for this namespace).
func TestLoad_RouteDeclarations(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "rel.toml", `
[jwt]
secret = "fixed-test-secret"

[route.public.fn_export]
path = "/export/{id}"
method = "GET"
template = "export.jet"
stream_upload = true
middleware = true
`)
	cfg, err := Load([]string{"--config=" + p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	decl, ok := cfg.Route["public"]["fn_export"]
	if !ok {
		t.Fatalf("expected route.public.fn_export to be declared, got %#v", cfg.Route)
	}
	if decl.Path != "/export/{id}" {
		t.Errorf("unexpected path: %q", decl.Path)
	}
	if decl.Method != "GET" {
		t.Errorf("unexpected method: %q", decl.Method)
	}
	if decl.Template != "export.jet" {
		t.Errorf("unexpected template: %q", decl.Template)
	}
	if !decl.StreamUpload {
		t.Error("expected stream_upload to be true")
	}
	if !decl.Middleware {
		t.Error("expected middleware to be true")
	}
}

// TestLoad_CspPolicyOverride confirms http.csp.policy loads as a plain raw
// string, independent of the individual directive keys.
func TestLoad_CspPolicyOverride(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "rel.toml", `
[jwt]
secret = "fixed-test-secret"

[http.csp]
policy = "default-src 'self'; script-src 'self' 'unsafe-inline'"
`)
	cfg, err := Load([]string{"--config=" + p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Http.Csp.Policy != "default-src 'self'; script-src 'self' 'unsafe-inline'" {
		t.Errorf("unexpected http.csp.policy: %q", cfg.Http.Csp.Policy)
	}
}

// Covers a caught bug : a Go-level fallback default applied after
// resolveFileIndirection was never itself resolved. t.Chdir isolates the
// $GEN$ file write to a temp dir. The default's first candidate,
// /secrets/jwt-secret, is assumed absent on the machine running this
// test — same assumption TestResolveFileValue_PlainMissingIsFatal already
// makes about /nonexistent/secret.txt — so resolution falls through to the
// second candidate, ./jwt-secret, relative to the isolated cwd.
func TestLoad_JwtSecretDefault_GenAndResolve(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg, err := Load([]string{"--config=" + writeFile(t, t.TempDir(), "rel.toml", "")})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Jwt.Secret == DefaultJwtSecret || strings.Contains(cfg.Jwt.Secret, "$FILE$") {
		t.Fatalf("expected jwt.secret's $FILE$/$GEN$ default to actually resolve, got the literal %q", cfg.Jwt.Secret)
	}
	if len(cfg.Jwt.Secret) != 32 {
		t.Errorf("expected a 32-character generated secret, got %d chars (%q)", len(cfg.Jwt.Secret), cfg.Jwt.Secret)
	}
	if _, err := os.Stat("jwt-secret"); err != nil {
		t.Errorf("expected the generated secret persisted to ./jwt-secret (the default's second candidate), got: %v", err)
	}
}

func TestLoad_YamlAndHumlParse(t *testing.T) {
	t.Chdir(t.TempDir()) // see TestLoad_PrecedenceFileEnvFlag's own note on why
	dir := t.TempDir()
	yamlPath := writeFile(t, dir, "rel.yaml", "pg:\n  host: yaml-host\n")
	cfg, err := Load([]string{"--config=" + yamlPath})
	if err != nil {
		t.Fatalf("Load (yaml): %v", err)
	}
	if cfg.Pg.Host != "yaml-host" {
		t.Errorf("expected yaml-parsed host, got %q", cfg.Pg.Host)
	}

	humlPath := writeFile(t, dir, "rel.huml", "pg::\n  host: \"huml-host\"\n")
	cfg, err = Load([]string{"--config=" + humlPath})
	if err != nil {
		t.Fatalf("Load (huml): %v", err)
	}
	if cfg.Pg.Host != "huml-host" {
		t.Errorf("expected huml-parsed host, got %q", cfg.Pg.Host)
	}
}

func TestLoad_UnreadableExplicitConfigIsFatal(t *testing.T) {
	_, err := Load([]string{"--config=/nonexistent/path/rel.toml"})
	if err == nil {
		t.Fatalf("expected an error for an unreadable explicit --config path")
	}
}

// A present but wrong-type value must fail Load, not silently fall back
// to the default (## Accessing configuration's "never silently coerced").
func TestLoad_MalformedValueIsFatal_TOML(t *testing.T) {
	t.Chdir(t.TempDir()) // see TestLoad_PrecedenceFileEnvFlag's own note on why
	p := writeFile(t, t.TempDir(), "rel.toml", `
[http]
port = "abc"
`)
	_, err := Load([]string{"--config=" + p})
	if err == nil {
		t.Fatalf("expected a malformed http.port to fail Load, not silently fall back to the default")
	}
}

func TestLoad_MalformedValueIsFatal_Env(t *testing.T) {
	t.Chdir(t.TempDir()) // see TestLoad_PrecedenceFileEnvFlag's own note on why
	t.Setenv("REL_HTTP__PORT", "abc")
	_, err := Load(nil)
	if err == nil {
		t.Fatalf("expected a malformed REL_HTTP__PORT to fail Load")
	}
}

// Exercises resolveFileIndirection's k.All()/k.Set() write-back through
// the real Load() pipeline, not just resolveFileValue's parsing in isolation.
func TestLoad_FileIndirectionEndToEnd(t *testing.T) {
	t.Chdir(t.TempDir()) // see TestLoad_PrecedenceFileEnvFlag's own note on why
	dir := t.TempDir()
	secretPath := writeFile(t, dir, "dbname.txt", "indirected_db\n")
	configPath := writeFile(t, dir, "rel.toml", `
[pg]
database = "$FILE$`+secretPath+`"
`)
	cfg, err := Load([]string{"--config=" + configPath})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Pg.Database != "indirected_db" {
		t.Errorf("expected pg.database resolved through $FILE$ end-to-end, got %q", cfg.Pg.Database)
	}
}

func TestResolveFileValue_Plain(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "secret.txt", "hunter2\n")
	got, err := resolveFileValue("$FILE$" + p)
	if err != nil {
		t.Fatalf("resolveFileValue: %v", err)
	}
	if got != "hunter2" {
		t.Errorf("expected trailing newline trimmed, got %q", got)
	}
}

func TestResolveFileValue_PlainMissingIsFatal(t *testing.T) {
	_, err := resolveFileValue("$FILE$/nonexistent/secret.txt")
	if err == nil {
		t.Fatalf("expected an error for a missing file with no $DEFAULT$")
	}
}

func TestResolveFileValue_DefaultFallbackWhenMissing(t *testing.T) {
	got, err := resolveFileValue("$FILE$/nonexistent/secret.txt$DEFAULT$fallback value")
	if err != nil {
		t.Fatalf("resolveFileValue: %v", err)
	}
	if got != "fallback value" {
		t.Errorf("expected fallback value, got %q", got)
	}
}

func TestResolveFileValue_DefaultUsesFileWhenPresent(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "secret.txt", "real value")
	got, err := resolveFileValue("$FILE$" + p + "$DEFAULT$fallback value")
	if err != nil {
		t.Fatalf("resolveFileValue: %v", err)
	}
	if got != "real value" {
		t.Errorf("expected file's own content when present, got %q", got)
	}
}

func TestResolveFileValue_GenGeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "generated.txt")
	got1, err := resolveFileValue("$FILE$" + p + "$GEN$16")
	if err != nil {
		t.Fatalf("resolveFileValue: %v", err)
	}
	if len(got1) != 16 {
		t.Fatalf("expected 16 generated characters, got %d (%q)", len(got1), got1)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("expected the generated value to be written to disk: %v", err)
	}
	// Second call must read back the SAME persisted value, not regenerate.
	got2, err := resolveFileValue("$FILE$" + p + "$GEN$16")
	if err != nil {
		t.Fatalf("resolveFileValue (2nd): %v", err)
	}
	if got1 != got2 {
		t.Errorf("expected the generated value to persist across calls, got %q then %q", got1, got2)
	}
}

// A caught bug : searching from the FIRST "$GEN$"/"$DEFAULT$" occurrence
// misparsed a path that itself contains the marker as a substring.
func TestResolveFileValue_PathContainingMarkerSubstring(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets_$GEN$_v2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := writeFile(t, dir, "token.txt", "real value")
	got, err := resolveFileValue("$FILE$" + p)
	if err != nil {
		t.Fatalf("resolveFileValue: %v", err)
	}
	if got != "real value" {
		t.Errorf("expected the file's real content despite $GEN$ appearing in its path, got %q", got)
	}
}

// A caught bug : fmt.Sscanf("%d", ...) silently accepted "16xyz" as 16.
func TestResolveFileValue_GenLengthRejectsTrailingGarbage(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "generated.txt")
	if _, err := resolveFileValue("$FILE$" + p + "$GEN$16xyz"); err == nil {
		t.Fatalf("expected an error for a $GEN$ length with trailing garbage, got nil")
	}
}

// A caught bug : two keys sharing a $GEN$ path with different lengths
// used to silently return whichever was generated first.
func TestResolveFileValue_GenLengthMismatchIsFatal(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "generated.txt")
	if _, err := resolveFileValue("$FILE$" + p + "$GEN$32"); err != nil {
		t.Fatalf("first resolveFileValue: %v", err)
	}
	if _, err := resolveFileValue("$FILE$" + p + "$GEN$64"); err == nil {
		t.Fatalf("expected an error when a second key requests a different length for the same $GEN$ path")
	}
}

func TestSplitPathList(t *testing.T) {
	got := SplitPathList(" /a : /b ::/c/d ")
	want := []string{"/a", "/b", "/c/d"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
			break
		}
	}
}

// resolveGenValue ## pass 1 : the first candidate that already holds a
// value wins, even though a later candidate would otherwise be the one
// resolveFileValue lands on first if it were reading rather than generating.
func TestResolveFileValue_GenMultiPath_FirstExistingWins(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a", "secret.txt")
	second := filepath.Join(dir, "b", "secret.txt")
	if err := os.MkdirAll(filepath.Dir(first), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(second), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(first, []byte("first-value"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if err := os.WriteFile(second, []byte("second-value"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	got, err := resolveFileValue("$FILE$" + first + ":" + second + "$GEN$11")
	if err != nil {
		t.Fatalf("resolveFileValue: %v", err)
	}
	if got != "first-value" {
		t.Errorf("expected the first candidate's existing value to win, got %q", got)
	}
}

// resolveGenValue ## pass 2 : a candidate whose parent directory doesn't
// exist is skipped (not fatal), falling through to the next candidate.
func TestResolveFileValue_GenMultiPath_SkipsMissingDir(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist", "secret.txt")
	present := filepath.Join(dir, "secret.txt")
	got, err := resolveFileValue("$FILE$" + missing + ":" + present + "$GEN$16")
	if err != nil {
		t.Fatalf("resolveFileValue: %v", err)
	}
	if len(got) != 16 {
		t.Fatalf("expected a 16-character generated value, got %d (%q)", len(got), got)
	}
	if _, err := os.Stat(present); err != nil {
		t.Errorf("expected the value written to the second candidate (the first's dir is missing): %v", err)
	}
	if _, err := os.Stat(missing); err == nil {
		t.Errorf("did not expect anything written at the candidate whose directory doesn't exist")
	}
}

// resolveGenValue ## pass 2 : a candidate whose parent directory EXISTS but
// refuses the write is immediately fatal — never skipped to the next
// candidate, and never degraded to an ephemeral value.
func TestResolveFileValue_GenMultiPath_ExistingDirWriteFailureIsFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, this check needs a real non-root process")
	}
	dir := t.TempDir()
	readonlyDir := filepath.Join(dir, "readonly")
	if err := os.MkdirAll(readonlyDir, 0o555); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { os.Chmod(readonlyDir, 0o755) }) // let TempDir's own cleanup remove it
	blocked := filepath.Join(readonlyDir, "secret.txt")
	fallback := filepath.Join(dir, "secret.txt")
	_, err := resolveFileValue("$FILE$" + blocked + ":" + fallback + "$GEN$16")
	if err == nil {
		t.Fatalf("expected a fatal error, not a fallback to the next candidate or an ephemeral value")
	}
	if _, statErr := os.Stat(fallback); statErr == nil {
		t.Errorf("did not expect the next candidate to be tried once an existing directory's write failed")
	}
}

// resolveGenValue ## pass 3 : every candidate's parent directory is
// missing — falls back to an ephemeral, unpersisted value rather than
// failing to boot.
func TestResolveFileValue_GenMultiPath_EphemeralWhenNoCandidateDirExists(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "no-such-dir-a", "secret.txt")
	b := filepath.Join(dir, "no-such-dir-b", "secret.txt")
	got, err := resolveFileValue("$FILE$" + a + ":" + b + "$GEN$16")
	if err != nil {
		t.Fatalf("expected the ephemeral fallback, not an error: %v", err)
	}
	if len(got) != 16 {
		t.Fatalf("expected a 16-character generated value, got %d (%q)", len(got), got)
	}
	// A second call must generate a DIFFERENT value — nothing was persisted.
	got2, err := resolveFileValue("$FILE$" + a + ":" + b + "$GEN$16")
	if err != nil {
		t.Fatalf("resolveFileValue (2nd): %v", err)
	}
	if got == got2 {
		t.Errorf("expected two independent ephemeral values across calls, got the same %q twice", got)
	}
}

func TestResolveFileValue_DefaultMultiPath_FirstExistingWins(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist.txt")
	present := writeFile(t, dir, "present.txt", "real value")
	got, err := resolveFileValue("$FILE$" + missing + ":" + present + "$DEFAULT$fallback")
	if err != nil {
		t.Fatalf("resolveFileValue: %v", err)
	}
	if got != "real value" {
		t.Errorf("expected the second (existing) candidate's content, got %q", got)
	}
}

func TestResolveFileValue_PlainMultiPath_FirstExistingWins(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist.txt")
	present := writeFile(t, dir, "present.txt", "real value")
	got, err := resolveFileValue("$FILE$" + missing + ":" + present)
	if err != nil {
		t.Fatalf("resolveFileValue: %v", err)
	}
	if got != "real value" {
		t.Errorf("expected the second (existing) candidate's content, got %q", got)
	}
}

func TestRejectArrays(t *testing.T) {
	k := koanf.New(".")
	if err := k.Load(confmap.Provider(map[string]any{
		"cors.origins": []string{"a", "b"},
	}, "."), nil); err != nil {
		t.Fatalf("loading confmap: %v", err)
	}
	if err := rejectArrays(k); err == nil {
		t.Fatalf("expected an error for an array value")
	}
}

func TestRejectArrays_ScalarsPass(t *testing.T) {
	k := koanf.New(".")
	if err := k.Load(confmap.Provider(map[string]any{
		"pg.host": "localhost",
		"pg.port": 5432,
	}, "."), nil); err != nil {
		t.Fatalf("loading confmap: %v", err)
	}
	if err := rejectArrays(k); err != nil {
		t.Errorf("expected no error for scalar values, got %v", err)
	}
}

func TestConfigReader_GetString(t *testing.T) {
	k := koanf.New(".")
	_ = k.Load(confmap.Provider(map[string]any{"pg.host": "localhost", "pg.port": 5432}, "."), nil)
	r := newReader(k, "", nil)

	if s, err := r.GetString("pg.host"); err != nil || s != "localhost" {
		t.Errorf("GetString(pg.host) = %q, %v", s, err)
	}
	if _, err := r.GetString("pg.port"); err == nil {
		t.Errorf("expected GetString on a numeric value to error")
	}
	if _, err := r.GetString("pg.missing"); err == nil {
		t.Errorf("expected GetString on a missing key to error")
	}
	if got := r.GetStringOrDefault("pg.missing", "fallback"); got != "fallback" {
		t.Errorf("GetStringOrDefault = %q, want fallback", got)
	}
}

func TestConfigReader_GetInt_NativeAndStringCoercion(t *testing.T) {
	k := koanf.New(".")
	_ = k.Load(confmap.Provider(map[string]any{
		"native": 42,
		"asStr":  "17",
		"bad":    "not-a-number",
		"bool":   true,
	}, "."), nil)
	r := newReader(k, "", nil)

	if n, err := r.GetInt("native"); err != nil || n != 42 {
		t.Errorf("GetInt(native) = %d, %v", n, err)
	}
	if n, err := r.GetInt("asStr"); err != nil || n != 17 {
		t.Errorf("GetInt(asStr) = %d, %v", n, err)
	}
	if _, err := r.GetInt("bad"); err == nil {
		t.Errorf("expected GetInt on a non-numeric string to error")
	}
	if _, err := r.GetInt("bool"); err == nil {
		t.Errorf("expected GetInt on a bool to error (structurally wrong type)")
	}
}

func TestConfigReader_GetObject_NotAnObjectErrors(t *testing.T) {
	k := koanf.New(".")
	_ = k.Load(confmap.Provider(map[string]any{"pg.host": "localhost"}, "."), nil)
	r := newReader(k, "", nil)
	if _, err := r.GetObject("pg.host"); err == nil {
		t.Errorf("expected GetObject on a scalar to error")
	}
	if _, err := r.GetObject("pg"); err != nil {
		t.Errorf("expected GetObject on an actual object to succeed, got %v", err)
	}
}

func TestConfigReader_GetIterator_ScalarAndObjectChildren(t *testing.T) {
	k := koanf.New(".")
	_ = k.Load(confmap.Provider(map[string]any{
		"logging.filter.a": "regexA",
		"logging.filter.b": "regexB",
	}, "."), nil)
	r := newReader(k, "", nil)

	it, err := r.GetIterator("logging.filter")
	if err != nil {
		t.Fatalf("GetIterator: %v", err)
	}
	got := map[string]string{}
	var order []string
	for key, child := range it {
		order = append(order, key)
		s, err := child.GetString("")
		if err != nil {
			t.Fatalf("child.GetString(\"\") for %q: %v", key, err)
		}
		got[key] = s
	}
	if got["a"] != "regexA" || got["b"] != "regexB" {
		t.Fatalf("expected scalar children readable via GetString(\"\"), got %v", got)
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Errorf("expected alphabetical order [a b], got %v", order)
	}
}
