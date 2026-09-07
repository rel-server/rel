---
icon: material/swap-horizontal
---

# Requests and responses

A route or middleware function talks to rel through two plain JSON shapes — no domain type to
create, no name to configure. A function declaring a `json`/`jsonb` first argument receives
`HttpRequest` there; a function using the full-control, two-`OUT`-column shape (see [HTTP
routes ## Function prototype](index.md#function-prototype)) returns `HttpResponse` as its
first `OUT` column.

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
