# Spec cleanup review

Discrepancies and other loose ends surfaced while porting `specs/*.md` content into
`docs/content/` and removing what's now covered there. Framed as possibly-not-yet-propagated
for you to judge, not as asserted errors.

## `GET /rel/database.json` looks documented, not built

`docs/content/typescript-client.md` documents `GET /rel/database.json` as a live endpoint (the
same introspected structure as `database.ts`, in plain JSON). The removed `specs/typescript.md`
`## Endpoints`/`## database.json` sections made the same assumption. But:

- A repo-wide grep for `database.json`/`DatabaseJSON` in Go code finds no route registration —
  only `GET /rel/database.ts` is mounted (`boot/mux.go`).
- `config/config.go`'s `Config` struct doc comment mentions gating "the endpoints" behind
  `Http.TypeScript/Http.Json`, but `Http.Json` doesn't exist as a field anywhere in the `Http`
  struct — only `Http.TypeScript` does.
- `typescript/shapes.ts`'s own comment calls a JSON-schema export of the database "not-yet-built
  (v2) work."

Three independent signals point the same way: this endpoint was planned (possibly even had a
config key stubbed for it) but never actually implemented. Worth confirming before deciding
whether to build `GET /rel/database.json`, or trim the doc page and the stale `Http.Json`
comment reference.

## `docs/content/http/uploads.md` — internal wording tension on recognized route shapes

`## Receiving raw bytes` states "Only four argument shapes are recognized route signatures at
all," but `## Choosing a destination without routing bytes through Postgres` on the same page
documents a fifth, distinct discovered-route shape family (the `__prepare`/mandatory-function
pair). Either the "four shapes" count needs updating, or the fifth shape needs folding into
that enumeration.

## Pre-existing mis-citation in `specs/http-content.md` (not fixed, out of scope for this pass)

The `__prepare` `volatile` warning in `specs/http-content.md` says it gets "the same treatment
`## Static files`'s `PUBLIC`-executable warning gets," but that warning actually lives in
`specs/route.md ### PUBLIC-reachable routes`, not in `http-content.md ## Static files`. Predates
this cleanup pass.

## Pre-existing mislabeled citation, fixed in passing

`query/write.go`'s writability error message cited `specs/query-engine.md ## Configuration` for
a rule that has always actually lived under `## Writability` — a stale/typo'd citation, unrelated
to this pass, only surfaced because `## Configuration` no longer exists at all after the cleanup.
Retargeted to `docs/content/query-language/writing.md`, where the rule is now documented.
