package config

import (
	"errors"
	"iter"
	"log/slog"
	"strconv"
	"strings"

	koanf "github.com/knadh/koanf/v2"
	"github.com/samber/oops"
)

// ConfigReader is the typed accessor over the merged koanf tree, scoped to path. Every reader derived from one root
// shares the tree, errs and tracker, and addresses by full dotted path so that a scalar child can be read like an
// object one.
//
// errs, when non-nil, collects every present-but-malformed value an *OrDefault accessor met, so Load still fails on
// it ; an absent path is never an error.
type ConfigReader struct {
	k       *koanf.Koanf
	path    string
	errs    *[]error
	tracker *keyTracker
}

// keyTracker records, across every reader derived from one root, each dotted path an accessor tried (attempted)
// and each it found a value for (found). Keys in the merged tree but not in found are reported as unused, with
// attempted as the "did you mean" pool.
type keyTracker struct {
	attempted map[string]struct{}
	found     map[string]struct{}
}

func newKeyTracker() *keyTracker {
	return &keyTracker{attempted: map[string]struct{}{}, found: map[string]struct{}{}}
}

// touch records that full was consulted, and whether a value existed there. A nil tracker is a no-op.
func (t *keyTracker) touch(full string, present bool) {
	if t == nil {
		return
	}
	t.attempted[full] = struct{}{}
	if present {
		t.found[full] = struct{}{}
	}
}

// newReader wraps k, scoped to path ("" for the root), sharing errs and tracker with every reader derived from it.
func newReader(k *koanf.Koanf, path string, errs *[]error, tracker *keyTracker) *ConfigReader {
	return &ConfigReader{k: k, path: path, errs: errs, tracker: tracker}
}

func (r *ConfigReader) join(sub string) string {
	if r.path == "" {
		return sub
	}
	if sub == "" {
		return r.path
	}
	return r.path + "." + sub
}

// recordMalformed appends err to the shared errs ; called whenever GetX fails for a reason other than absence.
func (r *ConfigReader) recordMalformed(err error) {
	if r.errs != nil {
		*r.errs = append(*r.errs, err)
	}
}

// errNotFound is wrapped by an absent key's error : the default applies silently.
var errNotFound = errors.New("not found")

// logErr logs path only, never the value. slog.Default() since logging imports config.
func logErr(path string, err error) error {
	slog.Default().Error("config: retrieval error", "path", path, "error", err.Error())
	return err
}

func notFoundErr(path string) error {
	return oops.With("key", path).Wrapf(errNotFound, "config: %q", path)
}

// Exists reports whether path is present, distinguishing "absent" from "set to the default" for precedence between
// two keys.
func (r *ConfigReader) Exists(path string) bool {
	full := r.join(path)
	ok := r.k.Exists(full)
	r.tracker.touch(full, ok)
	return ok
}

// isObject reports whether r's own scope holds an object, without logging or recording anything.
func (r *ConfigReader) isObject() bool {
	_, ok := r.k.Get(r.path).(map[string]any)
	return ok
}

// GetObject scopes to path ; an error if path is absent or not an object.
func (r *ConfigReader) GetObject(path string) (*ConfigReader, error) {
	full := r.join(path)
	v := r.k.Get(full)
	r.tracker.touch(full, v != nil)
	if v == nil {
		return nil, notFoundErr(full)
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, logErr(full, oops.With("key", full).Errorf("config: %q: not an object", full))
	}
	return newReader(r.k, full, r.errs, r.tracker), nil
}

// GetObjectOrDefault returns def() on a missing or bad path ; a bad path is still logged and recorded in errs, an
// absent one is not.
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

// GetIterator yields path's immediate children as readers, in alphabetical order. A child may be an object or a
// scalar ; the caller's accessor decides.
func (r *ConfigReader) GetIterator(path string) (iter.Seq2[string, *ConfigReader], error) {
	full := r.join(path)
	v := r.k.Get(full)
	r.tracker.touch(full, v != nil)
	if v == nil {
		return nil, notFoundErr(full)
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, logErr(full, oops.With("key", full).Errorf("config: %q: not an object", full))
	}
	keys := r.k.MapKeys(full) // already alphabetically sorted
	errs := r.errs
	tracker := r.tracker
	return func(yield func(string, *ConfigReader) bool) {
		for _, key := range keys {
			if !yield(key, newReader(r.k, full+"."+key, errs, tracker)) {
				return
			}
		}
	}, nil
}

// GetString reads path as a string ("" reads r's own scope, for a scalar reached through GetIterator). Only a native
// string is valid.
func (r *ConfigReader) GetString(path string) (string, error) {
	full := r.join(path)
	v := r.k.Get(full)
	r.tracker.touch(full, v != nil)
	if v == nil {
		return "", notFoundErr(full)
	}
	s, ok := v.(string)
	if !ok {
		return "", logErr(full, oops.With("key", full).Errorf("config: %q: not a string", full))
	}
	return s, nil
}

// GetStringOrDefault returns def on a missing or bad path, like GetObjectOrDefault (and every *OrDefault below).
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

// GetInt reads path as an int : a native number, or a string that parses cleanly, since env vars and flags only
// produce strings. Anything else is an error.
func (r *ConfigReader) GetInt(path string) (int, error) {
	full := r.join(path)
	v := r.k.Get(full)
	r.tracker.touch(full, v != nil)
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
			return 0, logErr(full, oops.With("key", full).Errorf("config: %q: not an integer", full))
		}
		return int(n), nil
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return 0, logErr(full, oops.With("key", full).Errorf("config: %q: not a valid int", full))
		}
		return i, nil
	default:
		return 0, logErr(full, oops.With("key", full).Errorf("config: %q: not an int", full))
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

// GetFloat64 reads path as a float64, with the same native-or-parseable-string rule as GetInt.
func (r *ConfigReader) GetFloat64(path string) (float64, error) {
	full := r.join(path)
	v := r.k.Get(full)
	r.tracker.touch(full, v != nil)
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
			return 0, logErr(full, oops.With("key", full).Errorf("config: %q: not a valid float", full))
		}
		return f, nil
	default:
		return 0, logErr(full, oops.With("key", full).Errorf("config: %q: not a float", full))
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

// GetBool reads path as a bool : a native bool, or a string strconv.ParseBool accepts. Stricter than IsTruthy on
// purpose : the value must really be a bool.
func (r *ConfigReader) GetBool(path string) (bool, error) {
	full := r.join(path)
	v := r.k.Get(full)
	r.tracker.touch(full, v != nil)
	if v == nil {
		return false, notFoundErr(full)
	}
	switch b := v.(type) {
	case bool:
		return b, nil
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(b))
		if err != nil {
			return false, logErr(full, oops.With("key", full).Errorf("config: %q: not a valid bool", full))
		}
		return parsed, nil
	default:
		return false, logErr(full, oops.With("key", full).Errorf("config: %q: not a bool", full))
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

// GetStrings reads path as a comma-separated string, trimmed, empty entries dropped.
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
