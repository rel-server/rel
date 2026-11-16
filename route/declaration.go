package route

// Route declarations, read from a COMMENT ON FUNCTION or the config route.<schema>.<function>.* namespace.

import (
	"strings"

	"github.com/bytedance/sonic"
	"github.com/huml-lang/go-huml"
	"github.com/samber/oops"

	"github.com/rel-server/rel/config"
)

// Declaration is a route's declared shape, minus function/schema, which always come from where it is declared.
type Declaration struct {
	// Path is a path in chi syntax ; an empty Path never makes its function routable.
	Path string `json:"path"`
	// Method is a comma-separated list of accepted HTTP methods ; empty means inferred.
	Method string `json:"method"`
	// Template is a Jet template path, used when the response doesn't set its own.
	Template string `json:"template"`
	// StreamUpload flags the two-call upload mechanism.
	StreamUpload bool `json:"stream_upload"`
	// Middleware flags this function as a middleware function rather than an ordinary route.
	Middleware bool `json:"middleware"`
}

func declFromConfig(d config.RouteDecl) Declaration {
	return Declaration{
		Path:         d.Path,
		Method:       d.Method,
		Template:     d.Template,
		StreamUpload: d.StreamUpload,
		Middleware:   d.Middleware,
	}
}

// parseCommentDeclaration reads a Declaration out of a function's COMMENT ON text. (nil, nil) means the comment
// holds no route declaration ; an error means one was detected but failed to parse.
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

// isJSONObjectAfterColon reports whether rest (what follows "route") is ":", optional whitespace, then "{", so a
// doc comment beginning "route: see below" isn't mistaken for a declaration.
func isJSONObjectAfterColon(rest string) bool {
	rest, ok := strings.CutPrefix(rest, ":")
	if !ok {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(rest), "{")
}

// parseHumlDeclaration parses the whole comment, starting "route::", as one inline HUML document and lifts its
// "route" key.
func parseHumlDeclaration(trimmed string) (*Declaration, error) {
	var doc map[string]any
	if err := huml.Unmarshal([]byte(trimmed), &doc); err != nil {
		return nil, oops.Wrapf(err, "route: parsing HUML route declaration")
	}
	routeVal, ok := doc["route"]
	if !ok {
		return nil, oops.Errorf("route: HUML route declaration has no \"route\" key")
	}
	// Re-encoded through JSON so both formats decode through the same json tags.
	raw, err := sonic.Marshal(routeVal)
	if err != nil {
		return nil, oops.Wrapf(err, "route: re-encoding HUML route declaration")
	}
	var decl Declaration
	if err := sonic.Unmarshal(raw, &decl); err != nil {
		return nil, oops.Wrapf(err, "route: decoding HUML route declaration")
	}
	return &decl, nil
}

// parseJSONDeclaration parses rest, "route" stripped but still carrying the leading ":", as a JSON object.
func parseJSONDeclaration(rest string) (*Declaration, error) {
	rest, _ = strings.CutPrefix(rest, ":")
	rest = strings.TrimSpace(rest)
	var decl Declaration
	if err := sonic.Unmarshal([]byte(rest), &decl); err != nil {
		return nil, oops.Wrapf(err, "route: decoding JSON route declaration")
	}
	return &decl, nil
}
