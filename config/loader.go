// Package config assembles rel's runtime Config from a config file, REL_ environment variables and CLI flags.
package config

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
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
	"github.com/samber/oops"
)

// configFileNames are the file names tried at each discovery location.
var configFileNames = []string{"rel.toml", "rel.yaml", "rel.yml", "rel.huml"}

// Load builds a *Config from, by increasing precedence : a config file, an inline config document, REL_ environment
// variables and dotted "--key=value" args. Every invalid value is reported together.
func Load(args []string) (*Config, error) {
	k := koanf.New(".")

	path, err := resolveConfigFilePath(args, os.Getenv("REL_CONFIG"), discoveryRoots())
	if err != nil {
		// slog.Default() throughout this file : logging imports config.
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
			// Not err.Error() : a parse error can echo part of a value.
			slog.Default().Error("config: loading config file: parse error", "path", path)
			return nil, oops.With("path", path).Errorf("config: %s: parse error", path)
		}
	}

	if err := loadInlineConfig(k); err != nil {
		return nil, err
	}

	if err := k.Load(env.Provider(".", env.Opt{
		Prefix: "REL_",
		TransformFunc: func(k, v string) (string, any) {
			key := strings.ToLower(strings.TrimPrefix(k, "REL_"))
			key = strings.ReplaceAll(key, "__", ".")
			if _, isInline := inlineConfigVars[k]; isInline || key == "config" {
				return "", nil // consumed by loadInlineConfig / resolveConfigFilePath, not settings
			}
			return key, v
		},
	}), nil); err != nil {
		slog.Default().Error("config: loading environment variables", "error", err.Error())
		return nil, oops.Wrapf(err, "config: loading environment variables")
	}

	flags, ferr := parseFlags(args)
	if ferr != nil {
		slog.Default().Error("config: parsing CLI flags", "error", ferr.Error())
		return nil, ferr
	}
	if len(flags) > 0 {
		if err := k.Load(confmap.Provider(flags, "."), nil); err != nil {
			slog.Default().Error("config: loading CLI flags", "error", err.Error())
			return nil, oops.Wrapf(err, "config: loading CLI flags")
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

// resolveConfigFilePath : --config/-c or REL_CONFIG take precedence, else the first root with exactly one match.
func resolveConfigFilePath(args []string, envConfig string, roots []string) (string, error) {
	if explicit := explicitConfigFlag(args); explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", oops.With("path", explicit).Wrapf(err, "config: --config %s", explicit)
		}
		return explicit, nil
	}
	if envConfig != "" {
		if _, err := os.Stat(envConfig); err != nil {
			return "", oops.With("path", envConfig).Wrapf(err, "config: REL_CONFIG=%s", envConfig)
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
			return "", oops.With("dir", root, "files", found).Errorf("config: ambiguous config files in %s: %s", root, strings.Join(found, ", "))
		}
	}
	return "", nil
}

// explicitConfigFlag scans args for --config/-c, before the general flag pass.
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

// discoveryRoots are the config file search locations, in order : ./, $XDG_CONFIG_HOME/rel (default ~/.config/rel),
// /etc/rel.
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

// inlineConfigVars maps each environment variable that can carry a whole
// config document to the extension whose parser reads it.
var inlineConfigVars = map[string]string{
	"REL_CONFIG__TOML": ".toml",
	"REL_CONFIG__YAML": ".yaml",
	"REL_CONFIG__HUML": ".huml",
}

// loadInlineConfig merges the document in REL_CONFIG__TOML, REL_CONFIG__YAML or REL_CONFIG__HUML into k, above any
// config file and below individual REL_ variables and flags. Setting more than one is an error.
func loadInlineConfig(k *koanf.Koanf) error {
	var name string
	for v := range inlineConfigVars {
		if os.Getenv(v) == "" {
			continue
		}
		if name != "" {
			a, b := min(name, v), max(name, v)
			err := oops.With("keys", []string{a, b}).Errorf("config: both %s and %s are set, pick one", a, b)
			slog.Default().Error(err.Error())
			return err
		}
		name = v
	}
	if name == "" {
		return nil
	}
	parser, _ := parserForExt(inlineConfigVars[name])
	// Parse errors can echo a fragment of a value, so only the variable name is reported.
	m, err := parser.Unmarshal([]byte(os.Getenv(name)))
	if err == nil {
		err = k.Load(confmap.Provider(m, "."), nil)
	}
	if err != nil {
		slog.Default().Error("config: loading inline config: parse error", "var", name)
		return oops.With("path", name).Errorf("config: %s: parse error", name)
	}
	return nil
}

// parserFor picks the koanf.Parser for path's extension.
func parserFor(path string) (koanf.Parser, error) {
	p, ok := parserForExt(strings.ToLower(filepath.Ext(path)))
	if !ok {
		return nil, oops.With("path", path).Errorf("config: %s: unrecognized extension (want .toml/.yaml/.yml/.huml)", path)
	}
	return p, nil
}

// parserForExt maps a lowercase file extension, dot included, to its parser.
func parserForExt(ext string) (koanf.Parser, bool) {
	switch ext {
	case ".toml":
		return toml.Parser(), true
	case ".yaml", ".yml":
		return yaml.Parser(), true
	case ".huml":
		return huml.Parser(), true
	}
	return nil, false
}

// parseFlags reads the highest-precedence source ; flags are not registered by name, so no stdlib flag package.
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
			return nil, oops.With("flag", body).Errorf("config: flag --%s has no value", body)
		}
		out[body] = args[i+1]
		i++
	}
	return out, nil
}

// resolveFileIndirection resolves $FILE$ values over the merged tree, before any type check.
func resolveFileIndirection(k *koanf.Koanf) error {
	for key, v := range k.All() {
		s, ok := v.(string)
		if !ok || !strings.HasPrefix(s, "$FILE$") {
			continue
		}
		resolved, err := resolveFileValue(s)
		if err != nil {
			slog.Default().Error("config: resolving $FILE$ value", "path", key, "error", err.Error())
			return oops.With("key", key).Wrapf(err, "config: %s", key)
		}
		if err := k.Set(key, resolved); err != nil {
			return oops.With("key", key).Wrapf(err, "config: %s: setting resolved $FILE$ value", key)
		}
	}
	return nil
}

// resolveFileValue resolves the three $FILE$ forms (plain, $DEFAULT$, $GEN$), each of whose path may be a
// colon-separated search list. The marker is found by its last occurrence, since a path can contain it.
func resolveFileValue(raw string) (string, error) {
	rest := strings.TrimPrefix(raw, "$FILE$")

	if idx := strings.LastIndex(rest, "$DEFAULT$"); idx >= 0 {
		pathList, fallback := rest[:idx], rest[idx+len("$DEFAULT$"):]
		if data, _, err := readFirstExisting(SplitPathList(pathList)); err == nil {
			return trimOneNewline(data), nil
		}
		return fallback, nil
	}

	// A length that doesn't parse falls through to the plain form, for a literal "$GEN$" in a path.
	if idx := strings.LastIndex(rest, "$GEN$"); idx >= 0 {
		pathList, lenStr := rest[:idx], rest[idx+len("$GEN$"):]
		if n, cerr := parseGenLength(lenStr); cerr == nil {
			return resolveGenValue(pathList, n)
		}
	}

	paths := SplitPathList(rest)
	data, _, err := readFirstExisting(paths)
	if err != nil {
		return "", oops.With("path", rest).Wrapf(err, "reading %s", rest)
	}
	return trimOneNewline(data), nil
}

// readFirstExisting returns the first candidate that reads ; any read error moves on to the next. Not for $GEN$,
// which writes.
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

// resolveGenValue resolves $GEN$ over pathList in three passes, each over the whole list so an earlier candidate
// always wins :
//  1. The first candidate whose file reads wins. A length mismatch is fatal : two keys share this $GEN$ file with
//     different lengths.
//  2. Otherwise, a value is generated and written at the first candidate whose parent directory exists. A write
//     failure there is fatal, not skipped : the directory was created on purpose.
//  3. No parent directory exists : an unpersisted value is generated for this run and logged, since it changes
//     (invalidating every session) on each restart.
func resolveGenValue(pathList string, n int) (string, error) {
	paths := SplitPathList(pathList)
	if len(paths) == 0 {
		return "", oops.Errorf("$GEN$: no path given")
	}

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", oops.With("path", p).Wrapf(err, "$GEN$: reading %s", p)
		}
		existing := trimOneNewline(data)
		if len(existing) != n {
			return "", oops.With("path", p).Errorf("$GEN$: %s already holds a %d-character value, but this key requested %d", p, len(existing), n)
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
			return "", oops.With("path", dir).Wrapf(err, "$GEN$: checking %s", dir)
		}
		if werr := os.WriteFile(p, []byte(generated), 0o600); werr != nil {
			return "", oops.With("path", p).Wrapf(werr, "$GEN$: writing generated value to %s", p)
		}
		return generated, nil
	}

	logEphemeralGenValue(pathList, generated)
	return generated, nil
}

// logEphemeralGenValue logs pass 3's value in full, at Warn. The never-log-a-secret rule covers errors, not this.
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

// SplitPathList splits a colon-separated search list : trimmed, empty entries dropped, order kept.
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

// trimOneNewline trims exactly one trailing "\n" or "\r\n", never more, so multi-line values survive intact.
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

// parseGenLength uses strconv.Atoi, which rejects "16xyz" where fmt.Sscanf would read 16.
func parseGenLength(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, oops.Errorf("$GEN$: invalid length %q", s)
	}
	return n, nil
}

// generateRandom returns n base32-alphabet random characters.
func generateRandom(n int) (string, error) {
	buf := make([]byte, (n*5+7)/8+1)
	if _, err := rand.Read(buf); err != nil {
		return "", oops.Wrapf(err, "generating random value")
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)
	if len(enc) < n {
		return "", oops.Errorf("generating random value: insufficient entropy")
	}
	return enc[:n], nil
}

// PostgresURI builds pg.uri from the granular pg.* fields, the inverse of splitPgURI.
func PostgresURI(host string, port int, database, user, password string) string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   "/" + database,
	}
	return u.String()
}

// splitPgURI extracts the granular pg.* fields from a "postgres://..." URI. port defaults to DefaultPgPort ;
// user/password are "" when the URI has no userinfo.
func splitPgURI(uri string) (host string, port int, database, user, password string, err error) {
	u, perr := url.Parse(uri)
	if perr != nil {
		return "", 0, "", "", "", oops.Wrapf(perr, "parsing pg.uri")
	}
	host = u.Hostname()
	port = DefaultPgPort
	if p := u.Port(); p != "" {
		n, aerr := strconv.Atoi(p)
		if aerr != nil {
			return "", 0, "", "", "", oops.Errorf("parsing pg.uri: invalid port %q", p)
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

// rejectArrays fails on any array value in the merged tree, naming only the key.
func rejectArrays(k *koanf.Koanf) error {
	for key, v := range k.All() {
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
			slog.Default().Error("config: array value not allowed", "path", key)
			return oops.With("key", key).Errorf("config: %s: array values are not allowed", key)
		}
	}
	return nil
}

// assemble builds *Config from k, collecting every field's error so a misconfigured deployment sees them all.
func assemble(k *koanf.Koanf) (*Config, error) {
	var errs []error
	tracker := newKeyTracker()
	root := newReader(k, "", &errs, tracker)

	cfg := &Config{}

	cfg.Pg.URI = root.GetStringOrDefault("pg.uri", "")

	// pg.uri, when set, populates the granular fields ; setting any of them alongside it is an error. Otherwise pg.uri
	// is derived from them, so reload.cmd interpolation always has all six keys.
	if cfg.Pg.URI != "" {
		if root.Exists("pg.host") || root.Exists("pg.port") || root.Exists("pg.database") || root.Exists("pg.user") || root.Exists("pg.password") {
			errs = append(errs, oops.Errorf("config: pg.host/pg.port/pg.database/pg.user/pg.password: cannot be set alongside pg.uri"))
		}
		host, port, database, user, password, uerr := splitPgURI(cfg.Pg.URI)
		if uerr != nil {
			errs = append(errs, oops.Wrapf(uerr, "config: pg.uri"))
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
		cfg.Pg.Database = root.GetStringOrDefault("pg.database", "")
		cfg.Pg.User = root.GetStringOrDefault("pg.user", "")
		cfg.Pg.Password = root.GetStringOrDefault("pg.password", "")
		cfg.Pg.URI = PostgresURI(cfg.Pg.Host, cfg.Pg.Port, cfg.Pg.Database, cfg.Pg.User, cfg.Pg.Password)
	}
	cfg.Pg.PoolSize = root.GetIntOrDefault("pg.pool_size", DefaultPgPoolSize)
	cfg.Pg.ConnectTimeout = root.GetIntOrDefault("pg.connect_timeout", DefaultPgConnectTimeout)

	cfg.Pg.Query.AnonymousRole = root.GetStringOrDefault("pg.query.anonymous_role", DefaultPgQueryAnonymousRole)

	cfg.Pg.Query.MaxDepth = root.GetIntOrDefault("pg.query.max_depth", DefaultMaxDepth)
	cfg.Pg.Query.WellKnownDirs = root.GetStringOrDefault("pg.query.wellknown_path", DefaultPgQueryWellKnownPath)

	// dev is independent of logging.level.
	cfg.Dev = root.GetBoolOrDefault("dev", false)

	// Each pg.query.allow_* defaults to Dev.
	noticesDefault := "warning"
	if cfg.Dev {
		noticesDefault = "notice"
	}
	cfg.Pg.NoticesLevel = root.GetStringOrDefault("pg.notices.level", noticesDefault)
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
	cfg.Http.Upload.MaxSize = root.GetIntOrDefault("http.upload.max_size", cfg.Http.MaxBodySize)
	cfg.Http.Upload.Dir = root.GetStringOrDefault("http.upload.dir", "")
	cfg.Http.MaxPartCount = root.GetIntOrDefault("http.max_part_count", DefaultHttpMaxPartCount)
	cfg.Http.ResponseBuffer = root.GetIntOrDefault("http.response_buffer", DefaultHttpResponseBuffer)
	cfg.Http.Functions.AllowedAuth = root.GetStringOrDefault("http.functions.allowed_auth", "")
	cfg.Http.Static.Path = root.GetStringOrDefault("http.static.path", DefaultHttpStaticPath)
	cfg.Route = readRoutes(root, "route")
	cfg.Http.Templates.Path = root.GetStringOrDefault("http.templates.path", DefaultHttpTemplatesPath)

	// Read after cfg.Dev, its default.
	cfg.Http.TypeScript.Enable = root.GetBoolOrDefault("http.typescript.enable", cfg.Dev)
	cfg.TypeScript.Schemas = root.GetStringOrDefault("typescript.schemas", "")
	cfg.TypeScript.Codec = root.GetStringOrDefault("typescript.codec", "")
	cfg.TypeScript.Types = readTypeDecls(root, "typescript.types", &errs)

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
	cfg.Http.Csp.WorkerSrc = root.GetStringOrDefault("http.csp.worker_src", "")
	cfg.Http.Csp.ObjectSrc = root.GetStringOrDefault("http.csp.object_src", "")
	cfg.Http.Csp.FrameAncestors = root.GetStringOrDefault("http.csp.frame_ancestors", "")
	cfg.Http.Csp.BaseUri = root.GetStringOrDefault("http.csp.base_uri", "")
	cfg.Http.Csp.FormAction = root.GetStringOrDefault("http.csp.form_action", "")
	cfg.Http.Csp.Policy = root.GetStringOrDefault("http.csp.policy", "")

	// An absent jwt.secret's default is itself "$FILE$..." and resolveFileIndirection only sees present keys.
	jwtSecret := root.GetStringOrDefault("jwt.secret", DefaultJwtSecret)
	if strings.HasPrefix(jwtSecret, "$FILE$") {
		resolved, ferr := resolveFileValue(jwtSecret)
		if ferr != nil {
			errs = append(errs, oops.Wrapf(ferr, "config: jwt.secret"))
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

	cfg.Http.Functions.SsoCallback = root.GetStringOrDefault("http.functions.sso_callback", "")
	cfg.Openid = readOpenidProviders(root, &errs)
	cfg.Saml.CertificatePath = root.GetStringOrDefault("saml.certificate_path", DefaultSamlCertificatePath)
	cfg.Saml.PrivateKeyPath = root.GetStringOrDefault("saml.private_key_path", DefaultSamlPrivateKeyPath)
	cfg.Saml.Providers = readSamlProviders(root)

	cfg.TypeScript.HelperPath = root.GetStringOrDefault("typescript.helper_path", "")

	cfg.Reload.Cmd = root.GetStringOrDefault("reload.cmd", "")
	cfg.Reload.Timeout = root.GetIntOrDefault("reload.timeout", DefaultReloadTimeout)
	cfg.Reload.DrainTimeout = root.GetIntOrDefault("reload.drain_timeout", DefaultReloadDrainTimeout)

	cfg.Dmut.Path = root.GetStringOrDefault("dmut.path", "")
	cfg.Dmut.Timeout = root.GetIntOrDefault("dmut.timeout", DefaultDmutTimeout)
	cfg.Dmut.User = root.GetStringOrDefault("dmut.user", "")
	cfg.Dmut.Password = root.GetStringOrDefault("dmut.password", "")
	cfg.Dmut.Mode = root.GetStringOrDefault("dmut.mode", DmutModeDefault)
	switch cfg.Dmut.Mode {
	case DmutModeDefault, DmutModeSimple, DmutModeFull:
	default:
		errs = append(errs, oops.Errorf("config: dmut.mode: %q is not one of %q, %q, %q", cfg.Dmut.Mode, DmutModeDefault, DmutModeSimple, DmutModeFull))
	}

	def := DefaultBlacklist()
	cfg.Blacklist.Functions = readBlacklist(root, "blacklist.functions", def.Functions)
	cfg.Blacklist.Relations = readBlacklist(root, "blacklist.relations", def.Relations)

	// Before the k.Set block below, which writes pg.* keys the user may never have set.
	warnUnusedKeys(k, tracker)

	// reload.cmd interpolation needs these even when only defaulted or derived.
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

// readStringMap reads path's immediate children as a flat map ; an absent or non-object path yields an empty map.
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

// readTypeDecls reads typescript.types.<schema>.<relation>.<column> : nested, or one quoted key holding dots
// ("public.doc.body"), split the same way. A value is a string (the type) or an object of comment, type and from.
func readTypeDecls(root *ConfigReader, path string, errs *[]error) map[string]TypeDecl {
	out := map[string]TypeDecl{}
	var walk func(r *ConfigReader, segments []string)
	walk = func(r *ConfigReader, segments []string) {
		full := path + "." + strings.Join(segments, ".")
		if len(segments) == 3 {
			if !r.isObject() {
				if s, err := r.GetString(""); err == nil {
					out[strings.Join(segments, ".")] = TypeDecl{Type: s}
				} else {
					r.recordMalformed(err)
				}
				return
			}
			var d TypeDecl
			fields, _ := r.GetIterator("")
			for field, child := range fields {
				s, err := child.GetString("")
				if err != nil {
					*errs = append(*errs, oops.With("key", full).Errorf("config: %q: %q must be a string", full, field))
					continue
				}
				switch field {
				case "comment":
					d.Comment = s
				case "type":
					d.Type = s
				case "from":
					d.From = s
				default:
					*errs = append(*errs, oops.With("key", full).Errorf("config: %q: unknown field %q (known: comment, type, from)", full, field))
				}
			}
			out[strings.Join(segments, ".")] = d
			return
		}
		if len(segments) > 3 {
			*errs = append(*errs, oops.With("key", full).Errorf("config: %q: a %s key is schema.relation.column", full, path))
			return
		}
		if !r.isObject() {
			*errs = append(*errs, oops.With("key", full).Errorf("config: %q: a %s key is schema.relation.column", full, path))
			return
		}
		it, _ := r.GetIterator("")
		for key, child := range it {
			walk(child, append(append([]string{}, segments...), strings.Split(key, ".")...))
		}
	}
	if r, err := root.GetObject(path); err == nil {
		walk(r, nil)
	} else if !errors.Is(err, errNotFound) {
		*errs = append(*errs, err)
	}
	return out
}

// readRoutes reads route.<schema>.<function>.{path,method}.
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

// readOpenidProviders reads openid.<name>.*. client_id/client_secret default to a "$FILE$..." path with <name>
// substituted, resolved here since resolveFileIndirection only sees present keys. Plain GetX, not *OrDefault :
// every *OrDefault key must have an Options row, and these are per-name fields.
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

// resolveOpenidSecretField reads openid.<name>.<field>, defaulting to defaultPathTemplate with <name> substituted,
// and resolves it when shaped like $FILE$.
func resolveOpenidSecretField(providerReader *ConfigReader, field, name, defaultPathTemplate string, errs *[]error) string {
	def := strings.ReplaceAll(defaultPathTemplate, "<name>", name)
	v := providerReader.GetStringOrDefault(field, def)
	if !strings.HasPrefix(v, "$FILE$") {
		return v
	}
	resolved, ferr := resolveFileValue(v)
	if ferr != nil {
		*errs = append(*errs, oops.With("key", "openid."+name+"."+field).Wrapf(ferr, "config: openid.%s.%s", name, field))
		return ""
	}
	return resolved
}

// readSamlProviders reads saml.<name>.* ; the shared saml.certificate_path/private_key_path are skipped, being
// scalars.
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

// readBlacklist reads blacklist.functions/relations.<schema>.<name> on top of def ; config only adds to the
// defaults.
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
