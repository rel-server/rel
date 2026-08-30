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
	// jwt.secret isn't set anywhere in this test ; its $GEN$ default would
	// otherwise write a "jwt-secret" file wherever `go test` runs from —
	// isolate cwd instead (see TestLoad_JwtSecretDefault_GenAndResolve's
	// own doc comment for the underlying behavior this works around).
	t.Chdir(t.TempDir())
	dir := t.TempDir()
	// querying.md ## Configuration's real keys : pg.host/pg.port.
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

// TestLoad_QueryUserDefaultsToPgUser covers querying.md's own explicit
// cross-default : "pg.query.user (default: pg.user if provided)" — the
// default's VALUE is another config key, not a constant, so this needs its
// own test distinct from the generic *OrDefault coverage elsewhere.
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

// TestLoad_PgURI_IsAuthoritative covers pg.uri's own all-or-nothing rule :
// when set, it wins outright, and the granular pg.host/port/user/password/
// database fields are ignored entirely rather than merged with it.
// pg.query.user, being an independent OPTIONAL override, must still apply
// on top — assemble() itself never parses pg.uri (that's cmd/rel's own
// concern, building the actual connection strings), it only decides
// whether pg.query.user/password fall back to pg.user/password (skipped
// when pg.uri is set, since there's no plain-string default to fall back
// to).
func TestLoad_PgURI_IsAuthoritative(t *testing.T) {
	t.Chdir(t.TempDir()) // see TestLoad_PrecedenceFileEnvFlag's own note on why
	p := writeFile(t, t.TempDir(), "rel.toml", `
[pg]
uri = "postgres://u:p@db.internal:5432/mydb"
host = "should-be-ignored"
user = "should-be-ignored"

[pg.query]
user = "query_user"
`)
	cfg, err := Load([]string{"--config=" + p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Pg.URI != "postgres://u:p@db.internal:5432/mydb" {
		t.Errorf("expected pg.uri read directly, got %q", cfg.Pg.URI)
	}
	if cfg.Pg.Query.User != "query_user" {
		t.Errorf("expected pg.query.user to still apply on top of pg.uri, got %q", cfg.Pg.Query.User)
	}
}

func TestLoad_DefaultsApplyWhenNothingSet(t *testing.T) {
	// jwt.secret is set explicitly here to a fixed value : its real default
	// is a $GEN$ expression that writes a generated-secret FILE to the
	// process's cwd when nothing else sets it — TestLoad_JwtSecretDefault_
	// GenAndResolve below exercises that behavior properly, isolated via
	// t.Chdir into a temp dir ; this test would otherwise leave a stray
	// "jwt-secret" file in the config/ package directory on every run.
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
	if cfg.Http.RequestDomainName != "RelHttpRequest" || cfg.Http.ResponseDomainName != "RelHttpResponse" {
		t.Errorf("expected default http domain names, got %+v", cfg.Http)
	}
	if cfg.Http.CookiesMaxAge != DefaultHttpCookiesMaxAge {
		t.Errorf("expected default http.cookies_max_age=%d, got %d", DefaultHttpCookiesMaxAge, cfg.Http.CookiesMaxAge)
	}
	if cfg.Http.Static.Path != DefaultHttpStaticPath {
		t.Errorf("expected default http.static.path=%q, got %q", DefaultHttpStaticPath, cfg.Http.Static.Path)
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
	// Default blacklist must still be present when config doesn't touch it.
	if !cfg.Blacklist.IsRelationBlacklisted("pg_catalog", "anything") {
		t.Errorf("expected DefaultBlacklist to still apply")
	}
}

// TestLoad_JwtSecretDefault_GenAndResolve covers a bug caught before it
// shipped : jwt.secret's own DEFAULT is a "$FILE$jwt-secret$GEN$32"
// expression, but resolveFileIndirection only resolves $FILE$ values
// actually PRESENT in the merged config tree — a Go-level fallback default
// applied afterward in assemble() was never being resolved at all, handing
// back the literal, unresolved "$FILE$..." string as if it were the real
// secret. t.Chdir isolates the $GEN$ file write to a temp directory, not
// wherever `go test` actually runs from.
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
		t.Errorf("expected the generated secret persisted to ./jwt-secret, got: %v", err)
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

// TestLoad_MalformedValueIsFatal_TOML covers the gap advisor-caught :
// assemble() previously used only *OrDefault accessors, whose errors were
// silently discarded (the "errs" slice was write-only), so a PRESENT but
// wrong-type value (e.g. http.port = "abc") would just silently fall back
// to the default instead of failing Load — contradicting ## Accessing
// configuration's "never silently coerced or zeroed" and ## The assembled
// Config object's "if any errors were collected, Rel logs all of them
// together and exits."
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

// TestLoad_FileIndirectionEndToEnd exercises resolveFileIndirection's own
// k.All()/k.Set() write-back through the real Load() pipeline, not just
// resolveFileValue's string-parsing logic in isolation (advisor : the
// koanf-specific assumptions there — Set unflattening a dotted key, All
// being safe to iterate while Setting — were previously unverified).
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

// TestResolveFileValue_PathContainingMarkerSubstring covers a bug an
// adversarial review caught : the marker search used to be the FIRST
// occurrence of "$GEN$"/"$DEFAULT$" in the remainder, so a real, readable
// file whose own path happens to contain "$GEN$" as a substring (e.g. a
// directory literally named "secrets_$GEN$_v2") got misparsed — the path
// was split at the substring instead of the real trailing marker, and the
// leftover path fragment was rejected as an invalid $GEN$ length.
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

// TestResolveFileValue_GenLengthRejectsTrailingGarbage covers a bug an
// adversarial review caught : parseGenLength used fmt.Sscanf("%d", ...),
// which silently accepts "16xyz" as 16 instead of rejecting the malformed
// trailing characters.
func TestResolveFileValue_GenLengthRejectsTrailingGarbage(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "generated.txt")
	if _, err := resolveFileValue("$FILE$" + p + "$GEN$16xyz"); err == nil {
		t.Fatalf("expected an error for a $GEN$ length with trailing garbage, got nil")
	}
}

// TestResolveFileValue_GenLengthMismatchIsFatal covers a bug an adversarial
// review caught : two config keys pointing at the same $GEN$ path with
// different declared lengths used to silently return whichever length was
// generated first, with no error — a config keys copy-paste or path
// collision would go completely unnoticed.
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
