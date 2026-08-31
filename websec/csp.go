// This file implements specs/http-content.md ## CSP : assembling the
// Content-Security-Policy header value from either the individual
// http.csp.* directives or a raw http.csp.policy/RelHttpResponse.csp
// override, and injecting the per-request nonce into script-src/style-src
// — synthesizing either directive from the effective default-src value
// when it isn't otherwise present, per ### Nonce's "Synthesis when the
// directive is absent" rule. The exact same injection logic applies
// whether the base policy came from the ten named config directives, a raw
// http.csp.policy, or a route function's own resp.csp — ## CSP's
// ### Per-response override is explicit that it's "the same synthesize-
// if-absent rule from ### Nonce above."
package websec

import (
	"strings"

	"github.com/ceymard/rel/config"
)

// Directive is one CSP directive name/value pair, kept in an ordered slice
// (not a map) so a raw http.csp.policy's own directive ORDER survives
// round-tripping through ParsePolicy/SerializePolicy unchanged.
type Directive struct {
	Name  string
	Value string
}

// cspDirectiveOrder is ## CSP ### Configuration's ten named directives, in
// the order BuildPolicyFromConfig emits them — default_src first (its
// value is what script-src/style-src synthesize from), the rest in the
// spec's own listed order.
var cspDirectiveOrder = []struct {
	name string
	get  func(config.HttpCsp) string
}{
	{"default-src", func(c config.HttpCsp) string { return c.DefaultSrc }},
	{"script-src", func(c config.HttpCsp) string { return c.ScriptSrc }},
	{"style-src", func(c config.HttpCsp) string { return c.StyleSrc }},
	{"img-src", func(c config.HttpCsp) string { return c.ImgSrc }},
	{"font-src", func(c config.HttpCsp) string { return c.FontSrc }},
	{"connect-src", func(c config.HttpCsp) string { return c.ConnectSrc }},
	{"object-src", func(c config.HttpCsp) string { return c.ObjectSrc }},
	{"frame-ancestors", func(c config.HttpCsp) string { return c.FrameAncestors }},
	{"base-uri", func(c config.HttpCsp) string { return c.BaseUri }},
	{"form-action", func(c config.HttpCsp) string { return c.FormAction }},
}

// BuildPolicyFromConfig assembles the directive list from the ten
// individual http.csp.* keys — only directives with a non-empty value are
// emitted ; an unset one simply doesn't appear in the header (CSP's own
// fallback rule then applies it to default-src at the BROWSER level, not
// something rel needs to emulate here).
func BuildPolicyFromConfig(csp config.HttpCsp) []Directive {
	var out []Directive
	for _, d := range cspDirectiveOrder {
		if v := d.get(csp); v != "" {
			out = append(out, Directive{Name: d.name, Value: v})
		}
	}
	return out
}

// ParsePolicy splits a raw, semicolon-separated Content-Security-Policy
// header value into its own Directive list, per ## CSP ### Configuration's
// http.csp.policy rule : "rel splits it on ';' and matches each segment's
// directive NAME as a whole token." Each segment's first whitespace-
// separated token is the name ; the rest (re-joined) is the value.
func ParsePolicy(policy string) []Directive {
	var out []Directive
	for _, seg := range strings.Split(policy, ";") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		fields := strings.Fields(seg)
		name := fields[0]
		value := strings.Join(fields[1:], " ")
		out = append(out, Directive{Name: name, Value: value})
	}
	return out
}

// SerializePolicy re-joins a Directive list into a header value, "name
// value; name value; ...".
func SerializePolicy(directives []Directive) string {
	parts := make([]string, len(directives))
	for i, d := range directives {
		if d.Value == "" {
			parts[i] = d.Name
		} else {
			parts[i] = d.Name + " " + d.Value
		}
	}
	return strings.Join(parts, "; ")
}

// InjectNonce implements ### Nonce's full injection rule : 'nonce-<value>'
// is appended to BOTH script-src and style-src, synthesizing either as a
// copy of the effective default-src value (plus the nonce) when neither
// directive is otherwise present — never a bare "script-src 'nonce-x'" on
// its own, which would silently narrow CSP's own default-src fallback.
// Directives are matched by whole-token NAME (script-src-elem/
// script-src-attr are distinct directives, never touched by this).
func InjectNonce(directives []Directive, nonce string) []Directive {
	nonceToken := "'nonce-" + nonce + "'"
	defaultSrc, haveDefault := "", false
	for _, d := range directives {
		if d.Name == "default-src" {
			defaultSrc, haveDefault = d.Value, true
			break
		}
	}

	out := make([]Directive, len(directives))
	copy(out, directives)

	haveScript, haveStyle := false, false
	for i := range out {
		switch out[i].Name {
		case "script-src":
			haveScript = true
			out[i].Value = appendToken(out[i].Value, nonceToken)
		case "style-src":
			haveStyle = true
			out[i].Value = appendToken(out[i].Value, nonceToken)
		}
	}
	// Synthesizing a nonce-only script-src/style-src when NEITHER that
	// directive NOR default-src is present would narrow the policy rather
	// than merely tighten it : with no default-src at all, the browser's own
	// fallback for an unlisted fetch type is "allowed" — injecting a
	// nonce-only script-src here would make scripts MORE restricted than the
	// base policy ever asked for, exactly the "never nonce-alone" case ###
	// Nonce warns against, just reached from an absent-default-src base
	// policy rather than an absent-script-src one. Skipping injection
	// entirely leaves that fetch type exactly as unrestricted as the base
	// policy already made it — the nonce simply isn't needed there.
	if !haveScript && haveDefault {
		out = append(out, Directive{Name: "script-src", Value: appendToken(defaultSrc, nonceToken)})
	}
	if !haveStyle && haveDefault {
		out = append(out, Directive{Name: "style-src", Value: appendToken(defaultSrc, nonceToken)})
	}
	return out
}

func appendToken(value, token string) string {
	if value == "" {
		return token
	}
	return value + " " + token
}

// Policy assembles the full Content-Security-Policy header value for one
// response : override (RelHttpResponse.csp, "" when unset) takes
// precedence over csp.Policy (http.csp.policy, "" when unset), which in
// turn REPLACES the individual directives entirely when set ; otherwise
// the ten named directives are used. The nonce is always injected on top,
// per ### Per-response override's "same synthesize-if-absent rule."
func Policy(csp config.HttpCsp, override string, nonce string) string {
	var directives []Directive
	switch {
	case override != "":
		directives = ParsePolicy(override)
	case csp.Policy != "":
		directives = ParsePolicy(csp.Policy)
	default:
		directives = BuildPolicyFromConfig(csp)
	}
	return SerializePolicy(InjectNonce(directives, nonce))
}
