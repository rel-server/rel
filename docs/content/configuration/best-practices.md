---
icon: material/shield-check
---

# Best practices

Nothing on this page is a separate mechanism — every item links back to a setting or pattern
covered in full elsewhere. This is the checklist for pulling them together into a hardened,
production deployment, since defaults have to stay usable for a first `just test-db-fresh` run
and can't assume every one of these choices for you.

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
that can otherwise read a lot. See [Configuration ### Restricting what a query can
reach](index.md#restricting-what-a-query-can-reach).

Beyond the blacklist, gate which functions are reachable as routes at all with
`http.functions.allowed_routes`, and which ones may mint or clear a session with
`http.functions.allowed_auth` — see [HTTP layer](../http/index.md) and
[Authentication](../http/authentication.md).

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
  `RelHttpResponse.csp` override or the per-request `Nonce` (see [Rendering HTML with
  templates](../http/templates.md)) for a one-off trusted inline script, rather than loosening
  `http.csp.script_src`/`style_src` process-wide to accommodate it.
- **Cookies** default to `secure`, `httponly`, `SameSite=Lax`. Widening `jwt.same_site` to
  `None` is only ever needed for a session cookie read across a genuine cross-site embed, and
  needs `secure` alongside it (browsers reject `SameSite=None` without it) — see
  [Configuration ### Sessions (JWT)](index.md#sessions-jwt).

## Size the resource limits to real traffic, not the defaults

`http.max_body_size` (10 MiB) and `http.max_part_count` (100) bound a single `/route` request;
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

`ceymard/rel` ships as a generic, non-root, scratch-based binary — dmut migrations, well-known
queries, static assets, and Jet templates are all *your* app's own versioned files, not
something the base image carries. Build them into your own image rather than bind-mounting
them from a host directory at deploy time, and keep `/secrets` as the one directory that lives
on a persistent volume. See [Docker deployment ## `/dmut`, `/wellknown`, `/static`,
`/template`: build them into your own image](docker-deployment.md#dmut-wellknown-static-template-build-them-into-your-own-image).
