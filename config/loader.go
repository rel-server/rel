// Package config assembles Rel's runtime Config from three sources — a
// config file, REL_ environment variables, and CLI flags — per
// specs/01-configuration.md. Load is the entry point ; everything else in
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
func resolveFileValue(raw string) (string, error) {
	rest := strings.TrimPrefix(raw, "$FILE$")

	if idx := strings.Index(rest, "$DEFAULT$"); idx >= 0 {
		filePath, fallback := rest[:idx], rest[idx+len("$DEFAULT$"):]
		data, err := os.ReadFile(filePath)
		if err != nil {
			return fallback, nil
		}
		return trimOneNewline(data), nil
	}

	if idx := strings.Index(rest, "$GEN$"); idx >= 0 {
		filePath, lenStr := rest[:idx], rest[idx+len("$GEN$"):]
		data, err := os.ReadFile(filePath)
		if err == nil {
			return trimOneNewline(data), nil
		}
		n, cerr := parseGenLength(lenStr)
		if cerr != nil {
			return "", cerr
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

	data, err := os.ReadFile(rest)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", rest, err)
	}
	return trimOneNewline(data), nil
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

func parseGenLength(s string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n <= 0 {
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
			return fmt.Errorf("config: %s: array values are not allowed (see specs/01-configuration.md ## No arrays)", key)
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

	cfg.Pg.Querier.User = root.GetStringOrDefault("pg.querier.user", "")
	cfg.Pg.Querier.Password = root.GetStringOrDefault("pg.querier.password", "")
	cfg.Pg.Admin.User = root.GetStringOrDefault("pg.admin.user", "")
	cfg.Pg.Admin.Password = root.GetStringOrDefault("pg.admin.password", "")
	cfg.Pg.Anonymous = root.GetStringOrDefault("pg.anonymous", "")
	cfg.Pg.Host = root.GetStringOrDefault("pg.host", "localhost")
	cfg.Pg.Port = root.GetIntOrDefault("pg.port", 5432)
	cfg.Pg.Database = root.GetStringOrDefault("pg.database", "")

	cfg.Query.MaxDepth = root.GetIntOrDefault("query.maxdepth", DefaultMaxDepth)
	cfg.Query.WellKnownDirs = root.GetStringOrDefault("query.wellknowndirs", "")

	cfg.Logging.Handler = root.GetStringOrDefault("logging.handler", DefaultLoggingHandler)
	cfg.Logging.Level = root.GetStringOrDefault("logging.level", DefaultLoggingLevel)
	cfg.Logging.Filter = readStringMap(root, "logging.filter")
	cfg.Logging.Exclude = readStringMap(root, "logging.exclude")

	cfg.Http.Host = root.GetStringOrDefault("http.host", "")
	cfg.Http.Port = root.GetIntOrDefault("http.port", DefaultHttpPort)

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
