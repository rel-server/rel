# HTTP

Static file serving, `RelHttpResponse.template` (Jet template rendering), and CORS/CSP are
covered in `specs/http-content.md`.

Routing uses the standard library's `net/http.ServeMux` (Go 1.22+ method/wildcard patterns,
e.g. `"GET /route/{schema}/{function}"` with `r.PathValue(...)`) — no external router
dependency. Individual database functions are dispatched dynamically from within the
`/route/{schema}/{function}` handler rather than registered as their own routes. Verify and
Renew (steps 2 and 4, `authentication.md ## Lifecycle`) need no database and are implemented
as ordinary `func(http.Handler) http.Handler` middleware. Check and Apply role (steps 3 and
5) need the request's own DB connection, which doesn't exist yet when generic middleware
runs, so those two are each handler's own responsibility (`/rel`, `/route`) once a
connection is acquired (`specs/TODO.md`'s connection-pool/lifecycle entry).

> Why `/route`, not `/api` or `/rpc` : `/api` describes nothing about what the endpoint
> does. `/rpc` was the original name, matching PostgREST's convention for exposing a
> Postgres function over HTTP, but the analogy doesn't hold : PostgREST's `/rpc/fn` call IS
> the whole RPC (scalar arguments in, a scalar/JSON result out), while a rel route function
> is dispatched HTTP-shaped work with its own request/response domain types, verb suffixes,
> and discovery/ambiguity rules. Calling a Postgres function with argument values — what
> PostgREST's `/rpc` actually names — is `/rel`'s job, not this endpoint's.

Route discovery (what makes a function a route, `__VERB` suffixes, ambiguous routes),
mimetype domains, and the `RelHttpRequest`/`RelHttpResponse` domain shapes are documented in
`docs/content/http/index.md`, `docs/content/http/static-files.md ## Returning binary or text
content directly`, and `docs/content/http/requests-responses.md`.

## Configuration

* `http.request_domain_name`, `http.response_domain_name`, `http.upload_domain_name` : names
  and defaults are in `docs/content/http/requests-responses.md` and
  `docs/content/http/uploads.md`. The resolution algorithm below is implementation detail not
  exposed there.

**Domain-name resolution.** Each of the three settings above names a domain by its bare,
unquoted identifier — a schema is not part of the setting's value, so the same domain name
works regardless of which schema declares it. Resolution at introspection/reload time : if
the configured value contains a `.`, it's treated as an already schema-qualified name and
matched exactly, no search. Otherwise rel searches every schema for a domain with that bare
name : exactly one match resolves normally ; zero matches gets the same non-fatal "didn't
resolve" warning `http.response_domain_name` already has (the affected mechanism doesn't
activate) ; more than one match (two different schemas each declaring their own
`RelHttpRequest`, say) is a fatal startup error, naming every schema the ambiguous match was
found in.

> Why a duplicate match is fatal rather than picking the first : there's nothing sensible to
> silently fall back to, and picking whichever one introspection happened to see first would
> be a silent correctness hazard.

* `http.templates.path` (default `/template`, renamed from the never-implemented
  `http.templatesdir`) : the Jet template directory.
* `http.cookies_max_age` (default `86400`) : default max-age for cookies set via the
  generic `cookies` field, when the response doesn't specify one. Does not apply to the JWT
  cookie (see `jwt.max_age`).
* `http.functions.allowed_auth`, `http.functions.allowed_routes` : see
  `docs/content/http/index.md`.
* `http.max_body_size` (default `10485760`, 10 MiB) : hard cap, in bytes, on a `/route`
  request's entire body — for multipart, the whole envelope (boundaries and part headers
  included, not just the sum of part payload bytes). Enforced before any of it is buffered
  in memory : a request whose `Content-Length` already exceeds this (or whose body exceeds
  it while streaming, for a chunked request with no declared length) is rejected outright
  (`413 Payload Too Large`), never partially read into memory first. Scoped to `/route`
  only — `/rel`'s own POST bodies (`query-engine.md`'s write payloads) are a distinct code
  path and not bounded by this setting.
  > Why : exists so `## Request bodies`' multipart/binary support doesn't turn `/route`
  > into an unauthenticated memory-exhaustion vector. 10 MiB is sized for a handful of
  > modest images/documents, not large media ; a deployment doing genuine large-file
  > uploads is expected to raise this explicitly.
* `http.max_part_count` (default `100`) : max number of `multipart/form-data` parts a
  single `/route` request may contain — see `## Request bodies ### Limits` for why this is
  a separate bound from `http.max_body_size`, not redundant with it.

## Anonymous route authorization

When anonymous access is enabled (`authentication.md ## Anonymous role existence`), rel
caches — at introspection/reload time, once per discovered route function, never per
request — whether the anonymous role can actually call it :
`has_schema_privilege(anonymous_role, schema, 'USAGE')` AND an EXPLICIT `EXECUTE` grant on
the function, to the anonymous role or to a role it's a member of. An anonymous request to a
route the anonymous role can't reach this way is rejected with `401`, before the request body
is read and before a pool connection is acquired.

> Why both conjuncts : `USAGE` on the schema is required to even reach the function,
> independent of `EXECUTE` on the function itself. Checking `EXECUTE` alone would go
> optimistic on any schema that gates access via `USAGE`, letting the cache say "allowed"
> for a request that dies at the real check anyway.

The `EXECUTE` half deliberately does NOT use `has_function_privilege`, unlike the schema
check : that built-in credits a `PUBLIC` grant to every role, anonymous included, which would
make this check say "reachable" for any function still sitting on Postgres's own `CREATE
FUNCTION` default (`### PUBLIC-reachable routes` below) regardless of whether the anonymous
role was ever meant to reach it. Instead : walk the
function's ACL via `aclexplode(coalesce(proacl, acldefault('f', proowner)))` (the `acldefault`
fallback covers a function whose `proacl` is still `NULL`, i.e. never touched by an explicit
`GRANT`/`REVOKE`), keep only rows whose `grantee` isn't `0` (aclexplode's marker for a `PUBLIC`
grant) and whose `privilege_type` is `EXECUTE`, and check `pg_has_role(anonymous_role, grantee,
'USAGE')` on what's left — a direct grant to the anonymous role, or a grant to a group role it
belongs to, either counts ; a grant that exists only because nobody revoked the default does
not.

This check is scoped to the anonymous role only ; one cached boolean per route, nothing
precomputed for any other role. Authenticated requests are not covered by this cache — the
existing live check at `SET LOCAL ROLE` + invocation time applies to them as before, and DOES
credit a `PUBLIC` grant the same way Postgres itself always has (`### PUBLIC-reachable routes`
below is the only defense against that for an authenticated caller).

> Why : unauthenticated requests are the cheap, high-volume attack surface, so a fail-fast
> cache pays for itself there. A deployment where each user has their own Postgres role
> ("1 user = 1 role") could have thousands to millions of distinct roles ; caching a full
> function × role matrix at that scale is a real memory and introspection-time cost for a
> case (authenticated abuse) this mechanism doesn't address.

### PUBLIC-reachable routes

Independent of the anonymous-role check, rel also warns — at introspection/reload — for
every discovered route function reachable by `PUBLIC` :
`has_schema_privilege('PUBLIC', schema, 'USAGE') AND
has_function_privilege('PUBLIC', function, 'EXECUTE')`, the same two-conjunct check as
above. Not fatal.

Both checks here are `/route`-specific. `/rel`'s own relation-level access (`SELECT`/
`INSERT`/`UPDATE`/`DELETE` grants, per column, further narrowed by rel's own
scoping/blacklist logic, `query-engine.md ## Scoping`) isn't addressed by either check here.

## Request

`RelHttpRequest`'s shape and its `body` content-type dispatch table are documented in
`docs/content/http/requests-responses.md`. Two cases that page doesn't cover :

* `text/*` matching ignores any `charset=` parameter — only the bare media type decides the
  branch.
* `application/json` whose body does not actually parse as valid JSON, or
  `application/x-www-form-urlencoded` whose body doesn't decode (a genuinely malformed
  percent-encoding, say) : `400` — no function is invoked, so no `RSxxx` mapping
  (`docs/content/http/index.md ## Errors are just exceptions`) applies.

`application/x-www-form-urlencoded` decodes through the same structural dot-path decoder
`query`/`GET /rel` use (`querystring.DecodeStructural`, `specs/query-json.md ## Structural
layer`) applied to the body's bytes instead of the URL's query string.

## Request bodies

`RelHttpRequest.body` covers the common case — one body, JSON or text or a single binary
blob. Real uploads (`multipart/form-data`, several independently-typed parts in one request,
or a single raw binary `POST`) are expressed as extra function parameters beyond
`req RelHttpRequest`, matched by their type sequence, never by parameter name. The four
recognized shapes, `files`/`parts_headers`' shape, and the `415`/empty-`files` mismatch rules
are documented in `docs/content/http/uploads.md`.

```typescript
interface RequestPart {
  name: string | null         // Content-Disposition name= (the multipart form field key) ; null for the synthesized pseudo-part below
  filename: string | null     // Content-Disposition filename= ; null when this part isn't a file (e.g. a plain form field)
  content_type: string | null // this part's own Content-Type header ; null if the part had no Content-Type header at all — NOT the empty string
  headers: {[name: string]: string[]} // ALL of this part's own headers, including Content-Type/Content-Disposition verbatim — same shape RelHttpRequest.headers already uses
}
```

For a single, non-multipart, raw binary `POST` (a request whose `Content-Type` isn't
`multipart/*` but also isn't JSON/text — the same set of requests that would otherwise
populate `body` via `## Request`'s "anything else" branch), `files` is a one-element array
holding the whole raw body, and `parts_headers` (if declared) is a one-element array holding
a synthesized pseudo-part : `name: null`, `filename: null`, `content_type` = the request's
own `content_type`, `headers` = the request's own `headers`.

A syntactically valid `multipart/form-data` envelope containing zero parts is treated the
same as the no-body case : empty `files`/`parts_headers`, not a `415` and not an error, on
any route (whether or not it declared `files`) — an empty multipart body is a genuinely
empty request, not a shape mismatch.

### Limits

* `http.max_body_size` (see `## Configuration` above) bounds total bytes.
* `http.max_part_count` (default `100`) bounds the number of multipart parts a single
  request may contain, independent of their total byte size. Exceeding it is a `413
  Payload Too Large`, checked incrementally while parsing — a request is rejected as soon
  as the count is exceeded, not after fully parsing an oversized part set. A synthesized
  single-binary-POST pseudo-part always counts as exactly one part.
  > Why a separate bound from `http.max_body_size` : a request built from a very large
  > number of near-empty parts stays under the byte-size cap while still costing real
  > CPU/memory in per-part header parsing and allocation, a distinct resource-exhaustion
  > shape. 100 is sized generously above what a genuine multi-file form submission needs.

### Known limitation : no true streaming to Postgres

`files`/`body` both require the entire relevant payload to be fully received and held in
memory (bounded by `http.max_body_size`) before the route function is invoked.

> Why not fixed here : `pg_largeobject` (`lo_*` functions, exposed to Go via
> `pgx.LargeObjects`) supports genuine chunked read/write without full in-memory buffering,
> but using it is a materially different, separately-designed mechanism — an `oid`
> reference in place of `bytea`, its own ACL/ownership and orphan-cleanup lifecycle, and
> rel's handler streaming HTTP bytes into it via `lo_write` calls before the route function
> runs. Not part of this pass.

## Responses

`RelHttpResponse`'s shape is documented in `docs/content/http/requests-responses.md`.
`jwt_attrs.samesite` overrides `jwt.same_site` for that one response's cookie only, the same
way `jwt_attrs.maxage` overrides `jwt.max_age` for that session only.
