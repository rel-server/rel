# Legacy Web Frontend, Codegen Templates & Integration Tests

This document covers everything in `_legacy` that is not the Go server proper: the
`web/` frontend, the top-level `patron.ts` code generator, the `templates/*.pat`
files it compiles, `parser_prompt.txt`, and the `test/` integration-test suite.
Paths below are relative to `/home/chris/Code/rel/_legacy` unless stated otherwise.

## 1. Overview

`web/` is **not** a product UI — it is a single-page developer/test console for
poking at the server's query API by hand. There is exactly one screen (built in
`web/test.tsx`): a SQL-style code editor on the left, and tabs on the right showing
the JSON result, the generated SQL, and (if any) an error. It is meant for the
person building the query engine to type ad-hoc queries and see what comes back,
not for end users.

Confirming evidence:
- `web/test.tsx` renders a two-pane split view (`sl-split-panel`) with a `CodeJar`+Prism
  SQL editor on one side and a `sl-tab-group` (JSON / SQL / Error tabs) on the other.
- It keeps a query history in `localStorage` (`_query_history`, capped at 50 entries,
  `web/test.tsx:37,107-127`) and lets you re-run/delete past queries via an
  `sl-dialog` (`show_history()`).
- The whole app is one `node_append(document.body, …)` call — no router, no pages.

### Build tooling

- **Runtime/tooling: Bun**, not Node/npm/pnpm/Vite. This is a hard project
  convention, spelled out in `web/.cursor/rules/use-bun-instead-of-node-vite-npm-pnpm.mdc`:
  use `bun <file>`, `bun test`, `bun build`, `bun install`, `bun run`; use
  `Bun.serve()`/`Bun.file`/`Bun.$` instead of express/fs/execa; don't use `dotenv`
  (Bun auto-loads `.env`).
- `web/package.json` — package name `@salesway/dbrest`, `private: true`. Key devDependencies:
  `@biomejs/biome` (lint/format), `elt` + `elt-fa` (the UI framework and its FontAwesome
  icon set), `codejar` (minimal code-editor widget), `prismjs` (SQL syntax highlighting),
  `sql-formatter` (pretty-prints the SQL the server echoes back), `@typescript/native-preview`
  (`tsgo`, a fast native TS checker) and `wtsc` (a `tsc`-output post-processor/watcher).
  `typescript` itself is only a peerDependency.
- `web/justfile` defines two tasks:
  ```
  check:  biome check && (tsgo --noEmit --ignoreConfig ./static/scripts/*.ts | wtsc)
  format: biome format --write && biome lint --write && biome check --write --formatter-enabled=false --linter-enabled=false
  ```
  Note `check` only type-checks `static/scripts/*.ts` (i.e. `model.ts`/`query.ts`), not
  `test.tsx` — consistent with `biome.json`'s `files.includes` which also only covers
  `static/scripts/**/*.{ts,tsx}`. The interactive test app itself is left unchecked, and for
  good reason: `test.tsx` **cannot type-check standalone**. It references at least three
  identifiers that are never imported or declared anywhere in the file — `show(...)` (line 199,
  used to open the history dialog), `theme.classes.theme` (line 226), and
  `$tooltip("Ctrl+Enter")` (line 241) — while its `import` block only pulls from `codejar`,
  `elt`, `prismjs`, `sql-formatter`, `elt-fa/solid`, and the local `.style.ts` modules. These
  survive untouched into the minified bundle (e.g. `p(document.body,c1(theme.classes.theme))`
  and a literal `$tooltip("Ctrl+Enter")` call appear verbatim in `web/static/test.js`'s tail), so
  they must be supplied by something loaded outside this bundle at runtime (plausibly a global
  `elt`/Shoelace-integration script not present in this repo) — which also explains the otherwise
  unexplained `sl-button`/`sl-dialog`/`sl-split-panel`/`sl-tab-group` Shoelace custom elements used
  throughout the same file with no corresponding import.
- `web/biome.json`: 2-space is not enforced but line width 120, semicolons `"asNeeded"`,
  `noExplicitAny: "error"` under `suspicious`, import-organizing turned off.
- `web/tsconfig.json`: `jsx: "react"` with a **custom JSX factory** `jsxFactory: "E"` /
  `jsxFragmentFactory: "E.Fragment"` — this is `elt`'s own JSX runtime, not React.
  `moduleResolution: "bundler"`, `noUncheckedIndexedAccess: true`, `verbatimModuleSyntax: true`.
- `web/global.d.ts` declares `*.css` imports as default-exported strings (so `.tsx` files
  can `import css_text from "some.css"`), which the Bun bundler backs with
  `--loader:.css=text` (see the root `Makefile`, §7 below).
- The actual **build command** lives in the root `Makefile`, not in `web/`:
  ```
  web/static/test.js: $(webfiles)
      bun build web/test.tsx --bundle --outdir=web/static --loader:.css=text --minify --sourcemap
  ```
  This is what produces the checked-in `web/static/test.js` (+ `.js.map`) and
  `web/static/test.css` (+ `.css.map`) that main.go embeds and serves.
- UI toolkit: the app uses `elt` (custom-element-flavored reactive JSX: `o()` observables,
  `$click`, `$observe`, `node_append`, `Repeat`, `If`, custom tags like `e-flex`/`e-box`) plus
  what appear to be Shoelace web components (`sl-button`, `sl-dialog`, `sl-split-panel`,
  `sl-tab-group`, `sl-textarea` — the `sl-` prefix is Shoelace's convention), which are
  assumed to be already registered/loaded by something outside this bundle (no import of
  a Shoelace package appears in `test.tsx`).
- `web/json.style.ts` / `web/test.style.ts` are tiny `elt` `css` template-literal modules
  defining a handful of CSS classes (`.string`, `.property`, `.constant`, `.punctuation`,
  `.container`, `.clickable`, `.copied`, `.dialog`, `.error`) used by the JSON pretty-printer
  and the dialog/error views respectively. Nothing generic — purely for this test page.

## 2. `static/scripts/model.ts` and `static/scripts/query.ts`

These two files are the real "product" code in `web/` — a typed client-side query builder
that talks to the server's textual query language (referred to elsewhere as "tinysql") and
to a JSON `Query`/`Expression` AST that appears to be a second, JSON-based representation of
the same query language.

### `query.ts` — JSON query AST types

This file is pure TypeScript **type declarations** (no runtime code) describing the shape of
a JSON query the server is expected to understand:

- `Query` (`query.ts:50-97`): `relation: {relation, schema?}`, optional `write` (see below),
  optional `arguments` (positional or named — for calling table-valued functions), `where`
  (an `Expression`), `on_conflict`, `insert_columns`, `update_columns`, `rels` (nested joins,
  keyed by alias, each itself a `Query`), `select` (a `SelectExpression`), and `offset`/`limit`.
- `Write` (`query.ts:24-48`): a `mode` enum documenting the write semantics very precisely —
  `"readonly" | "insert" | "upsert" | "merge" | "mergenew" | "merge-update" | "update" | "deleteonly"`.
  Comments spell out exactly what each does (e.g. `"merge"` = delete rows not in payload that
  match the where clause, insert new ones, update existing ones — including recursively through
  relationships), plus `on_conflict`/`insert_columns`/`update_columns` overrides.
- `Expression` (`query.ts:142-174`): a big tagged-tuple union modeling a full SQL-like expression
  language as JSON arrays: unary/binary operators (`=`, `<>`, `in`, `like`, `~`, `::`, arithmetic,
  bitwise, jsonb operators `->`/`->>`/`?|`/`?&`, etc.), `between`, `concat`/`concat_ws`/`coalesce`/`format`,
  `["agg"|"aggregate", fn, args, filter?]` for aggregates over incoming relations, `["call", fn, ...args]`,
  the star operator `["*", ...exclude]`, `["arr"/"array", ...]`, `["lst"/"list", ...]`, and column
  selection tuples `["col", name, default_get?, default_set?]` / `["get", name, default?]` /
  `["set", name, default?]`.
- A worked example is included in a comment (`query.ts:176-197`) showing a `movie` query joining
  `actor` with a `where` filter and a `select` that omits some columns.

This file is almost certainly meant to line up with the Go `query` package's AST (types like
`AstSelection`, `WhereClause` appear in `templates/*.pat`, see §6) — i.e. it's the client-visible
JSON shape of whatever the Go query parser/planner builds internally. **This is inferred**, not
verified by reading Go source (out of scope for this pass), but the vocabulary (`readonly`,
`upsert`, `merge`, `on_conflict`, relationship-aware delete/insert) matches the DML template
logic seen in `templates/json_flat.pat`.

### `model.ts` — fluent client-side ORM-ish builder

`model.ts` builds on top of `@salesway/scotty` (an external `s.Serializer<T>` library for
JSON<->typed-object (de)serialization — a sibling project, not part of this repo) to provide a
fluent API for describing models and building queries against the **textual** query language
(not the JSON AST from `query.ts` — the two seem to be alternate front-ends to the same server
capability, and this file targets the string form):

- `Model` type (`model.ts:81-88`): `{ name, fields: Record<string, Serializer>, computed_fields?,
  relations: () => Record<string, Relation>, pk?: string[] }`. Relations are lazy (a function) to
  allow circular references between models.
- `Rel(model, multiple, columns, distant_columns?)` (`model.ts:602-609`) constructs a `Relation`
  descriptor — `multiple` distinguishes to-one vs to-many.
- `RowBuilder<M>` (`model.ts:449-560`) is the entry point for building a selection: `.all` (every
  field), `.fields(...)`, `.field(name)`, and `.rel(name, ...)` (three overloads: pass a nested
  selector, a callback `(s) => ...`, or nothing for "all fields of the related model"). `.rel`
  resolves the target model via `model.relations()[name]`.
- `select(model, cbk?)` (`model.ts:584-599`) is the main entry point, producing a `Selector`.
- `PropsSelector`/`Selector`/`RelationSelector`/`RelationSelectorMap` (`Resulter<T>` subclasses)
  each implement `.build()` (emit the query-language selection fragment, e.g. `{ * }` or
  `{ name, tags: tags { tag } }`), `.serializer()` (build/cached scotty serializer matching the
  selection shape), and `.init()` (produce a zero-value / hydrate a partial result).
  - `.where()` / `.order()` accept either a plain string or a **tagged-template** `` where`year = ${1999}` ``
    that auto-quotes values via `sql_value()` (`model.ts:40-57`, handling strings, numbers,
    booleans, `null`, `Date`, `Dayjs`, and falling back to a `$$...$$`-quoted JSON blob for
    anything else — dollar-quoting to avoid escaping issues, mirroring Postgres `$$` syntax).
  - `RelationSelector.readonly` marks a joined relation as read-only in the generated query
    (prefixes `readonly ` before the relation name, `model.ts:381-389`), matching the
    `IsReadOnly` relationship flag referenced throughout the `.pat` templates in §6.
  - `.asMap(extractor)` turns a to-many relation's array result into a `Map` client-side, via a
    custom scotty serializer wrapper (`RelationSelectorMap`, `model.ts:421-447`).
- `Selector` also exposes the actual network operations, all going through a single `query()`
  helper (`model.ts:4-28`) that does:
  - `fetch()`: `GET`/`POST` to **`/rel`** with header `X-Query: <tinysql>` (backslashes and
    newlines escaped so the query round-trips as an HTTP header value), `credentials: "include"`,
    and reads the JSON body.
  - `create(r, {dry_run})`: `upsert $body into <model><selection>` (or `rollback upsert ...` for a
    dry run), body = serialized instance.
  - `save(instance|instances, merge?)`: `merge $body into ...` or `upsert $body into ...`.
  - `delete(instance)`: `delete <model> where <pk1> = <v1> and <pk2> = <v2> ...` — requires `pk`
    to be set on the `Model`.
  - The `call(name, args_ser, ret_ser, args)` helper (`model.ts:31-38`) calls a **server-side
    function** by building a `name()`-shaped tinysql query and passing serialized args as the
    POST body — i.e. functions are called the same way as table selections, just with `()`.

**Server API surface this code assumes** (some certain from direct evidence, some inferred):
- A single endpoint, **`/rel`**, accepting `GET` and `POST`, that takes the query as a custom
  `X-Query` request header (URL-escaped, single-line, in a small textual query language —
  "tinysql") rather than in the URL or JSON body. This is directly confirmed in both `model.ts`
  and the demo app `test.tsx` (`fetch("/rel", { headers: { "X-Query": ... } })`).
  `test.tsx` additionally sends `X-Reply-Sql: "true"` on a separate `GET` to `/rel` to fetch back
  the compiled SQL for display, and reads an `X-Sql-Query` response header on error responses.
  (Certain — read directly.)
- The query language supports: selection shape via `{ field, field2, rel: { subfield } }`
  braces; `where <expr>`; `order <expr>`; write verbs prefixing the relation
  (`upsert $body into model{...}`, `merge $body into ...`, `delete model where ...`,
  `rollback upsert ...` for dry-runs); function calls as `name(args)`; and `$body` as a
  placeholder for the POST payload. (Certain — read directly from `model.ts`'s query-string
  construction and from `goserver_test.ts`'s literal query strings, §7.)
- A parallel **JSON query AST** (`query.ts`'s `Query`/`Expression`/`Write` types) that the same
  or a related endpoint may accept as a structured alternative to the tinysql string — this is
  inferred from the type definitions existing at all and from vocabulary overlap with the
  string-based write modes, but no code in `web/` actually POSTs a `Query` object; nothing here
  proves the JSON shape is wired to a live endpoint versus being forward-looking/aspirational
  design. (Inferred, flagged as uncertain.)
- No websocket usage appears anywhere in `web/`, `patron.ts`, or `test/` — the client-side test
  tooling in this repo is HTTP-request/response only. (Confirmed by grep for `WebSocket`,
  `ws://`, `wss://` across `web/`, `test/`, `patron.ts`: the only hits are boilerplate mentions
  inside `web/.cursor/rules/use-bun-instead-of-node-vite-npm-pnpm.mdc` — generic Bun-API advice,
  not actual usage. No real websocket client code exists in this part of the tree.)

## 3. Embedded static test page (`web/static/`)

- `web/static/test.html` is a barebones HTML shell: standard favicon/apple-touch-icon boilerplate,
  a `<link rel="stylesheet" href="./css/pgrel.css">`, and a single
  `<script type="module">import("./test.js?" + (new Date()).toString())</script>` — the
  cache-busting `?timestamp` trick forces a fresh module fetch on every page load instead of
  relying on browser caching, which matters because `main.go` serves these as static embedded
  files without any cache-control negotiation.
- `web/static/test.js` (+ `test.js.map`) is the **built output** of `web/test.tsx` (see the
  Makefile rule in §1) — a ~330KB minified bundle including Prism, its SQL grammar, `elt`,
  `elt-fa`, `codejar`, and `sql-formatter`. It is committed to the repo (not gitignored), so the
  built bundle ships alongside source — likely so `go build`/`go run` works without requiring Bun
  to be installed at server-build time unless `web/test.tsx` actually changed (see the Makefile's
  file-timestamp-based dependency rule).
- `web/static/test.css`/`.css.map` — the bundled CSS output (mostly the Prism "twilight" theme,
  confirmed by the embedded `code[class*=language-]` rules matching `prismjs/themes/prism-twilight.css`).
- `web/static/css/pgrel.css` is a small **hand-written**, separate stylesheet (not part of the Bun
  bundle, referenced directly from `test.html`) defining a `--pg-color-*` CSS variable palette
  and generic error-page styling (`.error-header`, `.stack-table`, `.line-number`,
  `.function-name`, `.file-path`, `.error-container`). This is confirmed shared with the Go
  server's HTML error pages: `error_pages.go:29` links `<link rel="stylesheet"
  href="/css/pgrel.css">` directly, and its class names (`stack-table`, `line-number`,
  `error-container`, etc.) match exactly. So this one stylesheet is served to and used by two
  unrelated pages — the query test console and the server's own crash/error page — rather than
  being test-console-specific.
- Serving: `main.go` has `//go:embed web/static/*` (`main.go:75`) and serves the embed under
  `SW_STATIC_PATH` (default `/static`, `main.go:214`), falling back to `index.html` for
  non-existent paths / directories (SPA-style fallback, `main.go:234-281`) — deliberately not
  re-documented in depth here since that belongs to the Go-backend writeup.

## 4. `patron.ts`

`patron.ts` (repo root, executable via shebang `#!/usr/bin/env bun`) is a **standalone code
generator**: a from-scratch TypeScript **port of a Go templating tool called "go-patron"**
(explicitly stated in its header comment, `patron.ts:3-12`). It compiles `.pat` template files
into plain `.go` source files that write to an `io.Writer` (or return a `string`), i.e. it's a
text/templating DSL embedded in `@`-prefixed directives inside otherwise-literal text, similar
in spirit to Go's own `html/template` but compiled ahead-of-time to real Go code rather than
interpreted at runtime.

Its pipeline, matching Go's typical lex→emit structure:
- **Lexer** (`class Lexer`, `patron.ts:182-674`) tokenizes into `Text | Space | Newline | Code
  (@{ ... }) | OutputCode (@(expr) or @identifier) | Control (@func/@if/@for/@switch/@case/
  @default) | Statement (@break/@continue/@return) | End (}) | String (@"..."/@'...'/@`...`) |
  Slice (@...{prefix}expr{suffix}|{separator} — a loop-with-separator shorthand) | Illegal`.
  It tracks line/column for diagnostics, supports nested Go-code balancing (`advanceGoCodeUntil`,
  handles quotes/comments/brackets), an `fmt.Sprintf`-style `%`-verb suffix on output expressions
  (`@identifier%.1f`), and a whitespace/indentation "collapse" pass (`collapseSpaces()`) that
  mimics template-engine whitespace trimming around control-flow tags so generated Go text output
  doesn't end up full of stray blank lines/indentation from the template source.
- **Codegen** (`generateGoCode`, `patron.ts:690-914`) walks tokens and emits Go statements:
  literal text becomes `ø.Write([]byte("..."))` (escaped), `@(expr)`/`@identifier` becomes a
  `.Write([]byte(expr))` (or `fmt.Sprintf` if a format verb was present), `@if/@for/@switch`
  become literal Go control flow, `@func ... { ... }` become real Go functions — detecting
  whether the function signature declares an `io.Writer` parameter (then writes into that
  variable) or returns `string` (then buffers into a local `bytes.Buffer` and returns
  `.String()` at the end). The `@...{prefix}expr{suffix}|{separator}` "Slice" token generates a
  `for _, x := range expr { ... }` loop with optional separator-on-every-iteration-but-first logic
  (implemented via a generated boolean flag `ø sepN`).
- **CLI** (`patron.ts:920-977`): `bun patron.ts <file.pat> [...]` — for each `.pat` file, writes a
  sibling `.go` file (swapping the extension) with a generated `package <parent-dir-name>` header,
  standard imports (`io`, `fmt`, `bytes`, `testing`), the generated function bodies, and a trivial
  `TestIncludeN` function whose only purpose is to keep those imports from being flagged unused
  if a particular template doesn't happen to need all of them.

It is wired into the root `Makefile`:
```
_templates: $(patfiles) patron.ts
    ./patron.ts $(patfiles)
    touch _templates
```
so `make server` regenerates `templates/*.go` from `templates/*.pat` before compiling — `patron.ts`
is a **build-time SQL-generator generator**, not something end users or the running server ever
invoke. The presence of a full from-scratch TS reimplementation of a presumably-existing Go tool
("port of the go-patron package") suggests the author wanted the templates buildable without Go
tooling present (e.g., in this repo's Bun-only `web`/tooling environment), or wanted an
easier-to-iterate lexer while prototyping the DML-generation templates in §6.

## 5. `parser_prompt.txt`

This file is essentially **empty** — 2 bytes, containing only the text `In` (no trailing
newline). Whatever prompt/methodology write-up was intended here was apparently never
written, or was truncated/cleared. It reveals nothing about the query parser's design; there is
no AI-assisted-codegen methodology to summarize from its actual content. Its filename and
placement next to `templates/` and the Go `query/` package are the only signal — presumably
a placeholder for a prompt that was meant to guide an LLM in writing/maintaining the query parser
or its Go-code templates, abandoned before being filled in. Worth flagging explicitly: **do not
assume any design intent from this file** — there is nothing there to extract.

## 6. `templates/*.pat` files

These are the two actual template sources compiled by `patron.ts` (§4) into the `.go` files
already checked into `templates/`. The templating engine/tool being targeted is **go-patron**
(per `patron.ts`'s own header comment), using `@`-directive syntax as described above (not
Go's stdlib `html/template`/`text/template`, and not any well-known third-party Go template
engine — this looks like an in-house minimal templating DSL, tuned for emitting SQL text with
fine whitespace control).

Both templates generate **recursive-CTE-style SQL** for nested write operations, driven by a Go
`*query.AstSelection` tree (from the `sales-way.com/server/query` package — confirmed via the
`import` block at the top of each `.pat` file) that models a (possibly deeply nested) selection
with resolved relationships, primary keys, and column metadata.

- **`json_table.pat`** → `templates/json_table.go`: a single function,
  `GenerateDeleteQuery(w io.Writer, sel *query.AstSelection)`. It emits:
  ```sql
  CREATE TEMP TABLE <TempDMLTableName> ON COMMIT DROP AS
  WITH <UniqueTableNameWith("delete")> AS (
    DELETE FROM <DbTableName>
    [WHERE <WhereClause>]
    RETURNING *
  )
  SELECT * FROM <UniqueTableNameWith("delete")>
  ```
  I.e. a simple, non-recursive delete against one table/selection, guarded by an optional WHERE
  clause. This is presumably used for plain `delete <relation> where ...` queries with no nested
  relationships to cascade through.

- **`json_flat.pat`** → `templates/json_flat.go`: the much larger, recursive DML generator, with
  four mutually-recursive functions:
  - `OutputDmlTable2(w, sel, do_delete, do_upsert)`: for each node in the selection tree (walking
    outgoing relationships first, then incoming), builds an `insert` CTE that reshapes rows out
    of a flat staging table `__flat_json_temp` (populated elsewhere, presumably by
    `jsonb_populate_record`/`json_to_recordset` from the client's POST body — the CTE itself does
    `jsonb_populate_record(NULL::<table>, jt.json) as obj`). Per column it picks the value from:
    the *other side* of an outgoing relationship's already-materialized insert CTE if the column
    is a foreign key managed by that relationship; the *distant* column of an incoming
    relationship if this column is populated from a child; a `CASE WHEN obj."col" IS NULL THEN
    <default> ELSE obj."col" END` if the column has a NOT-NULL default and isn't relationship-driven;
    else `obj.col` directly. Selection nodes are disambiguated by a synthetic
    `___selection_index`/`___node_id`/`___parent_id` scheme in the staging rows, letting one flat
    JSON payload represent an arbitrarily nested object graph.
  - `OutputDeleteStatement(w, sel, do_delete, do_upsert)`: recursively emits `DELETE ... RETURNING
    *` CTEs for rows that exist in the DB but are **not** present in the incoming payload —
    implementing the "merge" write semantics described in `query.ts`'s `Write.mode` (delete rows
    not matching the payload). For reverse (child→parent) relationships it further restricts
    deletion to rows whose foreign key is among those actually touched by the parent's own insert
    set, so an update to one parent doesn't wipe out unrelated children.
  - `OutputInsertStatement(w, sel, do_upsert)`: emits the actual
    `INSERT INTO <table> (...) SELECT ... FROM <insert-CTE> [ON CONFLICT (<pk>) DO UPDATE SET
    ...] RETURNING *` for each node, recursively for outgoing then incoming relationships.
  - `GenerateFlatQuery(w, op, do_delete, do_upsert)`: the entry point — wraps everything in
    `CREATE TEMP TABLE <TempDMLTableName> ON COMMIT DROP AS WITH __empty AS (SELECT true AS
    empty) <DmlTable CTEs> <Delete CTEs> <Insert CTEs> SELECT * FROM <root insert_for_real>`.

  In short: **`json_flat` is the code that turns one arbitrarily-nested JSON write payload into a
  single multi-CTE SQL statement that inserts/upserts/merges every level of the object graph in
  one round trip**, honoring per-relationship read-only flags and reversed (child-owns-fk) vs.
  forward relationships. This directly backs the `upsert $body into ...{ *, tags: @items_tags{
  * } }` style nested-write queries exercised in `goserver_test.ts` (§7).

## 7. Integration test suite (`test/`)

Bun-based (`bun test`), black-box integration tests that exercise a **real, running server
binary** against a **real Postgres**, both in Docker containers driven directly via `dockerode`
(not `testcontainers-go`/`testcontainers-node` — despite the Go side depending on
`testcontainers-go/modules/postgres`, this TS suite manages containers by hand).

- **`test/setup-containers.ts`**: 
  - Pulls (if missing) and starts a `postgres:17` container named `goserver-test-pg` with
    `POSTGRES_USER/PASSWORD/DB=app/app/app`, auto-remove on stop, exposing 5432.
  - Waits 2s, then starts a second container `goserver-test-web` whose entrypoint is
    `/server/server` — i.e. it **bind-mounts the host's freshly-built `../server` binary and the
    `./dmut` migrations directory** into the container (`Binds: [cwd/../server:/server/server,
    cwd/dmut:/dmut]`) rather than building a Docker image; env vars passed in:
    `PGRST_DB_URI=postgres://app:app@<pg-ip>:5432/app`, `PGRST_DB_ANON_ROLE=~anonymous`,
    `PGRST_DB_SCHEMA=api`, `SW_ENABLE_DEBUG=true`, `VIRTUAL_HOST=test.localhost`. (The
    `PGRST_*` env var names are PostgREST-style — worth noting as a legacy naming convention
    likely inherited from an earlier PostgREST-compatible design.)
  - Demuxes the web container's stdout/stderr to the test runner's own streams for live logs.
  - `cleanupContainers(event)` stops both containers in parallel (5s grace) and is registered as
    a `beforeShutdown` handler (see below) so containers get cleaned up on Ctrl-C/uncaught errors,
    not just normal test completion.
  - Both containers are labeled `com.sales-way.test: "true"`, which is exactly what the root
    `Makefile`'s `test-cleanup` target greps for to force-remove stragglers before a fresh run.

- **`test/before-shutdown.ts`**: a small generic utility (no test-specific logic) implementing an
  ordered async shutdown-hook registry: `beforeShutdown(fn)` queues `fn`, and a single handler
  runs all queued listeners in order on `uncaughtException`/`SIGINT`/`SIGTERM`/`beforeExit`, with
  a 15-second hard-exit failsafe (`forceExitAfter`) in case a listener hangs. Used only to make
  sure `cleanupContainers` fires reliably.

- **`test/goserver_test.ts`**: the actual test scenarios, all issued through a `_(str, payload =
  null)` helper (`goserver_test.ts:9-38`) that **always** `fetch`es
  **`POST http://<web-container-ip>:3001/rel2`** with header `X-Query: str` and, if `payload` is
  non-null, `JSON.stringify(payload)` as the body. It also inspects `X-Sql-Query`/`X-Sql-Args`
  response headers for debug logging (commented out) and expects the body to always be JSON,
  throwing on non-200 or unparseable responses. Note: **`/rel2`**, not the `/rel` endpoint used by
  the web console — see the oddity noted in §8.
  - `beforeAll`: spins up containers via `setupContainers()`, then polls `GET /heartbeat` up to
    30 times (1s apart) until it gets a 200 whose JSON body has `{ ok: true, dmut: "ok", db: "ok"
    }` — i.e. the server self-reports both DB connectivity and migration ("dmut") status through
    a health endpoint; if it ever reports an error status the whole suite aborts early
    (`cleanupContainers` + `process.exit(1)`) rather than letting individual tests fail one by one.
  - `describe("schema")`: calls `_("GET", \`${BASE_URL}/_schema\`)` (`goserver_test.ts:90`) and
    asserts the result is an object with a `Tables` object. **This does not actually hit
    `/_schema`** — because of `_`'s fixed signature above, this sends `POST /rel2` with
    `X-Query: "GET"` and a body of `JSON.stringify("http://.../_schema")`; the URL string is just
    junk payload data, never a request target. This test call is a leftover from what was
    presumably an earlier two-argument `(method, url)` fetch helper, and as written provides
    **no real coverage of a `/_schema` endpoint** — whatever it's actually asserting on is
    whatever the server returns for the literal tinysql query `GET`. See §8.
  - `describe("insert")`, sequential/stateful (each test depends on DB state left by the previous
    one, using the fixture schema from `test/dmut/index.dmut`, see below):
    1. **Seeding**: `insert api.groups` with 3 rows, no `where`.
    2. **Simple select after insert**: `api.groups` (bare relation selection) returns those 3 rows.
    3. **Nested/recursive insert**: `api.items{ *, tags:@api.items_tags{ * } }` inserting 2 items
       each carrying an inline `tags` array — exercises the `json_flat` nested-write templates
       from §6 in the "insert" (no existing rows deleted) path; then makes a second call,
       `_("GET", \`${BASE_URL}/rel/api.items_tags\`)` (`goserver_test.ts:119`), to assert 4 tag
       rows exist — this has the **same `_`-signature bug** as the schema test: it does not fetch
       `/rel/api.items_tags`, it sends `X-Query: "GET"` with the URL string as a JSON body to
       `/rel2`. Whatever value is asserted on (`sub.length === 4`) is not actually a targeted
       re-fetch of `items_tags`; treat this assertion as unreliable/misleading as written.
    4. **Nested insert with a `where` filter** on the parent
       (`api.items{ *, tags: @items_tags{*} } where id = 1`), replacing item 1's tags with one new
       tag — but the test comment flags this as a **known bug**: "FIXME: should rewrite the
       `json_table` query to also apply the where clauses" — i.e. the where clause is not
       correctly propagated to constrain which existing child rows get deleted during the merge.
    5. **Same nested insert without any `where`** — documented as **intentionally deleting
       everything** in `items_tags` except what's in the new payload, since there's no `where` to
       scope the merge/delete. The test explicitly calls this out as the case to be careful about
       and leaves a longer comment about wanting per-relation, filter-aware recursive deletion
       ("Hard.") — i.e. an open design problem, not yet solved, directly relevant to whatever
       merge semantics the rewrite adopts.
  - The `@api.items_tags` / `@items_tags` relation-alias syntax (`@` prefixing a relation name
    inside a selection) appears here as the textual query language's way of naming a joined
    relation — distinct from `@` in `.pat` files (unrelated syntax, different language/context).

- **`test/package.json`**: deps `dockerode` (+ `@types/dockerode`), `sql-formatter`; peer dep
  `typescript`. **`test/tsconfig.json`**: same Bun/bundler-flavored strict config as `web/`
  (though `jsx: "react-jsx"` here, unused since there's no JSX in `test/`). **`test/bun.lock`**:
  lockfile pulling in `dockerode`'s own dependency tree (`@grpc/grpc-js`, `protobufjs`, ssh2
  types, etc., since `dockerode` can also talk to remote Docker-over-SSH — unused here).

- **Running the suite**: root `Makefile`:
  ```
  test: test-cleanup
      cd test && bun test --bail=1
  test-cleanup:
      docker rm -f $$(docker ps -a -q --filter "label=com.sales-way.test") # if any
  ```
  i.e. `cd test && bun test --bail=1` (stop at first failure), after force-removing any stray
  containers labeled from a previous crashed run. There is no `test/justfile` or npm script —
  the `Makefile` is the only documented entry point for this suite.

- **`test/dmut/index.dmut`**: the fixture migration file the `goserver-test-web` container is
  bind-mounted onto (`/dmut`) and presumably runs on startup, defining the `roles`/`api` schema,
  `~anonymous`/`~authenticated` roles, and tables `api.items`, `api.items_tags`, `api.groups`,
  `api.users`, `api.reviews` (with FKs, generated PK ids, and `grant all ... to "~anonymous"` —
  matching the `PGRST_DB_ANON_ROLE=~anonymous` env var passed to the container) with blanket
  grants to `~anonymous`, matching this test suite's unauthenticated requests. The `.dmut` file
  format itself (migration DSL: `mutation <name> [depends on <other>] ... ;`) is documented
  separately in `legacy-docs/dmut-migrations.md` — not duplicated here.

## 8. Notes / things to reconsider for the rewrite

- **The `_schema` and one `items_tags` re-fetch assertions in `goserver_test.ts` test nothing
  real.** As detailed in §7, `_("GET", url)` always sends `POST /rel2` with `X-Query: "GET"` and
  the URL as a JSON body — a leftover from what looks like an earlier `(method, url)` fetch
  helper. Concretely this means: the `describe("schema")` test (`goserver_test.ts:87-94`) never
  calls `/_schema`, and the tag-count re-check inside "inserting recursive data"
  (`goserver_test.ts:119`) never calls `/rel/api.items_tags`. **`/_schema` currently has zero real
  test coverage** despite appearing tested, and the tag-count check is asserting on the response
  to a bogus `X-Query: "GET"` query rather than an actual re-fetch. For the rewrite's test suite,
  make the query-issuing helper's signature unambiguous (e.g. separate helpers for "run a tinysql
  query" vs. "hit a plain REST endpoint") so this class of silent-no-op mistake can't recur, and
  don't port these two assertions as-is without first deciding what they should actually check.
- **No Go implementation of "go-patron" exists in this checkout.** `patron.ts`'s header claims to
  be a "port of the go-patron package," but grepping `*.go` files under `_legacy` for `patron`
  turns up nothing, and neither `go.mod` nor the `Makefile` reference a Go patron tool or
  dependency — the only Makefile reference to "patron" is invoking `patron.ts` itself. Either the
  original Go tool lived in a different repo/history that isn't part of this checkout, or the
  comment is aspirational. Don't assume a reference Go implementation is available to consult.
- **`/rel` vs `/rel2` split.** The embedded web console (`web/static/scripts/model.ts`,
  `web/test.tsx`) talks to `POST/GET /rel` (registered in `pg2.go`, confirmed by grep), while the
  integration test suite (`test/goserver_test.ts`) talks to `POST /rel2` (registered in
  `pg3.go`). This means the shipped demo UI and the automated test suite are validating two
  different code paths — worth deciding early in the rewrite whether there should be one query
  endpoint or an explicit, intentional v1/v2 split, and make sure whichever demo UI ships is
  actually testing the one that matters.
- **`query.ts`'s JSON `Query`/`Expression` AST looks unused from the client side.** Nothing in
  `web/` or `test/` ever constructs or POSTs one; only the textual "tinysql" language
  (`X-Query` header) is exercised. Either this JSON shape is meant for a different, not-yet-built
  client (e.g. a future non-string query builder), or it's a design sketch that never got wired
  up. Worth explicitly deciding, for the new server, whether a JSON query AST is a first-class
  supported input or purely an internal representation.
- **Known, acknowledged bug in recursive merge/delete + `where`.** `goserver_test.ts`'s own
  comments flag that a parent `where` clause is not propagated into the generated DELETE for
  nested/child tables during a merge (`json_table`/`json_flat` templates, §6), and that with no
  `where` at all a nested upsert will silently wipe an entire child table. This is exactly the
  kind of correctness issue the new query-compilation approach should design against from the
  start rather than patch onto generated-SQL templates after the fact.
- **`parser_prompt.txt` is empty.** If the intent for the rewrite is to lean on LLM-assisted
  query-parser development, that intent was never captured here — there's no prior art to carry
  forward from this file specifically.
- **Custom Go-code templating via `.pat` + `patron.ts`, duplicated in two languages.** The team
  maintains both a Go implementation of "go-patron" (implied, not present in this checkout under
  `_legacy` — only referenced, not found) and a from-scratch Bun/TypeScript reimplementation
  (`patron.ts`) whose only job is to run at build time and regenerate `templates/*.go` before
  `go build`. For the rewrite, consider whether a recursive multi-CTE SQL-generation approach is
  still the right strategy at all (vs. building SQL programmatically in Go, or using a real Go
  templating engine/query builder library), and if templates remain useful, whether maintaining
  two parallel lexer/codegen implementations (Go + TS) is worth the upkeep cost versus just
  shelling out to the one canonical implementation from both places.
- **Build-artifact-in-VCS.** `web/static/test.js`/`.js.map`/`test.css`/`.css.map` are committed,
  generated files (regenerated by `make server` only when `web/test.tsx`'s dependency files
  change per Makefile timestamps) — normal for a single Go-embed binary that shouldn't require
  Bun at deploy time, but worth a deliberate decision in the rewrite (embed a build step in CI
  instead of committing artifacts, or keep the current "vendor the bundle" approach).
  `web/static/*.map` source maps are also embedded and presumably served in production
  (`//go:embed web/static/*` has no exclusion), which is a minor content-leak/size consideration.
- **`web/` toolchain only type-checks `static/scripts/*.ts`.** `web/justfile check` and
  `web/biome.json`'s `files.includes` both scope to `static/scripts/**/*.{ts,tsx}` — the actual
  demo app `test.tsx` (and the two `*.style.ts` files) are excluded from both linting and
  type-checking. Not necessarily a bug (it's throwaway test-console code) but worth being
  deliberate about in the new project: decide up front what tooling coverage a "just a demo/test
  client" deserves.
- **Hand-managed Docker containers instead of `testcontainers`.** `test/setup-containers.ts`
  reimplements pull/start/inspect/cleanup logic directly against `dockerode`, duplicating what
  `testcontainers-go` already does for the Go-side tests. If the new server's test strategy wants
  a single approach for both Go and TS-side integration tests, standardizing on one
  container-orchestration library (or running the TS integration suite against a server spun up
  by the Go tests, sharing one Postgres) would reduce duplicated infra code.
- **Stateful/sequential test ordering.** `goserver_test.ts`'s `insert` describe-block is a chain
  of tests that each depend on the database state left by the previous one (no per-test
  isolation/reset). This is fragile (order-dependent, `--bail=1` masks which step actually broke
  invariants) and worth revisiting for the new suite — e.g. per-test transactions/rollback or
  fresh schemas per test.
