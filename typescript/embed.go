// Package typescript embeds the three hand-maintained files specs/
// typescript.md ## database.ts's generated output concatenates verbatim
// (querier.ts, query.ts, shapes.ts — schema.example.ts and example.ts are
// deliberately NOT embedded, see that spec section's own file listing).
// The embed directive can't reach outside its own package directory, which
// is why this lives here rather than in tsgen (github.com/ceymard/rel/
// tsgen), the package that actually does the concatenation.
package typescript

import _ "embed"

//go:embed querier.ts
var QuerierTS string

//go:embed query.ts
var QueryTS string

//go:embed shapes.ts
var ShapesTS string
