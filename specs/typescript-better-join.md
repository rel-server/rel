# TypeScript client: scoped join callback

`relation()`/`join()` (`typescript/querier.ts`) currently require every `join()` call to repeat
the *enclosing* relation's name as its own first argument, purely so `Relationships[key]` can be
looked up — the `example.ts` fixture calls this out explicitly ("root is `hotel.properties`, so
the key names *this* side of the relationship, not the target"). That repetition is untidy and
easy to get wrong when refactoring a query (rename the root relation, and every nested `join()`
call's first argument has to be updated too, or it silently type-errors against the wrong
`Relationships` entry). This spec adds a second, optional calling convention that removes it,
while keeping the existing form working unchanged.

## New calling convention

`relation()`'s (and `join()`'s) second argument becomes optional, and can be a callback instead
of a plain query object. The callback receives a `join` already scoped to the enclosing relation,
so nested joins never repeat it :

```ts
const properties = await relation("hotel.properties", (join) => ({
  where: [">=", "star_rating", 4],
  join: {
    rooms: join("hotel.rooms<;id:property_id", (join) => ({
      join: {
        room_type: join("hotel.room_types>;id:room_type_id"),
      },
    })),
  },
  select: { id: "id", name: "name", star_rating: "star_rating", rooms: "rooms" },
})).get()
```

Three independent changes, all applying to both `relation()` and `join()`:

1. **The second argument is optional.** `relation("hotel.properties")` / `join("hotel.rooms<;id:property_id")`
   with nothing else is a bare select-all — no `where`, no `join`, no `select`. This isn't new
   sugar for a special case : an absent `select` already means `["full"]` server-side
   (`query/node_resolve.go:212-217`, `docs/content/query-language/selecting.md`) — own columns
   plus every joined relation — so the shorthand is just "send `{schema, relation}` and nothing
   else," relying on that existing default. `ShapeFromRelationQuery` (`shapes.ts:352-366`) already
   falls back to `FullShape` when `select` is absent from `Q`, so this needs no change to
   `shapes.ts` — `relation("hotel.properties")` alone already infers the full row shape.

2. **The second argument can be a callback**, `(join: ScopedJoin<R>) => Q`, instead of `Q`
   directly. `R` is `relation()`'s own relation-name generic ; for `join()`, it's the *target*
   relation of the shortcut just passed (see below) — either way, "the relation this query node is
   rooted at," so the `join` handed to the callback is already scoped there and only takes
   `(shortcut, request?)`, never `(key, shortcut, request?)`.

3. **Both changes recurse**: the scoped `join`'s own second argument is `Q | ((join: ScopedJoin<Target>) => Q)`
   too, so nesting is uniform at every depth — see `room_type` above, nested two levels deep,
   with neither level repeating a relation name that TypeScript already knows.

The existing plain-object form keeps working unchanged, at every level — `relation(rel, {...})`,
and the free `join(key, shortcut, {...})` (or now `join(key, shortcut)` for a bare embed). Nothing
in `example.ts`'s current usage needs to change ; the callback form is purely additive.

## Types

```ts
// K is the relation this join is scoped to reading FROM ("hotel.properties", "hotel.rooms", ...).
// Any string is accepted (relation()'s R isn't constrained to keyof Relationships), and
// SafeRelationships<K> resolves to `never` for a name with no known relationships — making the
// scoped join uncallable there, the same as today for an unrecognized relation.
type SafeRelationships<K extends string> = K extends keyof Relationships ? Relationships[K] : never

// The target relation name embedded in a shortcut string itself : "hotel.rooms<;id:property_id"
// -> "hotel.rooms". This is what lets a nested join's own callback re-scope itself without a
// second explicit argument — the shortcut the caller already had to type carries it.
type TargetRelationName<S extends string> = S extends `${infer Rel}${"<" | ">"}${string}` ? Rel : never

export interface ScopedJoin<K extends string> {
  <
    S extends SafeRelationships<K>["shortcut"],
    const Q extends RelationQuery<
      Extract<SafeRelationships<K>, { shortcut: S }>["relation"]
    > = Record<string, never>,
  >(
    shortcut: S,
    request?: Q | ((join: ScopedJoin<TargetRelationName<S>>) => Q),
  ): Q & { shortcut: S }
}
```

`relation()` gains the matching optional/callback parameter and a default for its own `Q`:

```ts
export function relation<
  R extends string,
  const Q extends RelationQuery<ResolveRelationModel<R>> = Record<string, never>,
>(
  rel: R,
  request?: Q | ((join: ScopedJoin<R>) => Q),
): Querier<ShapeFromQuery<Q, ResolveRelationModel<R>>, WriteShapeFromQuery<Q, ResolveRelationModel<R>>, Params<Q>>
```

`join()` keeps its existing three required-looking parameters (`key`, `shortcut`, `request`) for
the explicit form, but `request` becomes the same optional `Q | ((join: ScopedJoin<Target>) => Q)`,
where `Target` is `TargetRelationName<S>` — the shortcut's own target, not `key`. At runtime, both
`relation()` and `join()` resolve `request` the same way : call it with a scoped join if it's a
function, default to `{}` if omitted, use it as-is otherwise. A `scopedJoin(key)` helper builds the
`(shortcut, request?) => ...` closure once and is reused by both entry points and by the recursive
case (`join()`'s own resolution scopes the *next* level to the shortcut's parsed target).

## One signature, not overloads

`relation()` (`querier.ts:32-34`'s own comment) is deliberately a single generic signature today,
specifically so a bad column errors instead of silently falling through to a looser overload
candidate. The new optional/union parameter (`request?: Q | ((join) => Q)`, with `Q`'s own default)
preserves that : it's still exactly one signature, just with a default and a wider parameter type,
not a second overload. `join()`'s scoped form (`ScopedJoin<K>`) is a single call signature for the
same reason.

## Why `TargetRelationName<S>` parses the shortcut instead of taking another argument

A nested `join()` call already fully identifies its target via `shortcut` alone (that's the whole
point of the sugar — `parseShortcut`, `querier.ts:85-107`, already extracts `schema`/`relation`
from it at runtime). Requiring a *second* explicit "and by the way, here's what relation you just
named" argument would reintroduce exactly the repetition this spec removes, one level down. Parsing
`Rel` out of the string at the type level (a template literal match on `<`/`>`) costs nothing the
caller has to supply — the same string already flows through `parseShortcut` at runtime for `on`/
`schema`/`relation`.

## Verified before writing this spec

- **Literal-type inference through the callback.** The biggest risk : does TypeScript's `const`
  type-parameter modifier still infer *literal* types (needed for `ShapeFromExpression`'s tuple/tag
  matching) when `Q` comes from a callback's return value instead of a directly-passed object
  literal ? Confirmed yes, via an isolated prototype (`directArg`/`viaCallback` test, both preserving
  a tuple literal `readonly [">=", "star_rating", 4]` through inference) — no widening.
- **`relation("hotel.properties")` (no second argument) infers the full shape.**
  `ShapeFromRelationQuery<Q, Rel>` (`shapes.ts:352-366`) already treats "no `select` key present in
  `Q`" as `FullShape<Rel, ExtractJoinMap<Q>, Depth>` — the same branch an explicit `select: ["full"]`
  takes. `Q`'s default (`Record<string, never>`, i.e. `{}`) has no `select` key, so this falls out
  for free ; no change needed to `shapes.ts`.
- **A full end-to-end prototype** (real `schema.example.ts`, modified `querier.ts`) confirms : the
  no-arg shorthand infers own+join columns ; the root callback form infers the same shape as the
  existing plain-object form for an equivalent query ; a two-level-deep nested callback
  (`properties -> rooms -> room_type`) infers correctly without repeating any relation name past
  the root ; passing a shortcut that isn't reachable from the current scope (e.g. `hotel.room_types`
  under a callback scoped to `hotel.properties`) is a compile error ; and the existing explicit-key
  form (`join("hotel.rooms", "hotel.properties>;id:property_id", {...})`) still compiles unchanged
  side-by-side with the new form in the same query.

> Note, not a regression: while prototyping, a structurally-invalid `where` (e.g. `where: 123`) and
> an unrecognized column name inside a `select` object map (e.g. `select: { oops: "not_a_column" }`)
> were both found to type-check without error, on *both* the old and new signatures alike — a
> pre-existing gap, already called out as future (v2) work (`shapes.ts` doc comments ; "full
> expression/column type-checking as a separate, harder concern"). This spec doesn't touch that ;
> confirming it's unaffected (equally permissive before and after) was part of validating that the
> new signature doesn't regress anything the old one caught.

## Out of scope

- `func()`/`wellknown()` don't get the same optional/callback treatment here. `func()` could
  symmetrically (it shares `relation()`'s `select`/`join` machinery), but wasn't asked for ; add it
  in a follow-up if wanted, same mechanism. `wellknown()`'s params/shape come from the query's own
  registered definition, not a caller-supplied `Q` — the callback form doesn't apply to it at all.

## Impacts

- `typescript/querier.ts` : `ScopedJoin<K>`, `SafeRelationships<K>`, `TargetRelationName<S>`,
  `scopedJoin()` added ; `relation()` and `join()` signatures updated (optional, union-typed
  `request` parameter, `Q`'s default) ; both resolve `request` (function vs. object vs. absent) the
  same way before building the wire query.
- `typescript/example.ts` : add a second fixture query using the callback form (with a nested
  join), alongside the existing plain-object one, so both stay covered by the "fails `just check` if
  the generic machinery stops accepting a well-formed query" guarantee this file already provides.
- `docs/content/typescript-client.md` : the join example rewritten to the callback form, since it's
  the more ergonomic default going forward, with a one-line mention that the old form still works.
