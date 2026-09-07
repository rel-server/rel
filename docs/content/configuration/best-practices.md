---
icon: material/shield-check
---

# Best practices

Nothing on this page is a separate mechanism — every item links back to a setting or pattern
covered in full elsewhere. This is the checklist for pulling them together into a hardened,
production deployment, since defaults have to stay usable for a first `just test-db-fresh` run
and can't assume every one of these choices for you.

## Turn off Postgres' default PUBLIC execute grant on functions

Postgres grants `EXECUTE` on every newly created function to `PUBLIC` by default — including
functions your own migrations create. rel calls a function under whatever role a request's
session switched to, and that role is typically a member of several application roles at once;
an ungranted function is reachable by any of them the instant it exists, whether or not you
meant to expose it as a route or through [`call`/`agg`](../query-language/computed-fields.md).
Turn the default off, then grant `EXECUTE` explicitly, the way you already grant any other
privilege:

```sql
-- affects functions created after this runs, in every schema `app_owner` creates one in
alter default privileges for role app_owner revoke execute on functions from public;

-- functions that already exist need revoking separately, per schema
revoke execute on all functions in schema hotel from public;

-- then grant explicitly, per function and role
grant execute on function hotel.rooms_available(int, date) to editor;
```

Do this before anything else on this page — it's the single Postgres-level setting most likely
to leave a function more reachable than you intended, independent of anything rel's own
blacklist or role separation below catches.

rel's own [anonymous route authorization](../http/authentication.md#deployment-checklist)
already refuses to treat a route as anonymously reachable on the strength of a `PUBLIC` grant
alone — an unauthenticated request still needs an explicit `EXECUTE` grant to the anonymous
role. That only covers the literal "no login at all" case, though: an *authenticated* caller,
and any function reached through [`call`/`agg`](../query-language/computed-fields.md) rather
than as a route, both fall back to Postgres's own live privilege check — which still credits
`PUBLIC` the same as it always has. Revoking the default above is what actually closes those,
and it's especially worth doing for a `SECURITY DEFINER` function: one of those left on the
`PUBLIC` default is callable by anyone with any authenticated role, running with the
privileges of whoever owns it, not the caller's own.

## Give requests a narrower Postgres role than migrations get

`pg.query.user` (see [Configuration](index.md)) is a separate, optional login for the
connection that actually serves requests, distinct from `pg.user` — the one rel uses for
startup introspection and dmut migrations. Set it, and grant that role membership in only the
application roles a request should ever `SET ROLE` into:

```sql
create role query_user login password '...';
grant "~anonymous" to query_user;
grant "editor" to query_user;
grant "admin" to query_user;
```

Never make that role `postgres`, a superuser, or a member of `pg_read_server_files`,
`pg_write_server_files`, `pg_execute_server_program`, or `pg_signal_backend`. rel's own
[relation/function blacklist](#restrict-what-a-query-can-reach) below stops a lot, but it can
only stop what it already knows the name of — a newly `CREATE EXTENSION`'d function
(`dblink`, `postgres_fdw`, ...) defaults to `PUBLIC EXECUTE` the moment it exists, and isn't
covered until someone notices and blacklists it by name. Keeping the role's own privileges
minimal is the backstop for exactly that gap: as long as it never holds those privileges or
memberships, arbitrary file/network/process access stays unreachable even from a function the
blacklist hasn't caught up to yet.

Access control itself stays entirely Postgres-native this way: roles plus row-level security,
not a separate authorization DSL rel introduces on top.

## Restrict what a query can reach

`pg_catalog` and `information_schema` are unqueryable by default, and a handful of individually
dangerous functions (`pg_sleep`, `pg_terminate_backend`, the `pg_advisory_*lock*` family,
`set_config`) are blacklisted out of the box. Add your own sensitive tables and functions to
`blacklist.relations.<schema>.<name>` / `blacklist.functions.<schema>.<name>` explicitly — an
internal audit table, a `dblink`/`postgres_fdw` connection your app installs for its own
server-side use, anything a client should never be able to name directly even under a role
that can otherwise read a lot. See [Configuration reference ## Restricting what a query can
reach](reference.md#restricting-what-a-query-can-reach).

Beyond the blacklist, every route/middleware function is reachable only at the path it's
explicitly declared at (see [HTTP routes](../http/index.md#declaring-a-route)) — there's no
separate "discovered at all" gate to configure. Restrict which ones may mint or clear a
session with `http.functions.allowed_auth` — see [Authentication](../http/authentication.md).

## Leave expensive operators off unless you actually need them

`like`/`ilike`, regex match (`~`/`~*`), and full-text search (`@@`) are disabled by default —
each can be made to run pathologically slowly against an unindexed or adversarially-chosen
input, and a client controls the pattern. Enable only the ones you use, on columns that are
actually indexed for that kind of match (trigram/GIN, a `tsvector` column) — see [Operators
reference](../query-language/operators.md).

## Lock down the browser surface

- **CORS** is closed by default (no `Access-Control-*` headers at all). List exact origins in
  `http.cors.allowed_origins` rather than reaching for `*` — `*` also silently stops rel from
  ever sending `Access-Control-Allow-Credentials`, so the JWT cookie becomes useless to a
  cross-origin caller anyway; it's only right for a genuinely public, anonymous-role-only API.
  See [CORS and CSP](../http/cors-csp.md).
- **CSP** ships a real `default-src 'self'` policy even unconfigured. Reach for a route's own
  `HttpResponse.csp` override or the per-request `Nonce` (see [Rendering HTML with
  templates](../http/templates.md)) for a one-off trusted inline script, rather than loosening
  `http.csp.script_src`/`style_src` process-wide to accommodate it.
- **Cookies** default to `secure`, `httponly`, `SameSite=Lax`. Widening `jwt.same_site` to
  `None` is only ever needed for a session cookie read across a genuine cross-site embed, and
  needs `secure` alongside it (browsers reject `SameSite=None` without it) — see
  [Configuration reference ## Sessions (JWT)](reference.md#sessions-jwt).

## Size the resource limits to real traffic, not the defaults

`http.max_body_size` (10 MiB) and `http.max_part_count` (100) bound a single declared-route request;
`pg.query.max_depth` (6) bounds how deeply a query can nest joins; `pg.pool_size` (10) bounds
how many connections actually serve requests concurrently. All four default to something
reasonable for getting started, not to whatever your production traffic and payload sizes
actually need — revisit them once you know.

## Keep `dev` off

`dev` (default `false`) adds the real Postgres error text and a stack trace to an otherwise
generic error response — useful while building against your own schema, a information leak
once real users can trigger errors. Confirm it's unset (or explicitly `false`) in production
rather than relying on the default alone if your deployment pipeline ever sets it per
environment. See [Configuration ## Development mode](index.md#development-mode).

## Package your own app as an image built `FROM` rel

`rel-server/rel` ships as a generic, non-root, scratch-based binary — whatever `reload.cmd`
needs, well-known queries, static assets, and Jet templates are all *your* app's own versioned
files, not something the base image carries. Build them into your own image rather than
bind-mounting them from a host directory at deploy time, and keep `/secrets` as the one
directory that lives on a persistent volume. See [Docker deployment ## `/wellknown`, `/static`,
`/template`, and reload.cmd: build them into your own
image](docker-deployment.md#wellknown-static-template-and-reloadcmd-build-them-into-your-own-image).
