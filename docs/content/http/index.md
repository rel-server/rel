---
icon: material/routes
---

# HTTP routes

`/rel` answers one shape: a query tree, read or written back. Anything that doesn't fit that
shape — logging a guest in, sending a confirmation email, running a multi-step booking
workflow with real side effects — goes through a declared route instead. A route is an
ordinary Postgres function, written in whatever language Postgres supports (`plpgsql`, `sql`,
`plv8`, ...), that you explicitly declare at a path and rel calls directly over HTTP.

## Declaring a route

A route is declared one of two ways: a `COMMENT ON FUNCTION` on the function itself, or a
`route.<schema>.<function>.*` entry in configuration. Either way, the declaration names the
path (chi syntax — `{name}` placeholders, `{name:regexp}` constraints) and, optionally, the
accepted HTTP method(s), a default template, and whether the function is `stream_upload` or
`middleware` (see [File uploads](uploads.md), [Templates](templates.md), and
[Middleware](#middleware) below).

The comment form uses HUML (a leading `route::`) or JSON (a leading `route:` followed by `{`)
— pick whichever you reach for:

```sql
comment on function hotel.guest_login(jsonb) is 'route:: path: "/hotel/login", method: "POST"';
```

```sql
comment on function hotel.guest_login(jsonb) is 'route: {"path": "/hotel/login", "method": "POST"}';
```

Or entirely in configuration, with no comment on the function at all:

```toml
[route.hotel.guest_login]
path = "/hotel/login"
method = "POST"
```

A config-declared route overrides a comment-declared one on the same function, with a
warning — the function only needs a `path`; `schema` and `function` are inferred from the
config key itself. Two functions colliding on the same anonymized path (see [How routes are
matched](#how-routes-are-matched) below) with an overlapping method are both disabled, with
an error naming both; the same path with disjoint methods (one `GET`, one `POST`) is fine —
both are registered, dispatched by method. `/auth/*`, `/rel`, and anything else rel itself
serves are reserved: a route or middleware can never be declared there, from either source.

```sql
create function hotel.guest_login(req jsonb, out resp jsonb, out content jsonb)
returns record language plpgsql as $$
declare
  guest_row hotel.guests;
begin
  select * into guest_row from hotel.guests
  where email = (req->'body')->>'email';

  if not found then
    raise exception 'Invalid credentials' using errcode = 'RS401';
  end if;

  resp := jsonb_build_object(
    'status', 200,
    'jwt', jsonb_build_object('role', 'guest', 'guest_id', guest_row.id)
  );
  content := jsonb_build_object('id', guest_row.id, 'name', guest_row.first_name);
end;
$$;
comment on function hotel.guest_login(jsonb) is 'route:: path: "/hotel/login", method: "POST"';
```

By default, any route (or middleware) function can set `jwt` on its response and thereby
authenticate the caller as any role. Restrict that with `http.functions.allowed_auth`, scoped
to your actual login functions — see [Configuration](../configuration/index.md). The rest of
a session's lifecycle (claims, renewal, roles) is covered in
[Authentication](authentication.md).

## Function prototype

- The **first** argument may be `json`/`jsonb`, in which case it always receives the request
  object (see [Requests and responses](requests-responses.md)). A route needing nothing from
  the request may omit it entirely.
- The **second** argument may be `bytea` or `bytea[]`, enabling uploads directly in Postgres —
  see [File uploads](uploads.md) for the full mismatch/sizing rules. A `stream_upload`
  function must not declare either.
- Any other "IN" argument must be named and typed `text`; it receives whatever the
  correspondingly-named `{placeholder}` in the path matched. An extra `text` argument with no
  matching placeholder — or any argument of another type past this point — disables the
  function with a discovery-time error.

Returning a single type replies `200` with a mimetype resolved structurally, same as ever:
`text` → `text/plain`, `json`/`jsonb` → `application/json`, `bytea` → `application/octet-stream`,
and a domain whose name contains `/` (over `bytea` or `text`) → that name as `Content-Type` —
see [Static files ## Returning binary or text content
directly](static-files.md#returning-binary-or-text-content-directly).

For control over the response itself — status, cookies, `jwt`, a template, deferring to a
static file — declare two trailing `OUT` columns instead: `..., OUT resp JSON/JSONB, OUT
content <type>) returns record`. `resp` carries every side-effecting field (see [Requests and
responses](requests-responses.md)); `content` behaves like the single-return value above,
except `resp.content_type` can override its resolved mimetype. This "full-control" shape is
mandatory for a `middleware` or `stream_upload` function, and the only way for any function to
set `jwt`, override `status`, or render a `template`.

## Middleware

A function declared with `middleware: true` runs ahead of every route (and, for a `/`-rooted
one, `/rel` and static files too) whose path it prefixes — the anonymized-path prefix match is
segment by segment, so `/api/{tenant}` covers `/api/{tenant}/orders` but not `/api2/...`. When
more than one middleware applies, they run shortest-prefix-first, then alphabetically by
`schema.function`, each as one more function call in the request's own transaction.

A middleware function takes the same request shape and the same full-control return shape as
any other route. Its response may:

- return nothing (`NULL`) — proceed unchanged;
- raise an `RSxxx` exception — short-circuit exactly like a route's own would;
- return a response with none of `status`/`template`/`content_type`/`static_file` set — its
  `content` (if any) is shallow-merged into `request.context` for the next middleware or the
  route function, later keys winning on conflict;
- return a response with any of those four set — terminate the request immediately, sent as-is.

Any middleware, terminating or not, may also set `cookies`/`headers`/`jwt`/`jwt_attrs`/`csp` —
merged per-key across the chain and into whatever eventually renders the response, later
values winning. This is what supersedes the old `check_session` function and
`http.static.access.*` rules — see [Authentication ## Session
lifecycle](authentication.md#session-lifecycle) and [Static files ## Restricting access to
part of the tree](static-files.md#restricting-access-to-part-of-the-tree).

`/auth/*` is exempt from all middleware, unconditionally — a session-checking middleware there
could make login unrecoverable, since the callback that would restore a valid session is
itself blocked by the check meant to detect the lack of one. `/rel` itself is a reserved path,
so a middleware can never be declared directly there; gate it by declaring at `/` (or any
other prefix that still covers it) instead.

Session-checking middleware runs *as the request's already-resolved role*, unlike
`check_session` used to (which ran on the primary connection, before the role switch). Grant
`EXECUTE` on every middleware function to every role that should reach the paths it covers —
including the anonymous role — or those requests fail with an ordinary Postgres
permission-denied `403`, the same as any other route call missing a grant.

## Errors are just exceptions

Raise a Postgres exception with an `RSxxx` error code and rel turns it directly into that HTTP
status, with the raised message as the body — no separate error-response plumbing to write:

```sql
raise exception 'Access denied' using errcode = 'RS401';
raise exception 'Room not found' using errcode = 'RS404';
```

Any other error surfaces as a `500`, unless it's a Postgres error code rel already maps to an
obvious HTTP status (a permission-denied error becomes `403`, for instance).

## How routes are matched

Every `{name}`/`{name:regexp}` placeholder in a path is replaced with the literal `{}` to
produce that path's anonymized form — the key routes are sorted and compared by. Routes sort
longest-anonymized-path-first (so a more specific path wins over a `*`-style catch-all);
middleware sorts the opposite way, shortest-prefix-first, so an outer gate runs before an
inner one.

## In this section

- **[HTTP reference](reference.md)** — every function signature and configuration key in this
  section, in one place.
- **[Requests and responses](requests-responses.md)** — the request/response JSON shapes every
  route and middleware function sees.
- **[Rendering HTML with templates](templates.md)** — server-side Jet templates, the CSP
  nonce for trusted inline scripts, and `extends`/`block` layouts.
- **[File uploads](uploads.md)** — receiving raw bytes, or streaming an upload straight to
  disk without routing its bytes through Postgres.
- **[Static files](static-files.md)** — the root-level static fallback, masking it with a
  declared route, and returning binary/text content directly from a route.
- **[CORS and CSP](cors-csp.md)** — cross-origin access and the response security policy.
- **[Authentication](authentication.md)** — sessions, OpenID Connect, SAML, and roles.

See [Configuration](../configuration/index.md) for the full list of `http.*` settings,
including request size limits and the `route.<schema>.<function>.*` namespace.
