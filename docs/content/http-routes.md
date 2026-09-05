---
icon: lucide/route
---

# HTTP routes

`/rel` answers one shape: a query tree, read or written back. Anything that doesn't fit that
shape — logging a guest in, sending a confirmation email, running a multi-step booking
workflow with real side effects — goes through `/route` instead. A route is an ordinary
Postgres function, written in whatever language Postgres supports (`plpgsql`, `sql`, `plv8`,
...), that rel discovers and calls directly over HTTP.

## What makes a function a route

Any function that isn't named starting with `_`, takes one of a few recognized argument
shapes, and returns `RelHttpResponse` (or a binary/text "mimetype domain", see
[Serving files and binary responses](#serving-files-and-binary-responses)) is callable at
`/route/<schema>/<function_name>`:

```sql
create function hotel.guest_login(req "RelHttpRequest") returns "RelHttpResponse"
language plpgsql as $$
declare
  guest_row hotel.guests;
begin
  select * into guest_row from hotel.guests
  where email = (req->'body')->>'email';

  if not found then
    raise exception 'Invalid credentials' using errcode = 'RS401';
  end if;

  return jsonb_build_object(
    'status', 200,
    'content_type', 'application/json',
    'content', jsonb_build_object('id', guest_row.id, 'name', guest_row.first_name),
    'jwt', jsonb_build_object('role', 'guest', 'guest_id', guest_row.id)
  );
end;
$$;
```

`hotel.guest_login` becomes reachable at `POST /route/hotel/guest_login` (or any verb — see
below). Suffix the function name with `__GET`, `__POST`, `__PUT`, `__PATCH`, or `__DELETE` to
restrict it to one HTTP verb; an unsuffixed function is the fallback for any verb without a
more specific match. A function name starting with `_` is never discovered as a route at all —
the way to write a private helper a route function calls internally without exposing it.

By default, any route function can set `jwt` on its response and thereby authenticate the
caller as any role. Restrict that with `http.functions.allowed_auth`, scoped to your actual
login functions — see [Configuration](configuration/index.md). The rest of a session's lifecycle
(claims, renewal, roles) is covered in [Authentication](configuration/authentication.md).

## The request

A route function that declares a `RelHttpRequest` argument receives the request as JSON:

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

`body` is decoded according to `content_type`: a JSON request decodes to the actual JSON
value; `application/x-www-form-urlencoded` decodes the same way a `GET /rel` query string
does, dotted keys included; plain text bodies come through as a string; anything else arrives
base64-encoded, for `decode(req.body, 'base64')::bytea` to recover. A request with no body at
all — a `GET`, most commonly — gets `body: null`.

## The response

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

### Rendering HTML with a template

Set `template` to a path relative to `http.templates.path` instead of building `content`
yourself — rel renders it with [Jet](https://github.com/CloudyKit/jet), exposing
`template_data` as `Data`, the request as `Req`, and the CSP nonce as `Nonce`:

```html
<!-- template/booking-confirmed.jet -->
<h1>Booking confirmed for {{ Data.guest_name }}</h1>
<script nonce="{{ Nonce }}">/* trusted inline code */</script>
```

Jet auto-escapes every `{{ value }}` for HTML; to embed dynamic data inside a `<script>`
block safely, pass it through the built-in `json(...)` helper into a JSON island rather than
interpolating it directly:

```html
<script type="application/json" id="data">{{ json(Data) | raw }}</script>
```

## Errors are just exceptions

Raise a Postgres exception with an `RSxxx` error code and rel turns it directly into that HTTP
status, with the raised message as the body — no separate error-response plumbing to write:

```sql
raise exception 'Access denied' using errcode = 'RS401';
raise exception 'Room not found' using errcode = 'RS404';
```

Any other error surfaces as a `500`, unless it's a Postgres error code rel already maps to an
obvious HTTP status (a permission-denied error becomes `401`, for instance).

## Handling file uploads

A route that needs raw bytes — not just JSON — declares one or two extra parameters, matched
by type, not by name:

```sql
create function hotel.upload_property_photo(req "RelHttpRequest", files bytea[])
returns "RelHttpResponse" language plpgsql as $$ ... $$;
```

`files` holds every uploaded part's raw bytes (a single non-multipart `POST` arrives as a
one-element array); add a third `parts_headers jsonb` parameter to also receive each part's
field name, filename, and content type. Uploads are bounded by `http.max_body_size` (10 MiB by
default) and `http.max_part_count` — see [Configuration](configuration/index.md) to raise either.

If a function only needs to *decide where* an upload lands on disk, without the bytes ever
passing through Postgres, a two-function `<name>__prepare`/`<name>` pair streams the upload
straight to a file under `http.static.path` instead — `__prepare` picks the destination from
the upload's metadata alone, before any bytes are read; the mandatory function runs once the
file is safely on disk. Reach for this over `files bytea[]` when you're storing the file itself
rather than inspecting its contents.

## Serving files and binary responses

Files placed under any directory listed in `http.static.path` are served at `/static/*`
automatically — no route function needed. There's no directory listing (a directory with no
`index.html` is a `404`) and no dotfile is ever served, regardless of what exists on disk.

Gate part of that tree behind a check by naming a prefix and a Postgres function:

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

Any number of these rules can coexist, each scoped to its own prefix; a path outside every
configured prefix is served exactly as before.

A route function can also return raw binary or text content directly, instead of JSON, by
declaring its return type as a domain named after a MIME type:

```sql
create domain "image/png" as bytea;

create function hotel.property_photo(req "RelHttpRequest") returns "image/png"
language plpgsql as $$ ... $$;
```

The function's return value becomes the response body verbatim, with that MIME type as
`Content-Type` — no `RelHttpResponse` wrapper needed.

## CORS and CSP

Both apply uniformly across `/rel`, `/route`, and `/static`. CORS is closed by default — no
`Access-Control-*` headers at all — until you list allowed origins:

```
http.cors.allowed_origins = "https://app.example.com,https://admin.example.com"
http.cors.allowed_methods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
```

Setting `http.cors.allowed_origins` to the literal `*` allows every origin but never sends
credentials — the JWT cookie becomes useless to a cross-origin caller in that mode, so `*` is
only right for a genuinely public, anonymous-role-only API.

A `Content-Security-Policy` header is sent on every response by default (`default-src 'self'`),
even with no configuration at all. Tighten or loosen individual directives:

```
http.csp.script_src = "'self' https://cdn.example.com"
http.csp.img_src = "'self' data:"
```

Every request also gets a fresh nonce (`req.csp_nonce`), automatically appended to that
response's `script-src`/`style-src`, for trusted inline `<script>`/`<style>` blocks without
loosening the policy for everything else. A single route can override the whole policy for
just its own response via `RelHttpResponse.csp`.

See [Configuration](configuration/index.md) for the full list of `http.*` settings, including
request size limits and the domain names rel looks for (`RelHttpRequest`, `RelHttpResponse`,
`RelUpload`) if you ever need to rename them.
