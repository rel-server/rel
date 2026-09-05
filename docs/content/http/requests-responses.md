---
icon: material/swap-horizontal
---

# Requests and responses

A route function talks to rel through two Postgres domains, `RelHttpRequest` and
`RelHttpResponse` — plain `jsonb` underneath, with a name rel recognizes rather than a type
it builds in for you. Nothing about the routing mechanism works until both exist in your
database.

## `RelHttpRequest`

Create the domain once, in any schema:

```sql
create domain "RelHttpRequest" as jsonb;
```

- **Name**: configurable via `http.request_domain_name` (default `RelHttpRequest`) — see
  [Configuration](../configuration/index.md).
- **Resolution**: rel matches this setting against the domain's bare name. A value containing
  a `.` is treated as already schema-qualified and matched exactly; otherwise rel searches
  every schema for a domain with that bare name. Zero matches only logs a warning — routes
  needing `RelHttpRequest` simply aren't discovered, not a fatal error. More than one match
  across different schemas **is** a fatal startup error, naming every schema the domain was
  found in — a multi-schema project can't have two distinct `RelHttpRequest` domains active
  at once.
- **Underlying type**: rel does not check that the domain is actually built on `jsonb` — it
  only matches the type by name. Use `jsonb` anyway; anything else won't decode the way this
  page describes.

A function that declares a `RelHttpRequest` argument (alone, or alongside one of the
[upload shapes](uploads.md#receiving-raw-bytes)) receives the request as this shape:

```typescript
interface RelHttpRequest {
  method: string
  uri: string
  query: unknown
  headers: {[name: string]: string[]}
  content_type: string
  body: unknown
  cookies: {[name: string]: string}
  jwt: JWT | null
  csp_nonce: string
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

A route declaring the `files bytea[]` upload parameter always gets `body: null` instead,
regardless of `content_type` — the payload arrives exclusively through `files`/
`parts_headers`, never duplicated into `body` as well. See [File uploads](uploads.md).

## `RelHttpResponse`

```sql
create domain "RelHttpResponse" as jsonb;
```

- **Name**: `http.response_domain_name` (default `RelHttpResponse`); same resolution and
  underlying-type rules as `RelHttpRequest` above. An unresolved `RelHttpResponse` domain
  disables every route that would return it — including every login function — so rel warns
  loudly when it can't find one.

```typescript
interface RelHttpResponse {
  status: number         // 200 if unset
  content_type: string
  content: unknown

  headers?: {[name: string]: string | string[]}
  cookies?: {[name: string]: Cookie | string}
  jwt?: JWT | null       // set a session ; null clears it (logout)
  jwt_attrs?: { samesite?: string, maxage?: number }
  csp?: string           // override the process-wide CSP for this one response

  template?: string          // render via a Jet template instead of `content`
  template_data?: unknown
}
```

Setting `cookies.<name>` to a plain string is shorthand for rel's own defaults (`secure`,
`httponly`, `SameSite=Lax`, `http.cookies_max_age`); pass the full `Cookie` object
(`{value, httponly, secure, samesite, maxage}`) to override any of them. `jwt_attrs.maxage`
overrides the session's own lifetime for this login only, without changing
`jwt.max_age` process-wide.

## Rendering HTML with a template

Set `template` (a path relative to `http.templates.path`) instead of building `content`
yourself, and rel renders it server-side with [Jet](https://github.com/CloudyKit/jet) —
including trusted inline `<script>`/`<style>` via the per-request CSP nonce, and layouts via
`extends`/`block`. See [Rendering HTML with templates](templates.md) for the full mechanism.
