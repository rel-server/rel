package query

// ResolvedField is what resolving an identifier (or a later hop in a
// dot/->/#>/#>> chain) produces — see specs/query-compiler.md's "Identifier
// resolution" section for the full reasoning. Deliberately left as a
// placeholder : pass 1 (tree inflation + DB resolution, in node.go) comes
// first, and this shouldn't be designed in isolation from how that pass
// actually shapes QueryNode.
//
// The concrete variants expected, once this exists :
//   - a column-backed field, wrapping *pg.Column : covers both a plain
//     column of the current relation *and* a composite sub-field reached by
//     walking *pg.Type.Relation.ColumnsMap — both are ultimately "this name
//     is this physical column," just sourced from a different ColumnsMap.
//     Already fully introspected by pg ; no new DB-side work needed for the
//     composite case. A leaf, in dot-chain terms : nothing further to
//     navigate from a scalar column, but chaining further from a composite
//     one recurses into its own ColumnsMap the same way.
//   - a node-backed field, wrapping *QueryNode : an embedded join alias.
//     Chaining further recurses into the target node's own ResolvedField
//     map, resolved the same way.
//   - a literal-backed field, wrapping a nested inline object literal from
//     the select JSON : purely syntactic, no DB lookup ; each of its own
//     keys recurses into whichever of the above it turns out to be.
//
// No cycle detection needed to resolve any of this : with sibling visibility
// excluded (query-compiler.md), a dot-chain only ever travels downward into
// a node's own already-parsed children — a finite JSON-derived tree, not a
// graph. Memoization is still worth keeping (a given node's fields can
// legitimately be requested from more than one place), but only as a
// performance nicety now, not for correctness.
//
// Scoping/blacklist checks apply uniformly to every hop. A composite column
// or function return type is walkable the moment the query legitimately has
// access to the column/call it lives on — there is no separate gate for
// composite sub-fields, and reaching an otherwise-blacklisted relation this
// way is allowed by design : the developer may want to expose it in a
// controlled way precisely by routing access through a specific column.
//
// This same walk also has to build what querying.md's Writing Algorithm
// calls the node's "extractor" (### Implementation, step 1) : not just each
// writable column's *name* in the exported shape, but *where* to find its
// value in an actual data payload shaped like that select — i.e. a
// column -> JSON-path-within-`data` map, since a payload's shape mirrors the
// select (renamed/nested/omitted) rather than raw column names. That map is
// what step 2 (denormalizing `data` into the flat `_data` array) walks
// alongside the payload for every node. Building it is a natural side effect
// of the same walk above — a column-backed field records its current JSON
// path as that column's extraction site — but it's a distinct output from
// ResolvedField itself (a column's read location, not the kind of thing
// found there), so it'll likely need its own small type alongside this one,
// not be folded into it.
type ResolvedField any
