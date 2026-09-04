// Package wellknown implements specs/well-known-queries.md : loading query
// definition files from pg.query.wellknown_path, resolving/compiling each
// one exactly once (at boot, and again on every SIGUSR1 reload — see
// boot/reload.go), and validating a request's own params against what was
// declared.
//
// A *Compiled entry's *query.QueryNode (Root) and precompiled read
// statement (Read) are safe to reuse concurrently across every request
// naming it, for as long as the *Registry that produced them is live : pass
// 1/2 resolution (query.ResolveContext.ResolveQuery/ResolveExpressions/
// DeriveShapes) is the only thing that ever writes to a *QueryNode's own
// fields — nothing in the write path (query.ExecuteWriteStateParams and
// everything it calls) mutates the tree, only reads it — so the SAME
// resolved root serves both the read path (via Read, compiled once) and
// the write path (recompiled fresh per request, same as a plain /rel write
// always has been ; see specs/well-known-queries.md ## Behaviour's own
// note on the write-side compile/execute split still being open work).
package wellknown

import (
	"github.com/ceymard/rel/query"
	"github.com/ceymard/rel/writer"
)

// ParamDef is one declared WellKnownParam{type?, default?} — see
// query.RawWellKnownParam's own doc comment for why HasDefault/Default are
// presence-aware rather than a plain map lookup.
type ParamDef struct {
	Type       string
	HasDefault bool
	Default    []byte
}

// Compiled is one successfully loaded, resolved, and validated well-known
// query — absent from a *Registry entirely (not tombstoned) when it was
// never validly defined, deactivated by a duplicate name, or failed
// validation ; specs/well-known-queries.md ## Behaviour : "a request naming
// a deactivated (or never-validly-defined) query is rejected the same way
// a genuinely unknown name would be", so there's no distinct state to carry
// for that case.
type Compiled struct {
	Name   string
	Params map[string]ParamDef
	Root   *query.QueryNode

	// Read is query.CompileSelect(Root), compiled once at load time (##
	// Behaviour's "prepared" story) ; only ResolveArgs' paramValues change.
	Read *writer.SQLWriter

	// QueryRaw is the definition's own "query" field, verbatim JSON —
	// query.RawWellKnownDefinition.QueryRaw carried through unchanged.
	// tsgen embeds this as a TS literal for Wellknowns (specs/typescript.md
	// ## Wellknowns) rather than re-deriving its shape in Go.
	QueryRaw []byte
}

// Registry is every well-known query successfully loaded from
// pg.query.wellknown_path, keyed by their own declared name.
type Registry struct {
	byName map[string]*Compiled
}

// Lookup finds name's *Compiled entry, or false if it's unknown or was
// deactivated (see Compiled's own doc comment). Safe to call on a nil
// *Registry (no directories configured/found — BuildRegistry still returns
// a valid, empty *Registry in that case, but a nil check here costs
// nothing and matches static.Server's own defensive posture).
func (r *Registry) Lookup(name string) (*Compiled, bool) {
	if r == nil {
		return nil, false
	}
	c, ok := r.byName[name]
	return c, ok
}

// All returns every registered *Compiled entry, unordered. Safe to call on
// a nil *Registry (see Lookup). tsgen walks this to generate Wellknowns.
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
