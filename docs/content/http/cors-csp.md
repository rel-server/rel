---
icon: material/web
---

# CORS and CSP

Both apply uniformly across `/rel`, `/route`, and `/static`.

## CORS

CORS is closed by default — no `Access-Control-*` headers at all — until you list allowed
origins:

```
http.cors.allowed_origins = "https://app.example.com,https://admin.example.com"
http.cors.allowed_methods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
```

- `http.cors.allowed_origins` — a comma-separated list of exact origins (`scheme://host[:port]`,
  no wildcards within an entry), or the literal `*`.
- `http.cors.allowed_methods` (default `GET, POST, PUT, PATCH, DELETE, OPTIONS`) — methods a
  preflight may approve.
- `http.cors.allowed_headers` (default `Content-Type`) — request headers a preflight may
  approve, beyond what every browser always allows.
- `http.cors.max_age` (default `600` seconds) — how long a browser may cache one preflight
  response.

There is no separate credentials toggle: whenever a request's `Origin` matches an entry in
`allowed_origins`, rel always sends `Access-Control-Allow-Credentials: true` alongside it.
Setting `http.cors.allowed_origins` to the literal `*` allows every origin but never sends
credentials in that mode — the JWT cookie becomes useless to a cross-origin caller, so `*` is
only right for a genuinely public, anonymous-role-only API.

A non-"simple" cross-origin request (a JSON body, a custom header, a method outside
`GET`/`HEAD`/`POST`) triggers a browser preflight, answered entirely by rel itself — never by
invoking a route function — whether or not a route actually exists at that path yet.

## CSP

A `Content-Security-Policy` header is sent on every response by default (`default-src 'self'`),
even with no configuration at all. Tighten or loosen individual directives:

```
http.csp.script_src = "'self' https://cdn.example.com"
http.csp.img_src = "'self' data:"
```

- `http.csp.default_src` (default `'self'`) and one key per other directive
  (`script_src`/`style_src`/`img_src`/`font_src`/`connect_src`/`object_src`/
  `frame_ancestors`/`base_uri`/`form_action`) — each falls back to `default_src` when unset.
- `http.csp.policy` — a full, raw `Content-Security-Policy` header value; replaces every
  individual `http.csp.*` directive above entirely when set, rather than merging with them.

### Nonce

Every request gets a fresh nonce (`req.csp_nonce`), automatically appended to that response's
`script-src`/`style-src`, for trusted inline `<script>`/`<style>` blocks without loosening the
policy for everything else.

### Per-response override

A single route can override the whole policy for just its own response via
`RelHttpResponse.csp` (see [Requests and responses](requests-responses.md)) — the same nonce
rules apply, scoped to that response's own effective `default-src`. There is no equivalent
override for CORS: a preflight is answered before any route function runs, so there's no
response to read a per-route policy from — CORS policy is always static and process-wide.
