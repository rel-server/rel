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

// dynamicNamespaceKeys are loader.go's path-prefix keys (readStringMap/
// readBlacklist) — Help() documents these as prose, not table rows.
var dynamicNamespaceKeys = map[string]bool{
	"logging.filter":      true,
	"logging.exclude":     true,
	"blacklist.functions": true,
	"blacklist.relations": true,
	"http.static.access":  true,
}

// assembleOrDefaultKeyPattern matches every "root.GetXxxOrDefault("key""
// call in loader.go's assemble() — the authoritative list of scalar keys.
var assembleOrDefaultKeyPattern = regexp.MustCompile(`Get\w+OrDefault\("([^"]+)"`)

// Guards Options (config/help.go, hand-maintained) against drifting from
// assemble()'s actual key set as keys are added/removed/renamed.
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

// Guards against Help()'s render loop silently dropping a key/env-var pair
// that Options still declares — not a check against assemble() itself.
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

// Guards against wrapIndented regressing to one-word-per-line (a real past
// bug) — every line but the last must carry more than one word.
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
