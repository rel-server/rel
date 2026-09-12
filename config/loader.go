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
	"net"
	"net/url"
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
		// slog.Default() throughout this file, not logging.For(...) : see
		// reader.go's logErr doc comment (ordering, plus an import cycle).
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
			// Not err.Error() : TOML/YAML/HUML parse errors can echo a
			// fragment of the offending value verbatim (## Error handling and secrets).
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

// resolveConfigFilePath is ## Config file discovery : --config/-c or
// REL_CONFIG take precedence, else the first root with exactly one match.
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

// explicitConfigFlag scans args for --config/-c (either "=path" or a
// separate " path" arg) before the general flag-loading pass runs.
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

// parseFlags is ## Sources and precedence's highest-precedence source ;
// not stdlib flag/pflag, which need flags pre-registered by name.
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
// fully merged tree, before any type coercion or validation.
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

// resolveFileValue implements the three $FILE$ forms (plain/$DEFAULT$/$GEN$) ;
// the marker is found via its LAST occurrence, since a path can itself contain it as a substring.
// The path portion of any of the three forms may itself be
// SplitPathList's colon-separated search list — see resolveGenValue's own
// doc comment for why $GEN$ needs a stricter per-candidate rule than the
// two read-only forms below.
func resolveFileValue(raw string) (string, error) {
	rest := strings.TrimPrefix(raw, "$FILE$")

	if idx := strings.LastIndex(rest, "$DEFAULT$"); idx >= 0 {
		pathList, fallback := rest[:idx], rest[idx+len("$DEFAULT$"):]
		if data, _, err := readFirstExisting(SplitPathList(pathList)); err == nil {
			return trimOneNewline(data), nil
		}
		return fallback, nil
	}

	// A split that doesn't parse as a valid length falls through to the
	// plain-path case instead of erroring — handles a literal "$GEN$" in a path segment.
	if idx := strings.LastIndex(rest, "$GEN$"); idx >= 0 {
		pathList, lenStr := rest[:idx], rest[idx+len("$GEN$"):]
		if n, cerr := parseGenLength(lenStr); cerr == nil {
			return resolveGenValue(pathList, n)
		}
	}

	paths := SplitPathList(rest)
	data, _, err := readFirstExisting(paths)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", rest, err)
	}
	return trimOneNewline(data), nil
}

// readFirstExisting returns the first candidate (in order) that reads
// successfully — any read error, including "doesn't exist", just moves on
// to the next candidate, matching http.static.path/pg.query.wellknown_path's
// own "first match wins, a missing entry is silently skipped" convention.
// Used by the plain and $DEFAULT$ forms only ; $GEN$ needs the stricter,
// error-distinguishing walk in resolveGenValue instead, since a write is
// involved.
func readFirstExisting(paths []string) (data []byte, path string, err error) {
	var errs []error
	for _, p := range paths {
		d, e := os.ReadFile(p)
		if e == nil {
			return d, p, nil
		}
		errs = append(errs, e)
	}
	return nil, "", errors.Join(errs...)
}

// resolveGenValue is $GEN$'s own branch, split out so resolveFileValue's
// "isn't shaped like $GEN$" fallthrough stays a plain early return.
// pathList is SplitPathList's colon-separated search list — e.g. jwt.secret's
// own default, "/secrets/jwt-secret:./jwt-secret", tries the deployment's
// intended mount point first and falls back to a plain cwd-relative file for
// an unconfigured dev run, per specs/configuration.md ## $GEN$ multi-path
// resolution.
//
// Three passes, each over the FULL candidate list before falling through to
// the next pass — never per-candidate interleaved, so an earlier candidate
// always wins over a later one regardless of which pass satisfies it :
//
//  1. Any candidate that already holds a value (i.e. its file exists and
//     reads successfully) wins immediately — the first such candidate, in
//     list order. A length mismatch against an existing candidate's value is
//     immediately fatal (never skipped to the next candidate) : it almost
//     certainly means two keys share this $GEN$ reference with different
//     declared lengths, the exact mistake the single-path version of this
//     check has always caught.
//  2. Nothing has a value yet ; try to CREATE one, at the first candidate
//     whose parent directory exists. A candidate whose parent directory is
//     simply missing is skipped, not fatal (it means that path's deployment
//     mechanism — a volume mount, typically — was never wired up). A
//     candidate whose parent directory DOES exist but still refuses the
//     write (permissions, read-only filesystem, ...) is immediately fatal,
//     not skipped : the operator went to the trouble of creating that
//     directory, so a write failure there is a real misconfiguration to
//     surface loudly, not a signal to go looking for value in some other,
//     unintended location.
//  3. Every candidate's parent directory is missing : nothing was ever
//     configured to receive this secret. Rather than fail to boot outright,
//     generate an EPHEMERAL, unpersisted value for this run only — purely a
//     convenience for a developer spinning up rel with no volumes wired at
//     all — and print it, loudly flagged, since it is otherwise invisible
//     and regenerates (invalidating every session/token issued against it)
//     on every single restart. specs/configuration.md ## Error handling and
//     secrets' "never log the resolved value" rule deliberately does not
//     cover this : that rule is scoped to logged ERRORS, and this isn't one.
func resolveGenValue(pathList string, n int) (string, error) {
	paths := SplitPathList(pathList)
	if len(paths) == 0 {
		return "", fmt.Errorf("$GEN$: no path given")
	}

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("$GEN$: reading %s: %w", p, err)
		}
		existing := trimOneNewline(data)
		if len(existing) != n {
			return "", fmt.Errorf("$GEN$: %s already holds a %d-character value, but this key requested %d", p, len(existing), n)
		}
		return existing, nil
	}

	generated, gerr := generateRandom(n)
	if gerr != nil {
		return "", gerr
	}

	for _, p := range paths {
		dir := filepath.Dir(p)
		if _, err := os.Stat(dir); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("$GEN$: checking %s: %w", dir, err)
		}
		if werr := os.WriteFile(p, []byte(generated), 0o600); werr != nil {
			return "", fmt.Errorf("$GEN$: writing generated value to %s: %w", p, werr)
		}
		return generated, nil
	}

	logEphemeralGenValue(pathList, generated)
	return generated, nil
}

// logEphemeralGenValue is resolveGenValue's pass 3 : deliberately printed at
// Warn, in full, stark enough not to scroll past unnoticed — see
// resolveGenValue's own doc comment for why this doesn't fall under ##
// Error handling and secrets' never-log-the-value rule.
func logEphemeralGenValue(pathList, generated string) {
	l := slog.Default()
	l.Warn("################################################################")
	l.Warn("$GEN$: none of the configured directories exist — this is a NEW, EPHEMERAL, UNPERSISTED value for THIS RUN ONLY")
	l.Warn("$GEN$: tried, in order: " + pathList)
	l.Warn("$GEN$: it will NOT survive a restart, and every session/token issued against it is invalidated the moment it doesn't")
	l.Warn("$GEN$: do not run this way in production — configure one of the paths above")
	l.Warn("$GEN$: value: " + generated)
	l.Warn("################################################################")
}

// SplitPathList splits a colon-separated search list the way
// http.static.path/pg.query.wellknown_path/$FILE$'s own path portion all do
// : trimmed, empty entries dropped, order preserved (first entry is always
// tried/preferred first).
func SplitPathList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ":") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
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

// parseGenLength uses strconv.Atoi (full-string match), not fmt.Sscanf,
// which silently accepts "16xyz" as 16 instead of rejecting the typo.
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

// PostgresURI builds a pgx connection URI via net/url.URL (QueryEscape
// mis-escapes userinfo) and net.JoinHostPort (IPv6 needs bracketing) — the
// inverse of splitPgURI, used to derive pg.uri when only the granular
// fields (pg.host/pg.port/pg.database/pg.user/pg.password) are set, so
// cfg.Raw's "pg.uri" stays populated either way (specs/pg-uri-precedence.md).
func PostgresURI(host string, port int, database, user, password string) string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   "/" + database,
	}
	return u.String()
}

// splitPgURI extracts host/port/database/user/password from a
// "postgres://..." connection string, for specs/pg-uri-precedence.md's
// pg.uri -> pg.host/pg.port/pg.database/pg.user/pg.password derivation.
// port defaults to DefaultPgPort when the URI names none, matching
// pg.port's own default. user/password are "" when the URI carries no
// userinfo at all, same as pg.user/pg.password's own unset default.
func splitPgURI(uri string) (host string, port int, database, user, password string, err error) {
	u, perr := url.Parse(uri)
	if perr != nil {
		return "", 0, "", "", "", fmt.Errorf("parsing pg.uri: %w", perr)
	}
	host = u.Hostname()
	port = DefaultPgPort
	if p := u.Port(); p != "" {
		n, aerr := strconv.Atoi(p)
		if aerr != nil {
			return "", 0, "", "", "", fmt.Errorf("parsing pg.uri: invalid port %q", p)
		}
		port = n
	}
	database = strings.TrimPrefix(u.Path, "/")
	if u.User != nil {
		user = u.User.Username()
		password, _ = u.User.Password()
	}
	return host, port, database, user, password, nil
}

// rejectArrays is ## No arrays' enforcement pass over the fully merged
// tree ; a slice/array value is fatal, naming only the key.
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

// assemble builds *Config from k ; every field's error is collected
// (errors.Join), so a misconfigured deployment sees everything at once.
func assemble(k *koanf.Koanf) (*Config, error) {
	var errs []error
	root := newReader(k, "", &errs)

	cfg := &Config{}

	// pg.uri/pg.user/pg.query.*/... — see config.go's Pg/PgQuery doc
	// comments for the full shape ; pg.query.* is optional, never required.
	cfg.Pg.URI = root.GetStringOrDefault("pg.uri", "")

	// specs/pg-uri-precedence.md : pg.uri, when set, takes precedence and
	// populates Host/Port/Database/User/Password itself ; any of those set
	// alongside it is a configuration error, not a silently-ignored or
	// silently-overridden value. Otherwise, the granular fields are read
	// and pg.uri is itself derived from them, so cfg.Raw (and hence
	// reload.cmd's {pg.uri}/{pg.user}/... interpolation) always has every
	// one of these six keys populated, regardless of which shape was
	// actually configured.
	if cfg.Pg.URI != "" {
		if root.Exists("pg.host") || root.Exists("pg.port") || root.Exists("pg.database") || root.Exists("pg.user") || root.Exists("pg.password") {
			errs = append(errs, fmt.Errorf("config: pg.host/pg.port/pg.database/pg.user/pg.password: cannot be set alongside pg.uri (see specs/pg-uri-precedence.md)"))
		}
		host, port, database, user, password, uerr := splitPgURI(cfg.Pg.URI)
		if uerr != nil {
			errs = append(errs, fmt.Errorf("config: pg.uri: %w", uerr))
		} else {
			cfg.Pg.Host = host
			cfg.Pg.Port = port
			cfg.Pg.Database = database
			cfg.Pg.User = user
			cfg.Pg.Password = password
		}
	} else {
		cfg.Pg.Host = root.GetStringOrDefault("pg.host", DefaultPgHost)
		cfg.Pg.Port = root.GetIntOrDefault("pg.port", DefaultPgPort)
		// pg.database : not itemized in docs/content/configuration/index.md's
		// Postgres connection table either — an invented key (specs/TODO.md).
		cfg.Pg.Database = root.GetStringOrDefault("pg.database", "")
		cfg.Pg.User = root.GetStringOrDefault("pg.user", "")
		cfg.Pg.Password = root.GetStringOrDefault("pg.password", "")
		cfg.Pg.URI = PostgresURI(cfg.Pg.Host, cfg.Pg.Port, cfg.Pg.Database, cfg.Pg.User, cfg.Pg.Password)
	}
	cfg.Pg.PoolSize = root.GetIntOrDefault("pg.pool_size", DefaultPgPoolSize)

	cfg.Pg.Query.AnonymousRole = root.GetStringOrDefault("pg.query.anonymous_role", DefaultPgQueryAnonymousRole)

	cfg.Pg.Query.MaxDepth = root.GetIntOrDefault("pg.query.max_depth", DefaultMaxDepth)
	// well-known-queries.md ## Configuration : pg.query.wellknown_path,
	// flattened (not nested) to match every other single-scalar key.
	cfg.Pg.Query.WellKnownDirs = root.GetStringOrDefault("pg.query.wellknown_path", DefaultPgQueryWellKnownPath)

	// dev : docs/content/configuration/index.md ## Development mode, default false,
	// kept orthogonal from logging.level so error detail and log verbosity can
	// be toggled independently.
	cfg.Dev = root.GetBoolOrDefault("dev", false)

	// specs/complex-query.md ## Availability : each defaults to Dev, same
	// "true if dev enabled" rule as http.typescript.enable below.
	cfg.Pg.Query.AllowCount = root.GetBoolOrDefault("pg.query.allow_count", cfg.Dev)
	cfg.Pg.Query.AllowStats = root.GetBoolOrDefault("pg.query.allow_stats", cfg.Dev)
	cfg.Pg.Query.AllowQueryPlan = root.GetBoolOrDefault("pg.query.allow_query_plan", cfg.Dev)
	cfg.Pg.Query.AllowSql = root.GetBoolOrDefault("pg.query.allow_sql", cfg.Dev)
	cfg.Pg.Query.AllowRollback = root.GetBoolOrDefault("pg.query.allow_rollback", cfg.Dev)

	cfg.Logging.Handler = root.GetStringOrDefault("logging.handler", DefaultLoggingHandler)
	cfg.Logging.Level = root.GetStringOrDefault("logging.level", DefaultLoggingLevel)
	cfg.Logging.Filter = readStringMap(root, "logging.filter")
	cfg.Logging.Exclude = readStringMap(root, "logging.exclude")

	cfg.Http.Host = root.GetStringOrDefault("http.host", "")
	cfg.Http.PublicHost = root.GetStringOrDefault("http.public_host", "")
	cfg.Http.Port = root.GetIntOrDefault("http.port", DefaultHttpPort)
	cfg.Http.CookiesMaxAge = root.GetIntOrDefault("http.cookies_max_age", DefaultHttpCookiesMaxAge)
	cfg.Http.MaxBodySize = root.GetIntOrDefault("http.max_body_size", DefaultHttpMaxBodySize)
	cfg.Http.MaxUploadSize = root.GetIntOrDefault("http.max_upload_size", cfg.Http.MaxBodySize)
	cfg.Http.MaxPartCount = root.GetIntOrDefault("http.max_part_count", DefaultHttpMaxPartCount)
	cfg.Http.Functions.AllowedAuth = root.GetStringOrDefault("http.functions.allowed_auth", "")
	cfg.Http.Static.Path = root.GetStringOrDefault("http.static.path", DefaultHttpStaticPath)
	cfg.Route = readRoutes(root, "route")
	cfg.Http.Templates.Path = root.GetStringOrDefault("http.templates.path", DefaultHttpTemplatesPath)

	// http.typescript.enable defaults to Dev, same "true if dev enabled"
	// rule as specs/typescript.md ## Configuration states — a computed
	// default, not a plain constant, so it's read after cfg.Dev above.
	cfg.Http.TypeScript.Enable = root.GetBoolOrDefault("http.typescript.enable", cfg.Dev)
	cfg.Http.TypeScript.Schemas = root.GetStringOrDefault("http.typescript.schemas", "")

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

	// DefaultJwtSecret is itself "$FILE$..." ; resolveFileIndirection only
	// walks keys present in the merged tree, so an absent jwt.secret needs its own resolve here.
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

	// specs/oauth-saml.md : openid.<name>.*/saml.* — see readOpenidProviders'
	// own doc comment for why client_id/client_secret need the same manual
	// $FILE$-resolve-of-an-absent-key treatment jwt.secret does above.
	cfg.Http.Functions.SsoCallback = root.GetStringOrDefault("http.functions.sso_callback", "")
	cfg.Openid = readOpenidProviders(root, &errs)
	cfg.Saml.CertificatePath = root.GetStringOrDefault("saml.certificate_path", DefaultSamlCertificatePath)
	cfg.Saml.PrivateKeyPath = root.GetStringOrDefault("saml.private_key_path", DefaultSamlPrivateKeyPath)
	cfg.Saml.Providers = readSamlProviders(root)

	cfg.TypeScript.HelperPath = root.GetStringOrDefault("typescript.helper_path", "")

	cfg.Reload.Cmd = root.GetStringOrDefault("reload.cmd", "")
	cfg.Reload.Timeout = root.GetIntOrDefault("reload.timeout", DefaultReloadTimeout)
	cfg.Reload.DrainTimeout = root.GetIntOrDefault("reload.drain_timeout", DefaultReloadDrainTimeout)

	def := DefaultBlacklist()
	cfg.Blacklist.Functions = readBlacklist(root, "blacklist.functions", def.Functions)
	cfg.Blacklist.Relations = readBlacklist(root, "blacklist.relations", def.Relations)

	// Config.Raw's doc comment : reload.cmd's {name} interpolation needs
	// pg.host/pg.port/pg.database/pg.uri/pg.user/pg.password kept in sync
	// even when only defaulted/derived, not explicitly set by the user.
	_ = k.Set("pg.uri", cfg.Pg.URI)
	_ = k.Set("pg.host", cfg.Pg.Host)
	_ = k.Set("pg.port", cfg.Pg.Port)
	_ = k.Set("pg.database", cfg.Pg.Database)
	_ = k.Set("pg.user", cfg.Pg.User)
	_ = k.Set("pg.password", cfg.Pg.Password)
	cfg.Raw = k.All()

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return cfg, nil
}

// readStringMap reads path's immediate children as a flat map[string]string.
// An absent/non-object path yields an empty map, not an error — optional.
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

// readStaticAccess reads http.static.access.<name>.{prefix,function} —
// named sub-keys, not an array (config can't hold arrays).
// readRoutes reads route.<schema>.<function>.{path,method} —
// specs/new-routes.md ## In the configuration : two levels deep, since a
// route is identified by TWO levels (schema, then function name), not one.
func readRoutes(root *ConfigReader, path string) map[string]map[string]RouteDecl {
	out := map[string]map[string]RouteDecl{}
	schemas, err := root.GetIterator(path)
	if err != nil {
		return out
	}
	for schema, schemaReader := range schemas {
		functions, err := schemaReader.GetIterator("")
		if err != nil {
			continue
		}
		for function, fnReader := range functions {
			var decl RouteDecl
			if s, err := fnReader.GetString("path"); err == nil {
				decl.Path = s
			}
			if s, err := fnReader.GetString("method"); err == nil {
				decl.Method = s
			}
			if s, err := fnReader.GetString("template"); err == nil {
				decl.Template = s
			}
			if b, err := fnReader.GetBool("stream_upload"); err == nil {
				decl.StreamUpload = b
			}
			if b, err := fnReader.GetBool("middleware"); err == nil {
				decl.Middleware = b
			}
			if decl.Path == "" {
				continue
			}
			if out[schema] == nil {
				out[schema] = map[string]RouteDecl{}
			}
			out[schema][function] = decl
		}
	}
	return out
}

// readOpenidProviders reads openid.<name>.* — named sub-keys, same shape
// readStaticAccess uses. client_id/client_secret's default is itself
// "$FILE$..." with "<name>" substituted for this entry's own name (unlike
// jwt.secret, a single fixed default) : resolveFileIndirection (loader.go's
// own pass over the merged tree, run before assemble) only resolves a
// $FILE$ value that's actually PRESENT in the tree, so an entry that
// doesn't set client_id/client_secret explicitly needs that computed
// default resolved here, by hand, same as jwt.secret's own absent-key case.
// Deliberately plain GetString/GetBool/GetStrings, not the *OrDefault
// variants readStaticAccess also avoids for its own two fields — help_test.go's
// TestOptions_MatchAssembleKeys scans loader.go for every literal
// Get\w+OrDefault("...") call and demands a matching config/help.go entry ;
// "issuer"/"client_id"/... are per-name fields, not real top-level dotted
// keys, so using *OrDefault here would produce bogus required Options rows.
func readOpenidProviders(root *ConfigReader, errs *[]error) map[string]OpenidProvider {
	out := map[string]OpenidProvider{}
	it, err := root.GetIterator("openid")
	if err != nil {
		return out
	}
	for name, providerReader := range it {
		var p OpenidProvider
		if s, err := providerReader.GetString("issuer"); err == nil {
			p.Issuer = s
		}
		p.ClientID = resolveOpenidSecretField(providerReader, "client_id", name, DefaultOpenidClientIDPath, errs)
		p.ClientSecret = resolveOpenidSecretField(providerReader, "client_secret", name, DefaultOpenidClientSecretPath, errs)
		p.Scopes = splitStrings(DefaultOpenidScopes)
		if ss, err := providerReader.GetStrings("scopes"); err == nil {
			p.Scopes = ss
		}
		if b, err := providerReader.GetBool("fetch_userinfo"); err == nil {
			p.FetchUserinfo = b
		}
		if s, err := providerReader.GetString("callback_function"); err == nil {
			p.CallbackFunction = s
		}
		if s, err := providerReader.GetString("public_host"); err == nil {
			p.PublicHost = s
		}
		out[name] = p
	}
	return out
}

// resolveOpenidSecretField reads openid.<name>.<field>, defaulting to
// defaultPathTemplate with "<name>" substituted for name, resolving the
// result through resolveFileValue when it's shaped like $FILE$ — whether
// that's the substituted default or a value the deployment set explicitly
// (resolveFileIndirection already resolved an explicitly-set value once,
// so this is a no-op re-resolve for that case, not a second file read with
// different semantics).
func resolveOpenidSecretField(providerReader *ConfigReader, field, name, defaultPathTemplate string, errs *[]error) string {
	def := strings.ReplaceAll(defaultPathTemplate, "<name>", name)
	v := providerReader.GetStringOrDefault(field, def)
	if !strings.HasPrefix(v, "$FILE$") {
		return v
	}
	resolved, ferr := resolveFileValue(v)
	if ferr != nil {
		*errs = append(*errs, fmt.Errorf("config: openid.%s.%s: %w", name, field, ferr))
		return ""
	}
	return resolved
}

// readSamlProviders reads saml.<name>.* the same named-sub-key way
// readOpenidProviders reads openid.<name>.* ; saml.certificate_path/
// saml.private_key_path are NOT nested under here (they're the shared SP
// identity across every entry, not per-name — see Saml's own doc comment),
// so unlike openid's iterator this one skips them by simply never
// descending into a key holding a scalar itself (GetIterator only
// succeeds on an object).
func readSamlProviders(root *ConfigReader) map[string]SamlProvider {
	out := map[string]SamlProvider{}
	it, err := root.GetIterator("saml")
	if err != nil {
		return out
	}
	for name, providerReader := range it {
		if name == "certificate_path" || name == "private_key_path" {
			continue
		}
		p := SamlProvider{ForceSignedRequests: DefaultSamlForceSignedRequests}
		if s, err := providerReader.GetString("idp_metadata_url"); err == nil {
			p.IdpMetadataUrl = s
		}
		if b, err := providerReader.GetBool("force_signed_requests"); err == nil {
			p.ForceSignedRequests = b
		}
		if s, err := providerReader.GetString("callback_function"); err == nil {
			p.CallbackFunction = s
		}
		if s, err := providerReader.GetString("public_host"); err == nil {
			p.PublicHost = s
		}
		if s, err := providerReader.GetString("certificate_path"); err == nil {
			p.CertificatePath = s
		}
		if s, err := providerReader.GetString("private_key_path"); err == nil {
			p.PrivateKeyPath = s
		}
		out[name] = p
	}
	return out
}

// readBlacklist reads blacklist.functions/relations.<schema>.<name> merged
// on top of def — config only adds to the built-in defaults, never removes.
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
