// Unused-key detection, over the tree merged from every source.
package config

import (
	"log/slog"
	"sort"

	koanf "github.com/knadh/koanf/v2"
)

// warnUnusedKeys logs a warning per dotted key in k that nothing read, usually a typo or a renamed setting. Not an
// error : a shared .env may carry extra REL_ vars. Must run before assemble writes pg.host/pg.port/... back onto k,
// or those show up as unused.
func warnUnusedKeys(k *koanf.Koanf, tracker *keyTracker) {
	if tracker == nil {
		return
	}
	all := k.All()
	unused := make([]string, 0, len(all))
	for key := range all {
		if _, ok := tracker.found[key]; !ok {
			unused = append(unused, key)
		}
	}
	if len(unused) == 0 {
		return
	}
	sort.Strings(unused)

	for _, key := range unused {
		args := []any{"key", key, "env", EnvVar(key)}
		if suggestion, ok := closestKey(key, tracker.attempted); ok {
			args = append(args, "did_you_mean", suggestion, "did_you_mean_env", EnvVar(suggestion))
		}
		slog.Default().Warn("config: key set but never read by rel — check for a typo or a stale/renamed setting", args...)
	}
}

// closestKey returns the candidate closest to key by edit distance ; ok is false when none is within a quarter of
// key's length (minimum 3), since a wrong suggestion misleads more than none. Ties resolve in sorted order.
func closestKey(key string, candidates map[string]struct{}) (string, bool) {
	names := make([]string, 0, len(candidates))
	for c := range candidates {
		names = append(names, c)
	}
	sort.Strings(names)

	threshold := len(key) / 4
	if threshold < 3 {
		threshold = 3
	}

	best, bestDist := "", threshold+1
	for _, c := range names {
		if c == key {
			continue
		}
		if d := levenshtein(key, c); d < bestDist {
			best, bestDist = c, d
		}
	}
	if best == "" || bestDist > threshold {
		return "", false
	}
	return best, true
}

// levenshtein is the rune-aware edit distance between a and b.
func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			cur[j] = m
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}
