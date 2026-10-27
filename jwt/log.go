package jwt

import (
	"log/slog"
	"maps"
	"math"
	"slices"
)

// LogValue renders claims as a slog group, for log lines. A plain map would
// go through %v, which prints the float64 claims (all JSON numbers decode to
// float64) as 1.793112102e+09 ; integral values are logged as int64 instead.
func LogValue(c Claims) slog.Value {
	if len(c) == 0 {
		return slog.AnyValue(map[string]any{}) // an empty group would drop the key from the line
	}
	attrs := make([]slog.Attr, 0, len(c))
	for _, k := range slices.Sorted(maps.Keys(c)) {
		v := c[k]
		if f, ok := v.(float64); ok && f == math.Trunc(f) && math.Abs(f) < 1<<53 {
			v = int64(f)
		}
		attrs = append(attrs, slog.Any(k, v))
	}
	return slog.GroupValue(attrs...)
}
