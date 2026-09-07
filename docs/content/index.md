---
icon: material/home
---

# Rel

A Postgres-native API server. You send it a query shaped like the data you want; it reads
Postgres and answers with exactly that shape — and you can send that same shape back to write
it, across as many related tables as the query touched, in one transaction.

## Edit the data where it lives: in your own local shape

Most tools that let you query across relations — GraphQL servers, PostgREST's resource
embedding — only read that way. Once you have the data, writing means switching to a
different mechanism: mutations, one-table-at-a-time POSTs, hand-written SQL, restating what
you already had in a shape the write API demands instead of the shape you queried. Rel skips
that step. The object you got back is the object you edit — change a field, push a row into
an embedded array, delete one — and you send that same object back, as it sits, to the same
endpoint. Rel denormalizes it back down to one row per relation it touched and applies it —
insert, update, upsert, or merge (delete what's missing) — per relation, not by diffing
against a prior read. Each relation in the tree picks its own write mode, so you control, per
relation, whether an omitted child row is left alone or deleted.

## Nested writes, even before the parent exists

Insert a row and three related rows that point back at it, in one request — before the parent
has an id. Rel resolves the dependency order inside the transaction and threads the generated
key through to its children itself. No client-side orchestration, no multi-request dance to
get an id back before you can insert what depends on it.

## Batch unrelated writes, still atomic

A request isn't limited to one query tree. Send several — unrelated shapes, different
relations, no common root — in one request, and they all run in the same transaction: every
write across every query, plus every read-back, start to finish. Nothing partially succeeds.
If any one of them fails, the whole batch rolls back, so you never have to reason about a
client left holding a half-applied write.

## Postgres stays in charge

Rel introspects your schema at startup — tables, foreign keys, constraints, functions — and
builds the query engine from it. There's no separate schema DSL to keep in sync. Access
control is Postgres's own: rel switches role per request and lets grants and row-level
security do the enforcing, rather than reimplementing an authorization layer on top.

## Large results don't cost large memory

Responses stream row by row instead of building the whole result as one `json_agg` in memory
first. A result with ten rows or ten million costs the server roughly the same RAM either way.

## Authentication included, not bolted on

Username/password, OpenID Connect, and SAML all ship in rel itself — configure as many
providers of each as you need (`openid.<name>.*`, `saml.<name>.*`), and rel serves their
login/callback/metadata routes, verifies what comes back, and mints the session cookie. No
separate auth service to run and keep in sync, no hand-rolled OIDC/SAML client code.

## A generated client, not a build step

`/rel/database.ts` serves a single, self-sufficient TypeScript file generated from your live
schema: typed query building, typed results, no codegen step to wire into your build.

## How it compares

| | **Rel** | **PostgREST** | **PostGraphile** |
|---|---|---|---|
| Query shape | one JSON tree, arbitrary depth | resource embedding via URL params | GraphQL query |
| Writing nested data | same tree, sent back, per-relation write mode | one table per request | separate GraphQL mutations |
| Writes across relations | one transaction, dependency order resolved automatically | one row/table at a time | one transaction per mutation, written by hand or via plugins |
| Access control | Postgres roles + RLS, `SET LOCAL ROLE` per request | Postgres roles + RLS | Postgres roles + RLS |
| Client | generated TypeScript file, no build step | OpenAPI schema, third-party clients | generated GraphQL schema, GraphQL client |
| Static files & auth | built in — static file serving (including binary content served straight out of a table), username/password, OpenID Connect, and SAML | not included — front it with a reverse proxy and a separate auth service | not included — front it with a reverse proxy and a separate auth service |

Rel and PostgREST share the same trust in Postgres for access control. The difference is what
happens once a request needs to touch more than one relation at once: PostgREST embeds on
read but still wants one write per table; Rel treats the whole tree, read or write, as a
single unit. PostGraphile gets you there through GraphQL mutations, at the cost of a second
vocabulary — reads are queries, writes are a different shape entirely, and nested writes
usually mean reaching for `pg_functions` or a plugin. Rel only has one shape.

Rel is also more batteries-included as a deployment: PostgREST and PostGraphile both assume
you'll put something else in front of them for static assets, file uploads, and login —
rel serves all three itself, from the same binary, configured the same way as everything
else. See [Feature list](#feature-list) below for the rest of what ships in that one binary.

## Feature list

**Query language** — see [The query language](query-language/index.md)

- Arbitrary-depth joins in both directions, aggregates over a joined relation, computed fields
  (any Postgres function taking a row type as its whole argument, selectable like a real
  column), and queries rooted on or embedding a table-valued function.
- The same read-only query as a plain `GET`, URL-encoded — see [Querying with
  GET](query-language/get-requests.md).
- Named, pre-parsed, typed queries registered server-side — see [Well-known
  queries](query-language/well-known-queries.md).
- Row counts, `EXPLAIN` plans, the compiled SQL text itself, and a dry-run rollback, each an
  opt-in flag on any query — see [Complex queries](query-language/complex-query.md).
- Several unrelated queries or writes, batched into one transaction — see [Batching
  queries](query-language/batching.md).

**HTTP layer** — see [HTTP routes](http/index.md)

- Arbitrary server-side logic as a plain Postgres function — no separate app server, no ORM.
- Middleware, chained by path prefix, for cross-cutting concerns like an auth gate or logging.
- File uploads, received as `bytea` in Postgres or streamed straight to disk without routing
  their bytes through it at all — see [File uploads](http/uploads.md).
- Server-rendered HTML via Jet templates, with CSP nonces for trusted inline scripts — see
  [Rendering HTML with templates](http/templates.md).
- A built-in static file server, including binary or text content served directly out of a
  table instead of the filesystem — see [Static files](http/static-files.md).
- CORS and a configurable Content-Security-Policy, out of the box — see [CORS and
  CSP](http/cors-csp.md).

**Authentication** — see [Authentication](http/authentication.md)

- Username/password, OpenID Connect, and SAML, all shipped in rel itself, as many providers of
  each as you need — no separate auth service to run and keep in sync.

**Clients and tooling**

- A generated, dependency-free TypeScript client (`database.ts`): typed query building, typed
  results, no codegen step in your build — see [TypeScript client](typescript/index.md).
- The same introspected schema as plain JSON (`database.json`), for a non-TypeScript consumer
  or your own tooling — see [database.json](typescript/database-json.md).

**Operations**

- One binary, configured via environment variables, flags, or a TOML/YAML/HUML file, layered
  predictably — see [Configuration](configuration/index.md).
- A small, non-root, scratch-based Docker image, and a realistic deployment recipe (Postgres,
  a reverse proxy, automatic TLS) — see [Docker deployment](getting-started/docker-deployment.md).
- One `reload.cmd` hook to run your own migration tool before every schema reload, decoupled
  from any specific tool — see [Reload](configuration/reload.md).
