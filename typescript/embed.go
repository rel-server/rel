// Package typescript embeds the hand-maintained files the generated database.ts's output concatenates
// verbatim (pg_values.ts, querier.ts, query.ts, shapes.ts — schema.example.ts and example.ts are deliberately
// NOT embedded, see tsgen/embed.go's own file listing). The embed directive can't reach outside its own package
// directory, which is why this lives here rather than in tsgen (github.com/rel-server/rel/tsgen), the package
// that actually does the concatenation.
package typescript

import "embed"

//go:embed pg_values.ts
var PgValuesTS string

//go:embed querier.ts
var QuerierTS string

//go:embed query.ts
var QueryTS string

//go:embed shapes.ts
var ShapesTS string

// Codecs is the option-dependent runtime code of the generated file, by file name under codecs/ : the time chunks
// (time-text, time-date, time-temporal), the wire chunks (json-bigint, wire-cbor) and the CBOR tag chunks
// (cbor-time-date, cbor-time-temporal). tsgen picks the ones the requested codec options need.
//
//go:embed codecs/*.ts
var Codecs embed.FS
