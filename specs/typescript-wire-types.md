# Typescript Wire Types

`pg_values.ts`, `proto`'s descriptor-map mechanism, and every branded type/accessor/cast this spec
originally covered are implemented — see `docs/content/typescript/index.md ## Typed wire values`
and `## Attaching behavior to rows` for the current, durable reference. What's left:

## Reusable helpers, remaining work

`moneyAccessor` and `bytesAccessor` (a `Uint8Array` via hex-decode, including the empty `bytea`
case, which still renders `"\\x"`) follow the exact same pattern as `timestampTzAccessor`
(`pg_values.ts`) — not yet written.

## `create()`

The shortcut marker redesign this depends on is implemented : `>` outgoing (to-one),
`<` incoming-and-unique (to-one), `*` incoming-and-multiple (to-many), no separate `unique` field,
no `;` separator — the marker character is the separator (`hotel.rooms*id:property_id`).
`JoinCardinality` (`shapes.ts`) pattern-matches the marker directly.

A `Querier.create()` method, reusing `this.query`'s own `proto` tree recursively (spanning
`join`), so a caller can build a new row (for `.write()`) with the same accessors as a read row,
without a manual `Object.setPrototypeOf` per level, is not yet written. With the marker carrying
full cardinality, it needs no runtime schema data and no unresolved guess : walking
`this.query`'s `join` map, each key's own shortcut marker says definitively whether to seed it
with `[]` (to-many, `*`) or a single `create()`-built nested value (to-one, `<` or `>`).

## GIS types and other extensions

Out of scope for this spec — see `specs/postgis.md`. `citext` is the one popular extension type
already handled today (`baseScalarTypes` already maps it to `string`, needing no correction).
