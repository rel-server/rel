# Error Handling

Use https://github.com/samber/oops everywhere and provide context for all errors, taking care of using its immutable pattern (`oops.With(...)`, `oc.Wrapf(...)`/`oc.Errorf(...)` — see `pg/helpers.go`, `query/node_resolve.go` for the established call pattern). `oops` is already a real dependency, not just an aspiration : this document specifies how its `.Code(...)`, `.Public(...)`, and stack-capture facilities plug into the two problems below.

Logs log errors' full attached context (`logging.md ## Error integration with samber/oops`), regardless of what a client response is allowed to contain — the client-facing redaction this document specifies is about the HTTP response only, never about what gets logged server-side.

> Status: implemented. `errcode` (rel-internal codes), `pgerr.Classify` (RSxxx + PG_* classification, the `Detail` field allow-list), `config.Dev`, and both `/rel` (`server/response.go`) and `/route` (`route/response.go`) envelopes/gating are wired through. The `## Rel-internal codes` query-compile-error family (`UNKNOWN_IDENTIFIER`, `JOIN_MISSING_INDEX`, `WRITE_FORBIDDEN`, `WRITE_FORBIDDEN_FUNCTION_ROOT`, `QUERY_INVALID_EXPRESSION`) is now attached at each `query`/`pg` package raise site via `oc.Code(errcode.X)`, and read back by `server/rel.go`'s `codeOrUnclassified` — `errcode.Unclassified` is only the fallback for whatever residual case isn't covered by the taxonomy yet.

## Configuration

- `dev` (default false) : puts rel in dev mode to make it more verbose (in case of errors) especially when replying to clients

## Error codes

Two separate, non-overlapping code spaces feed one `code` field :

1. **Postgres-raised codes** — `RSxxx` and possible future `R`-prefixed families, 5 characters, constrained to what a real SQLSTATE can carry (`raise exception ... using errcode = 'RS404'`). This is a PL/pgSQL-authoring-time feature : a function body picks its own HTTP status (or, for a future family, some other stable signal) without rel needing to know about that function in advance. See `## Postgres-raised codes` below.
2. **Rel-internal codes** — everything rel itself classifies before or after ever touching Postgres : malformed requests, query-compile rejections, auth gates, infra failures. These never cross a `raise ... using errcode` boundary, so there's no reason to fit them into 5-char SQLSTATE shape — they're `SCREAMING_SNAKE_CASE` tokens, attached at the error's construction site via `oc.Code("UNKNOWN_RELATION")`. See `## Rel-internal codes` below.

`code` is **always present**, on every error response, including 500s — a code is a static, safe-to-send enum token (never derived from user input or internal state), and knowing "this was `DB_UNAVAILABLE` not `TRANSACTION_ERROR`" is exactly the kind of thing worth telling even an anonymous caller, since it's what lets a support conversation or a client-side error path distinguish failure kinds without parsing message text. There is no unclassified/silent case : an error that reaches a response with no more specific code gets `INTERNAL` (5xx) or `UNCLASSIFIED` (4xx) rather than an absent field — client code should never need to check for `code`'s presence, only switch on its value.

### Delivery

- **`X-Rel-Errorcode` response header**, set to the `code` value, on every error response from both `/rel` and `/route` — the one channel that works regardless of body framing (`/rel`'s JSON envelope, `/route`'s plain text, an in-flight route function's own arbitrary mimetype/template output once it's already started writing).
- **`code` field inside `RelErrorResponse`** (`/rel` only — its body is always JSON, so the header is redundant there but harmless ; a JSON API consumer would rather parse the body than inspect headers). `/route`'s built-in error paths (route lookup, request-body decoding, transaction handling — everything before or around an actual route function running) stay plain-text, unchanged from today ; the header is that path's only channel, deliberately, per `route.md ## Postgres Exceptions`'s existing "no separate JSON property" stance — extending that to full content-negotiated JSON error bodies would need a concrete driving case, which doesn't exist yet.

### Postgres-raised codes

Unchanged from the existing convention (`route.md ## Postgres Exceptions`, `pgerr` package) : `RSxxx` gives the response status `xxx`, the raised message becomes the body, and — new — `code` is set to the errcode itself (`"RS404"`), echoed in the header/JSON field like any other code.

`route.md` also already claims *"any other error code results in the error page with status 500, unless it's a known Postgres code with unambiguous HTTP semantics (e.g. permission denied → 401)"* — this is presently **aspirational, not implemented** : `pgerr.RSStatus` only recognizes the `RSxxx` pattern today: anything else, including `permission_denied`/`unique_violation`/etc., already falls through to a generic 500. No longer an optional follow-up : `## Postgres error detail` below makes this mapping load-bearing, not just cosmetic — it's what decides whether a constraint violation is safe to show a client in full, so it belongs in the same implementation pass as the rest of this taxonomy.

If implemented, the natural shape : a small fixed table of well-known SQLSTATEs → `(status, code)`, `code` namespaced `PG_` so it's visibly distinct from both `RSxxx` and rel-internal codes at a glance (`PG_PERMISSION_DENIED` → 403, `PG_UNIQUE_VIOLATION` → 409, `PG_FOREIGN_KEY_VIOLATION` → 409, `PG_NOT_NULL_VIOLATION`/`PG_CHECK_VIOLATION` → 400). Everything else stays `INTERNAL`/500. How much of the underlying Postgres text each of these is allowed to show — full detail always vs. dev-gated — isn't uniform across this list ; see `## Postgres error detail` below, which splits it by what the message could actually reveal, not by SQLSTATE membership alone.

### Rel-internal codes

Candidate taxonomy — a first pass, grouped by what a client can actually act on differently. Not all of these are equally worth having their own code ; marked where collapsing to a coarser one seems more honest than false precision.

**Transport / never reaches app logic**

| code | status | where |
|---|---|---|
| `METHOD_NOT_ALLOWED` | 405 | `/rel` GET/POST check — currently wrongly returns 400, see below |
| `ROUTE_NOT_FOUND` | 404 | `/route` lookup miss |
| `MALFORMED_BODY` | 400 | JSON/form parse failure |
| `UNSUPPORTED_MEDIA_TYPE` | 415 | wrong content-type (e.g. upload routes) |
| `BODY_TOO_LARGE` | 413 | over `http.max_body_size` |
| `MALFORMED_MULTIPART` | 400 | multipart decode failure |

> Found while drafting, not just documenting : `server/rel.go`'s "/rel only accepts GET or POST" path currently returns `badRequest` (400). That's really a 405. Worth fixing as part of landing this taxonomy, not left as a pre-existing inconsistency now that it has a code that says the wrong thing.

**Auth / authorization**

| code | status | where |
|---|---|---|
| `ANONYMOUS_DISABLED` | 401 | no anonymous role configured, no credentials |
| `ANONYMOUS_ROUTE_FORBIDDEN` | 401 | route not anonymous-authorized |
| `NO_ROLE_CONFIGURED` | 500 | `query.anonymous_role` unset and request anonymous — a deployment misconfiguration, not a request problem ; kept as its own code specifically so it doesn't read like an ordinary request failure in logs/monitoring |

**Query compile errors** — today one undifferentiated 400 bucket (`fmt.Errorf` message text only, no code at all). This is the biggest actual gap the taxonomy needs to close :

| code | status | covers |
|---|---|---|
| `QUERY_MALFORMED_JSON` | 400 | not valid JSON / wrong top-level shape |
| `QUERY_INVALID_EXPRESSION` | 400 | the operand-arity/shape family (`"query: %q needs..."`) |
| `UNKNOWN_IDENTIFIER` | 400 | unresolved relation/column/function/operator name |
| `JOIN_MISSING_INDEX` | 400 | `query-engine.md ## Join eligibility`'s compile-time rejection — actionable ("add an index"), worth distinguishing from a typo |
| `WRITE_FORBIDDEN` | 400 | non-writable identity, `write_mode` violations in general |
| `WRITE_FORBIDDEN_FUNCTION_ROOT` | 400 | the function-rooted-node case specifically (`query-engine.md ## Reading Algorithm ### Function-rooted nodes`) — split from the generic one since it's a distinct, documented rule a client might want to explain differently |

Well-known query codes (`WELL_KNOWN_*`) are their own family, listed in full in
`well-known-queries.md ## Compilation Errors` / `## Execution Errors` rather than duplicated
here.

**Runtime / infra** — mostly 500 ; a client can't act differently on "commit failed" vs "connection acquire failed," so collapsing these to one `INTERNAL` may be more honest than manufacturing false precision. Listed separately here so each can be vetoed/merged individually rather than deciding unilaterally :

| code | status | where |
|---|---|---|
| `DB_UNAVAILABLE` | 500 | acquiring a connection failed |
| `TRANSACTION_ERROR` | 500 | begin/commit/rollback failures |
| `TEMPLATE_ERROR` | 500 | `/route` template rendering |
| `UPLOAD_IO_ERROR` | 500 | mkdir/create/rename/swap failures |

**Upload-specific** — already has real HTTP-status granularity in code today, just needs codes attached :

| code | status |
|---|---|
| `UPLOAD_CONFLICT` | 409 |
| `UPLOAD_TOO_LARGE` | 413 |
| `UPLOAD_WRONG_TYPE` | 415 |

## Postgres error detail

`RelErrorResponse.pg_error` (present only when the failure came from a Postgres error, per its own doc comment) is **always logged in full, server-side, regardless of mode** — this section is only about what a *client response* is allowed to contain, never about what gets logged.

A blanket dev-only gate on every Postgres error throws away real value : a foreign-key or unique violation has always been a legible, actionable signal — "this row already exists," "that reference doesn't point at anything" — and a client reasonably wants to interpret it and show something better than a generic message, in production too, not only when a developer happens to be poking at a dev deployment. So the gate isn't "is this from Postgres," it's **"does this message reveal anything beyond what the client's own request already implied."** Three tiers, not one switch :

1. **Constraint violations the client's own submitted data triggered** — `PG_UNIQUE_VIOLATION`, `PG_FOREIGN_KEY_VIOLATION`, `PG_NOT_NULL_VIOLATION`, `PG_CHECK_VIOLATION`. The constraint targets columns of a relation already named in the request's own `query`/`data` — the client supplied the offending value, so the constraint name and column names aren't new information to them. **`pg_error` is included unconditionally, in production too** — but built from an explicit field allow-list (below), never `pgErr.Error()`'s full text or the raw struct. `error` gets a short synthesized message per violation kind (e.g. `"a unique constraint was violated"`), still safe and readable without the raw text ; `pg_error` carries the allow-listed fields alongside it for a client that wants the constraint/column specifics.
2. **`PG_PERMISSION_DENIED`** — status/`code` (403) are always shown, same as any other classified error, but the *message* stays a fixed generic string (`"insufficient permissions for this operation"`) in production. Unlike a constraint name, the object name Postgres's raw permission-denied text may include can reveal the *existence* of something an unprivileged caller had no other way to confirm — exactly the fingerprinting-via-error-text risk `specs/TODO.md` already flags. Full raw text only under `dev: true`.
3. **Everything unclassified** (falls through to `INTERNAL`/500 — not one of the recognized SQLSTATEs above) — no way to know in advance what an arbitrary Postgres error might contain, so this tier keeps the conservative default : `pg_error` omitted, `error` generic, in production ; both included only when `configuration.md`'s `dev` key is `true`.

This means `dev` mode's actual job shrinks to "show me the unclassified tail and stack traces" — the classified, client-data-caused cases (tier 1) were never the risk it was meant to guard, and gating them anyway was papering over the fact that `pgerr` doesn't classify anything but `RSxxx` yet (`## Postgres-raised codes` above). Implementing the `PG_*` table is now doing double duty : better status codes AND the thing that decides what's safe to always show.

Deliberately NOT a knob : gating any of the above by whether the *caller* is authenticated, rather than by tier. Considered and rejected — "authenticated" mostly means "completed signup," which in an app with self-service registration or OAuth is a near-zero bar ; it doesn't track the actual risk in either direction (tier 1 is safe regardless of who's asking, since it's about the caller's own data, not their privilege ; tier 2/3 are risky regardless of who's asking, since a self-registered or compromised session is just as capable of probing as an anonymous one). Gating on it would train operators to believe there's a security boundary that isn't really there. `dev` stays a deployment-level switch (an operator's own staging vs. production choice), not a per-request one. A future, *narrowly scoped* escalation — e.g. a specific `debug` JWT claim for an internal tool — is a legitimate separate feature if a concrete need shows up ; not folded into this tiering now.

### `pg_error` field allow-list

Built from `pgconn.PgError`'s fields, never its full `.Error()` string (which today is just `Severity: Message (SQLSTATE Code)` — narrower than the struct, but still `Message`-only, which is the field tier 2/3 gate specifically). Included, tier 1 only : `Message`, `Detail`, `SchemaName`, `TableName`, `ColumnName`, `ConstraintName` — all describe the shape of data/objects the client's own request already named. Excluded, always, every tier : `Where`, `InternalQuery`, `Position`, `InternalPosition`, `File`, `Line`, `Routine` — these describe rel's own generated SQL and Postgres's internal execution path, not the client's data, and are exactly the codegen-fingerprinting risk from `## What's unsafe` below.

> Caveat, not solved here : tier 1's reasoning assumes a constraint violation is self-referential — about the table/columns the client's own request touched. A trigger-cascaded write can, in principle, raise a constraint violation naming a table the request never mentioned. This isn't defended against by the tiering above ; flagging as a known residual edge case rather than pretending it's covered.

## What's unsafe (and why), concretely

Grounded in what actually flows through this codebase's real error paths, not a generic "internals" gesture :

- **Object/schema existence an unprivileged caller couldn't otherwise confirm** — `PG_PERMISSION_DENIED`'s `Message` is typically `"permission denied for table foo"`, confirming `foo` exists to a caller who may have no other way to know that. This is `specs/TODO.md`'s already-flagged schema-enumeration-via-error-fingerprinting risk, and it's why tier 2 keeps `Message` generic rather than just narrowing which fields are shown.
- **Rel's own generated SQL, if codegen ever produces a bad query.** A Postgres syntax error embeds a fragment of the actual query text at fault — if that query is rel's own (not the client's), the fragment can expose internal alias schemes, the `_data` staging table name, join-strategy artifacts. `pg/helpers.go`'s `oops.With("query", query)` attaches the raw SQL as *structured context*, not as part of `.Error()`'s string (confirmed : `oops.OopsError.Error()` returns only `"message: wrapped_error"`, never the `.With()` context) — so this isn't leaking today, but it's a live trap : **any future call site that renders `%+v` or serializes `.ToMap()`/`.Context()` into a client-facing response defeats this entire design.** Client-facing serialization MUST only ever use `.Error()` or `oops.GetPublic(...)` — never `%+v`, never `.Context()`/`.ToMap()`. Worth a lint/review rule, not just documentation, once this lands in code.
- **Server filesystem layout.** `route/upload_handler.go`'s `os.MkdirAll`/`os.OpenFile`/`os.Rename` failures wrap `*os.PathError`, whose `.Error()` is literally `"open /var/lib/rel/uploads/.upload-<token>: permission denied"` — full server path, directory layout, sometimes the OS user rel runs as. Nothing stops this unwrapping through a `%w` chain into a response today ; it needs the same generic-in-prod treatment as tier 3.
- **DB connectivity detail.** Connection-acquire failures can embed host:port from pgx's own error text (never a password — pgx doesn't include that) — real infra topology nonetheless.
- **Secrets accidentally interpolated into error text.** Not hypothetical — `config/reader.go`'s `logErr` already states this exact rule for config values (`$FILE$` can put a resolved secret at any path, so an error message must never embed the resolved value, only the path/key). The same rule applies wherever any wrapped error might echo back a sensitive value rather than just its identifying key — this document generalizes what `config` already enforces locally, not inventing a new principle.
- **Stack traces / dependency fingerprinting** — package import paths and dependency versions visible in a frame are real recon value (look up known CVEs for the exact version shown). Already covered by `## Stack traces`' existing dev-gate ; restated here as the same category of risk as the above, not a separate one.

## Stack traces

`oops` captures a stack trace automatically at error-construction time (`oc.Errorf`/`oc.Wrapf`) — nothing extra needs to be threaded through call sites for this ; `OopsError.Stacktrace()` / `StackFrames()` are already there to read back.

**Eligibility : 5xx only, never 4xx.** A stack trace has no debugging value for a 400 — the client sent something rel correctly rejected, there's no bug to point at, and shipping one anyway just trains people to skip past the noise. Gating on status class rather than a hand-picked per-code list also means every future 500-class code is automatically covered with no separate flag to remember to set.

**Sent only when `dev: true`, same gate as `pg_error`, same reasoning** : a stack trace leaks source file paths and package layout — real information disclosure, not cosmetic — so it stays off by default.

**Delivery** : `RelErrorResponse` gains a `stacktrace?: string[]` field (one frame per entry, from `OopsError.StackFrames()`), present exactly when both gates (5xx, `dev: true`) hold. For `/route`'s plain-text error bodies, the trace is appended after the message, separated by a blank line, under the same two gates — there's no JSON body to add a field to there, and plain text is already the existing convention for that path, so this doesn't introduce a new response shape for it.

This supersedes this document's earlier "a special page... displaying the stack trace" idea (never built, `specs/TODO.md` had it flagged as an implementation gap) — a dedicated dev-mode HTML page would duplicate what a `dev: true` JSON/text field already gives a developer, for one extra rendering path to maintain. Dropping the HTML-page idea in favor of the field-based approach above ; flagging the change explicitly since it was written down before, not silently dropping it.

## `RelErrorResponse`, updated shape

```typescript
interface RelErrorResponse {
  status: "error"          // literal discriminant, always this string
  code: string              // always present — see ## Error codes
  error: string               // human-readable message
  pg_error?: PgErrorDetail     // present for a classified constraint violation (always) or PG_PERMISSION_DENIED/unclassified Postgres error (dev: true only) — see ## Postgres error detail
  stacktrace?: string[]         // dev: true only, and only for a 5xx response
}

// The ### `pg_error` field allow-list, structured — not the single string
// an earlier draft of this section specified. A client wanting "which
// column" gets it machine-readably instead of parsing a message string.
interface PgErrorDetail {
  message: string
  detail?: string
  schema_name?: string
  table_name?: string
  column_name?: string
  constraint_name?: string
}
```

`error`'s content itself is status-gated, not just the two fields above — found while drafting this, not a pre-existing documented behavior : today `error` is unconditionally `err.Error()`, the full wrapped error chain, sent to every client regardless of mode. For a 4xx that's fine (the message is inherently about the client's *own* malformed input — nothing server-internal to leak). For a 5xx it's the same disclosure shape as `pg_error`/`stacktrace` : a wrapped chain can easily embed a file path, a DSN fragment, an internal package name. So : for a 5xx response, `error` is a fixed, safe, generic string in the default mode (`"internal error"`, or an `oops.Public(...)` message set explicitly at the call site via `oops.GetPublic(err, "internal error")` when a more specific-but-still-safe message is worth giving) ; the full chain only appears in `error` when `dev: true`. 4xx `error` text is unchanged — still the full descriptive message, since it was always safe.
