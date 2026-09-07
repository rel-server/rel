---
icon: material/swap-horizontal
---

# Requests and responses

A route is an ordinary Postgres function that rel calls over HTTP. Nothing about a function's
own signature makes it a route automatically — it becomes one only once you declare it at a
path, and rel then talks to it through the plain JSON shapes this page documents.

## Declaring a route

Declare a route one of two ways: a `COMMENT ON FUNCTION` on the function itself, or a
`route.<schema>.<function>.*` entry in configuration. Either way, the declaration names the
`path` (chi syntax — `{name}` placeholders, `{name:regexp}` constraints) and, optionally, the
accepted HTTP `method`(s), a default `template`, and whether the function is `stream_upload`
or `middleware` (a function that runs ahead of other routes — see [HTTP routes ##
Middleware](index.md#middleware)).

The comment form uses HUML (a leading `route::`) or JSON (a leading `route:` followed by `{`):

```sql
comment on function hotel.guest_login(jsonb) is 'route:: path: "/hotel/login", method: "POST"';
```

```sql
comment on function hotel.guest_login(jsonb) is 'route: {"path": "/hotel/login", "method": "POST"}';
```

Or entirely in configuration, with no comment on the function at all — `schema` and
`function` are inferred from the config key itself:

```toml
[route.hotel.guest_login]
path = "/hotel/login"
method = "POST"
```

A config-declared route overrides a comment-declared one on the same function, with a
warning. `/auth/*`, `/rel`, and anything else rel itself serves are reserved — a route can
never be declared there, from either source.

## Function prototype

- The **first** argument may be `json`/`jsonb`, in which case it always receives the
  `HttpRequest` object below. A route needing nothing from the request may omit it entirely.
- The **second** argument may be `bytea` or `bytea[]`, enabling uploads directly in Postgres —
  see [File uploads](uploads.md) for the full mismatch/sizing rules.
- Any other argument must be named and typed `text`; it receives whatever the
  correspondingly-named `{placeholder}` in the path matched.

```sql
create function hotel.room(req jsonb, id text) returns jsonb language sql as $$
  select jsonb_build_object('id', id, 'query', req->'query');
$$;
comment on function hotel.room(jsonb, text) is 'route:: path: "/hotel/rooms/{id}"';
```

Returning a single type replies `200` with a mimetype resolved structurally: `text` →
`text/plain`, `json`/`jsonb` → `application/json`, `bytea` → `application/octet-stream`, and a
domain whose name contains `/` (over `bytea` or `text`) → that name as `Content-Type` — see
[Static files ## Returning binary or text content
directly](static-files.md#returning-binary-or-text-content-directly).

For control over the response itself — status, cookies, `jwt`, a template, deferring to a
static file — declare two trailing `OUT` columns instead: `..., OUT resp JSON/JSONB, OUT
content <type>) returns record`. `resp` receives `HttpResponse` (below); `content` behaves
like the single-return value above, except `resp.content_type` can override its resolved
mimetype. This "full-control" shape is the only way for a function to set `jwt`, override
`status`, or render a `template`, and is mandatory for a `middleware`/`stream_upload`
function.

## `HttpRequest`

```typescript
interface HttpRequest {
  method: string
  uri: string
  query: unknown
  headers: {[name: string]: string[]}
  content_type: string              // as claimed by the client
  sniffed_content_type?: string     // as detected by rel from the body's bytes ; present whenever the request carried a body
  body: unknown
  cookies: {[name: string]: string}
  parts?: Part[]
  jwt: JWT | null
  csp_nonce: string
  context?: unknown          // shallow-merged in by any middleware that ran first ; absent if none did
  static?: { exists: boolean, size?: number, modified_at?: string }  // what would be served at this path under http.static.path — see Static files ## Static path masking
  upload?: Upload             // stream_upload routes only
}

interface Part {
  name: string | null
  filename: string | null
  content_type: string | null       // as claimed by the client
  sniffed_content_type?: string     // as detected by rel from the bytes themselves ; absent when the bytes haven't been received yet (stream_upload's first call)
  headers: {[name: string]: string[]}
}
```

`body` is decoded according to `content_type`:

| `content_type` | `body` becomes |
|---|---|
| `application/json` (or any `+json` suffix) | the request's actual JSON value — object, array, or scalar |
| `text/*` | the plain string |
| `application/x-www-form-urlencoded` | a JSON object, decoded the same structural way a `GET /rel` query string is — dotted keys nest the same way |
| anything else | a base64-encoded string of the raw bytes — `decode(req.body, 'base64')::bytea` recovers them |
| no body at all (a `GET`, most commonly) | JSON `null` |

A route declaring a `bytea`/`bytea[]` second argument always gets `body: null` instead,
regardless of `content_type` — the payload arrives exclusively through the dedicated
argument(s), never duplicated into `body` as well. `parts` is always filled in for a
multipart request, regardless of whether the route even declares `bytea[]` — a route that
doesn't still sees each part's metadata, just not its bytes. See [File
uploads](uploads.md).

`static` is computed once per request, before any route runs — see [Static files ## Static
path masking](static-files.md#static-path-masking). `context` is only ever set once a
middleware ahead of this function has actually merged something in — see [HTTP routes ##
Middleware](index.md#middleware).

## `HttpResponse`

Only a full-control function (the two-`OUT`-column shape) can return this — a plain-return
function's value is sent as the body directly, per the mimetype table in [HTTP
reference](reference.md#function-prototypes).

```typescript
interface HttpResponse {
  status?: number         // 200 if unset

  headers?: {[name: string]: string | string[]}
  cookies?: {[name: string]: Cookie | string | null}
  jwt?: JWT | null       // set a session ; null clears it (logout)
  jwt_attrs?: { samesite?: string, maxage?: number }
  csp?: string           // override the process-wide CSP for this one response

  template?: string          // render via a Jet template, the second OUT column is then used as Data
  static_file?: string       // if the second OUT column is null and this is set, reply with the static file at that path instead
  content_type?: string      // overrides the second OUT column's own resolved mimetype

  upload?: Upload             // stream_upload routes only, first call
}

interface Cookie {
  value: string
  httponly?: boolean   // default true
  secure?: boolean     // default true
  samesite?: string    // default "Lax"
  maxage?: number      // default http.cookies_max_age ; setting the cookie to null instead clears it
}

interface Upload {
  path?: string                      // relative to http.static.path's first directory ; omitted = discard the upload once received
  mkdir?: boolean                    // create path's parent directory if missing
  overwrite?: 'allow' | 'disallow'   // default 'disallow'
  max_size?: number                  // tighten http.max_upload_size for this request only ; can only lower it, never raise it
  part?: Part                        // filled in by rel before the function runs
  size?: number                      // rel-filled, post-stream, the actual observed byte count ; absent on the first call
}
```

Setting `cookies.<name>` to a plain string is shorthand for rel's own defaults (`secure`,
`httponly`, `SameSite=Lax`, `http.cookies_max_age`); pass the full `Cookie` object
(`{value, httponly, secure, samesite, maxage}`) to override any of them. Setting a cookie to
`null` clears it (`MaxAge: -1`), rather than setting an empty, persistent value.
`jwt_attrs.maxage` overrides the session's own lifetime for this login only, without changing
`jwt.max_age` process-wide.

A route or middleware ahead of it can also mint/clear a session by setting `jwt` — see
[Authentication ## Minting a session](authentication.md#minting-a-session) for the full rules,
including `http.functions.allowed_auth`.

## Rendering HTML with a template

Set `template` (a path relative to `http.templates.path`) instead of building the second
`OUT` column yourself as HTML, and rel renders it server-side with
[Jet](https://github.com/CloudyKit/jet) — including trusted inline `<script>`/`<style>` via
the per-request CSP nonce, and layouts via `extends`/`block`. A route's own declaration can
also set a default `template`, rendered whenever the function's response doesn't set one
itself — the only way to render a template from a plain-return function at all, since it
never returns an envelope to set `template` on. See [Rendering HTML with
templates](templates.md) for the full mechanism.
