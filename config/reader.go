package config

import (
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"strconv"
	"strings"

	koanf "github.com/knadh/koanf/v2"
)

// ConfigReader is specs/configuration.md ## Accessing configuration's
// typed accessor over the merged koanf tree. k is always the SAME shared,
// whole-tree *koanf.Koanf across every ConfigReader derived from one root —
// path is this reader's own dotted scope prefix ("" at the root), prepended
// to every sub-path an accessor is called with. Addressing by full dotted
// path against one shared tree (rather than physically scoping via
// koanf.Cut) is deliberate : Cut only works when the target is itself an
// object, which breaks for a scalar leaf (e.g. logging.filter.<key>, whose
// value is a plain string) — GetIterator needs to yield readers over BOTH
// object and scalar children uniformly.
//
// errs, when non-nil, is a pointer to a slice shared by every ConfigReader
// derived from one root (see assemble in loader.go) : every *OrDefault
// accessor appends to it when the path was PRESENT but malformed (never for
// a merely absent path, the expected/common case an *OrDefault call
// tolerates by design). This is how a wrong-type value survives past a
// call site that only wanted a default to still make Load fail overall,
// per ## The assembled Config object : "every error... is collected rather
// than raised immediately... If any errors were collected, Rel logs all of
// them together and exits."
type ConfigReader struct {
	k    *koanf.Koanf
	path string
	errs *[]error
}

// newReader wraps k, scoped to path ("" for the root reader), sharing errs
// with every reader derived from it.
func newReader(k *koanf.Koanf, path string, errs *[]error) *ConfigReader {
	return &ConfigReader{k: k, path: path, errs: errs}
}

// join builds the full dotted path for a sub-lookup : r's own scope prefix
// plus the sub-path being looked up.
func (r *ConfigReader) join(sub string) string {
	if r.path == "" {
		return sub
	}
	if sub == "" {
		return r.path
	}
	return r.path + "." + sub
}

// recordMalformed appends err to the shared errs slice (ConfigReader's doc
// comment) ; called whenever GetX fails for a reason other than "absent".
func (r *ConfigReader) recordMalformed(err error) {
	if r.errs != nil {
		*r.errs = append(*r.errs, err)
	}
}

// errNotFound is the sentinel a merely-absent key's error wraps — silent
// (use the default), unlike logErr's malformed-value case (logged, recorded).
var errNotFound = errors.New("not found")

// logErr logs path only, never the value (## Error handling and secrets).
// slog.Default(), not logging.For(...) : logging imports config, so importing back would cycle.
func logErr(path string, err error) error {
	slog.Default().Error("config: retrieval error", "path", path, "error", err.Error())
	return err
}

// notFoundErr is a plain "key absent" miss — silent, unlike logErr's
// logged malformed-value case ; errNotFound is what *OrDefault checks for.
func notFoundErr(path string) error {
	return fmt.Errorf("config: %q: %w", path, errNotFound)
}

// GetObject scopes to path, returning a ConfigReader over just that
// sub-tree. An error, not an empty result, if path doesn't exist or isn't an
// object.
func (r *ConfigReader) GetObject(path string) (*ConfigReader, error) {
	full := r.join(path)
	v := r.k.Get(full)
	if v == nil {
		return nil, notFoundErr(full)
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, logErr(full, fmt.Errorf("config: %q: not an object", full))
	}
	return newReader(r.k, full, r.errs), nil
}

// GetObjectOrDefault returns def() instead of an error on a missing/bad
// path — a bad (wrong-type) path is still logged via GetObject/logErr AND
// recorded as a real error (see ConfigReader's doc comment) ; a merely
// absent one is neither.
func (r *ConfigReader) GetObjectOrDefault(path string, def func() *ConfigReader) *ConfigReader {
	obj, err := r.GetObject(path)
	if err != nil {
		if !errors.Is(err, errNotFound) {
			r.recordMalformed(err)
		}
		return def()
	}
	return obj
}

// GetIterator scopes to path like GetObject, then yields its immediate
// children as (key, *ConfigReader) pairs in alphabetical order (spec :
// "GetIterator yields entries in a deterministic (alphabetically sorted)
// order"). A child may itself be an object or a scalar — GetIterator makes
// no assumption either way, unlike GetObject ; the caller's own accessor
// call on the yielded reader (GetString("") for a scalar, another
// GetIterator for a nested object, ...) determines that.
func (r *ConfigReader) GetIterator(path string) (iter.Seq2[string, *ConfigReader], error) {
	full := r.join(path)
	v := r.k.Get(full)
	if v == nil {
		return nil, notFoundErr(full)
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, logErr(full, fmt.Errorf("config: %q: not an object", full))
	}
	keys := r.k.MapKeys(full) // already alphabetically sorted
	errs := r.errs
	return func(yield func(string, *ConfigReader) bool) {
		for _, key := range keys {
			if !yield(key, newReader(r.k, full+"."+key, errs)) {
				return
			}
		}
	}, nil
}

// GetString reads path (relative to r's own scope — "" reads r's own scope
// itself, used to read a scalar leaf reached via GetIterator) as a string.
// Only a native string value is valid.
func (r *ConfigReader) GetString(path string) (string, error) {
	full := r.join(path)
	v := r.k.Get(full)
	if v == nil {
		return "", notFoundErr(full)
	}
	s, ok := v.(string)
	if !ok {
		return "", logErr(full, fmt.Errorf("config: %q: not a string", full))
	}
	return s, nil
}

// GetStringOrDefault returns def instead of an error on a missing/bad path
// — see GetObjectOrDefault's doc comment for the absent-vs-malformed
// distinction this (and every other *OrDefault accessor below) applies.
func (r *ConfigReader) GetStringOrDefault(path string, def string) string {
	s, err := r.GetString(path)
	if err != nil {
		if !errors.Is(err, errNotFound) {
			r.recordMalformed(err)
		}
		return def
	}
	return s
}

// GetInt reads path as an int : a native numeric value (TOML/YAML-sourced,
// koanf.Get preserves the original int64/float64), or a string value (the
// only type env vars/flags can produce) that parses cleanly via strconv —
// this is the one deliberate reconciliation of "never silently coerced"
// with "env vars are inherently strings" ; anything else (bool, object,
// slice, or a string that doesn't parse) is an error.
func (r *ConfigReader) GetInt(path string) (int, error) {
	full := r.join(path)
	v := r.k.Get(full)
	if v == nil {
		return 0, notFoundErr(full)
	}
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		if n != float64(int(n)) {
			return 0, logErr(full, fmt.Errorf("config: %q: not an integer", full))
		}
		return int(n), nil
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return 0, logErr(full, fmt.Errorf("config: %q: not a valid int", full))
		}
		return i, nil
	default:
		return 0, logErr(full, fmt.Errorf("config: %q: not an int", full))
	}
}

// GetIntOrDefault returns def instead of an error on a missing/bad path.
func (r *ConfigReader) GetIntOrDefault(path string, def int) int {
	i, err := r.GetInt(path)
	if err != nil {
		if !errors.Is(err, errNotFound) {
			r.recordMalformed(err)
		}
		return def
	}
	return i
}

// GetFloat64 reads path as a float64 : a native numeric value, or a string
// (env vars/flags) that parses cleanly via strconv.ParseFloat — same
// native-or-parseable-string rule as GetInt.
func (r *ConfigReader) GetFloat64(path string) (float64, error) {
	full := r.join(path)
	v := r.k.Get(full)
	if v == nil {
		return 0, notFoundErr(full)
	}
	switch n := v.(type) {
	case float64:
		return n, nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0, logErr(full, fmt.Errorf("config: %q: not a valid float", full))
		}
		return f, nil
	default:
		return 0, logErr(full, fmt.Errorf("config: %q: not a float", full))
	}
}

// GetFloat64OrDefault returns def instead of an error on a missing/bad path.
func (r *ConfigReader) GetFloat64OrDefault(path string, def float64) float64 {
	f, err := r.GetFloat64(path)
	if err != nil {
		if !errors.Is(err, errNotFound) {
			r.recordMalformed(err)
		}
		return def
	}
	return f
}

// GetBool reads path as a bool : a native bool, or a string (env vars/
// flags) accepted by strconv.ParseBool. Note this is a stricter set than
// config.IsTruthy's "y"/"yes"/"true"/"1" convention used elsewhere for the
// Blacklist's own raw-string values — GetBool's contract is "this really is
// a bool", not "does this look truthy".
func (r *ConfigReader) GetBool(path string) (bool, error) {
	full := r.join(path)
	v := r.k.Get(full)
	if v == nil {
		return false, notFoundErr(full)
	}
	switch b := v.(type) {
	case bool:
		return b, nil
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(b))
		if err != nil {
			return false, logErr(full, fmt.Errorf("config: %q: not a valid bool", full))
		}
		return parsed, nil
	default:
		return false, logErr(full, fmt.Errorf("config: %q: not a bool", full))
	}
}

// GetBoolOrDefault returns def instead of an error on a missing/bad path.
func (r *ConfigReader) GetBoolOrDefault(path string, def bool) bool {
	b, err := r.GetBool(path)
	if err != nil {
		if !errors.Is(err, errNotFound) {
			r.recordMalformed(err)
		}
		return def
	}
	return b
}

// GetStrings reads path as a comma-separated string, trimmed and filtered
// of empty entries — configuration.md ## No arrays' convention for "a
// list of plain, unnamed scalars".
func (r *ConfigReader) GetStrings(path string) ([]string, error) {
	s, err := r.GetString(path)
	if err != nil {
		return nil, err
	}
	return splitStrings(s), nil
}

// GetStringsOrDefault returns def instead of an error on a missing/bad path.
func (r *ConfigReader) GetStringsOrDefault(path string, def []string) []string {
	ss, err := r.GetStrings(path)
	if err != nil {
		if !errors.Is(err, errNotFound) {
			r.recordMalformed(err)
		}
		return def
	}
	return ss
}

// splitStrings implements ## No arrays' comma-separated convention : split,
// trim, drop empties.
func splitStrings(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}
