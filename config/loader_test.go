package config

import (
	"os"
	"path/filepath"
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
	dir := t.TempDir()
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

func TestLoad_DefaultsApplyWhenNothingSet(t *testing.T) {
	cfg, err := Load([]string{"--config=" + writeFile(t, t.TempDir(), "rel.toml", "")})
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
	if cfg.Query.MaxDepth != DefaultMaxDepth {
		t.Errorf("expected default max depth %d, got %d", DefaultMaxDepth, cfg.Query.MaxDepth)
	}
	// Default blacklist must still be present when config doesn't touch it.
	if !cfg.Blacklist.IsRelationBlacklisted("pg_catalog", "anything") {
		t.Errorf("expected DefaultBlacklist to still apply")
	}
}

func TestLoad_YamlAndHumlParse(t *testing.T) {
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
