
# HTTP

Static file serving, `RelHttpResponse.template` (Jet template rendering), and CORS/CSP (including
the `RelHttpRequest.csp_nonce`/`RelHttpResponse.csp` fields referenced below) are all covered
separately, in full, in `specs/http-content.md` — not repeated here.

Routing is done with the standard library's `net/http.ServeMux` (Go 1.22+ method/wildcard patterns, e.g. `"GET /rpc/{schema}/{function}"` with `r.PathValue(...)`) — no external router dependency. This fits rel's actual routing needs: a small, fixed set of patterns (`/rpc/{schema}/{function}`, `/rel`, `/auth/*`, `/js/*`, static files), since individual database functions are dispatched dynamically from within the `/rpc/{schema}/{function}` handler rather than registered as their own routes. Verify and Renew (steps 2 and 4 — see `authentication.md ## Lifecycle`) need no database and are implemented as ordinary `func(http.Handler) http.Handler` middleware, composed by hand — no framework-specific request/context type involved. Check and Apply role (steps 3 and 5) need the request's own DB connection, which doesn't exist yet when generic middleware runs, so those two are each handler's own responsibility instead (`/rel`, `/rpc`) once a connection is acquired — see `specs/TODO.md`'s connection-pool/lifecycle entry.

> Why `/rpc`, not `/api` : `/api` describes nothing about what the route actually does (every HTTP endpoint in existence is "an API"). `/rpc` names the actual mechanism — a named backend function called directly over HTTP — and matches PostgREST's own convention for the identical concept, which `authentication.md`'s own `# Roles` section already invokes as a point of comparison.

Creating the domain types `RelHttpRequest` and `RelHttpResponse` on JSON or JSONB (their schema
doesn't matter by default — see `## Configuration`'s domain-name resolution rule below for the
precise mechanism, including what happens if more than one schema declares a same-named domain)
and using them in function prototypes, or creating a domain over `bytea` OR `text` with a `/` in their name, enables functions to return binary (or plain-text) content with the domain name as mime-type — a "mimetype domain". Which underlying type a mimetype domain uses only changes how its return value becomes the response body : a `bytea`-underlying one's raw bytes ARE the body ; a `text`-underlying one's string IS the body directly, with no base64 or other encoding involved (unlike `RelHttpRequest.body`'s own binary case — see `## Request` below — a mimetype domain's return value is never itself wrapped in JSON, so there's no jsonb-safety concern forcing an encoding here). `"text/plain"` over `text` is the common case — deliberately expressed this way (a NAMED domain, same mechanism as `"image/png"`) rather than as a special case for a bare, undecorated `returns text`/`returns json` with no domain wrapper at all : a bare scalar return type carries no name to derive a `Content-Type` from, so it would need an invented default (`text/plain` is a defensible guess for bare `text` ; there's no equally obvious guess for bare `bytea`, which is exactly why mimetype domains exist in the first place — the type's OWN name states the content-type explicitly instead of rel guessing one).

A function whose name does **not** start with `_`, and that takes the argument shapes `## Request bodies` below describes (0 arguments, one `RelHttpRequest` argument, or `RelHttpRequest` plus one of the extra body-parameter shapes), and that returns `RelHttpResponse` or a mimetype domain, is a HTTP route function — callable directly at `/rpc/<schema>/<function_name>` (and, if `http.functions.allowed_routes` is set, only if its fully qualified name also matches that regexp). A leading `_` opts a function out of route discovery unconditionally, with no configuration needed — this is how internal helpers that happen to match the signature stay unexposed.

Such a function's name can end with `__VERB` (`__GET`, `__POST`, ...) to restrict which HTTP verb it answers to; case doesn't matter. If a verb-suffixed function is defined alongside an unsuffixed one, the unsuffixed function is the fallback for verbs with no specific match. Conforming to web semantics (e.g. `__GET` must not mutate state — see the CSRF note under Cookies) is the function author's responsibility; rel does not enforce it.

Two or more discovered functions sharing the same (schema, base name, verb) key — the same schema,
the same name once any `__VERB` suffix is stripped, and the same resolved verb — is an ambiguous
route: NEITHER function becomes the registered route at that key, not just whichever one lost a
first-vs-second conflict. Route discovery logs an error naming both colliding functions, and a
third (or later) function sharing that same key is excluded the same way once a key is known
ambiguous — a later collision never silently becomes the sole owner just because an earlier
conflict already vacated the slot. This is a discovery-time warning, not a fatal error: the
affected key is simply never routable until the collision is resolved in the schema itself.

`RelHttpResponse` can render via [Jet templates](github.com/CloudyKit/jet) through the optional `template` key — see `specs/http-content.md ## Templates` for the full rendering/escaping contract.

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

* `http.request_domain_name` (default `RelHttpRequest`) : the unquoted name of the JSON domain for request-typed functions. See the domain-name resolution rule immediately below for what "unquoted name" means when it isn't schema-qualified.
* `http.response_domain_name` (default `RelHttpResponse`) : the unquoted name of the JSON domain rel interprets as an HTTP response return type. Not an error if it doesn't exist, but rel will warn, since without it no authentication flow can work.
* `http.upload_domain_name` (default `RelUpload`) : the unquoted name of the JSON domain used by `specs/http-content.md ## Static files ### Upload destinations`' two-function upload mechanism. Not an error if it doesn't exist — that mechanism simply isn't discovered, same non-fatal treatment as the two domain names above.

**Domain-name resolution.** Each of the three settings above names a domain by its bare, unquoted
identifier — a schema is deliberately not part of the setting's own value, so the same domain
name works regardless of which schema a project happens to keep its `RelHttpRequest`/
`RelHttpResponse`/`RelUpload` domain in. Resolution at introspection/reload time : if the
configured value contains a `.`, it's treated as an already schema-qualified name and matched
exactly, no search. Otherwise (the common, default case) rel searches every schema for a domain
with that bare name : exactly one match resolves normally ; zero matches gets the same non-fatal
"didn't resolve" warning `http.response_domain_name` already has (route discovery/the affected
mechanism just doesn't activate) ; MORE than one match (two different schemas each declaring their
own `RelHttpRequest`, say) is a FATAL startup error instead, naming every schema the ambiguous
match was found in — unlike "not found," there's nothing sensible to silently fall back to here,
and picking whichever one introspection happened to see first would be a real, silent correctness
hazard rather than a merely-inactive feature. A genuinely multi-schema project that wants two
distinct domains of the same conceptual role active at once isn't served by any of these three
settings today — out of scope for this pass, same as the rest of this document's "no per-schema
override" settings.

* `http.templates.path` (default `/template`, renamed from the never-implemented
  `http.templatesdir` — see `specs/http-content.md ## Templates`) : the Jet template directory.
* `http.cookies_max_age` (default `86400`) : default max-age for cookies set via the generic `cookies` field, when the response doesn't specify one. Does not apply to the JWT cookie (see `jwt.max_age`).
* `http.functions.allowed_auth` (default empty) : regexp restricting which functions' responses rel will honor a `jwt` field from, matched against the fully qualified, unquoted function name. Empty means unrestricted.
* `http.functions.allowed_routes` (default empty) : regexp a function's fully qualified, unquoted name must additionally match to become a public route, on top of having the right signature and not starting with `_`. Empty means unrestricted (any matching-signature, non-`_` function is routed).
* `http.max_body_size` (default `10485760`, 10 MiB) : hard cap, in bytes, on a `/rpc` request's ENTIRE body — for multipart, the whole envelope (boundaries and part headers included, not just the sum of part payload bytes), a strictly tighter bound than "just the files" would be. Enforced before any of it is buffered in memory, not after — a request whose `Content-Length` already exceeds this (or whose body turns out to exceed it while streaming, for a chunked request with no declared length) is rejected outright (`413 Payload Too Large`), never partially read into memory first. Exists specifically so `## Request bodies`' multipart/binary support doesn't turn `/rpc` into an unauthenticated memory-exhaustion vector — every request body was already read into memory unconditionally before this setting existed, which was fine for small JSON payloads and not fine once arbitrary file uploads are in scope. 10 MiB is a starting point sized for "a handful of modest images/documents," not large media — a deployment doing genuine large-file uploads is expected to raise this explicitly, a deliberate choice rather than an accidentally-permissive default. Deliberately scoped to `/rpc` only : `/rel`'s own POST bodies (`query-engine.md`'s write payloads) are legitimately large for a bulk write and already a distinct code path — bounding those, if ever needed, is a separate setting/decision, not silently folded into this one.
* `http.max_part_count` (default `100`) : max number of `multipart/form-data` parts a single `/rpc` request may contain — see `## Request bodies ### Limits` for why this is a separate bound from `http.max_body_size`, not redundant with it.

## Anonymous route authorization

When anonymous access is enabled (`authentication.md ## Anonymous role existence`), rel additionally caches — at introspection/reload time, once per discovered route function, never per request — whether the anonymous role can actually call it : `has_schema_privilege(anonymous_role, schema, 'USAGE') AND has_function_privilege(anonymous_role, function, 'EXECUTE')`, BOTH conjuncts, matching exactly what Postgres itself checks at call time (`USAGE` on the schema is required to even reach the function, independent of `EXECUTE` on the function itself — checking `EXECUTE` alone would go optimistic on any schema that gates access via `USAGE`, which defeats the point for exactly the locked-down deployments this exists for ; go check either primitive alone and the cache can say "allowed" for a request that dies at the real check anyway, silently turning this back into the thing it was built to prevent). This reuses Postgres's own authority rather than inventing a parallel one — it already accounts for direct grants, `PUBLIC` grants, and role membership/inheritance correctly on both checks. An anonymous request to a route the anonymous role can't reach is rejected with `401` — before the request body is read, and before a pool connection is acquired — the same fail-fast reasoning as the existence check above, scoped one level narrower (per route, rather than "is anonymous access on at all").

This check is intentionally scoped to the ANONYMOUS role only, never every role in the database, for two independent reasons :

* Unauthenticated requests are the cheap, high-volume attack surface — anyone can send one, no valid JWT required. An authenticated request already implies possession of a signed session, a real (if imperfect) barrier that changes the cost/risk calculus enough that the existing live check at `SET LOCAL ROLE` + invocation time remains the right tradeoff for it — this fail-fast cache does not extend to authenticated requests at all.
* A deployment where each individual user has their own Postgres role (a common pattern — "1 user = 1 role") could have thousands to millions of distinct roles. Caching a full function × role matrix at that scale is a real memory and introspection-time cost, for a case (authenticated abuse) this mechanism was never trying to address in the first place. One cached boolean per route (anonymous only), nothing precomputed for any other role, ever.

Independent of the anonymous-role existence check, rel also warns — at introspection/reload — for every discovered route function that's actually reachable by `PUBLIC` : `has_schema_privilege('PUBLIC', schema, 'USAGE') AND has_function_privilege('PUBLIC', function, 'EXECUTE')`, the same two-conjunct check as above, for the same reason — a function with `PUBLIC EXECUTE` sitting in a schema without `PUBLIC USAGE` isn't actually publicly callable, and warning on it anyway would just teach operators to ignore the warning. Postgres grants `EXECUTE` to `PUBLIC` by default on `CREATE FUNCTION`, unless the schema's default privileges were altered or the grant explicitly revoked afterward — a well-known footgun independent of anything rel does, meaning a route can be silently callable by every role in the database (anonymous included, and every "1 user = 1 role" tenant alike) without the schema author ever intending it. Not fatal — revoking `PUBLIC` by default is a schema-authoring discipline rel can surface loudly but can't enforce.

Both checks here are `/rpc`-specific. `/rel`'s own relation-level access (`SELECT`/`INSERT`/`UPDATE`/`DELETE` grants, per column, further narrowed by rel's own scoping/blacklist logic — `query-engine.md ## Scoping`) is a fundamentally more nuanced shape than a single `EXECUTE` bit and isn't addressed by either check here.

## Cookies

The `cookies` field, present on both `RelHttpRequest` and `RelHttpResponse`, is a convenience over manipulating `Set-Cookie`/`Cookie` headers directly.

```typescript
interface Cookie {
  value: string
  httponly: boolean
  secure: boolean
  samesite: string
  maxage: number
}
```

Unless a response overrides them, rel sets `secure: true`, `httponly: true`, `samesite: Lax`, and a max-age of `http.cookies_max_age` on any cookie it sets.

The JWT itself is read/set more directly through the `jwt` field on `RelHttpRequest`/`RelHttpResponse`, rather than through `cookies` — see `authentication.md`'s Claims/Lifecycle sections. Setting `jwt: null` clears the session (logout).

**Warning:** by default, *any* HTTP route function can set `jwt` on its response and thereby authenticate the caller as any role — the `jsonb` payload is trusted at face value. Set `http.functions.allowed_auth` to restrict which functions have this power; consider defaulting it to your login/session functions only (e.g. everything under an `auth` schema), rather than leaving it unrestricted.

`SameSite=Lax` on both the JWT cookie and default-configured cookies, combined with the requirement that `__GET` functions not mutate state, is rel's CSRF defense — there is no separate CSRF token mechanism.

## Request

If a function has exactly one argument of type `RelHttpRequest`, or `RelHttpRequest` plus one of the extra body-parameter shapes `## Request bodies` describes, the request is encoded as:

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

`RelHttpRequest.body` (renamed from an earlier, always-a-raw-string `content` field) is typed by `content_type`, the same way `RelHttpResponse.content` already lets its own `content_type` drive interpretation on the response side — this is that same convention, now also applied to the request side, not a new one :

* `application/json`, or any `+json` suffix (`application/vnd.api+json`, ...) : `body` decodes to the request's actual JSON value — an object, array, or scalar — never a JSON-encoded STRING of it. This is the change that actually motivated the rename : the old `content: string` forced a JSON request body through `(req->>'content')::jsonb` inside every function that wanted to use it, double-encoding for no reason.
* `text/*` (any subtype, `charset=` parameter ignored for this match) : `body` is the plain string, exactly as `content` used to always be.
* `application/x-www-form-urlencoded` (a plain `<form>` POST with no `enctype` — the default, used for any form with no file input) : `body` decodes to a JSON object, through the exact same structural dot-path decoder `query`/`GET /rel` already use (`querystring.DecodeStructural` — see `specs/query-json.md ## Structural layer`) applied to the body's own bytes instead of the URL's query string. `name=John&email=john%40example.com` becomes `{"name": "John", "email": "john@example.com"}` ; a dotted key nests the same way it already does for `query`/`GET /rel` (`user.email=...` → `{"user": {"email": "..."}}`). Reusing that decoder rather than inventing a second flat key-value parser is deliberate : `application/x-www-form-urlencoded` and a URL's own query string are the identical percent-encoded `key=value&key=value` syntax (same encoding, just carried in the body instead of after a `?`), so treating the body case as a second, separate thing to parse would just be duplicated logic with its own chance to drift from the first.
* Anything else (binary) : `body` is a base64-encoded string of the raw bytes — the only encoding that survives unmodified inside jsonb (a raw byte sequence isn't valid JSON text on its own, and a Go/JS/Postgres JSON encoder given arbitrary non-UTF-8 bytes as a "string" mangles them, replacing invalid sequences — silently corrupting the payload). Postgres's own `decode(body, 'base64')::bytea` recovers the original bytes inside the function body. Only reachable on a route that did NOT declare `## Request bodies`' `files bytea[]` parameter — see there for why a real upload should use that instead.
* No body at all (a `GET`, or any request with an empty body) : `body` is JSON `null`.
* A route declaring `## Request bodies`' `files bytea[]` parameter : `body` is ALWAYS JSON `null`, regardless of `content_type` — the payload is delivered exclusively through `files`/`parts_headers` in that case, never duplicated into `body` as well (a `(req, files bytea[])` route receiving a 10 MiB upload does NOT also carry a ~13 MiB base64 copy of the same bytes inside `req`).
* `application/json` whose body does not actually parse as valid JSON, or `application/x-www-form-urlencoded` whose body doesn't decode (a genuinely malformed percent-encoding, say) : `400`, same as any other malformed-request case (`## Postgres Exceptions` doesn't apply — this never reaches a function at all).

`RelHttpRequest.cookies` deliberately does NOT reuse the full `Cookie` shape (`value`/`httponly`/`secure`/`samesite`/`maxage`) the response side uses — a browser's `Cookie` header only ever sends `name=value`, the other four attributes are response-only (`Set-Cookie` attributes) and can never be known for an inbound cookie. Value-only avoids four fields that would always be empty/false/zero.

## Request bodies

`RelHttpRequest.body` above covers the common case — one body, JSON or text or a single binary blob. Real uploads (`multipart/form-data`, several independently-typed parts in one request, or a single raw binary `POST`) are expressed instead as EXTRA function parameters beyond `req RelHttpRequest`, matched by their TYPE SEQUENCE — never by parameter name, so a function author names them however they like (`files`, `attachments`, `uploads`, doesn't matter to rel) :

```sql
-- bytes only, no metadata
create function schema.upload(req RelHttpRequest, files bytea[]) returns RelHttpResponse /* ... */;
-- bytes plus full per-part metadata
create function schema.upload_with_headers(req RelHttpRequest, files bytea[], parts_headers jsonb) returns RelHttpResponse /* ... */;
```

Only these four shapes — `()`, `(req)`, `(req, files bytea[])`, `(req, files bytea[], parts_headers jsonb)` — are recognized route-function signatures for THIS mechanism — anything else (extra parameters, a different order, a different type) is simply not discovered as a route at all, same as any other signature mismatch today. (`specs/http-content.md ## Static files ### Upload destinations` adds a separate two-function shape family, `(req, part jsonb) returns RelUpload`/`(req, upload RelUpload) returns RelHttpResponse`, for an unrelated mechanism — the database making a binding decision about a file's destination without ever receiving its bytes — distinguished from the four here purely by its own argument/return TYPES, same type-sequence matching rule as everything else.) There is deliberately no separate single-file-only shape (an earlier draft of this section had `body bytea`/`bodies bytea[]` as two distinct families, five shapes beyond `(req)` alone — `body`, `body,mime`, `bodies`, `bodies,names`, `bodies,names,mimes`) : a single raw binary `POST` is treated as a ONE-ELEMENT `files` array (see below) rather than its own mechanism, so `files bytea[]` alone already covers "exactly one file" — `files[1]` — with no dedicated singular form needed. This also collapses what used to be three separate mismatch rules (see below) into two.

`parts_headers`, when declared, is a JSON array, index `i` describing `files[i]` :

```typescript
interface RequestPart {
  name: string | null         // Content-Disposition name= (the multipart form field key) ; null for the synthesized pseudo-part below
  filename: string | null     // Content-Disposition filename= ; null when this part isn't a file (e.g. a plain form field)
  content_type: string | null // this part's own Content-Type header ; null if the part had no Content-Type header at all — NOT the empty string
  headers: {[name: string]: string[]} // ALL of this part's own headers, including Content-Type/Content-Disposition verbatim (redundant with content_type/name/filename above, same convenience-plus-raw relationship RelHttpRequest.content_type/.headers already have) — same shape RelHttpRequest.headers already uses
}
```

`name` and `filename` are kept as their own first-class fields (parsing `Content-Disposition`'s own parameters isn't something a function should have to redo in plpgsql), rather than folded into `headers` — though `headers` still legitimately also contains the raw `Content-Disposition`/`Content-Type` header values verbatim, same relationship `RelHttpRequest.content_type`/`.headers` already have with each other (one promoted for convenience, both present). Unlike the earlier draft's `names`-falls-back-to-the-field-key rule, `name` and `filename` are now always independently available — a route distinguishing two differently-named file inputs no longer loses the field key just because a filename is also present.

For a single, non-multipart, raw binary `POST` (a request whose `Content-Type` isn't `multipart/*` but also isn't JSON/text — the same set of requests that would otherwise populate `body` via `## Request`'s "anything else" branch), `files` is a one-element array holding the whole raw body, and `parts_headers` (if declared) is a one-element array holding a SYNTHESIZED pseudo-part : `name: null`, `filename: null`, `content_type` = the request's own `content_type`, `headers` = the request's own `headers` (there's no separate "part" envelope to have its own headers when there was no multipart wrapper to begin with).

A real multipart form frequently mixes plain (non-file) fields with file uploads in one submission (a `title` text input alongside an attached file, say) — nothing here drops them : a plain field still lands in `files` (as its raw UTF-8 bytes — `convert_from(files[i], 'utf8')` recovers the string inside the function), with `parts_headers[i].name` set to its field key and `parts_headers[i].filename` staying `null` (no `filename=` on a plain field). There's no structural separation between "this index is a real file" and "this index is a plain form value" beyond checking `parts_headers[i].filename IS NULL` — a function that cares about the difference makes that call itself.

Declaring `files bytea[]` (with or without `parts_headers`) is a hard contract about what the route accepts — a mismatch against the actual request is always a `415 Unsupported Media Type`, never a silent fallback :

* A route declaring `files` receiving a request whose `content_type` falls into `## Request`'s JSON, `text/*`, or `application/x-www-form-urlencoded` branches (i.e. `body` would otherwise have been populated) is a `415` — there is no byte payload to hand over as a "file" separately from what's already fully represented as `body`.
* A route NOT declaring `files` (a plain `(req)`-only route) receiving an actual `multipart/form-data` request is ALSO a `415` — multipart data has nowhere to go without a `files` parameter, and is never silently base64-encoded whole (boundaries and all) into `body` as a fallback.
* A route declaring `files`, called with NO body at all (no `Content-Type`, empty body — e.g. a `__GET`-suffixed variant of an upload route, or any request that simply omits one) : NOT a `415` — `files` is an empty array (`'{}'::bytea[]`), `parts_headers` (if declared) an empty JSON array. A route that genuinely requires at least one file checks `array_length(files, 1)` itself and raises its own `RSxxx` if that matters to it ; rel does not treat "declared `files` but got none" as a shape mismatch on its own; only a WRONG shape (JSON/text body on a `files` route) is.
* A syntactically valid `multipart/form-data` envelope containing zero parts is treated the same as the no-body case immediately above — empty `files`/`parts_headers`, not a `415` and not an error, on any route (whether or not it declared `files`) : an empty multipart body is a genuinely empty request, not a shape mismatch.

### Limits

* `http.max_body_size` (see `## Configuration` above) bounds total bytes.
* `http.max_part_count` (default `100`) bounds the number of multipart parts a single request may contain, independent of their total byte size — without this, a request built from a very large number of near-empty parts stays under `http.max_body_size` while still costing real CPU/memory in per-part header parsing and allocation, a distinct resource-exhaustion shape from the byte-size one. 100 is sized generously above what a genuine multi-file form submission needs (a handful to a few dozen files) while still bounding the cost of a pathological one ; exceeding it is a `413 Payload Too Large`, checked incrementally while parsing (a request is rejected as soon as the count is exceeded, not after fully parsing an oversized part set). A synthesized single-binary-POST pseudo-part always counts as exactly one part, trivially under any sane limit.

### Known limitation : no true streaming to Postgres

`files`/`body` both require the ENTIRE relevant payload to be fully received and held in memory (bounded by `http.max_body_size`) before the route function is ever invoked — there is no mechanism here for a function to receive a live stream, or for rel to write bytes to Postgres incrementally as they arrive over HTTP. Raising `http.max_body_size` only moves this ceiling ; it does not change the fact that the whole payload is buffered at once. This is not an oversight to silently work around later — genuinely large uploads (video, multi-GB archives) are not what `files`/`body` are for.

This is not an inherent limitation of Postgres itself, though : `pg_largeobject` (`lo_*` functions, exposed to Go via `pgx.LargeObjects`) supports genuine chunked read/write without full in-memory buffering on either side. Using it would be a materially different, separately-designed mechanism — an `oid` reference in place of `bytea`, its own ACL/ownership and orphan-cleanup lifecycle distinct from ordinary table storage, and rel's handler streaming HTTP bytes into it via a sequence of `lo_write` calls before the route function runs — not something this pass adds. Documented here as the answer to "is streaming possible at all," not as a promise of when.

## Responses

If a function's return type is `RelHttpResponse`, rel expects the following shape and handles it as a response:

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

`jwt_attrs` controls how a `jwt` set by this same response is minted — `maxage` overrides `jwt.max_age` for this session (both the token's `exp - iat` and the cookie's `Max-Age` follow it), `samesite` overrides `jwt.same_site` for this cookie only.

## Postgres Exceptions

Raised while handling an HTTP route function or the session-check function, an exception with code `RSxxx` gives the response status `xxx`, with whatever text was raised as the body.

```sql
raise exception 'Access denied' using errcode = 'RS401';
raise exception 'Not found' using errcode = 'RS404';
```

Any other error code results in the error page with status 500, unless it's a known Postgres code with unambiguous HTTP semantics (e.g. permission denied → 401).
