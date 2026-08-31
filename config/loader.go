// Package config assembles Rel's runtime Config from three sources — a
// config file, REL_ environment variables, and CLI flags — per
// specs/configuration.md. Load is the entry point ; everything else in
// this file is Load's own machinery.
package config

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/knadh/koanf/parsers/huml"
	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	env "github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	koanf "github.com/knadh/koanf/v2"
)

// configFileNames is the ## Config file discovery search order's per-step
// filename set, tried at each of the three search locations.
var configFileNames = []string{"rel.toml", "rel.yaml", "rel.yml", "rel.huml"}

// Load builds a *Config from a config file (if any), REL_ environment
// variables, and CLI-style args (dotted "--key=value"/"--key value" pairs),
// later sources overriding earlier ones, per ## Sources and precedence.
// args is normally os.Args[1:] ; passed explicitly so callers/tests don't
// depend on the real process argv.
func Load(args []string) (*Config, error) {
	k := koanf.New(".")

	path, err := resolveConfigFilePath(args, os.Getenv("REL_CONFIG"), discoveryRoots())
	if err != nil {
		// slog.Default() throughout this file, not logging.For(...) — see
		// reader.go's logErr doc comment for why (ordering AND an actual
		// import cycle, not a convenience shortcut).
		slog.Default().Error("config: resolving config file path", "error", err.Error())
		return nil, err
	}
	if path != "" {
		parser, perr := parserFor(path)
		if perr != nil {
			slog.Default().Error("config: unsupported config file extension", "path", path, "error", perr.Error())
			return nil, perr
		}
		if err := k.Load(file.Provider(path), parser); err != nil {
			// Deliberately NOT logging/wrapping err.Error() here : TOML/YAML/
			// HUML parse errors can embed a fragment of the offending VALUE
			// verbatim (confirmed empirically — e.g. go-toml's "no value can
			// start with s" for an unquoted "pg.password = supersecret..."
			// line literally echoes the value's first character(s)), which
			// ## Error handling and secrets forbids regardless of whether the
			// value looks like a secret. Only the path is safe to surface.
			slog.Default().Error("config: loading config file: parse error", "path", path)
			return nil, fmt.Errorf("config: %s: parse error", path)
		}
	}

	if err := k.Load(env.Provider(".", env.Opt{
		Prefix: "REL_",
		TransformFunc: func(k, v string) (string, any) {
			key := strings.ToLower(strings.TrimPrefix(k, "REL_"))
			key = strings.ReplaceAll(key, "__", ".")
			return key, v
		},
	}), nil); err != nil {
		slog.Default().Error("config: loading environment variables", "error", err.Error())
		return nil, fmt.Errorf("config: loading environment variables: %w", err)
	}

	flags, ferr := parseFlags(args)
	if ferr != nil {
		slog.Default().Error("config: parsing CLI flags", "error", ferr.Error())
		return nil, ferr
	}
	if len(flags) > 0 {
		if err := k.Load(confmap.Provider(flags, "."), nil); err != nil {
			slog.Default().Error("config: loading CLI flags", "error", err.Error())
			return nil, fmt.Errorf("config: loading CLI flags: %w", err)
		}
	}

	if err := resolveFileIndirection(k); err != nil {
		return nil, err
	}

	if err := rejectArrays(k); err != nil {
		return nil, err
	}

	return assemble(k)
}

// resolveConfigFilePath is ## Config file discovery : an explicit --config/
// -c flag (highest precedence, checked first) or REL_CONFIG env var names
// exactly one file to load, fatally if it can't be found ; otherwise walk
// roots in order, returning the first step with exactly one matching
// filename, fatally if a step has more than one. No match anywhere is not
// an error — returns "", nil.
func resolveConfigFilePath(args []string, envConfig string, roots []string) (string, error) {
	if explicit := explicitConfigFlag(args); explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("config: --config %s: %w", explicit, err)
		}
		return explicit, nil
	}
	if envConfig != "" {
		if _, err := os.Stat(envConfig); err != nil {
			return "", fmt.Errorf("config: REL_CONFIG=%s: %w", envConfig, err)
		}
		return envConfig, nil
	}
	for _, root := range roots {
		var found []string
		for _, name := range configFileNames {
			p := filepath.Join(root, name)
			if _, err := os.Stat(p); err == nil {
				found = append(found, p)
			}
		}
		switch len(found) {
		case 0:
			continue
		case 1:
			return found[0], nil
		default:
			return "", fmt.Errorf("config: ambiguous config files in %s: %s", root, strings.Join(found, ", "))
		}
	}
	return "", nil
}

// explicitConfigFlag scans args for --config/-c, either "--config=path",
// "--config path", "-c=path", or "-c path" — a small, self-contained scan
// since this decides WHICH file to load before the general flag-loading
// pass (parseFlags) even runs.
func explicitConfigFlag(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		for _, prefix := range []string{"--config=", "-c="} {
			if strings.HasPrefix(a, prefix) {
				return a[len(prefix):]
			}
		}
		if a == "--config" || a == "-c" {
			if i+1 < len(args) {
				return args[i+1]
			}
		}
	}
	return ""
}

// discoveryRoots is ## Config file discovery's three search locations, in
// order : ./, $XDG_CONFIG_HOME/rel (default ~/.config/rel), /etc/rel.
func discoveryRoots() []string {
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		if home, err := os.UserHomeDir(); err == nil {
			xdg = filepath.Join(home, ".config")
		}
	}
	roots := []string{"."}
	if xdg != "" {
		roots = append(roots, filepath.Join(xdg, "rel"))
	}
	roots = append(roots, "/etc/rel")
	return roots
}

// parserFor picks the koanf.Parser for path's extension.
func parserFor(path string) (koanf.Parser, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		return toml.Parser(), nil
	case ".yaml", ".yml":
		return yaml.Parser(), nil
	case ".huml":
		return huml.Parser(), nil
	default:
		return nil, fmt.Errorf("config: %s: unrecognized extension (want .toml/.yaml/.yml/.huml)", path)
	}
}

// parseFlags is ## Sources and precedence's highest-precedence source :
// "--logging.handler=value" or "--logging.handler value" -> "logging.handler".
// Not stdlib flag/pflag : both require every flag pre-registered by name,
// which doesn't fit arbitrary dotted config keys known only at the config
// schema level, not compiled into this loader. --config/-c are skipped here
// (handled separately by explicitConfigFlag, since which file to load must
// be resolved before this general pass runs).
func parseFlags(args []string) (map[string]any, error) {
	out := map[string]any{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			continue
		}
		body := a[2:]
		if body == "config" {
			i++ // --config path : consume the value, already handled elsewhere
			continue
		}
		if strings.HasPrefix(body, "config=") {
			continue
		}
		if eq := strings.IndexByte(body, '='); eq >= 0 {
			out[body[:eq]] = body[eq+1:]
			continue
		}
		if i+1 >= len(args) {
			return nil, fmt.Errorf("config: flag --%s has no value", body)
		}
		out[body] = args[i+1]
		i++
	}
	return out, nil
}

// resolveFileIndirection is ## $FILE$ value indirection, run once over the
// fully merged tree (per the spec : "resolved once, over the fully merged
// configuration tree, before any type coercion or validation").
func resolveFileIndirection(k *koanf.Koanf) error {
	for key, v := range k.All() {
		s, ok := v.(string)
		if !ok || !strings.HasPrefix(s, "$FILE$") {
			continue
		}
		resolved, err := resolveFileValue(s)
		if err != nil {
			slog.Default().Error("config: resolving $FILE$ value", "path", key, "error", err.Error())
			return fmt.Errorf("config: %s: %w", key, err)
		}
		if err := k.Set(key, resolved); err != nil {
			return fmt.Errorf("config: %s: setting resolved $FILE$ value: %w", key, err)
		}
	}
	return nil
}

// resolveFileValue implements the three $FILE$ forms : plain, $DEFAULT$
// fallback, $GEN$ generate-and-write. filePath is resolved relative to the
// process's current working directory (the spec's own rule) — that's
// os.ReadFile's default behavior for a relative path, nothing extra needed.
//
// The marker is located via the LAST occurrence of "$DEFAULT$"/"$GEN$" in
// the remainder, not the first : the spec's own syntax puts the marker
// right before the fallback/length suffix at the END of the value, and a
// real file PATH can itself legitimately contain either marker string as a
// substring (e.g. a directory literally named "secrets_$GEN$_v2") — using
// the first occurrence would misparse the path itself as the split point.
// This isn't a full fix (an arbitrary $DEFAULT$ fallback string could in
// principle also contain "$DEFAULT$"), but it correctly handles the much
// more likely case : the path containing the marker, not the value.
func resolveFileValue(raw string) (string, error) {
	rest := strings.TrimPrefix(raw, "$FILE$")

	if idx := strings.LastIndex(rest, "$DEFAULT$"); idx >= 0 {
		filePath, fallback := rest[:idx], rest[idx+len("$DEFAULT$"):]
		data, err := os.ReadFile(filePath)
		if err != nil {
			return fallback, nil
		}
		return trimOneNewline(data), nil
	}

	// $GEN$'s own syntax requires a positive integer immediately after the
	// marker (nothing else), unlike $DEFAULT$'s free-form fallback text —
	// so unlike $DEFAULT$, a $GEN$-shaped split that DOESN'T parse as a
	// valid length is treated as "this wasn't really a $GEN$ marker, just a
	// path that happens to contain the substring" and falls through to the
	// plain-path case below, rather than erroring immediately. This
	// resolves the realistic case (a path segment literally named e.g.
	// "secrets_$GEN$_v2") without needing an escape syntax the spec never
	// defined ; a genuine $GEN$ typo (garbage after a real trailing marker)
	// still ends up as an error either way, just via "file not found" on
	// the whole string instead of "invalid length".
	if idx := strings.LastIndex(rest, "$GEN$"); idx >= 0 {
		filePath, lenStr := rest[:idx], rest[idx+len("$GEN$"):]
		if n, cerr := parseGenLength(lenStr); cerr == nil {
			return resolveGenValue(filePath, n)
		}
	}

	data, err := os.ReadFile(rest)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", rest, err)
	}
	return trimOneNewline(data), nil
}

// resolveGenValue is $GEN$'s own branch, split out of resolveFileValue so
// the "isn't shaped like $GEN$" fallthrough above stays a plain early
// return.
func resolveGenValue(filePath string, n int) (string, error) {
	data, err := os.ReadFile(filePath)
	if err == nil {
		existing := trimOneNewline(data)
		// A cheap, unambiguous safety check : two config keys pointing at
		// the same $GEN$ path but declaring different lengths is almost
		// certainly a config mistake (copy-paste, or two unrelated fields
		// accidentally sharing a path), not an intentional "reuse
		// whatever's there" — silently returning the wrong length would be
		// exactly the kind of value-shaped surprise ## Error handling and
		// secrets' "malformed value is fatal" posture is meant to catch.
		if len(existing) != n {
			return "", fmt.Errorf("$GEN$: %s already holds a %d-character value, but this key requested %d", filePath, len(existing), n)
		}
		return existing, nil
	}
	generated, gerr := generateRandom(n)
	if gerr != nil {
		return "", gerr
	}
	if werr := os.WriteFile(filePath, []byte(generated), 0o600); werr != nil {
		return "", fmt.Errorf("writing generated value to %s: %w", filePath, werr)
	}
	return generated, nil
}

// trimOneNewline trims exactly one trailing "\n" or "\r\n" — the spec's own
// wording — never more, so multi-line values survive intact.
func trimOneNewline(data []byte) string {
	s := string(data)
	if strings.HasSuffix(s, "\r\n") {
		return s[:len(s)-2]
	}
	if strings.HasSuffix(s, "\n") {
		return s[:len(s)-1]
	}
	return s
}

// parseGenLength uses strconv.Atoi (full-string match), not fmt.Sscanf :
// Sscanf("%d", ...) happily accepts "16xyz" as 16, silently ignoring the
// trailing garbage instead of rejecting the malformed value — confirmed
// empirically. A stray character after the number is a config typo that
// deserves the same fatal treatment every other malformed value gets here.
func parseGenLength(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("$GEN$: invalid length %q", s)
	}
	return n, nil
}

// generateRandom returns n base32-alphabet random characters.
func generateRandom(n int) (string, error) {
	// base32 encodes 5 bits/char ; over-generate bytes, then truncate the
	// encoded string to exactly n characters.
	buf := make([]byte, (n*5+7)/8+1)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating random value: %w", err)
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)
	if len(enc) < n {
		return "", fmt.Errorf("generating random value: insufficient entropy")
	}
	return enc[:n], nil
}

// rejectArrays is ## No arrays' enforcement pass, run over the fully merged
// tree : any value whose concrete type is a slice/array is fatal, naming
// only the key.
func rejectArrays(k *koanf.Koanf) error {
	for key, v := range k.All() {
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
			slog.Default().Error("config: array value not allowed", "path", key)
			return fmt.Errorf("config: %s: array values are not allowed (see specs/configuration.md ## No arrays)", key)
		}
	}
	return nil
}

// assemble builds *Config from k, per ## The assembled Config object :
// every field's error is collected (errors.Join), not raised immediately,
// so a misconfigured deployment sees everything wrong at once.
func assemble(k *koanf.Koanf) (*Config, error) {
	var errs []error
	root := newReader(k, "", &errs)

	cfg := &Config{}

	// pg.uri / pg.user / pg.query.* / pg.host / pg.port / pg.database —
	// see config.go's Pg/PgQuery doc comments for the full reasoning
	// behind this shape : one primary connection (URI, or the granular
	// fields), used for introspection and dmut migrations always, and as
	// the request-serving connection's own fallback ; pg.query.* is an
	// OPTIONAL narrower login for request-serving specifically, never
	// required. Previously dmut.*/query.* as two unrelated top-level
	// namespaces, then briefly pg.admin.*/pg.query.* — unified under
	// pg.* directly, no "admin" distinction : SET ROLE is what actually
	// restricts a request's data access, not the connecting login's own
	// privileges, so requiring two separate logins just to get started
	// was never buying real safety, only friction.
	cfg.Pg.URI = root.GetStringOrDefault("pg.uri", "")
	cfg.Pg.User = root.GetStringOrDefault("pg.user", "")
	cfg.Pg.Password = root.GetStringOrDefault("pg.password", "")
	cfg.Pg.Host = root.GetStringOrDefault("pg.host", DefaultPgHost)
	cfg.Pg.Port = root.GetIntOrDefault("pg.port", DefaultPgPort)
	// pg.database : NOT in query-engine.md at all — config.Pg had no field
	// naming which database to connect to, genuinely missing before this
	// (see specs/TODO.md's own note on this invented key).
	cfg.Pg.Database = root.GetStringOrDefault("pg.database", "")
	cfg.Pg.PoolSize = root.GetIntOrDefault("pg.pool_size", DefaultPgPoolSize)

	// pg.query.user/pg.query.password default to pg.user/pg.password when
	// not otherwise provided — the spec's own explicit cross-default, not
	// a general "OrDefault" fallback (the default VALUE is another config
	// key, not a constant). Left as an empty Login (not falling back) when
	// pg.uri is set instead of granular fields : swapping just the
	// userinfo on an otherwise-opaque URI is cmd/rel's own concern (see
	// dsn.go), not this package's — assemble() only resolves cross-
	// defaults it can express as plain config values.
	if cfg.Pg.URI == "" {
		cfg.Pg.Query.User = root.GetStringOrDefault("pg.query.user", cfg.Pg.User)
		cfg.Pg.Query.Password = root.GetStringOrDefault("pg.query.password", cfg.Pg.Password)
	} else {
		cfg.Pg.Query.User = root.GetStringOrDefault("pg.query.user", "")
		cfg.Pg.Query.Password = root.GetStringOrDefault("pg.query.password", "")
	}
	cfg.Pg.Query.AnonymousRole = root.GetStringOrDefault("pg.query.anonymous_role", DefaultPgQueryAnonymousRole)

	cfg.Pg.Query.MaxDepth = root.GetIntOrDefault("pg.query.max_depth", DefaultMaxDepth)
	// well-known-queries.md ## Configuration's actual key (originally
	// query.wellknown.path) : pg.query.wellknown_path, default
	// "/wellknown" — flattened to match every other single-scalar key's
	// underscore convention (it used to be the one gratuitously-nested
	// exception, nested two levels deep for no reason a sibling like
	// anonymous_role didn't share).
	cfg.Pg.Query.WellKnownDirs = root.GetStringOrDefault("pg.query.wellknown_path", DefaultPgQueryWellKnownPath)

	// dev : specs/configuration.md ## Development mode, default false.
	cfg.Dev = root.GetBoolOrDefault("dev", false)

	cfg.Logging.Handler = root.GetStringOrDefault("logging.handler", DefaultLoggingHandler)
	cfg.Logging.Level = root.GetStringOrDefault("logging.level", DefaultLoggingLevel)
	cfg.Logging.Filter = readStringMap(root, "logging.filter")
	cfg.Logging.Exclude = readStringMap(root, "logging.exclude")

	cfg.Http.Host = root.GetStringOrDefault("http.host", "")
	cfg.Http.Port = root.GetIntOrDefault("http.port", DefaultHttpPort)
	cfg.Http.RequestDomainName = root.GetStringOrDefault("http.request_domain_name", DefaultHttpRequestDomainName)
	cfg.Http.ResponseDomainName = root.GetStringOrDefault("http.response_domain_name", DefaultHttpResponseDomainName)
	cfg.Http.UploadDomainName = root.GetStringOrDefault("http.upload_domain_name", DefaultHttpUploadDomainName)
	cfg.Http.CookiesMaxAge = root.GetIntOrDefault("http.cookies_max_age", DefaultHttpCookiesMaxAge)
	cfg.Http.MaxBodySize = root.GetIntOrDefault("http.max_body_size", DefaultHttpMaxBodySize)
	cfg.Http.MaxPartCount = root.GetIntOrDefault("http.max_part_count", DefaultHttpMaxPartCount)
	cfg.Http.Functions.AllowedAuth = root.GetStringOrDefault("http.functions.allowed_auth", "")
	cfg.Http.Functions.AllowedRoutes = root.GetStringOrDefault("http.functions.allowed_routes", "")
	cfg.Http.Functions.CheckSession = root.GetStringOrDefault("http.functions.check_session", "")
	cfg.Http.Static.Path = root.GetStringOrDefault("http.static.path", DefaultHttpStaticPath)
	cfg.Http.Static.Access = readStaticAccess(root, "http.static.access")
	cfg.Http.Templates.Path = root.GetStringOrDefault("http.templates.path", DefaultHttpTemplatesPath)

	cfg.Http.Cors.AllowedOrigins = root.GetStringOrDefault("http.cors.allowed_origins", "")
	cfg.Http.Cors.AllowedMethods = root.GetStringOrDefault("http.cors.allowed_methods", DefaultHttpCorsAllowedMethods)
	cfg.Http.Cors.AllowedHeaders = root.GetStringOrDefault("http.cors.allowed_headers", DefaultHttpCorsAllowedHeaders)
	cfg.Http.Cors.MaxAge = root.GetIntOrDefault("http.cors.max_age", DefaultHttpCorsMaxAge)

	cfg.Http.Csp.DefaultSrc = root.GetStringOrDefault("http.csp.default_src", DefaultHttpCspDefaultSrc)
	cfg.Http.Csp.ScriptSrc = root.GetStringOrDefault("http.csp.script_src", "")
	cfg.Http.Csp.StyleSrc = root.GetStringOrDefault("http.csp.style_src", "")
	cfg.Http.Csp.ImgSrc = root.GetStringOrDefault("http.csp.img_src", "")
	cfg.Http.Csp.FontSrc = root.GetStringOrDefault("http.csp.font_src", "")
	cfg.Http.Csp.ConnectSrc = root.GetStringOrDefault("http.csp.connect_src", "")
	cfg.Http.Csp.ObjectSrc = root.GetStringOrDefault("http.csp.object_src", "")
	cfg.Http.Csp.FrameAncestors = root.GetStringOrDefault("http.csp.frame_ancestors", "")
	cfg.Http.Csp.BaseUri = root.GetStringOrDefault("http.csp.base_uri", "")
	cfg.Http.Csp.FormAction = root.GetStringOrDefault("http.csp.form_action", "")
	cfg.Http.Csp.Policy = root.GetStringOrDefault("http.csp.policy", "")

	// jwt.secret's own DEFAULT (DefaultJwtSecret) is itself a "$FILE$..."
	// expression — but resolveFileIndirection (called above, before
	// assemble runs) only walks k.All(), the keys ACTUALLY PRESENT in the
	// merged tree ; when nothing sets jwt.secret at all, it's absent from
	// that tree entirely, so the $FILE$ machinery never sees it, and
	// GetStringOrDefault would otherwise hand back the literal, unresolved
	// "$FILE$jwt-secret$GEN$32" string as if it were the real secret. Only
	// resolve it here, targeted, rather than a second blanket $FILE$ pass
	// over the whole tree for one field.
	jwtSecret := root.GetStringOrDefault("jwt.secret", DefaultJwtSecret)
	if strings.HasPrefix(jwtSecret, "$FILE$") {
		resolved, ferr := resolveFileValue(jwtSecret)
		if ferr != nil {
			errs = append(errs, fmt.Errorf("config: jwt.secret: %w", ferr))
		} else {
			jwtSecret = resolved
		}
	}
	cfg.Jwt.Secret = jwtSecret
	cfg.Jwt.CookieName = root.GetStringOrDefault("jwt.cookie_name", DefaultJwtCookieName)
	cfg.Jwt.Algorithm = root.GetStringOrDefault("jwt.algorithm", DefaultJwtAlgorithm)
	cfg.Jwt.SameSite = root.GetStringOrDefault("jwt.same_site", DefaultJwtSameSite)
	cfg.Jwt.MaxAge = root.GetIntOrDefault("jwt.max_age", DefaultJwtMaxAge)
	cfg.Jwt.RenewAfter = root.GetFloat64OrDefault("jwt.renew_after", DefaultJwtRenewAfter)
	cfg.Jwt.MaxSessionAge = root.GetIntOrDefault("jwt.max_session_age", DefaultJwtMaxSessionAge)

	cfg.Dmut.Path = root.GetStringOrDefault("dmut.path", DefaultDmutPath)
	cfg.Dmut.ReloadDrainTimeout = root.GetIntOrDefault("dmut.reload_drain_timeout", DefaultDmutReloadDrainTimeout)

	def := DefaultBlacklist()
	cfg.Blacklist.Functions = readBlacklist(root, "blacklist.functions", def.Functions)
	cfg.Blacklist.Relations = readBlacklist(root, "blacklist.relations", def.Relations)

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return cfg, nil
}

// readStringMap reads path's immediate children as a flat map[string]string
// — logging.filter.<key>/logging.exclude.<key>, each value a plain string.
// An absent/non-object path yields an empty map, not an error : filter/
// exclude are optional per the spec ("default empty").
func readStringMap(root *ConfigReader, path string) map[string]string {
	out := map[string]string{}
	it, err := root.GetIterator(path)
	if err != nil {
		return out
	}
	for key, child := range it {
		if s, err := child.GetString(""); err == nil {
			out[key] = s
		}
	}
	return out
}

// readStaticAccess reads http.static.access.<name>.{prefix,function} into
// a map[string]StaticAccessRule — same named-sub-key shape readBlacklist
// uses for blacklist.functions/relations, since config can't hold arrays
// (specs/http-content.md ### Access control). An absent/non-object path
// yields an empty map, not an error — access control is entirely opt-in.
func readStaticAccess(root *ConfigReader, path string) map[string]StaticAccessRule {
	out := map[string]StaticAccessRule{}
	it, err := root.GetIterator(path)
	if err != nil {
		return out
	}
	for name, ruleReader := range it {
		var rule StaticAccessRule
		if s, err := ruleReader.GetString("prefix"); err == nil {
			rule.Prefix = s
		}
		if s, err := ruleReader.GetString("function"); err == nil {
			rule.Function = s
		}
		out[name] = rule
	}
	return out
}

// readBlacklist reads blacklist.functions.<schema>.<name> (or
// blacklist.relations.<schema>.<name>, same shape, called separately for
// each) into Blacklist's two-level map shape, merged on top of def
// (DefaultBlacklist()'s own entries — config never removes the spec's
// built-in defaults, only adds to them, per ## Scoping's "Default
// blacklist").
func readBlacklist(root *ConfigReader, path string, def map[string]map[string]string) map[string]map[string]string {
	out := map[string]map[string]string{}
	for schema, names := range def {
		out[schema] = map[string]string{}
		maps.Copy(out[schema], names)
	}
	it, err := root.GetIterator(path)
	if err != nil {
		return out
	}
	for schema, schemaReader := range it {
		names, err := schemaReader.GetIterator("")
		if err != nil {
			continue
		}
		if out[schema] == nil {
			out[schema] = map[string]string{}
		}
		for name, nameReader := range names {
			if s, err := nameReader.GetString(""); err == nil {
				out[schema][name] = s
			}
		}
	}
	return out
}
