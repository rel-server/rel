// Assembles the Content-Security-Policy header from the http.csp.* directives or a raw policy, and injects the
// per-request nonce.
package websec

import (
	"strings"

	"github.com/rel-server/rel/config"
)

// Directive is one CSP directive ; kept in a slice so a raw policy's order survives ParsePolicy/SerializePolicy.
type Directive struct {
	Name  string
	Value string
}

// cspDirectiveOrder is the eleven named directives, in emission order.
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
	{"worker-src", func(c config.HttpCsp) string { return c.WorkerSrc }},
	{"object-src", func(c config.HttpCsp) string { return c.ObjectSrc }},
	{"frame-ancestors", func(c config.HttpCsp) string { return c.FrameAncestors }},
	{"base-uri", func(c config.HttpCsp) string { return c.BaseUri }},
	{"form-action", func(c config.HttpCsp) string { return c.FormAction }},
}

// BuildPolicyFromConfig assembles the directive list from the http.csp.* keys, skipping empty ones.
func BuildPolicyFromConfig(csp config.HttpCsp) []Directive {
	var out []Directive
	for _, d := range cspDirectiveOrder {
		if v := d.get(csp); v != "" {
			out = append(out, Directive{Name: d.name, Value: v})
		}
	}
	return out
}

// ParsePolicy splits a raw Content-Security-Policy value on ';' ; each segment's first token is the name, the
// rest the value.
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

// SerializePolicy re-joins a Directive list into a header value, "name value; name value; ...".
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

// InjectNonce appends 'nonce-<value>' to script-src and style-src, creating a missing one as a copy of
// default-src plus the nonce, since a nonce alone would narrow the policy. script-src-elem/-attr are not
// touched. An empty nonce (/rel, /static) leaves directives untouched.
func InjectNonce(directives []Directive, nonce string) []Directive {
	if nonce == "" {
		return directives
	}
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
	// Skip when default-src is absent too : a nonce-only script-src would narrow the policy.
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

// Policy assembles one response's Content-Security-Policy : override (RelHttpResponse.csp), else
// http.csp.policy, else the named directives ; the nonce is injected on top. "" means unset.
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
