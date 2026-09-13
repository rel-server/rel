# Typescript Wire Types

`pg_values.ts`, `proto`'s descriptor-map mechanism (including its merge into `WriteShape`), the
shortcut marker redesign, and `Querier.create()` are all implemented — see
`docs/content/typescript/index.md ## Typed wire values`, `## Attaching behavior to rows`, and
`### Building a new row` for the current, durable reference. What's left:

## Reusable helpers, remaining work

`moneyAccessor` and `bytesAccessor` (a `Uint8Array` via hex-decode, including the empty `bytea`
case, which still renders `"\\x"`) follow the exact same pattern as `timestampTzAccessor`
(`pg_values.ts`) — not yet written.

## GIS types and other extensions

Out of scope for this spec — see `specs/postgis.md`. `citext` is the one popular extension type
already handled today (`baseScalarTypes` already maps it to `string`, needing no correction).
