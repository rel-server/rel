# Typescript Wire Types

`pg_values.ts`, `proto`'s descriptor-map mechanism, and every branded type/accessor/cast this spec
originally covered are implemented — see `docs/content/typescript/index.md ## Typed wire values`
and `## Attaching behavior to rows` for the current, durable reference. What's left:

## Reusable helpers, remaining work

`moneyAccessor` and `bytesAccessor` (a `Uint8Array` via hex-decode, including the empty `bytea`
case, which still renders `"\\x"`) follow the exact same pattern as `timestampTzAccessor`
(`pg_values.ts`) — not yet written.

## `create()`

A `Querier.create()` method, reusing `this.query`'s own `proto` tree recursively (spanning
`join`), so a caller can build a new row (for `.write()`) with the same accessors as a read row,
without a manual `Object.setPrototypeOf` per level.

Cardinality (whether a join key should be `[]` or a single nested object) becomes recoverable
from the shortcut string alone, by widening its direction marker from two characters to three,
each telling both direction and cardinality on its own : `>` outgoing (necessarily to-one — this
relation owns the FK, unchanged), `<` incoming-and-unique (to-one — a 1:1 relationship modeled via
a unique incoming FK, redefined from its current meaning), `*` incoming-and-multiple (to-many —
the joined relation owns the FK, not unique).

> **Why:** this redefines `<`'s current meaning (today, incoming defaults to to-many) rather than only adding a marker ; the common incoming-to-many case earns the visually plainest symbol (`*`, reading naturally as "multiple"), and `<`/`>` become a symmetric pair — a relationship is unique in exactly one of two directions, `<` or `>`, with `*` marking the one shape that isn't. Acceptable as a redefinition, not just an addition, since shortcuts are `tsgen`-generated (`schema.example.ts`'s own header comment), not typically hand-authored — a regenerated `database.ts` is internally consistent regardless of which convention it was generated under, and this feature is still evolving pre-stable.

The `;` separator between the direction marker and the column-pair list is dropped ; the (now
three-way) marker character itself is the separator, since none of `<`/`>`/`*` can otherwise
appear in an unquoted schema/relation/column name.

```
hotel.rooms*id:property_id            -- to-many, incoming (was '<' before this redefinition)
hotel.properties>id:property_id       -- to-one, outgoing (unchanged)
hotel.profile<id:user_id              -- to-one, incoming-but-unique (was Relationships' separate unique:true case)
```

`Relationships`' separate `unique: boolean` field (`schema.example.ts`) is retired ;
`JoinCardinality` (`shapes.ts`) derives cardinality by pattern-matching the shortcut's own
template literal type instead of a lookup.

> **Why:** carrying both the marker and a redundant `unique` field would be two sources of truth for the same fact.

With the marker carrying full cardinality, `create()` needs no runtime schema data and no
unresolved guess : walking `this.query`'s `join` map, each key's own shortcut marker says
definitively whether to seed it with `[]` (to-many, `*`) or a single `create()`-built nested value
(to-one, `<` or `>`).

## GIS types and other extensions

Out of scope for this spec — see `specs/postgis.md`. `citext` is the one popular extension type
already handled today (`baseScalarTypes` already maps it to `string`, needing no correction).
