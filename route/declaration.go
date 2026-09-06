package route

// This file implements specs/new-routes.md's route declaration surface :
// the Route JSON/HUML shape and the two places it's read from — a
// database COMMENT ON FUNCTION (## In comments), and the config
// route.<schema>.<function>.* namespace (## In the configuration).

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/huml-lang/go-huml"

	"github.com/rel-server/rel/config"
)

// Declaration is specs/new-routes.md ## JSON Types' Route interface,
// minus function/schema — both are always known from context (the
// function the comment sits on, or the config key path), never read out
// of the declaration text itself.
type Declaration struct {
	// Path is a path in chi syntax. Required — a Declaration with an
	// empty Path never makes its function routable.
	Path string `json:"path"`
	// Method is a comma-separated list of accepted HTTP methods ; empty
	// means inferred — see ## Method inference.
	Method string `json:"method"`
	// Template is a Jet template path, used when the response doesn't set
	// its own — see ## Templates.
	Template string `json:"template"`
	// StreamUpload flags the two-call upload mechanism — see
	// ## `stream_upload`.
	StreamUpload bool `json:"stream_upload"`
	// Middleware flags this function as a ## Middleware function rather
	// than an ordinary route.
	Middleware bool `json:"middleware"`
}

// declFromConfig adapts a config.RouteDecl (already schema/function-scoped
// by its map position) into a Declaration.
func declFromConfig(d config.RouteDecl) Declaration {
	return Declaration{
		Path:         d.Path,
		Method:       d.Method,
		Template:     d.Template,
		StreamUpload: d.StreamUpload,
		Middleware:   d.Middleware,
	}
}

// parseCommentDeclaration reads a Declaration out of a function's raw
// COMMENT ON text — ## In comments. Returns (nil, nil) when the trimmed
// comment carries no "route" declaration at all (an ordinary doc comment,
// or none) ; a non-nil error means a "route" declaration was detected but
// failed to parse, which the caller turns into a discovery-time warning
// disabling that one function, not a fatal error.
func parseCommentDeclaration(comment string) (*Declaration, error) {
	trimmed := strings.TrimSpace(comment)
	rest, ok := strings.CutPrefix(trimmed, "route")
	if !ok {
		return nil, nil
	}

	switch {
	case strings.HasPrefix(rest, "::"):
		return parseHumlDeclaration(trimmed)
	case isJSONObjectAfterColon(rest):
		return parseJSONDeclaration(rest)
	default:
		return nil, nil
	}
}

// isJSONObjectAfterColon reports whether rest — whatever follows the
// literal "route" in a trimmed comment — is a single ":", then optional
// whitespace, then "{". This is deliberately narrower than "starts with
// route:" alone, so an ordinary doc comment beginning "route: see below"
// isn't mistaken for a declaration.
func isJSONObjectAfterColon(rest string) bool {
	rest, ok := strings.CutPrefix(rest, ":")
	if !ok {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(rest), "{")
}

// parseHumlDeclaration parses trimmed (the whole comment, starting
// "route::...") as one inline HUML document and lifts its "route" key —
// HUML's own "::" compact-inline-value syntax is what makes a single-line
// declaration like `route:: path: "/some/path", method: "POST"` a complete,
// valid document on its own.
func parseHumlDeclaration(trimmed string) (*Declaration, error) {
	var doc map[string]any
	if err := huml.Unmarshal([]byte(trimmed), &doc); err != nil {
		return nil, fmt.Errorf("route: parsing HUML route declaration: %w", err)
	}
	routeVal, ok := doc["route"]
	if !ok {
		return nil, fmt.Errorf("route: HUML route declaration has no \"route\" key")
	}
	// Re-encoded through JSON rather than HUML struct tags, so both
	// formats decode into Declaration through the exact same json tags.
	raw, err := sonic.Marshal(routeVal)
	if err != nil {
		return nil, fmt.Errorf("route: re-encoding HUML route declaration: %w", err)
	}
	var decl Declaration
	if err := sonic.Unmarshal(raw, &decl); err != nil {
		return nil, fmt.Errorf("route: decoding HUML route declaration: %w", err)
	}
	return &decl, nil
}

// parseJSONDeclaration parses rest — "route" already stripped, still
// carrying the leading ":" and the JSON object itself — as
// `{"path": "/some/path"}`-shaped JSON.
func parseJSONDeclaration(rest string) (*Declaration, error) {
	rest, _ = strings.CutPrefix(rest, ":")
	rest = strings.TrimSpace(rest)
	var decl Declaration
	if err := sonic.Unmarshal([]byte(rest), &decl); err != nil {
		return nil, fmt.Errorf("route: decoding JSON route declaration: %w", err)
	}
	return &decl, nil
}
