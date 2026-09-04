package websec

import (
	"strings"
	"testing"

	"github.com/ceymard/rel/config"
)

func TestPolicy_DefaultSynthesizesScriptAndStyleSrc(t *testing.T) {
	csp := config.HttpCsp{DefaultSrc: "'self'"}
	got := Policy(csp, "", "abc123")
	if !strings.Contains(got, "default-src 'self'") {
		t.Errorf("expected default-src 'self' in %q", got)
	}
	if !strings.Contains(got, "script-src 'self' 'nonce-abc123'") {
		t.Errorf("expected synthesized script-src with nonce in %q", got)
	}
	if !strings.Contains(got, "style-src 'self' 'nonce-abc123'") {
		t.Errorf("expected synthesized style-src with nonce in %q", got)
	}
}

func TestPolicy_ExistingScriptSrcGetsNonceAppended(t *testing.T) {
	csp := config.HttpCsp{DefaultSrc: "'self'", ScriptSrc: "'self' https://cdn.example.com"}
	got := Policy(csp, "", "xyz")
	if !strings.Contains(got, "script-src 'self' https://cdn.example.com 'nonce-xyz'") {
		t.Errorf("expected nonce appended to existing script-src, got %q", got)
	}
}

// TestInjectNonce_NoDefaultSrcSkipsSynthesis : with neither script-src/
// style-src nor default-src present, injection must be skipped entirely.
func TestInjectNonce_NoDefaultSrcSkipsSynthesis(t *testing.T) {
	directives := ParsePolicy("frame-ancestors 'none'")
	got := SerializePolicy(InjectNonce(directives, "abc123"))
	if strings.Contains(got, "script-src") || strings.Contains(got, "style-src") {
		t.Errorf("expected no script-src/style-src synthesized without a default-src to base it on, got %q", got)
	}
	if !strings.Contains(got, "frame-ancestors 'none'") {
		t.Errorf("expected the existing directive to survive untouched, got %q", got)
	}
}

// TestInjectNonce_DefaultSrcPresentStillSynthesizes : when default-src IS
// present, synthesis still happens as before.
func TestInjectNonce_DefaultSrcPresentStillSynthesizes(t *testing.T) {
	directives := ParsePolicy("default-src 'self'; frame-ancestors 'none'")
	got := SerializePolicy(InjectNonce(directives, "abc123"))
	if !strings.Contains(got, "script-src 'self' 'nonce-abc123'") {
		t.Errorf("expected script-src synthesized from default-src, got %q", got)
	}
	if !strings.Contains(got, "style-src 'self' 'nonce-abc123'") {
		t.Errorf("expected style-src synthesized from default-src, got %q", got)
	}
}

func TestPolicy_RawPolicyOverrideReplacesDirectivesEntirely(t *testing.T) {
	csp := config.HttpCsp{DefaultSrc: "'self'", ScriptSrc: "should-be-ignored"}
	got := Policy(csp, "", "n1")
	_ = got

	csp2 := config.HttpCsp{Policy: "default-src 'none'; connect-src 'self'"}
	got2 := Policy(csp2, "", "n2")
	if strings.Contains(got2, "should-be-ignored") {
		t.Errorf("http.csp.policy should fully replace individual directives: %q", got2)
	}
	if !strings.Contains(got2, "default-src 'none'") || !strings.Contains(got2, "connect-src 'self'") {
		t.Errorf("expected raw policy segments preserved: %q", got2)
	}
	// No script-src/style-src in the raw policy -> synthesized from
	// default-src's own effective value, same rule as the structured case.
	if !strings.Contains(got2, "script-src 'none' 'nonce-n2'") {
		t.Errorf("expected script-src synthesized from raw policy's default-src: %q", got2)
	}
}

func TestPolicy_PerResponseOverrideTakesPrecedenceOverConfigPolicy(t *testing.T) {
	csp := config.HttpCsp{Policy: "default-src 'self'"}
	got := Policy(csp, "default-src 'none'", "n3")
	if strings.Contains(got, "'self'") {
		t.Errorf("expected per-response override to win over http.csp.policy: %q", got)
	}
	if !strings.Contains(got, "default-src 'none'") {
		t.Errorf("expected override's own directives present: %q", got)
	}
}

func TestPolicy_WholeTokenMatchNotSubstring(t *testing.T) {
	// script-src-elem must not match a bare "script-src" search, nor block
	// a separate script-src from being synthesized.
	csp := config.HttpCsp{Policy: "default-src 'self'; script-src-elem 'self'"}
	got := Policy(csp, "", "n4")
	if !strings.Contains(got, "script-src-elem 'self'") {
		t.Errorf("expected script-src-elem untouched: %q", got)
	}
	if !strings.Contains(got, "script-src 'self' 'nonce-n4'") {
		t.Errorf("expected a genuinely separate script-src synthesized: %q", got)
	}
}

func TestParsePolicy_RoundTrip(t *testing.T) {
	raw := "default-src 'self'; script-src 'self' 'unsafe-inline'"
	directives := ParsePolicy(raw)
	if len(directives) != 2 {
		t.Fatalf("expected 2 directives, got %d: %+v", len(directives), directives)
	}
	if directives[0].Name != "default-src" || directives[0].Value != "'self'" {
		t.Errorf("unexpected first directive: %+v", directives[0])
	}
	if directives[1].Name != "script-src" || directives[1].Value != "'self' 'unsafe-inline'" {
		t.Errorf("unexpected second directive: %+v", directives[1])
	}
	if got := SerializePolicy(directives); got != raw {
		t.Errorf("round-trip mismatch: got %q want %q", got, raw)
	}
}
