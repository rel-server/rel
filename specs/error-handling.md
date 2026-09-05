# Error Handling

Use https://github.com/samber/oops everywhere and provide context for all errors, using its immutable pattern (`oops.With(...)`, `oc.Wrapf(...)`/`oc.Errorf(...)` — see `pg/helpers.go`, `query/node_resolve.go` for the established call pattern). This document specifies how `.Code(...)`, `.Public(...)`, and stack-capture plug into client responses and logging.

Logs record an error's full attached context regardless of what a client response is allowed to contain (`logging.md ## Error integration with samber/oops`). Client-facing redaction, specified below, only governs the HTTP response — never server-side logs.

Implemented. `errcode` (rel-internal codes), `pgerr.Classify` (RSxxx + PG_* classification, the `Detail` field allow-list), `config.Dev`, and both `/rel` (`server/response.go`) and `/route` (`route/response.go`) envelopes/gating are wired through. The `## Rel-internal codes` query-compile-error family (`UNKNOWN_IDENTIFIER`, `JOIN_MISSING_INDEX`, `WRITE_FORBIDDEN`, `WRITE_FORBIDDEN_FUNCTION_ROOT`, `QUERY_INVALID_EXPRESSION`) is attached at each `query`/`pg` package raise site via `oc.Code(errcode.X)`, and read back by `server/rel.go`'s `codeOrUnclassified` — `errcode.Unclassified` is the fallback for any case not yet covered by the taxonomy.

## Configuration

- `dev` (default false) : puts rel in dev mode, more verbose error replies to clients.

## Error codes

Two separate, non-overlapping code spaces feed one `code` field :

1. **Postgres-raised codes** — `RSxxx` and possible future `R`-prefixed families, 5 characters, constrained to SQLSTATE shape (`raise exception ... using errcode = 'RS404'`). A PL/pgSQL function body picks its own HTTP status this way without rel needing to know about that function in advance. See `## Postgres-raised codes`.
2. **Rel-internal codes** — everything rel itself classifies before or after touching Postgres : malformed requests, query-compile rejections, auth gates, infra failures. `SCREAMING_SNAKE_CASE` tokens, attached at the error's construction site via `oc.Code("UNKNOWN_RELATION")`. See `## Rel-internal codes`.

`code` is always present, on every error response, including 500s. An error that reaches a response with no more specific code gets `INTERNAL` (5xx) or `UNCLASSIFIED` (4xx) — never an absent field. Client code switches on `code`'s value without checking for its presence first.

> Why: a code is a static, safe-to-send enum token, never derived from user input or internal state — distinguishing `DB_UNAVAILABLE` from `TRANSACTION_ERROR` is worth telling even an anonymous caller, since it lets a support conversation or client-side error path branch without parsing message text.

### Delivery

- **`X-Rel-Errorcode` response header**, set to the `code` value, on every error response from both `/rel` and `/route` — the one channel that works regardless of body framing (`/rel`'s JSON envelope, `/route`'s plain text, an in-flight route function's own arbitrary mimetype/template output once it's already started writing).
- **`code` field inside `RelErrorResponse`** (`/rel` only — its body is always JSON). `/route`'s built-in error paths (route lookup, request-body decoding, transaction handling) stay plain-text, unchanged ; the header is that path's only channel, per `route.md ## Postgres Exceptions`'s "no separate JSON property" stance.

### Postgres-raised codes

Unchanged from the existing convention (`route.md ## Postgres Exceptions`, `pgerr` package) : `RSxxx` gives the response status `xxx`, the raised message becomes the body, and `code` is set to the errcode itself (`"RS404"`), echoed in the header/JSON field like any other code.

`route.md`'s further claim — *"any other error code results in the error page with status 500, unless it's a known Postgres code with unambiguous HTTP semantics"* — is not yet implemented. `pgerr.RSStatus` only recognizes the `RSxxx` pattern today ; every other code, including `permission_denied`/`unique_violation`/etc., falls through to a generic 500. `## Postgres error detail` below makes the mapping load-bearing rather than cosmetic, since it decides whether a constraint violation is safe to show a client in full.

Table of well-known SQLSTATEs → `(status, code)`, `code` namespaced `PG_` (`PG_PERMISSION_DENIED` → 403, `PG_UNIQUE_VIOLATION` → 409, `PG_FOREIGN_KEY_VIOLATION` → 409, `PG_NOT_NULL_VIOLATION`/`PG_CHECK_VIOLATION` → 400). Everything else stays `INTERNAL`/500. How much of the underlying Postgres text is shown for each of these is not uniform — see `## Postgres error detail`, which splits by tier, not by SQLSTATE membership.

### Rel-internal codes

Candidate taxonomy, grouped by what a client can act on differently.

**Transport / never reaches app logic**

| code | status | where |
|---|---|---|
| `METHOD_NOT_ALLOWED` | 405 | `/rel` GET/POST check — currently wrongly returns 400, see below |
| `ROUTE_NOT_FOUND` | 404 | `/route` lookup miss |
| `MALFORMED_BODY` | 400 | JSON/form parse failure |
| `UNSUPPORTED_MEDIA_TYPE` | 415 | wrong content-type (e.g. upload routes) |
| `BODY_TOO_LARGE` | 413 | over `http.max_body_size` |
| `MALFORMED_MULTIPART` | 400 | multipart decode failure |

`server/rel.go`'s "/rel only accepts GET or POST" path currently returns `badRequest` (400). It must return 405. Fix as part of landing this taxonomy.

**Auth / authorization**

| code | status | where |
|---|---|---|
| `ANONYMOUS_DISABLED` | 401 | no anonymous role configured, no credentials |
| `ANONYMOUS_ROUTE_FORBIDDEN` | 401 | route not anonymous-authorized |
| `NO_ROLE_CONFIGURED` | 500 | `query.anonymous_role` unset and request anonymous — a deployment misconfiguration, kept as its own code so it doesn't read like an ordinary request failure in logs/monitoring |

**Query compile errors** — today one undifferentiated 400 bucket (`fmt.Errorf` message text only, no code). The biggest gap this taxonomy closes :

| code | status | covers |
|---|---|---|
| `QUERY_MALFORMED_JSON` | 400 | not valid JSON / wrong top-level shape |
| `QUERY_INVALID_EXPRESSION` | 400 | the operand-arity/shape family (`"query: %q needs..."`) |
| `UNKNOWN_IDENTIFIER` | 400 | unresolved relation/column/function/operator name |
| `JOIN_MISSING_INDEX` | 400 | `query-engine.md ## Join eligibility`'s compile-time rejection |
| `WRITE_FORBIDDEN` | 400 | non-writable identity, `write_mode` violations in general |
| `WRITE_FORBIDDEN_FUNCTION_ROOT` | 400 | the function-rooted-node case (`query-engine.md ## Reading Algorithm ### Function-rooted nodes`), kept distinct from the generic code |

Well-known query codes (`WELL_KNOWN_*`) are their own family, listed in full in `well-known-queries.md ## Compilation Errors` / `## Execution Errors`.

**Runtime / infra** — mostly 500 ; a client can't act differently on "commit failed" vs "connection acquire failed."

| code | status | where |
|---|---|---|
| `DB_UNAVAILABLE` | 500 | acquiring a connection failed |
| `TRANSACTION_ERROR` | 500 | begin/commit/rollback failures |
| `TEMPLATE_ERROR` | 500 | `/route` template rendering |
| `UPLOAD_IO_ERROR` | 500 | mkdir/create/rename/swap failures |

**Upload-specific** — already has real HTTP-status granularity in code today, needs codes attached :

| code | status |
|---|---|
| `UPLOAD_CONFLICT` | 409 |
| `UPLOAD_TOO_LARGE` | 413 |
| `UPLOAD_WRONG_TYPE` | 415 |

**SSO (`oauth-saml.md`)** — protocol-level failures Go itself detects on `/auth/oidc/*`/
`/auth/saml/*`, distinct from the SSO callback function's own `RSxxx` rejection
(`oauth-saml.md ## Callback function`) :

| code | status | where |
|---|---|---|
| `SSO_NOT_READY` | 503 | discovery/IdP metadata hasn't resolved yet (`oauth-saml.md ## Metadata fetch is lazy`) |
| `SSO_BAD_REQUEST` | 400 | malformed callback request (missing `code`, unparseable form) |
| `SSO_BAD_STATE` | 400 | missing/mismatched OAuth2 state |
| `SSO_BAD_NONCE` | 400 | ID token nonce doesn't match the one this login minted |
| `SSO_TOKEN_EXCHANGE_FAILED` | 502 | the issuer's token endpoint rejected the exchange |
| `SSO_NO_ID_TOKEN` | 502 | token response carried no `id_token` |
| `SSO_INVALID_ID_TOKEN` | 502 | `id_token` failed signature/issuer/audience verification |
| `SSO_USERINFO_FAILED` | 502 | `fetch_userinfo`'s own call to the issuer failed |
| `SSO_SAML_INVALID_RESPONSE` | 400 | SAML response/assertion failed to parse or verify |
| `SSO_INTERNAL` | 500 | rel's own logic failed (state/nonce generation, encoding) |

## Postgres error detail

`RelErrorResponse.pg_error` (present only when the failure came from a Postgres error) is always logged in full, server-side, regardless of mode. This section governs only what a client response may contain.

The gate is not "is this from Postgres" but **"does this message reveal anything beyond what the client's own request already implied."** Three tiers :

1. **Constraint violations the client's own submitted data triggered** — `PG_UNIQUE_VIOLATION`, `PG_FOREIGN_KEY_VIOLATION`, `PG_NOT_NULL_VIOLATION`, `PG_CHECK_VIOLATION`. `pg_error` is included unconditionally, in production too, built from an explicit field allow-list (below) — never `pgErr.Error()`'s full text or the raw struct. `error` gets a short synthesized message per violation kind (e.g. `"a unique constraint was violated"`) ; `pg_error` carries the allow-listed fields alongside it.
   > Why: the constraint targets columns of a relation already named in the request's own `query`/`data` — the client supplied the offending value, so the constraint/column names aren't new information to them.
2. **`PG_PERMISSION_DENIED`** — status/`code` (403) are always shown, but the message stays a fixed generic string (`"insufficient permissions for this operation"`) in production. Full raw text only under `dev: true`.
   > Why: unlike a constraint name, the object name in Postgres's raw permission-denied text can reveal the *existence* of something an unprivileged caller had no other way to confirm (`specs/TODO.md`'s fingerprinting-via-error-text risk).
3. **Everything unclassified** (falls through to `INTERNAL`/500) — `pg_error` omitted, `error` generic, in production ; both included only when `dev: true`.

`dev` mode's job is showing the unclassified tail and stack traces — tier 1 is never gated by it.

Gating is by tier, never by whether the caller is authenticated.

> Why: "authenticated" mostly means "completed signup," a near-zero bar under self-service registration or OAuth — it doesn't track actual risk (tier 1 is safe regardless of privilege, since it's about the caller's own data ; tier 2/3 are risky regardless of who's asking). Gating on it would train operators to believe there's a security boundary that isn't there. `dev` stays a deployment-level switch, not a per-request one. A future narrowly-scoped escalation (e.g. a `debug` JWT claim for an internal tool) is a separate feature if a concrete need arises.

### `pg_error` field allow-list

Built from `pgconn.PgError`'s fields, never its full `.Error()` string. Included, tier 1 only : `Message`, `Detail`, `SchemaName`, `TableName`, `ColumnName`, `ConstraintName`. Excluded, always, every tier : `Where`, `InternalQuery`, `Position`, `InternalPosition`, `File`, `Line`, `Routine` — these describe rel's own generated SQL and Postgres's internal execution path, the codegen-fingerprinting risk from `## What's unsafe` below.

Tier 1 assumes a constraint violation is self-referential — about the table/columns the client's own request touched. A trigger-cascaded write can raise a constraint violation naming a table the request never mentioned ; not defended against here.

## What's unsafe (and why), concretely

- **Object/schema existence an unprivileged caller couldn't otherwise confirm** — `PG_PERMISSION_DENIED`'s `Message` is typically `"permission denied for table foo"`, confirming `foo` exists to a caller who may have no other way to know that (`specs/TODO.md`'s schema-enumeration-via-error-fingerprinting risk). This is why tier 2 keeps `Message` generic rather than only narrowing which fields are shown.
- **Rel's own generated SQL, if codegen ever produces a bad query.** A Postgres syntax error embeds a fragment of the query text at fault — if that query is rel's own, the fragment can expose internal alias schemes, the `_data` staging table name, join-strategy artifacts. `pg/helpers.go`'s `oops.With("query", query)` attaches the raw SQL as structured context, not part of `.Error()`'s string (`oops.OopsError.Error()` returns only `"message: wrapped_error"`, never `.With()` context), so this isn't leaking today. Client-facing serialization MUST only ever use `.Error()` or `oops.GetPublic(...)` — never `%+v`, never `.Context()`/`.ToMap()`.
- **Server filesystem layout.** `route/upload_handler.go`'s `os.MkdirAll`/`os.OpenFile`/`os.Rename` failures wrap `*os.PathError`, whose `.Error()` includes the full server path, directory layout, sometimes the OS user rel runs as. This needs the same generic-in-prod treatment as tier 3.
- **DB connectivity detail.** Connection-acquire failures can embed host:port from pgx's own error text — real infra topology (never a password — pgx doesn't include that).
- **Secrets accidentally interpolated into error text.** `config/reader.go`'s `logErr` already enforces this for config values (`$FILE$` can put a resolved secret at any path, so an error message must never embed the resolved value, only the path/key). The same rule applies wherever any wrapped error might echo back a sensitive value rather than just its identifying key.
- **Stack traces / dependency fingerprinting** — package import paths and dependency versions visible in a frame are recon value. Covered by `## Stack traces`'s dev-gate.

## Stack traces

`oops` captures a stack trace automatically at error-construction time (`oc.Errorf`/`oc.Wrapf`) — `OopsError.Stacktrace()` / `StackFrames()` read it back, nothing extra needs threading through call sites.

**Eligibility : 5xx only, never 4xx.**

> Why: a stack trace has no debugging value for a 400 — the client sent something rel correctly rejected. Gating on status class rather than a hand-picked per-code list also means every future 500-class code is automatically covered.

**Sent only when `dev: true`**, same gate as `pg_error` — a stack trace leaks source file paths and package layout.

**Delivery** : `RelErrorResponse` gains a `stacktrace?: string[]` field (one frame per entry, from `OopsError.StackFrames()`), present exactly when both gates (5xx, `dev: true`) hold. For `/route`'s plain-text error bodies, the trace is appended after the message, separated by a blank line, under the same two gates.

## `RelErrorResponse`, updated shape

```typescript
interface RelErrorResponse {
  status: "error"          // literal discriminant, always this string
  code: string              // always present — see ## Error codes
  error: string               // human-readable message
  pg_error?: PgErrorDetail     // present for a classified constraint violation (always) or PG_PERMISSION_DENIED/unclassified Postgres error (dev: true only) — see ## Postgres error detail
  stacktrace?: string[]         // dev: true only, and only for a 5xx response
}

interface PgErrorDetail {
  message: string
  detail?: string
  schema_name?: string
  table_name?: string
  column_name?: string
  constraint_name?: string
}
```

`error`'s content is status-gated, same as `pg_error`/`stacktrace` : today `error` is unconditionally `err.Error()`, the full wrapped error chain, sent to every client regardless of mode. For a 5xx response, `error` is a fixed, safe, generic string in the default mode (`"internal error"`, or an explicit `oops.Public(...)` message read via `oops.GetPublic(err, "internal error")` when a more specific-but-still-safe message is worth giving) ; the full chain only appears in `error` when `dev: true`. 4xx `error` text is unchanged — the full descriptive message, since it is inherently about the client's own malformed input.
