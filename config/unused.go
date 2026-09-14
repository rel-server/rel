// Unused-key detection : specs/configuration.md's "no configuration-
// looking value is silently ignored" rule, regardless of which of the
// three sources (config file, REL_ environment variable, CLI flag) it came
// from — they're all merged into one koanf tree before assemble ever runs,
// so one pass over that tree, diffed against keyTracker's own bookkeeping
// (reader.go), covers all three uniformly.
package config

import (
	"log/slog"
	"sort"

	koanf "github.com/knadh/koanf/v2"
)

// warnUnusedKeys logs one "warn" line per dotted key present in k that
// nothing in assemble ever read — almost always a typo or a stale/renamed
// setting. Non-fatal, and not collected into assemble's own errs : a
// deployment that intentionally carries extra REL_ vars (a shared .env
// across several services, say) shouldn't be forced to fail startup over
// it, only told about it. Must run before assemble's own trailing
// k.Set("pg.uri", ...)/... block (Config.Raw's doc comment) — those write
// pg.host/pg.port/... back onto k unconditionally, even when the user
// never set them, which would otherwise show up here as false positives.
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

// closestKey returns the candidate (from candidates — keyTracker.attempted,
// every dotted key assemble actually tried to read this run, present or
// not) with the smallest Levenshtein distance to key ; ok is false if
// candidates is empty or nothing is close enough to be worth suggesting.
// The threshold (a quarter of key's own length, minimum 3) is deliberately
// loose — a missed suggestion costs nothing, but a wildly-off one actively
// misleads, so this is biased toward silence over noise. Candidates are
// walked in sorted order so a tie always resolves to the same suggestion.
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

// levenshtein is the classic single-row dynamic-programming edit distance
// (insert/delete/substitute, each cost 1) between two strings, rune-aware
// (dotted config keys are ASCII in practice, but this makes no assumption
// beyond that).
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
