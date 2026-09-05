# Documentation site

The documentation site is rel's user-facing reference, built with Zensical and versioned with
the Zensical-compatible fork of `mike`, published to GitHub Pages. Everything it needs —
`docs/zensical.toml`, its markdown source under `docs/content/`, and its template overrides
under `docs/overrides/` — lives under `docs/`, so nothing it produces (`docs/site/`, the
`.venv-docs/` tool environment) ends up published as site content itself; every `zensical`/
`mike` invocation points `-f`/`-F` at `docs/zensical.toml` explicitly rather than assuming it
sits in the working directory.

## Scope and lifecycle

- A documentation page is written for someone operating rel or building a client against it,
  never for someone implementing rel itself.
- A documentation page MUST NOT link to, cite, or otherwise reference any `specs/*.md` file.
- A new feature is still specified under `specs/` first, the same way as today, until it is
  implemented and ported to the documentation site.
- `specs/testing.md` is developer-process documentation, not user documentation. It is never
  ported and stays under `specs/` permanently.

## Landing page

`docs/content/index.md` opens with rel's core mechanic — a query's result shape is also its write
shape — then covers nested writes into not-yet-existing parents, atomic multi-query batches,
Postgres-native access control (roles + RLS, no separate authorization DSL), constant-memory
streamed responses, built-in username/password + OIDC + SAML authentication, and the generated
TypeScript client. It closes with a comparison table against PostgREST and PostGraphile.

## Site structure

The nav is tabbed at the top level (Home / Getting started / The query language / TypeScript
client / Configuration); "The query language" and "Configuration" are each a page group, not
a single page. "Source" is what to draw the content from; the page itself never cites it (see
`## Scope and lifecycle`).

| Tab | Page(s) | Source spec(s) |
|---|---|---|
| — | Home (`content/index.md`) | — (landing page, see `## Landing page`) |
| — | Getting started | `configuration.md` (minimal config), `migrations.md` (boot), `query-engine.md` (first query) — a walkthrough, not a section-by-section port |
| The query language | Overview, Database reference | `test/dmut` (the hotel-booking dev fixture schema itself, not a spec) |
| The query language | Shaping a query | `query-engine.md ## Scoping` |
| The query language | Filtering with `where` | `query.ts`'s `Expression`/operator types |
| The query language | Selecting fields | `query.ts`'s `select`-family `Expression` forms |
| The query language | Joining and embedding relations | `query-engine.md ## Scoping ### Join eligibility` |
| The query language | Calling functions | `query-engine.md ## Reading Algorithm ### Function-rooted nodes` |
| The query language | Computed fields and aggregates | `query.ts`'s `call`/`agg` forms |
| The query language | Operators reference | `query.ts`'s operator types, `query-json.md`'s word-form table |
| The query language | Ordering, distinctness, and pagination | `query.ts`'s `order_by`/`distinct`/`limit`/`offset` fields |
| The query language | Writing data back | `query-engine.md ## Writability`, `## Writing Algorithm` |
| The query language | Batching queries | `query.ts`'s `Query[]` sequence form |
| The query language | Querying with GET | `query-json.md` |
| The query language | Well-known queries | `well-known-queries.md`, `query-json.md ## Well-known queries on GET /rel` |
| TypeScript client | — | `typescript.md` |
| Configuration | Configuration | `configuration.md` |
| Configuration | Best practices | `query-engine.md ## Scoping` (role separation, blacklist), `http-content.md ## CSP` (nonce/CORS), the docker deployment page (packaging) — a synthesis page, not a section-by-section port |
| Configuration | Authentication | `authentication.md`, `oauth-saml.md` |
| Configuration | HTTP routes | `route.md`, `http-content.md` |
| Configuration | Rendering HTML with templates | `http-content.md ## Templates` |
| Configuration | Operations | `logging.md`, `error-handling.md`, `migrations.md` |

"Getting started" and "The query language" are reorganized around what a reader is trying to
do (read a relation, join/nest, filter, write back, control writability), not around
`query-engine.md`'s own two-pass algorithm structure. "Authentication", "HTTP routes", and
"Operations" sit under the "Configuration" tab rather than each getting their own top-level
tab — all three are deployment-facing, the same audience as plain configuration.

"Database reference" is a page in its own right, not spec-derived: an ER diagram plus a table
reference for the hotel-booking schema every other page's examples query against, so a reader
can see the whole shape before or while reading the rest of the section.

"Operators reference" is a page in its own right (not folded into "Filtering with `where`")
precisely because operators show up in `where`, `select`, `order_by`, and `agg`/`call`
arguments alike — it's referenced from all of them rather than owned by one.

## Porting a page

1. Read every source spec listed for the page in `## Site structure`.
2. Write the page for a reader who wants to accomplish something, with runnable examples
   against the `just test-db-fresh` dev fixture (`test/dmut`, `test/seed`) wherever practical.
3. Translate implementation-only language (Go types, internal pass names, file paths) out
   entirely — none of it belongs in a user-facing page.


> Question: `specs/index.md` and `specs/TODO.md` themselves — once every remaining topic has
> either moved to the doc site or been marked permanent (`testing.md`), do they get deleted
> too, or repurposed as an index of what's still spec-only?
>: Do not touch specs for now, I shall delete it afterwards.
