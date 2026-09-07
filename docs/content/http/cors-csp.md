---
icon: material/web
---

# CORS and CSP

Both apply uniformly across `/rel`, every declared route, and the static fallback.

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
invoking a route or middleware function — whether or not a route actually exists at that path
yet.

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

Every declared-route request — and every SSO callback (`/auth/oidc/{name}/callback`,
`/auth/saml/{name}/acs`; see [Authentication](authentication.md)) — gets a fresh, random nonce,
automatically appended to that response's `script-src`/`style-src` as `'nonce-<value>'`: for
trusted inline `<script>`/`<style>` blocks without loosening the policy for everything else. To
use it, put the exact same value in that tag's own `nonce` attribute:
`<script nonce="<value>">...</script>`. A `<script>`/`<style>` tag without a matching `nonce`
(and without `'unsafe-inline'` explicitly configured) simply doesn't run, browser-enforced.

A route function reaches the value two ways, both equally valid: `req.csp_nonce` on the
request it received (see [Requests and responses](requests-responses.md)) — usable in
any hand-built HTML the route returns itself, no template involved — or `{{ Nonce }}` inside a
Jet template (see [Rendering HTML with templates](templates.md) for the full mechanics and
worked examples, including why dynamic data belongs in a JSON island rather than a direct
interpolation). Either way it's the same nonce, so a value copied out of `req.csp_nonce` and one
read from `{{ Nonce }}` always agree. An SSO callback function only has the template path: it
never receives a request object at all (just `{jwt, identity, state}`, as `jsonb`), so
`{{ Nonce }}` inside a template its response points at is the only way it ever touches the
nonce — the callback function doesn't need to read the value itself for this to work, since the
template renderer resolves it from the request automatically.

`/rel` and the static fallback never get a nonce at all — not merely one that goes unused.
`/rel` always answers JSON, never HTML, and the static fallback serves files as-is with no
per-request templating to inject a nonce into, so neither can ever have a use for one; their
`Content-Security-Policy` header carries the configured policy with no nonce token appended.

### Per-response override

A single full-control route or middleware can override the whole policy for just its own
response via `HttpResponse.csp` (see [Requests and responses](requests-responses.md)) — the
same nonce rules apply, scoped to that response's own effective `default-src`. There is no
equivalent override for CORS: a preflight is answered before any route function runs, so
there's no response to read a per-route policy from — CORS policy is always static and
process-wide.
