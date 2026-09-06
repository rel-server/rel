package route

// classifyShape implements specs/new-routes.md ## Function prototype's
// structural argument/return-type matching, ## Method inference, and the
// ## Templates template+binary-return incompatibility check.

import (
	"strings"

	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/pg"
)

// log is this package's module-tagged logger, per specs/logging.md
// ## Domain scoping.
var log = logging.For("route")

func isJsonType(t *pg.Type) bool {
	return t != nil && t.PgIdentifier.String() == "pg_catalog.json"
}

func isJsonbType(t *pg.Type) bool {
	return t != nil && t.PgIdentifier.String() == "pg_catalog.jsonb"
}

func isByteaType(t *pg.Type) bool {
	return t != nil && t.PgIdentifier.String() == "pg_catalog.bytea"
}

func isTextType(t *pg.Type) bool {
	return t != nil && t.PgIdentifier.String() == "pg_catalog.text"
}

// isBytesArrayType reports whether t is exactly bytea[], matched by type
// rather than through a domain wrapper.
func isBytesArrayType(t *pg.Type) bool {
	return t != nil && t.IsArray() && t.ElementType != nil && t.ElementType.PgIdentifier.String() == "pg_catalog.bytea"
}

// isMimeTypeUnderlying is docs/content/http/static-files.md ## Returning
// binary or text content directly's mimetype-domain rule : a domain over
// bytea or over text.
func isMimeTypeUnderlying(underlying *pg.Type) bool {
	name := underlying.PgIdentifier.String()
	return name == "pg_catalog.bytea" || name == "pg_catalog.text"
}

// classifyShape resolves rd's function against ## Function prototype ;
// (Route{}, false) means classification failed and a discovery-time
// error was already logged.
func classifyShape(rd resolvedDeclaration) (Route, bool) {
	fn, decl := rd.fn, rd.decl

	if decl.Path == "" {
		log.Error("route: declared route has no path, function disabled",
			"schema", fn.Identifier.Schema, "function", fn.Identifier.Name)
		return Route{}, false
	}

	var inArgs, outArgs []pg.FunctionArgument
	for _, a := range fn.Arguments {
		switch {
		case a.IsIn():
			inArgs = append(inArgs, a)
		case a.IsOut():
			outArgs = append(outArgs, a)
		default:
			log.Error("route: unsupported argument mode, function disabled",
				"schema", fn.Identifier.Schema, "function", fn.Identifier.Name, "mode", a.PgMode)
			return Route{}, false
		}
	}

	idx := 0
	hasRequestArg := false
	if len(inArgs) > 0 && (isJsonType(inArgs[0].Type) || isJsonbType(inArgs[0].Type)) {
		idx = 1
		hasRequestArg = true
	}

	var acceptsBytes, acceptsBytesArray bool
	if !decl.StreamUpload && idx < len(inArgs) {
		switch {
		case isByteaType(inArgs[idx].Type):
			acceptsBytes = true
			idx++
		case isBytesArrayType(inArgs[idx].Type):
			acceptsBytesArray = true
			idx++
		}
	}
	if decl.StreamUpload && idx < len(inArgs) && (isByteaType(inArgs[idx].Type) || isBytesArrayType(inArgs[idx].Type)) {
		log.Error("route: stream_upload function must not take a bytea/bytea[] argument, function disabled",
			"schema", fn.Identifier.Schema, "function", fn.Identifier.Name)
		return Route{}, false
	}

	placeholders := map[string]bool{}
	for _, p := range pathPlaceholderNames(decl.Path) {
		placeholders[p] = true
	}

	var pathArgs []string
	for _, a := range inArgs[idx:] {
		if !isTextType(a.Type) {
			log.Error("route: argument has an unsupported type for a path placeholder, function disabled",
				"schema", fn.Identifier.Schema, "function", fn.Identifier.Name, "argument", a.Name)
			return Route{}, false
		}
		if !placeholders[a.Name] {
			log.Error("route: argument not present in the parsed path, function disabled",
				"schema", fn.Identifier.Schema, "function", fn.Identifier.Name, "argument", a.Name)
			return Route{}, false
		}
		pathArgs = append(pathArgs, a.Name)
	}

	entry := Route{
		Function:          fn,
		Path:              decl.Path,
		AnonPath:          anonymizePath(decl.Path),
		HasRequestArg:     hasRequestArg,
		PathArgs:          pathArgs,
		AcceptsBytes:      acceptsBytes,
		AcceptsBytesArray: acceptsBytesArray,
		Template:          decl.Template,
		StreamUpload:      decl.StreamUpload,
		IsMiddleware:      decl.Middleware,
		Source:            rd.src,
	}

	switch len(outArgs) {
	case 0:
		contentType, binary, ok := classifySingleReturn(fn)
		if !ok {
			log.Error("route: unsupported return type, function disabled",
				"schema", fn.Identifier.Schema, "function", fn.Identifier.Name)
			return Route{}, false
		}
		entry.ContentType = contentType
		entry.ContentIsBinary = binary
	case 2:
		if !(isJsonType(outArgs[0].Type) || isJsonbType(outArgs[0].Type)) {
			log.Error("route: full-control return's first OUT column must be json/jsonb, function disabled",
				"schema", fn.Identifier.Schema, "function", fn.Identifier.Name)
			return Route{}, false
		}
		contentType, binary, ok := classifySingleReturnType(outArgs[1].Type)
		if !ok {
			log.Error("route: full-control return's second OUT column has an unsupported type, function disabled",
				"schema", fn.Identifier.Schema, "function", fn.Identifier.Name)
			return Route{}, false
		}
		entry.FullControl = true
		entry.ContentType = contentType
		entry.ContentIsBinary = binary
	default:
		log.Error("route: unsupported OUT-argument shape, function disabled",
			"schema", fn.Identifier.Schema, "function", fn.Identifier.Name, "out_args", len(outArgs))
		return Route{}, false
	}

	if decl.Middleware && !entry.FullControl {
		log.Error("route: a middleware function must use the two-OUT-column full-control return shape, function disabled",
			"schema", fn.Identifier.Schema, "function", fn.Identifier.Name)
		return Route{}, false
	}

	if decl.StreamUpload && !entry.FullControl {
		log.Error("route: a stream_upload function must use the two-OUT-column full-control return shape, to set \"upload\" on its response, function disabled",
			"schema", fn.Identifier.Schema, "function", fn.Identifier.Name)
		return Route{}, false
	}

	if decl.Template != "" && entry.ContentIsBinary {
		log.Error("route: template is incompatible with this function's binary return type, function disabled",
			"schema", fn.Identifier.Schema, "function", fn.Identifier.Name)
		return Route{}, false
	}

	entry.Methods = resolveMethods(decl.Method, acceptsBytes, acceptsBytesArray, decl.StreamUpload)

	return entry, true
}

// classifySingleReturn is ## Function prototype's single-return-value
// mimetype table, applied to fn's own ReturnType.
func classifySingleReturn(fn *pg.Function) (contentType string, isBinary bool, ok bool) {
	if fn.ReturnType == nil {
		return "", false, false
	}
	return classifySingleReturnType(fn.ReturnType)
}

// classifySingleReturnType is classifySingleReturn's type-only half, reused
// for a full-control function's second OUT column — both resolve to the
// exact default Content-Type ## Function prototype's table states, not
// just whether the value is binary.
func classifySingleReturnType(t *pg.Type) (contentType string, isBinary bool, ok bool) {
	if t == nil {
		return "", false, false
	}
	switch t.PgIdentifier.String() {
	case "pg_catalog.text":
		return "text/plain", false, true
	case "pg_catalog.json", "pg_catalog.jsonb":
		return "application/json", false, true
	case "pg_catalog.bytea":
		return "application/octet-stream", true, true
	}
	if t.IsDomain() && t.Underlying() != nil && isMimeTypeUnderlying(t.Underlying()) && strings.Contains(t.PgIdentifier.Name, "/") {
		binary := t.Underlying().PgIdentifier.String() == "pg_catalog.bytea"
		return t.PgIdentifier.Name, binary, true
	}
	return "", false, false
}

// resolveMethods is ## Method inference : GET by default, POST when the
// function takes upload bytes (either shape) or declares stream_upload.
func resolveMethods(raw string, acceptsBytes, acceptsBytesArray, streamUpload bool) []string {
	if raw == "" {
		if acceptsBytes || acceptsBytesArray || streamUpload {
			return []string{"POST"}
		}
		return []string{"GET"}
	}
	seen := map[string]bool{}
	var methods []string
	for _, p := range strings.Split(raw, ",") {
		m := strings.ToUpper(strings.TrimSpace(p))
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		methods = append(methods, m)
	}
	if len(methods) == 0 {
		return []string{"GET"}
	}
	return methods
}
