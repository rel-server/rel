// Package wellknown loads well-known query files from pg.query.wellknown_path, compiling each once per boot or
// reload, and validates a request's params against the declared ones.
//
// A *Compiled's Root and Read are safe for concurrent use by every request : only resolution writes to a
// QueryNode, and the write path only reads it.
package wellknown

import (
	"github.com/rel-server/rel/declcomment"
	"github.com/rel-server/rel/query"
	"github.com/rel-server/rel/writer"
)

// ParamDef is one declared WellKnownParam{type?, default?} ; HasDefault tells a null default from none.
type ParamDef struct {
	Type       string
	HasDefault bool
	Default    []byte
	// TS is the declared TypeScript type ("ts"), for the generated client only.
	TS declcomment.TypeDecl
}

// Compiled is one loaded and validated well-known query. An invalid or duplicate one is simply absent from the
// Registry.
type Compiled struct {
	Name   string
	Params map[string]ParamDef
	Root   *query.QueryNode

	// Read is query.CompileSelect(Root), compiled once at load time ; only ResolveArgs' paramValues change.
	Read *writer.SQLWriter

	// ReadCBOR is the same statement in query.FormatCBOR, for a request that Accepts application/cbor.
	ReadCBOR *writer.SQLWriter

	// QueryRaw is the definition's "query" field, verbatim JSON ; tsgen embeds it as a TS literal.
	QueryRaw []byte
}

// Registry is every well-known query loaded, by name.
type Registry struct {
	byName map[string]*Compiled
}

// Lookup finds name's *Compiled entry, or false if it's unknown or was deactivated. Safe on a nil *Registry.
func (r *Registry) Lookup(name string) (*Compiled, bool) {
	if r == nil {
		return nil, false
	}
	c, ok := r.byName[name]
	return c, ok
}

// All returns every registered *Compiled entry, unordered. Safe on a nil *Registry.
func (r *Registry) All() []*Compiled {
	if r == nil {
		return nil
	}
	out := make([]*Compiled, 0, len(r.byName))
	for _, c := range r.byName {
		out = append(out, c)
	}
	return out
}
