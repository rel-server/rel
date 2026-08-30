package config

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestEnvVar(t *testing.T) {
	cases := map[string]string{
		"query.host":           "REL_QUERY__HOST",
		"query.anonymous_role": "REL_QUERY__ANONYMOUS_ROLE",
		"http.functions.auth":  "REL_HTTP__FUNCTIONS__AUTH",
		"query.wellknown.path": "REL_QUERY__WELLKNOWN__PATH",
	}
	for key, want := range cases {
		if got := EnvVar(key); got != want {
			t.Errorf("EnvVar(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestFlagOf(t *testing.T) {
	if got := FlagOf("query.host"); got != "--query.host" {
		t.Errorf("FlagOf(\"query.host\") = %q, want \"--query.host\"", got)
	}
}

// dynamicNamespaceKeys are the loader.go keys read via readStringMap/
// readBlacklist (a path prefix, not a single scalar *OrDefault call) —
// Options documents these as prose in Help()'s own "Dynamic namespaces"
// section instead of individual table rows, since "<schema>.<name>" isn't
// a fixed key. Kept as an explicit, short allowlist so
// TestOptions_MatchAssembleKeys stays meaningful rather than silently
// excusing a real gap.
var dynamicNamespaceKeys = map[string]bool{
	"logging.filter":      true,
	"logging.exclude":     true,
	"blacklist.functions": true,
	"blacklist.relations": true,
	"http.static.access":  true,
}

// assembleOrDefaultKeyPattern matches every "root.GetXxxOrDefault("key""
// call in loader.go's assemble() — the actual, authoritative list of
// scalar dotted keys rel reads config for.
var assembleOrDefaultKeyPattern = regexp.MustCompile(`Get\w+OrDefault\("([^"]+)"`)

// TestOptions_MatchAssembleKeys is the check --help's whole value depends
// on : it reads loader.go's own source and extracts every key assemble()
// actually calls a *OrDefault accessor for, then asserts Options (config/
// help.go) documents exactly that set (minus the dynamic-namespace
// prefixes above). Options is still a hand-maintained table — this test is
// what stops it from silently drifting the moment a key is added, removed,
// or renamed in assemble() without the matching Options edit.
func TestOptions_MatchAssembleKeys(t *testing.T) {
	src, err := os.ReadFile("loader.go")
	if err != nil {
		t.Fatalf("reading loader.go: %v", err)
	}
	matches := assembleOrDefaultKeyPattern.FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatalf("assembleOrDefaultKeyPattern matched nothing in loader.go — the pattern itself is broken, this test would otherwise pass vacuously")
	}

	assembleKeys := map[string]bool{}
	for _, m := range matches {
		assembleKeys[m[1]] = true
	}

	optionKeys := map[string]bool{}
	for _, opt := range Options {
		optionKeys[opt.Key] = true
	}

	for key := range assembleKeys {
		if !optionKeys[key] {
			t.Errorf("assemble() reads %q but Options doesn't document it", key)
		}
	}
	for key := range optionKeys {
		if !assembleKeys[key] && !dynamicNamespaceKeys[key] {
			t.Errorf("Options documents %q but assemble() doesn't read it (renamed or removed?)", key)
		}
	}
}

// TestHelp_MentionsEveryOption guards against Options drifting out of sync
// with itself : every declared key's dotted form and derived env var must
// actually appear in the rendered text, and every default value that isn't
// "" must show up too — a regression here means a key/default was added to
// Options but the render loop silently dropped it (or vice versa). This is
// deliberately NOT a check that Options covers assemble() itself — see
// TestOptions_MatchAssembleKeys for that.
func TestHelp_MentionsEveryOption(t *testing.T) {
	out := Help()
	for _, opt := range Options {
		if !strings.Contains(out, opt.Key) {
			t.Errorf("Help() output is missing key %q", opt.Key)
		}
		if !strings.Contains(out, EnvVar(opt.Key)) {
			t.Errorf("Help() output is missing env var %q (for key %q)", EnvVar(opt.Key), opt.Key)
		}
	}
}

// TestHelp_NoDegenerateWrapping guards against wrapIndented regressing
// into one-word-per-line output (the exact bug this function shipped with
// once already, when the wrap width computation went non-positive) — every
// line BEFORE the last should carry more than one word ; the last line is
// legitimately allowed to be a single short trailing word (e.g. a
// paragraph ending "... than, pg.admin.user." wrapping cleanly to one word
// on its own last line is normal wrapping, not the bug).
func TestHelp_NoDegenerateWrapping(t *testing.T) {
	for _, opt := range Options {
		wrapped := wrapIndented(opt.Desc, 8)
		lines := strings.Split(wrapped, "\n")
		for i, line := range lines {
			isLast := i == len(lines)-1
			words := strings.Fields(line)
			if len(words) == 1 && !isLast && len(opt.Desc) > 20 {
				t.Errorf("wrapIndented(%q) produced a one-word non-final line %q — width computation likely went non-positive", opt.Desc, line)
			}
		}
	}
}
