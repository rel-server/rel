# HTTP

Static file serving, `RelHttpResponse.template` (Jet template rendering), and CORS/CSP
(including the `RelHttpRequest.csp_nonce`/`RelHttpResponse.csp` fields referenced below) are
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

Creating the domain types `RelHttpRequest` and `RelHttpResponse` on JSON or JSONB (schema
doesn't matter by default — `## Configuration`'s domain-name resolution rule below) and
using them in function prototypes, or creating a domain over `bytea` or `text` with a `/` in
its name, enables functions to return binary (or plain-text) content with the domain name as
mime-type — a "mimetype domain". A `bytea`-underlying mimetype domain's raw bytes are the
response body directly ; a `text`-underlying one's string is the body directly, with no
base64 or other encoding (unlike `RelHttpRequest.body`'s own binary case, `## Request`
below — a mimetype domain's return value is never wrapped in JSON). `"text/plain"` over
`text` is the common case, expressed as a named domain, same mechanism as `"image/png"` —
there is no bare, undecorated `returns text`/`returns json` special case with an invented
default `Content-Type`.

A function whose name does **not** start with `_`, and that takes the argument shapes
`## Request bodies` below describes (0 arguments, one `RelHttpRequest` argument, or
`RelHttpRequest` plus one of the extra body-parameter shapes), and that returns
`RelHttpResponse` or a mimetype domain, is a HTTP route function — callable directly at
`/route/<schema>/<function_name>` (and, if `http.functions.allowed_routes` is set, only if
its fully qualified name also matches that regexp). A leading `_` opts a function out of
route discovery unconditionally, with no configuration needed.

Such a function's name can end with `__VERB` (`__GET`, `__POST`, ...) to restrict which
HTTP verb it answers to ; case doesn't matter. If a verb-suffixed function is defined
alongside an unsuffixed one, the unsuffixed function is the fallback for verbs with no
specific match. Conforming to web semantics (e.g. `__GET` must not mutate state — see the
CSRF note under Cookies) is the function author's responsibility ; rel does not enforce it.

Two or more discovered functions sharing the same (schema, base name, verb) key — the same
schema, the same name once any `__VERB` suffix is stripped, and the same resolved verb — is
an ambiguous route : neither function becomes the registered route at that key. Route
discovery logs an error naming both colliding functions ; a third (or later) function
sharing that key is excluded the same way. This is a discovery-time warning, not a fatal
error : the affected key is simply never routable until the collision is resolved in the
schema itself.

`RelHttpResponse` can render via [Jet templates](github.com/CloudyKit/jet) through the
optional `template` key — see `specs/http-content.md ## Templates` for the full
rendering/escaping contract.

```sql
create domain "RelHttpRequest" as jsonb;
create domain "RelHttpResponse" as jsonb;
create domain "image/png" AS bytea;
create domain "text/plain" AS text;
create function schema.some_function(req RelHttpRequest) returns RelHttpResponse /* ... */;
create function schema.returns_binary() returns "image/png" /* ... */;
create function schema.returns_text() returns "text/plain" /* ... */;
```

## Configuration

* `http.request_domain_name` (default `RelHttpRequest`) : the unquoted name of the JSON
  domain for request-typed functions. See the domain-name resolution rule immediately below
  for what "unquoted name" means when it isn't schema-qualified.
* `http.response_domain_name` (default `RelHttpResponse`) : the unquoted name of the JSON
  domain rel interprets as an HTTP response return type. Not an error if it doesn't exist,
  but rel will warn, since without it no authentication flow can work.
* `http.upload_domain_name` (default `RelUpload`) : the unquoted name of the JSON domain
  used by `specs/http-content.md ## Static files ### Upload destinations`' two-function
  upload mechanism. Not an error if it doesn't exist — that mechanism simply isn't
  discovered, same non-fatal treatment as the two domain names above.

**Domain-name resolution.** Each of the three settings above names a domain by its bare,
unquoted identifier — a schema is not part of the setting's value, so the same domain name
works regardless of which schema declares it. Resolution at introspection/reload time : if
the configured value contains a `.`, it's treated as an already schema-qualified name and
matched exactly, no search. Otherwise rel searches every schema for a domain with that bare
name : exactly one match resolves normally ; zero matches gets the same non-fatal "didn't
resolve" warning `http.response_domain_name` already has (the affected mechanism doesn't
activate) ; more than one match (two different schemas each declaring their own
`RelHttpRequest`, say) is a fatal startup error, naming every schema the ambiguous match was
found in. A multi-schema project that wants two distinct domains of the same conceptual role
active at once isn't served by any of these three settings.

> Why a duplicate match is fatal rather than picking the first : there's nothing sensible to
> silently fall back to, and picking whichever one introspection happened to see first would
> be a silent correctness hazard.

* `http.templates.path` (default `/template`, renamed from the never-implemented
  `http.templatesdir` — see `specs/http-content.md ## Templates`) : the Jet template
  directory.
* `http.cookies_max_age` (default `86400`) : default max-age for cookies set via the
  generic `cookies` field, when the response doesn't specify one. Does not apply to the JWT
  cookie (see `jwt.max_age`).
* `http.functions.allowed_auth` (default empty) : regexp restricting which functions'
  responses rel will honor a `jwt` field from, matched against the fully qualified, unquoted
  function name. Empty means unrestricted.
* `http.functions.allowed_routes` (default empty) : regexp a function's fully qualified,
  unquoted name must additionally match to become a public route, on top of having the
  right signature and not starting with `_`. Empty means unrestricted (any
  matching-signature, non-`_` function is routed).
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

> Why: the anonymous role is the literal "no login at all" case — the cheapest, highest-volume
> way to reach a route, and the one rel already claims to gate here. A `PUBLIC` grant nobody
> meant to leave in place authorizing that gate silently is exactly the failure this check
> exists to prevent, not an acceptable edge case of it.

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

> Why : Postgres grants `EXECUTE` to `PUBLIC` by default on `CREATE FUNCTION` unless
> default privileges were altered or the grant explicitly revoked, a well-known footgun
> independent of rel — a route can be silently callable by every role in the database
> without the schema author intending it.

Both checks here are `/route`-specific. `/rel`'s own relation-level access (`SELECT`/
`INSERT`/`UPDATE`/`DELETE` grants, per column, further narrowed by rel's own
scoping/blacklist logic, `query-engine.md ## Scoping`) isn't addressed by either check here.

## Cookies

The `cookies` field, present on both `RelHttpRequest` and `RelHttpResponse`, is a
convenience over manipulating `Set-Cookie`/`Cookie` headers directly.

```typescript
interface Cookie {
  value: string
  httponly: boolean
  secure: boolean
  samesite: string
  maxage: number
}
```

Unless a response overrides them, rel sets `secure: true`, `httponly: true`,
`samesite: Lax`, and a max-age of `http.cookies_max_age` on any cookie it sets.

The JWT itself is read/set more directly through the `jwt` field on
`RelHttpRequest`/`RelHttpResponse`, rather than through `cookies` — see
`authentication.md`'s Claims/Lifecycle sections. Setting `jwt: null` clears the session
(logout).

**Warning:** by default, *any* HTTP route function can set `jwt` on its response and
thereby authenticate the caller as any role — the `jsonb` payload is trusted at face value.
Set `http.functions.allowed_auth` to restrict which functions have this power ; consider
defaulting it to your login/session functions only (e.g. everything under an `auth`
schema), rather than leaving it unrestricted.

`SameSite=Lax` on both the JWT cookie and default-configured cookies, combined with the
requirement that `__GET` functions not mutate state, is rel's CSRF defense — there is no
separate CSRF token mechanism.

## Request

If a function has exactly one argument of type `RelHttpRequest`, or `RelHttpRequest` plus
one of the extra body-parameter shapes `## Request bodies` describes, the request is
encoded as :

```typescript
interface RelHttpRequest {
  method: string
  uri: string
  query: unknown
  headers: {[name: string]: string[]}
  content_type: string
  body: unknown // shape depends on content_type — see below

  cookies: {[name: string]: string} // value only — see the note below
  jwt: JWT | null // null when the request carries no valid session
  csp_nonce: string // see specs/http-content.md ## CSP ### Nonce
}
```

`RelHttpRequest.body` is typed by `content_type`, the same convention
`RelHttpResponse.content` uses to drive interpretation on the response side :

* `application/json`, or any `+json` suffix (`application/vnd.api+json`, ...) : `body`
  decodes to the request's actual JSON value — an object, array, or scalar — never a
  JSON-encoded string of it.
* `text/*` (any subtype, `charset=` parameter ignored for this match) : `body` is the plain
  string.
* `application/x-www-form-urlencoded` (a plain `<form>` POST with no `enctype`) : `body`
  decodes to a JSON object, through the same structural dot-path decoder `query`/`GET /rel`
  use (`querystring.DecodeStructural`, `specs/query-json.md ## Structural layer`) applied to
  the body's bytes instead of the URL's query string. `name=John&email=john%40example.com`
  becomes `{"name": "John", "email": "john@example.com"}` ; a dotted key nests the same way
  it does for `query`/`GET /rel` (`user.email=...` → `{"user": {"email": "..."}}`).
  > Why the same decoder : `application/x-www-form-urlencoded` and a URL query string are
  > the identical percent-encoded `key=value&key=value` syntax, just carried in the body
  > instead of after a `?`.
* Anything else (binary) : `body` is a base64-encoded string of the raw bytes. Postgres's
  `decode(body, 'base64')::bytea` recovers the original bytes inside the function body.
  Only reachable on a route that did not declare `## Request bodies`' `files bytea[]`
  parameter.
  > Why base64 : the only encoding that survives unmodified inside jsonb — a raw byte
  > sequence isn't valid JSON text, and a JSON encoder given arbitrary non-UTF-8 bytes as a
  > string mangles invalid sequences, corrupting the payload.
* No body at all (a `GET`, or any request with an empty body) : `body` is JSON `null`.
* A route declaring `## Request bodies`' `files bytea[]` parameter : `body` is always JSON
  `null`, regardless of `content_type` — the payload is delivered exclusively through
  `files`/`parts_headers` (a `(req, files bytea[])` route receiving a 10 MiB upload does not
  also carry a base64 copy of the same bytes inside `req`).
* `application/json` whose body does not actually parse as valid JSON, or
  `application/x-www-form-urlencoded` whose body doesn't decode (a genuinely malformed
  percent-encoding, say) : `400`, same as any other malformed-request case
  (`## Postgres Exceptions` doesn't apply — this never reaches a function at all).

`RelHttpRequest.cookies` does not reuse the full `Cookie` shape (`value`/`httponly`/
`secure`/`samesite`/`maxage`) the response side uses — only `name=value`.

> Why : a browser's `Cookie` header only ever sends `name=value` ; the other four
> attributes are response-only (`Set-Cookie` attributes) and can never be known for an
> inbound cookie.

## Request bodies

`RelHttpRequest.body` above covers the common case — one body, JSON or text or a single
binary blob. Real uploads (`multipart/form-data`, several independently-typed parts in one
request, or a single raw binary `POST`) are expressed as extra function parameters beyond
`req RelHttpRequest`, matched by their type sequence, never by parameter name (a function
author names them however they like) :

```sql
-- bytes only, no metadata
create function schema.upload(req RelHttpRequest, files bytea[]) returns RelHttpResponse /* ... */;
-- bytes plus full per-part metadata
create function schema.upload_with_headers(req RelHttpRequest, files bytea[], parts_headers jsonb) returns RelHttpResponse /* ... */;
```

Only these four shapes — `()`, `(req)`, `(req, files bytea[])`, `(req, files bytea[],
parts_headers jsonb)` — are recognized route-function signatures for this mechanism ;
anything else (extra parameters, a different order, a different type) is not discovered as
a route at all. `specs/http-content.md ## Static files ### Upload destinations` adds a
separate two-function shape family, `(req, part jsonb) returns RelUpload`/`(req, upload
RelUpload) returns RelHttpResponse`, for an unrelated mechanism, distinguished from the four
here purely by its own argument/return types. There is no separate single-file-only shape :
a single raw binary `POST` is treated as a one-element `files` array (below), so
`files bytea[]` alone covers "exactly one file" (`files[1]`).

`parts_headers`, when declared, is a JSON array, index `i` describing `files[i]` :

```typescript
interface RequestPart {
  name: string | null         // Content-Disposition name= (the multipart form field key) ; null for the synthesized pseudo-part below
  filename: string | null     // Content-Disposition filename= ; null when this part isn't a file (e.g. a plain form field)
  content_type: string | null // this part's own Content-Type header ; null if the part had no Content-Type header at all — NOT the empty string
  headers: {[name: string]: string[]} // ALL of this part's own headers, including Content-Type/Content-Disposition verbatim — same shape RelHttpRequest.headers already uses
}
```

`name` and `filename` are kept as their own first-class fields rather than folded into
`headers`, though `headers` still contains the raw `Content-Disposition`/`Content-Type`
header values verbatim (one promoted for convenience, both present). `name` and `filename`
are always independently available — a route distinguishing two differently-named file
inputs does not lose the field key just because a filename is also present.

> Why first-class fields : parsing `Content-Disposition`'s own parameters isn't something a
> function should have to redo in plpgsql.

For a single, non-multipart, raw binary `POST` (a request whose `Content-Type` isn't
`multipart/*` but also isn't JSON/text — the same set of requests that would otherwise
populate `body` via `## Request`'s "anything else" branch), `files` is a one-element array
holding the whole raw body, and `parts_headers` (if declared) is a one-element array holding
a synthesized pseudo-part : `name: null`, `filename: null`, `content_type` = the request's
own `content_type`, `headers` = the request's own `headers`.

A real multipart form frequently mixes plain (non-file) fields with file uploads in one
submission (a `title` text input alongside an attached file, say) — nothing here drops
them : a plain field lands in `files` (as its raw UTF-8 bytes — `convert_from(files[i],
'utf8')` recovers the string inside the function), with `parts_headers[i].name` set to its
field key and `parts_headers[i].filename` staying `null` (no `filename=` on a plain field).
There is no structural separation between "this index is a real file" and "this index is a
plain form value" beyond checking `parts_headers[i].filename IS NULL` — a function that
cares about the difference makes that call itself.

Declaring `files bytea[]` (with or without `parts_headers`) is a hard contract about what
the route accepts — a mismatch against the actual request is always a
`415 Unsupported Media Type`, never a silent fallback :

* A route declaring `files` receiving a request whose `content_type` falls into
  `## Request`'s JSON, `text/*`, or `application/x-www-form-urlencoded` branches (i.e.
  `body` would otherwise have been populated) is a `415` — there is no byte payload to hand
  over as a "file" separately from what's already fully represented as `body`.
* A route not declaring `files` (a plain `(req)`-only route) receiving an actual
  `multipart/form-data` request is also a `415` — multipart data has nowhere to go without
  a `files` parameter, and is never silently base64-encoded whole (boundaries and all) into
  `body` as a fallback.
* A route declaring `files`, called with no body at all (no `Content-Type`, empty body —
  e.g. a `__GET`-suffixed variant of an upload route, or any request that simply omits
  one) : not a `415` — `files` is an empty array (`'{}'::bytea[]`), `parts_headers` (if
  declared) an empty JSON array. A route that genuinely requires at least one file checks
  `array_length(files, 1)` itself and raises its own `RSxxx` if that matters to it ; rel
  does not treat "declared `files` but got none" as a shape mismatch on its own — only a
  wrong shape (JSON/text body on a `files` route) is.
* A syntactically valid `multipart/form-data` envelope containing zero parts is treated the
  same as the no-body case immediately above — empty `files`/`parts_headers`, not a `415`
  and not an error, on any route (whether or not it declared `files`) : an empty multipart
  body is a genuinely empty request, not a shape mismatch.

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
memory (bounded by `http.max_body_size`) before the route function is invoked — there is no
mechanism for a function to receive a live stream, or for rel to write bytes to Postgres
incrementally as they arrive. Raising `http.max_body_size` only moves this ceiling ; it does
not change the fact that the whole payload is buffered at once. Genuinely large uploads
(video, multi-GB archives) are not what `files`/`body` are for.

> Why not fixed here : `pg_largeobject` (`lo_*` functions, exposed to Go via
> `pgx.LargeObjects`) supports genuine chunked read/write without full in-memory buffering,
> but using it is a materially different, separately-designed mechanism — an `oid`
> reference in place of `bytea`, its own ACL/ownership and orphan-cleanup lifecycle, and
> rel's handler streaming HTTP bytes into it via `lo_write` calls before the route function
> runs. Not part of this pass.

## Responses

If a function's return type is `RelHttpResponse`, rel expects the following shape and
handles it as a response :

```typescript
interface RelHttpResponse {
  status: number // 200 if not specified
  content_type: string
  content: unknown

  template?: string // see specs/http-content.md ## Templates
  template_data?: unknown // see specs/http-content.md ## Templates
  headers?: {[name: string]: string | string[]}
  cookies?: {[name: string]: Cookie | string}
  jwt?: JWT | null
  jwt_attrs?: {
    samesite?: string
    maxage?: number
  }
  csp?: string // see specs/http-content.md ## CSP ### Per-response override
}
```

`jwt_attrs` controls how a `jwt` set by this same response is minted — `maxage` overrides
`jwt.max_age` for this session (both the token's `exp - iat` and the cookie's `Max-Age`
follow it), `samesite` overrides `jwt.same_site` for this cookie only.

## Postgres Exceptions

Raised while handling an HTTP route function or the session-check function, an exception
with code `RSxxx` gives the response status `xxx`, with whatever text was raised as the
body.

```sql
raise exception 'Access denied' using errcode = 'RS401';
raise exception 'Not found' using errcode = 'RS404';
```

Any other error code results in the error page with status 500, unless it's a known
Postgres code with unambiguous HTTP semantics (e.g. permission denied → 401).
