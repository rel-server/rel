package route

// Route declarations, read from a COMMENT ON FUNCTION or the config route.<schema>.<function>.* namespace.

import (
	"github.com/bytedance/sonic"
	"github.com/samber/oops"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/declcomment"
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
	raw, found, err := declcomment.Extract(comment, "route")
	if !found || err != nil {
		return nil, err
	}
	var decl Declaration
	if err := sonic.Unmarshal(raw, &decl); err != nil {
		return nil, oops.Wrapf(err, "route: decoding route declaration")
	}
	return &decl, nil
}
