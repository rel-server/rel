# New Routes Specification

- Current route system involves database discoverability and forces the use of `/route/schema/function`.

**Problems**
- Not possible to define arbitrary routes
- Schema constrains heavily the shape of the API
- uploads have a lot of different mechanics
- static files permissions and uploads are split
- We have to impose to the user to create domains to discover them
- It is impossible to override index

This specs aims to resolve these issues.

## New mechanism

Functions are associated with chi routes. There are two sources of declaration : the config or database comments on the functions themselves.

At every reload, the router is recreated, and database routes are merged to it.

## JSON Types

```typescript

// In configuration or in pg comment on the function
interface Route {
  function: string
  schema: string
  path: string // a path in chi syntax

  method?: string // a comma separated list of accepted methods
  template?: string // if not provided by the response, the template
  stream_upload?: boolean
  middleware?: boolean
}

// For multi-part
interface Part {
  name: string
  filename: string | null
  content_type: string              // as claimed by the client
  sniffed_content_type?: string     // as detected by rel from the bytes themselves ; absent when the bytes haven't been received yet (stream_upload's first call) ; the function decides whether/how a mismatch matters
  headers: {[name: string]: string[]}
}

interface HttpRequest {
  method: string
  uri: string
  query: unknown
  headers: {[name: string]: string[]}
  content_type: string              // as claimed by the client
  sniffed_content_type?: string     // as detected by rel from the body's bytes ; present whenever the request carried a body, same purpose as Part.sniffed_content_type
  body: unknown
  cookies: {[name: string]: string}
  parts?: Part[]
  jwt: JWT | null
  csp_nonce: string
  context?: unknown          // shallow-merged in by any middleware that ran first ; absent if none did
  static?: { exists: boolean, size?: number, modified_at?: string }  // what would be served at this path under http.static.path

  upload?: Upload
}

interface Cookie {
  value: string
  httponly?: boolean   // default true
  secure?: boolean     // default true
  samesite?: string    // default "Lax"
  maxage?: number      // default http.cookies_max_age ; 0 or negative clears the cookie
}

interface HttpResponse {
  upload?: Upload

  status?: number         // 200 if unset

  headers?: {[name: string]: string | string[]}
  cookies?: {[name: string]: Cookie | string | null}
  jwt?: JWT | null       // set a session ; null clears it (logout)
  jwt_attrs?: { samesite?: string, maxage?: number }
  csp?: string           // override the process-wide CSP for this one response

  template?: string          // render via a Jet template, the other return type is then used as the Data
  static_file?: string // if the result is null and this is set, reply with the static file at that path
  content_type?: string // introspected
}

interface Upload {
  path?: string                      // relative to http.static.path's first directory ; omitted = discard the upload once received
  mkdir?: boolean                    // create path's parent directory if missing
  overwrite?: 'allow' | 'disallow'   // default 'disallow'
  max_size?: number                  // tighten http.max_upload_size for this request only (e.g. a per-user quota) ; can only lower it, never raise it
  part?: Part                        // absent when returned by __prepare ; filled in by rel before the mandatory function runs
  size?: number                      // rel-filled, post-stream, the actual observed byte count
}

```

## Cookie clearing

Setting a cookie to `null` in its response clears it.

## Function prototype

Functions have limited prototype possibilites.

- The first argument *may* be of type `json `or `jsonb`, in which case, they will always receive the request object.
- The second argument *may* be of type `bytea` or `bytea[]`, enabling upload capabilities directly in postgres, arrays being for multipart contents (parts will be filled in the request). A route not defining them may not receive files.
  > `bytea[]` only ever receives bytes via a `multipart/form-data` body ; a non-multipart body sent to such a route is a `415`. Conversely `bytea` (singular) only ever receives bytes via a raw, non-multipart body (the whole request body becomes the single value) ; a `multipart/form-data` body sent to such a route is also a `415`, since there'd be nowhere to route the individual parts. A route declaring neither still receives `parts` in the request for a multipart body, even though it can't retrieve their bytes.
- Other arguments must be named and of type `text` and will receive what the chi placeholders matched in the path.

If the function has other "IN" arguments not present in the parsed path, rel prints an error and disables the function.

A route function may have no "in" parameters.

A route function may return a single type, in which case if it returns normally, rel replies with `200` and the following mimetypes :

- text : text/plain
- json/jsonb : application/json
- bytea : application/octet-stream
- any domain that has a `/` in its name : the domain name as mimetype
  > this is what allows bytea to be aliased to things like `application/png`

To have more control over the response, to mint JWT for instance or set cookies or any other actions over the response, then rel expects the following return structure : `..., OUT JSON/JSONB, OUT some_other_type) returns record`

Where `some_other_type` acts like the single-return version - except if the response overrides the mimetype in the returned JSON in `content_type`.

## `stream_upload`

If a route defines `stream_upload`, then on receiving a single upload, it will be called twice: the first time with `upload: {part}` in the request — the upload's metadata (filename, claimed `content_type`, no `sniffed_content_type` or `size` yet, since no bytes have been received) before any of its bytes are received — expecting the single `upload` key in the response where it may specify `path`/`mkdir`/`overwrite`/`max_size` and a `NULL` response content; the second time with `upload` filled in the request (`part` plus the now-known `size`) indicating that the file was correctly received. This part works the same as with "choosing a destination without routing bytes through Postgres."

A `stream_upload` function takes no `bytea`/`bytea[]` argument at all — the whole point is that the file's bytes never enter Postgres, streamed straight to disk instead, the same as today's `__prepare`.

A `stream_upload` function must use the full-control `..., OUT JSON/JSONB, OUT some_other_type) returns record` shape (`## Function prototype`) — it's the only way to return the `upload` key on the first call. On that first call, only the `upload` key of the response is read ; any other side-effecting field (`status`/`cookies`/`headers`/`jwt`/`template`/`content_type`) is ignored, and the second `OUT` column (content) is discarded. The second call's response is the one actually sent to the client, exactly like any other full-control route.

## Content-type sniffing

rel sniffs the actual bytes of any request body or upload part and exposes the result as `sniffed_content_type` (`HttpRequest`) or `Part.sniffed_content_type`, alongside the client-claimed `content_type` it already exposes. rel never rejects a mismatch itself — a function compares the two and `raise`s `RSxxx` if a mismatch matters for that route; one that doesn't care about the claimed type at all can ignore both.

## Static path masking

A function defined at an exact path takes priority over a static file at that same path. Returning `static_file` in the response serves that file instead of `content`; returning neither serves a `404` — but only when `status` is ALSO unset. A response with an explicit `status` (a middleware terminating with `{status: 401}`, say) and no `content`/`static_file` is a real, deliberate answer, not a masking no-op, and is sent as that status with an empty body.

`static_file` resolves under the same traversal rules as `http.static.path` and `Upload.path` — no `..`, no dotfile path segment, nothing outside the configured static directories.

A route masking `/` overrides the default index behavior.

For every request, not just one hitting a masking function, rel `os.Stat`s the path that would be served under `http.static.path` (one stat call, negligible cost) and includes the result as `request.static: { exists: boolean, size?: number, modified_at?: string }` — `exists: false` when nothing is there, or when the path fails traversal validation. There is no implicit fallback to the static file when a masking function returns neither `content` nor `static_file` — with `request.static` available, the function has what it needs to decide explicitly, including returning `static_file` itself to defer to disk.

Restricting a subtree beyond exact-path masking is superseded by middleware — see `## Middleware`.

## Middleware

A function marked `middleware` runs before every route whose path is prefixed by this function's path. The prefix match is over each path's anonymized form (see `## How it works`'s `{name:regexp?}` → `{}` sort key), segment by segment — `/api/{tenant}` (anonymized `/api/{}`) is a prefix of `/api/{tenant}/orders` (anonymized `/api/{}/orders`).

When more than one middleware function applies to a request, they run shortest-prefix-first, then in alphabetical order by `schema`.`function` to break a tie between two middleware at the same prefix length, each as one additional function call on the request's own connection, in the same transaction as the route function, sequentially.

Middleware applies uniformly across `/rel`, since it's served by the same router — a middleware function at `/` runs on every request regardless of which one it targets, except `/auth/*` (see below).

A middleware function takes the same request shape as an ordinary route function, and returns the same `..., OUT JSON/JSONB, OUT some_other_type) returns record` shape a full-control route does (`## Function prototype`). It may return:

- nothing (`NULL`), letting the request proceed unchanged;
- a Postgres exception with an `RSxxx` errcode, short-circuiting the request with that status, the same as any other route function;
- a response with none of `status`/`template`/`content_type`/`static_file` set, and `content` (if any) shallow-merged into `request.context` before the next middleware or the route function runs — never into the request's own top-level fields (`jwt`, `body`, `parts`, ...). A later middleware's keys win over an earlier one's on conflict.
- a response with `status`, `template`, `content_type`, or `static_file` set, terminating the request immediately with that response sent as-is; no further middleware or route function runs.

A middleware function may also set any of `HttpResponse`'s side-effecting fields — `cookies`, `headers`, `jwt`, `jwt_attrs`, `csp` — on its returned object, whether or not it terminates the request; on a per-key basis, a later middleware's or the route function's own value for the same key wins over an earlier middleware's (an earlier middleware's other keys in the same field still apply). See `## Cookie clearing` for how a middleware (or any response) clears an individual cookie.

Middleware runs only ahead of a request that resolves to something — an ordinary route, a static file, or `/rel` — never on a request that resolves to nothing, so a middleware gating `/static/private/*` still fires for files under that prefix even though there's no route function there.

A middleware function marked at a prefix that also serves static files applies to static serving under that prefix too, superseding `http.static.access.<name>.prefix`/`.function`.

Middleware supersedes `http.functions.check_session` — a session-revocation check becomes an ordinary middleware function instead, running after the role switch the same as any other middleware, terminating the request with `{status: 401, jwt: null, ...}` (see above) to reject and clear the session in one step, the same net effect `check_session`'s own rejection has today.

`/auth/*` (OIDC/SAML login, callback, ACS, metadata) is exempt from all middleware, unconditionally — a session-checking middleware there could make login unrecoverable, since the callback that would let a user regain a valid session is itself blocked by the check meant to detect the lack of one.

`/auth/*` and `/rel`, and any other path rel itself serves, are reserved: no user-declared route or middleware is ever registered there, from config or from a database comment alike — see `## How it works`. This means a middleware can never be declared directly at `/rel` itself ; gating `/rel` means declaring the middleware at a shorter prefix that still covers it (`/`, at minimum).

Renew (session cookie renewal) always runs after the whole middleware chain has let a request through, never before — a middleware that rejects a request must never hand back a freshly renewed cookie for the very session it just rejected. This mirrors `check_session`'s own ordering today (Check before Renew).

> Thoughts: `stream_upload`'s middleware pass (see `## \`stream_upload\`` below) runs once, ahead of the first call only — its own accumulated cookies/headers/jwt/jwt_attrs/csp still apply to the second call's response, the one actually sent to the client, even though the chain itself never runs a second time.

> Question: when a middleware sets `jwt` ahead of a route function that never itself sets `jwt`, which function's identifier gates `http.functions.allowed_auth` — the middleware's own, or the route's? Advise.

## Templates

A response's `template` field renders a Jet template with the function's own returned content as `Data`, valid only when the return type is text or json/jsonb.

Jet renders text only. A function returning a bytea or mimetype-domain type must not also set `template`; rel disables the route with a warning at discovery time if both are present.

## In the configuration

In the configuration, the `route` namespace need at least `route.<schema>.<function>.path` to make a function routable, `schema` and `function` are inferred.

Routes in configuration override routes defined in the database with a warning.

## In comments

Comments are trimmed and inspected to figure out if there is a route configuration object given, in one of two formats — HUML, or JSON as a fallback for a comment author who reflexively reaches for it:

```huml
route:: path: "/some/path", method: "POST"
```

```json
route: {"path": "/some/path"}
```

Detection: leading `route::` → HUML; `route:` followed by `{` → JSON.

## Method inference

When unspecified, method is assumed to be `GET`, unless it has a `bytea`/`bytea[]` argument or defines `stream_upload`, in which case it becomes `POST`

Even if it violates HTTP spec, a `bytea` `bytea[]` method declared to be on `GET` may receive a body if present on such verb.

## How it works

To resolve ambiguities, routes are sorted by longest route paths _first_, so that paths with more specificity are preferred over generic ones, and to let `*` star paths absorb non-matching routes, except for middlewares that are sorted shorted prefix first, then schema/function name.

Sorting is done by "anonymizing" paths variables, so that `{name:regexp?}` becomes `{}` as a sort key.

If two routes "collide" — same simplified name *and* an overlapping resolved method — a warning is printed and no route is enabled at that name/method. Two routes at the same simplified name with disjoint methods (e.g. one `GET`, one `POST`) don't collide at all — both are enabled, dispatched by method.

A path under a reserved prefix (`/auth/*`, `/rel`, or any other path rel itself serves) is a different, higher-priority case, not an ordinary collision: rel's own endpoint always wins unconditionally, over both config- and database-declared routes, and discovery logs a distinct "reserved path" error naming the shadowed function — never the generic collision warning.

## Related topics

Auth machinery doesn't change, except `http.functions.check_session`, superseded by middleware — see `## Middleware`.

# Impacts

- `/route` disappears, and so do the named domains `RelHttpRequest`, `RelHttpResponse`, and `RelUpload` — they existed only to make a function's shape discoverable, which the comment/config declaration now does directly. Mimetype-named domains (e.g. `"image/png"`) are unaffected and stay load-bearing: they're how a route's return type resolves to a `Content-Type`, not a discovery mechanism.
- `http.request_domain_name`, `http.response_domain_name`, and `http.upload_domain_name` are removed along with the domains they named.
- The `__VERB` function-name suffix convention (`__GET`, `__POST`, ...) is removed in favor of `Route.method`.
- `http.functions.allowed_routes` is removed entirely — it only ever existed to bound automatic shape-based discovery, which no longer exists now that every route is explicitly declared.
- `http.functions.allowed_auth` carries over unchanged, and matters more than before: since any function can now sit on any path, it's the only thing stopping an arbitrary function from minting a session.
- check_session is obsolete in favor of middleware, documentation should mention how middlewares can be used to implement session checking — see `## Middleware` for the reject-and-clear-session pattern.
- static checks functions (`http.static.access.*`) are no longer necessary; documentation should also be written on how to gate access to files with middleware.
- Session-checking middleware runs *as the request's already-resolved role*, unlike `check_session`, which runs on the primary connection before the role switch. Every role that should reach a path — including `~anonymous` — needs an explicit `EXECUTE` grant on every middleware function covering that path, or requests under that prefix start failing with `403` (an ordinary Postgres permission-denied error, classified like any other route call's).
