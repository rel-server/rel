package query

// ShapeField will describe one hop a dot-chain (or ->/#> access) can land on
// when resolving a node's exported select shape, once pass 2 (expression/
// scope resolution) is designed — see specs/querying.md and this session's
// discussion for the reasoning. Deliberately left as a placeholder : pass 1
// (tree inflation + DB resolution, in node.go) comes first, and this
// shouldn't be designed in isolation from how that pass actually shapes
// QueryNode.
//
// The variants a resolved field is expected to need, once this exists :
//   - leaf : nothing further to navigate (a plain scalar column or
//     expression result).
//   - composite column/return type : *pg.Type.IsComposite() is true, walk
//     *pg.Type.Relation.ColumnsMap for the next hop. Already fully
//     introspected by pg ; no new DB-side work needed for this case.
//   - embedded join : walk the target *QueryNode's own exported shape,
//     recursively, same lazy/memoized/cycle-checked resolution as the rest
//     of pass 2.
//   - nested inline object literal from the select JSON : purely syntactic,
//     each key recurses into whichever of the above it turns out to be.
//
// Scoping/blacklist checks apply uniformly to every hop. A composite column
// or function return type is walkable the moment the query legitimately has
// access to the column/call it lives on — there is no separate gate for
// composite sub-fields, and reaching an otherwise-blacklisted relation this
// way is allowed by design : the developer may want to expose it in a
// controlled way precisely by routing access through a specific column.
type ShapeField any
