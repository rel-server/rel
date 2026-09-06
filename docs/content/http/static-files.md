---
icon: material/folder-multiple
---

# Static files

Files placed under any directory listed in `http.static.path` (colon-separated, default
`/static`) are served at `/static/*` automatically — no route function needed. A path is
searched across every listed directory in order, first match wins; a directory that doesn't
exist is silently skipped.

- **No directory listing.** A directory with no `index.html` inside it is a `404`, never a
  generated listing page. A directory *with* an `index.html` serves it automatically.
- **No dotfile is ever served**, regardless of what exists on disk — a request for `.env`,
  `.git/config`, or anything else with a `.`-prefixed path segment is a `404`.
- By default, no authentication or role check applies here — files under `http.static.path`
  are meant to be publicly reachable. Gate part of the tree with the access-control rules
  below for anything that shouldn't be.

## Restricting access to part of the tree

Gate a subpath behind a database check by naming a prefix and a Postgres function:

```
http.static.access.private_docs.prefix = "private/"
http.static.access.private_docs.function = "hotel.check_private_access"
```

```sql
create function hotel.check_private_access(payload jsonb) returns void
language plpgsql security definer as $$
begin
  if (payload->'jwt'->>'role') is distinct from 'staff' then
    raise exception 'Not found' using errcode = 'RS404';
  end if;
end;
$$;
```

`payload` is `{"path": "<request path, relative to http.static.path>", "jwt": JWT | null}`.
The same `jwt` value is also available as `current_setting('rel.jwt.claims', true)::jsonb`
inside this function (see [Authentication ## Session
lifecycle](authentication.md#session-lifecycle)) — useful for a nested function call that
doesn't have `payload` in scope, without threading it through as an extra argument.
Any number of these rules can coexist, each scoped to its own prefix (`<name>` above is
yours to choose); a path outside every configured prefix is served exactly as before.
Existence is checked first: a request for a path that doesn't exist anywhere under
`http.static.path` is a plain `404`, before the configured function ever runs — which means
a file's mere existence under a gated prefix is not itself kept private by this mechanism.

## Returning binary or text content directly

A route function can return raw binary or text content directly, instead of JSON, by
declaring its return type as a domain named after a MIME type — necessary conditions:

- its underlying type is `bytea` or `text`, nothing else;
- its name contains a `/`, the same way a real MIME type does.

```sql
create domain "image/png" as bytea;

create function hotel.property_photo(req "RelHttpRequest") returns "image/png"
language plpgsql as $$ ... $$;
```

The function's return value becomes the response body verbatim, with that domain's name as
`Content-Type` — no `RelHttpResponse` wrapper needed. A `bytea`-underlying domain's return
value is the raw bytes directly; a `text`-underlying one's return value is the raw string
directly — neither is base64-encoded the way a plain `RelHttpRequest.body` binary payload is.
