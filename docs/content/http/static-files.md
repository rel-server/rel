---
icon: material/folder-multiple
---

# Static files

Files placed under any directory listed in `http.static.path` (colon-separated, default
`/static`) are served at the **router root** — `/`, not a fixed `/static/*` prefix — as the
fallback for any path no declared route claims. A path is searched across every listed
directory in order, first match wins; a directory that doesn't exist is silently skipped, and
the fallback isn't mounted at all if every listed directory is missing.

- **No directory listing.** A directory with no `index.html` inside it is a `404`, never a
  generated listing page. A directory *with* an `index.html` serves it automatically —
  including at `/` itself, the default index.
- **No dotfile is ever served**, regardless of what exists on disk — a request for `.env`,
  `.git/config`, or anything else with a `.`-prefixed path segment is a `404`.
- By default, no authentication or role check applies here — files under `http.static.path`
  are meant to be publicly reachable. Gate part of the tree with [middleware](#restricting-access-to-part-of-the-tree)
  for anything that shouldn't be.

## Rendering a `.jet` template

If no plain file answers a request, rel looks for a `.jet` source that would render one: `foo`
falls back to `foo.html`, then `foo.html.jet` ; `foo.svg` falls back to `foo.svg.jet` ; a
directory falls back to `index.html`, then `index.html.jet`. A reachable plain file always
wins — `.jet` is the last resort, never tried ahead of a file that already exists at the
requested name.

A matched `.jet` file renders exactly like a route's own `template` (see [Rendering HTML with
templates](templates.md)) — the same `Req`, `Nonce`, and `rel()` are in scope, and it shares
that same template set/parse cache. `{{ extends "./layout.jet" }}`/`{{ include "sibling.jet" }}`
(a relative name, or a bare one with no leading `/`) resolves relative to the static file's own
location on disk, the same as it would for a route's own template ; a name with a leading `/`
(`{{ extends "/layout.jet" }}`) reaches into `http.templates.path` instead, alongside a route's
own templates. The rendered response's `Content-Type` comes from the
extension immediately before `.jet` (`widget.svg.jet` renders as `image/svg+xml`), and never
carries a static file's own caching headers (`ETag`/`Last-Modified`) — a jet render always sends
`Cache-Control: no-store` instead, since it's dynamic and role-scoped, never something a browser
or CDN should cache across users.

No file under `http.upload.dir` (see [File uploads](uploads.md)) is ever eligible for this
fallback, at any depth, including through a `{{ include }}` that would otherwise resolve there —
uploaded content stays servable as plain static bytes, never executable as a template.

## Static path masking

A declared route at an exact path always takes priority over a static file at that same
path — chi tries every registered route before falling back to a static file, so a route
masking `/` overrides the default index entirely.

For every request, rel `os.Stat`s the path that would be served under `http.static.path` (one
stat call, negligible cost) and includes the result as `request.static`:

```typescript
{ exists: boolean, size?: number, modified_at?: string }
```

When the request resolves to a `.jet` fallback, `size`/`modified_at` describe that `.jet`
**source** file itself, not anything about the eventual render — the render's size isn't known
without executing it. `exists: false` when nothing is there, when the path fails traversal
validation, or when it resolves into `http.upload.dir` (jet-excluded paths report as
nonexistent here too). There is
no implicit fallback to the static file when a masking route returns neither content nor
`static_file` — with `request.static` available, the function has what it needs to decide
explicitly, including returning `static_file` itself to defer to disk:

```sql
create function hotel.gallery_photo(req jsonb, out resp jsonb, out content bytea)
returns record language plpgsql as $$
begin
  if not (req->'static'->>'exists')::boolean then
    raise exception 'Not found' using errcode = 'RS404';
  end if;
  resp := jsonb_build_object('static_file', req->>'uri');
  content := null;
end;
$$;
```

Returning `static_file` in the response serves that file instead of the second `OUT` column's
own content; returning neither serves a `404` — but only when `status` is *also* unset. A
response with an explicit `status` (a middleware terminating with `{status: 401}`, say) and no
content/`static_file` is a real, deliberate answer, not a masking no-op, and is sent as that
status with an empty body. `static_file` resolves under the same traversal rules as
`http.static.path` and a `stream_upload` route's own `path` — no `..`, no dotfile path
segment, nothing outside the configured static directories — and through the same `.jet`
fallback described above, rendering rather than serving raw template source when it applies.

## Restricting access to part of the tree

Gate a subpath by declaring a [middleware](index.md#middleware) at the covering prefix — this
fully supersedes the old, database-backed `http.static.access.*` mechanism:

```sql
create function hotel.gate_private_docs(req jsonb, out resp jsonb, out content jsonb)
returns record language plpgsql security definer as $$
begin
  if (req->'jwt'->>'role') is distinct from 'staff' then
    resp := jsonb_build_object('status', 404);
    content := null;
    return;
  end if;
  resp := null;
  content := null;
end;
$$;
comment on function hotel.gate_private_docs(jsonb) is 'route:: path: "/private", middleware: true';
```

`req.jwt` is the same session claims object any other route/middleware sees — see
[Authentication](authentication.md) — also available as
`current_setting('rel.jwt.claims', true)::jsonb` inside a nested function call that doesn't
have `req` in scope. A middleware declared at `/private` applies to every request under that
prefix, static file or declared route alike — grant it `EXECUTE` for every role that should
reach anything under that prefix, including the anonymous role, or those requests fail with an
ordinary Postgres permission-denied `403` (session-checking middleware runs as the request's
already-resolved role, so a missing grant is a real authorization gap, not a silent no-op).

## Returning binary or text content directly

A route function can return raw binary or text content directly, instead of JSON, by
declaring its return type as a domain named after a MIME type — necessary conditions:

- its underlying type is `bytea` or `text`, nothing else;
- its name contains a `/`, the same way a real MIME type does.

```sql
create domain "image/png" as bytea;

create function hotel.property_photo(req jsonb) returns "image/png"
language plpgsql as $$ ... $$;
comment on function hotel.property_photo(jsonb) is 'route:: path: "/hotel/photo"';
```

The function's return value becomes the response body verbatim, with that domain's name as
`Content-Type`. A `bytea`-underlying domain's return value is the raw bytes directly; a
`text`-underlying one's return value is the raw string directly — neither is base64-encoded
the way a plain `HttpRequest.body` binary payload is. A route returning a `bytea`/binary
mimetype domain must not also declare a `template` — rel disables it with a warning at
discovery time, since Jet only ever renders text.

## Serving binary files from the DB

A route reading its bytes from a table instead of the filesystem works the same way — there's
nothing upload-specific about a `bytea` column. Take a table storing property photos, one known
format per row, and one column recording what that format actually is:

```sql
create table hotel.property_photos (
  id bigint generated always as identity primary key,
  property_id bigint not null references hotel.properties (id),
  mime_type text not null,
  data bytea not null
);
```

**With a domain type**, when the route's own content type is fixed and known ahead of time —
every photo normalized to JPEG on the way in, say — a plain-return function is the whole thing:

```sql
create domain "image/jpeg" as bytea;

create function hotel.property_photo(req jsonb, id bigint) returns "image/jpeg"
language plpgsql as $$
declare
  photo_data bytea;
begin
  select data into photo_data from hotel.property_photos where property_photos.id = id;
  if not found then
    raise exception 'Photo not found' using errcode = 'RS404';
  end if;
  return photo_data;
end;
$$;
comment on function hotel.property_photo(jsonb, bigint) is 'route:: path: "/hotel/photos/{id}"';
```

**Without a domain type**, when the content type varies per row — `mime_type` above is a real
column, not always the same format — a fixed-return-type domain can't express that; declare the
full-control, two-`OUT`-column shape instead and set `resp.content_type` from the row itself:

```sql
create function hotel.property_photo_raw(req jsonb, id bigint, out resp jsonb, out content bytea)
returns record language plpgsql as $$
declare
  photo hotel.property_photos;
begin
  select * into photo from hotel.property_photos where property_photos.id = id;
  if not found then
    raise exception 'Photo not found' using errcode = 'RS404';
  end if;

  resp := jsonb_build_object('content_type', photo.mime_type);
  content := photo.data;
end;
$$;
comment on function hotel.property_photo_raw(jsonb, bigint) is 'route:: path: "/hotel/photos/{id}/raw"';
```

`content`'s declared type here is plain `bytea` — with no domain to name a fixed `Content-Type`,
it would otherwise resolve to the generic `application/octet-stream` (see [HTTP
reference](reference.md#function-prototypes)); `resp.content_type` overrides that per request,
straight from `photo.mime_type`. This is also the shape to reach for the moment a binary route
needs anything else full-control brings — a `404` via `status` when the row doesn't exist rather
than an unhandled exception, a `Cache-Control` header, an ETag for conditional requests — none
of which a plain-return domain route can set. See [Requests and responses ##
`HttpResponse`](requests-responses.md#httpresponse).
