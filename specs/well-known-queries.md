# Well Known Queries

They're basically what views achieve in SQL, but in rel, with the write operations planned for.

- rel reads them on startup, but also when receiving `SIGUSR2`
- rel reevaluates them whenever the database is introspected

> Question: these two bullets need to name two distinct triggers precisely, since "read" and "reevaluate" aren't the same operation. Reintrospection already happens on startup AND on `SIGUSR1` (`boot/reload.go`'s existing 7-step reload : re-run migrations, `pg.ReIntrospect`, rebuild the rpc registry, rebuild the mux, atomic-swap). Proposed division : `SIGUSR2` re-reads the well-known query *files* from disk (picks up added/edited/removed files) ; any reintrospection event (startup, or `SIGUSR1`) re-resolves/recompiles the *currently loaded* well-known query set against the fresh schema, without re-reading files from disk. Confirm this split, and confirm `SIGUSR2` reuses `boot/reload.go`'s existing maintenance-window/drain/atomic-swap discipline rather than a separate mechanism (nothing in-flight should ever see a half-reloaded well-known query set, same reasoning `SIGUSR1` already applies to the schema/mux) ?
>:

## Configuration

* `pg.query.wellknown_path` (default `'/wellknown'`) : a colon `:` separated list of directories containing well-known queries in json, yaml or ruml format.

>: What other configuration options would be relevant ?

> Question: the config field for this already exists (`config.Pg.Query.WellKnownDirs`, `config/config.go:340`) but is stored unsplit, exactly the state `http.static.path` was in before static serving landed — its own colon-split happens at the *consumption* site (`static/static.go:56`, `strings.Split(cfg.Static.Path, ":")`), not in the config loader. Proposal : mirror that exactly, split at whatever function first walks `wellknown_path` for loading. Confirm ?
>:
>
> Question: `http.static.path`'s own doc (`config/help.go`) states a missing/nonexistent directory in the list is silently skipped, not an error. Does `wellknown_path` follow the same rule ?
>:
>
> Question: any cap on recursion depth or file count while walking the directories ? Nothing else in this codebase defends against a pathological directory tree, so "none" is a reasonable answer, just confirming it's deliberate rather than unconsidered.
>:

## Behaviour

Rel reads the directories of `pg.query.wellknown_path` recursively and consider every .json, .yml, .yaml, .ruml files whose name doesn't start with '_'.

> Question: `ruml` doesn't exist anywhere in this codebase or its dependencies (`go.mod`/`go.sum`) — it's mentioned only in this spec file, nowhere else. Either it needs its own grammar defined before it can be implemented, or it should be dropped as a supported format for now (JSON + YAML only) and reconsidered later if there's a concrete need. Which ?
>:
>
> Question: JSON parsing throughout this codebase runs on `sonic/ast` (`query.ParseExpression`/`ParseQuery`), a JSON-specific parser — there's no generic-value-tree parse path today. A `.yml` file needs either (a) a YAML→JSON-bytes conversion pass before handing it to the existing JSON parser, or (b) a second, format-agnostic entry point into the same resolve/parse logic. (a) is far less code and reuses everything that exists ; (b) would matter only if YAML's own type system (e.g. distinguishing an explicit `null` from an absent key, or richer scalar types) needs to survive into the query tree in a way JSON round-tripping would lose. Proposal : (a), unless there's a concrete reason YAML's extra expressiveness is actually needed here. Confirm ?
>:

If a query has an error, a warning is displayed, and the query is deactivated. If a query introduces a name already existing, rel prints a warning a deactivates all queries on that name, replying instead an error when it is queries.

> Question: this sentence's ending is ambiguous enough to change the actual behavior, not just a wording nit — "replying instead an error when it is queries" could mean (a) a request naming a collided query gets some distinct error code/status forever (until the collision is fixed and rel reloads), or (b) something else entirely. Proposed concrete rewrite : *"If a query introduces a name that collides with an already-loaded one, rel logs a warning and deactivates every well-known query registered under that name (not just the newest one) — a request naming a deactivated query gets `RW001`/whatever it's renamed to (see below), the same way a genuinely unknown name would, rather than silently picking one of the colliding definitions."* Confirm this is the intent, or correct it directly here ?
>:
>
> Question: is `name` purely the value declared inside the file (as `## Definition` shows), with the directory layout under `wellknown_path` contributing nothing to it — so two files in unrelated subdirectories can collide on the same declared `name` with no path-based disambiguation ? That's what the spec as written implies ; confirming it's deliberate.
>:

Well-known queries are not meant to be mixed-and-matched with other queries ; they're evaluated once and their statements are prepared, ready to be queried for maximum performance.

> Question: "prepared" needs to mean one specific thing before this can be built, since the two readings lead to different implementations : (a) genuine Postgres `PREPARE`, which is connection-scoped and awkward under a connection pool (a statement prepared on one pooled connection doesn't exist on another, so this would need a prepare-on-acquire hook, or pinning well-known execution to specific connections) ; or (b) "the SQL text is compiled once, at load time, into a string handed to whichever pooled connection serves a given request" — in which case pgx's own default per-connection statement cache (`QueryExecModeCacheStatement`) already gives the performance property for free, and rel doesn't need to do anything beyond not recompiling the SQL text itself on every request. Proposal : (b), since it needs no new execution-layer machinery beyond caching the compiled `*writer.SQLWriter`/SQL string in memory. Confirm ?
>:
>
> Question: does the read/write split for one stored `WellKnownQuery` follow the same wire-level rule `/rel` already uses — a request supplying `data` always runs the full phased Writing Algorithm (`ExecuteWriteState`) off the query's already-resolved tree, one omitting `data` always runs a plain compiled `SELECT` off the same tree — so ONE definition can serve both a read and a write invocation depending on what a given request supplies, never fixed at definition time ? This matters because "evaluated once... statements are prepared" (previous question) is a much simpler story for the read half (one cached SQL string) than the write half (`ExecuteWriteState` always denormalizes the request's own payload fresh, so "prepared" can only ever mean "the tree is pre-resolved," not "the DML is precompiled text"). Confirm this reading, or state whether a well-known query is meant to commit to read-only or write-capable at definition time instead ?
>:

The shape of their output is known and exported in typescript.

## Definition

A well-known file contains either one `WellKnownQuery` or `WellKnownQuery[]`. It defines a query in the Rel JSON Query Language ; the only difference is that it declares parameters that can then be used by the query with `["$param", param_name: string, optional_cast?: string]`


```typescript
interface WellKnownQuery {
  name: string
  params: {
    [name: string]: WellKnownParam
  }
  query: QueryNode
}

interface WellKnownParam {
  type?: string // any postgres type that the json value will then cast to - unless if it is JSON since it will be JSON by default
  default?: unknown // must be of the specified type or castable. Can be null to indicate non-requiredness
}
```

> Question: `WellKnownParam.type` (declared once, for the whole query) and `["$param", name, cast?]`'s own `cast` (declared per usage site) both name a Postgres type for the same param — the spec doesn't say what happens when a `$param` usage's `cast` disagrees with its declaration's `type`, or why a per-usage override would ever be needed if the param already has a declared type. Proposal : drop `$param`'s own `cast` entirely and rely solely on `WellKnownParam.type` — a param has exactly one type, declared once, matching how an ordinary column's type is never re-specified per reference either. If there's a real use case for a per-usage override (e.g. the same param used as both `text` and `int` in different branches of one query), say so instead and the two-type-sites design stays, with an explicit precedence rule. Which ?
>:
>
> Question: `default?: unknown` conflates three states behind one nullable field — required (no `default` key at all, matching `RW011`'s "did not have a default"), optional-defaulting-to-SQL-NULL (`default: null`), and optional-defaulting-to-a-value (`default: <value>`). Distinguishing "key absent" from "key present with value `null`" needs presence-aware JSON decoding on the Go side (a plain `map[string]any` lookup can't tell them apart once decoded) — worth stating explicitly so the loader is built with that distinction from the start rather than discovered as a bug later. Confirm these are the intended three states ?
>:

> Question: `["$param", ...]` parses fine (`ParamExpr{Name, Cast}`, `expression_parse.go:412-427`) and resolves as a no-op passthrough (`expression_resolve.go`'s `default:` case, no validation against a declared param set), but codegen hard-errors on it today — `sql_expr.go:175-176`, `"sql: $param (well-known query parameters) are not yet supported by codegen"`. The real gap isn't the switch case itself, it's that `SQLWriter.Bind` (`writer/pg.go`) binds a *value* into `args` immediately at compile time, and a well-known query's whole premise is compiling once while the param's actual value only exists per-request. So `$param` needs either : (a) compile to a numbered placeholder reserved by name, with a separate step at *request* time that assembles `args` in `$N` order from the request's own `params` object (looking up each reserved name) ; or (b) something else entirely. (a) seems like the natural fit given the existing `Bind`/`Args()` mechanism, but needs a new method alongside `Bind` (e.g. `BindParam(name string)` that reserves a slot without a value) rather than reusing `Bind` as-is. Confirm (a), or describe an alternative ?
>:
>
> Question: `RW002`/`RW003` (unused param / non-existing param reference) are load-time checks that need something to walk the *fully parsed* expression tree collecting every `ParamExpr.Name` actually referenced, then diff that set against the declared `params` map's keys — nothing today walks a tree purely to collect `ParamExpr` occurrences (resolution passes over it without recording anything). Confirm this collection happens once, at file-load time, before the tree is cached/reused for every subsequent request — not re-walked per request ?
>:

## Querying

Wellknown query are available on the endpoint `/wellknown`, who expects

```typescript
type WellknownQuery {
  name: string
  params?: {
    [name: string]: unknown
  }
  data?: unknown
}
```

`data` should only be supplied for write queries. `POST` and `GET` are available for wellknown just like for `/rel` for easy querying capabilities.

> Question: nothing here says whether `/wellknown` goes through the same auth pipeline `/rel` and `/rpc` share — `jwt.Middleware`'s Verify, then `check_session`/Renew/`SET LOCAL ROLE` (`server/rel.go`'s `applyRole`, `rpc/handler.go`'s equivalent, deliberately unified to run in the same order on both — see the recent session that fixed their ordering divergence). A third endpoint with no stated auth behavior risks silently reinventing or skipping this. Confirm `/wellknown` reuses the exact same steps (and, mechanically, gets mounted through `boot.BuildMux` alongside `/rel`/`/rpc`/`/static` so `websec.Middleware`'s CORS/CSP and `logging.RequestMiddleware`'s request-id logging apply to it automatically, the same way they already apply uniformly to the other three) ?
>:
>
> Question: response shape isn't stated — same manual `["`,`","`,`"]"` streaming + `RelErrorResponse` JSON error envelope `/rel` uses (`specs/error-handling.md`, `server/response.go`), or something specific to `/wellknown` ? Given the stated goal is "querying convenience... just like `/rel`," reusing `/rel`'s envelope verbatim seems like the default assumption — confirm, or specify what differs.
>:
>
> Question: `/rel`'s GET path decodes a bare `Relation` from the query string (`querystring.DecodeRelation`) — nothing decodes a `{name, params, data}` shape today, and this one can't just copy `/rel`'s pattern since the shape itself is different (a name plus a nested params object, not a single flat relation). Two candidate URL shapes : (a) `/wellknown/{name}?paramA=1&paramB=2` (name as a path segment, params flat in the query string) ; (b) `/wellknown?name={name}&params.paramA=1` (everything in the query string, `/wellknown` itself stays a single flat endpoint like `/rel` does). (a) reads more naturally as "invoking a named thing," (b) keeps `/wellknown` structurally parallel to `/rel`'s own single-endpoint-plus-query-string design. Which, and what happens to `data` on GET — forbidden outright (matching "data should only be supplied for write queries," and GET requests conventionally carrying no body-shaped intent), or is there a real case for a GET-with-data ?
>:

## Compilation Errors

> Question: `RWxxx` doesn't fit either of the two error-code families `specs/error-handling.md` actually establishes — `RSxxx` is reserved specifically for a PL/pgSQL author's own `raise ... using errcode`, and every rel-internal code (auth gates, malformed requests, query-compile rejections) is `SCREAMING_SNAKE_CASE` via `errcode.Code`, delivered through the same `X-Rel-Errorcode` header/`code` field every other error uses (`errcode/errcode.go`'s own doc comment is explicit that this is the *whole* rel-internal family — no numbered sub-scheme). A fourth, `RWxxx`-numbered family would be new, unprecedented, and inconsistent with how a client reads every other error this API produces. Proposed replacement names, following the existing `QUERY_*`/`WRITE_*` naming already in `errcode.go` :
>
> | old | proposed |
> |---|---|
> | `RW001` | `WELL_KNOWN_DUPLICATE_NAME` |
> | `RW002` | `WELL_KNOWN_UNUSED_PARAM` |
> | `RW003` | `WELL_KNOWN_UNKNOWN_PARAM` |
> | `RW010` | `WELL_KNOWN_PARAM_TYPE_MISMATCH` |
> | `RW011` | `WELL_KNOWN_PARAM_REQUIRED` |
>
> Confirm the rename (exact names negotiable), or say why a separate numbered family is actually wanted here despite the inconsistency ?
>:

- `RW001` : duplicate well-known name
  Two well-known queries intended to register the same name
- `RW002` : unused param
  The query declares a parameter it doesn't use
- `RW003` : non-existing param
  A `[$param]` statement calls a non-existing param

## Execution Errors

- `RW010` : a supplied param was of the wrong type
- `RW011` : the user did not specify a param that did not have a default
