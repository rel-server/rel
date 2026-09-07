---
icon: material/upload
---

# File uploads

There are two separate mechanisms for a route to receive an upload, depending on whether the
function needs the bytes themselves or only needs to decide where they land on disk.

## Receiving raw bytes

A route that needs raw bytes — not just JSON — declares a second argument, beyond its own
`json`/`jsonb` request argument, matched by type, never by name:

```sql
create function hotel.upload_property_photo(req jsonb, files bytea[])
returns jsonb language plpgsql as $$ ... $$;
comment on function hotel.upload_property_photo(jsonb, bytea[]) is 'route:: path: "/hotel/photos", method: "POST"';
```

- **`bytea[]`** only ever receives bytes via a `multipart/form-data` body — `files[i]` pairs
  with `req.parts[i]`'s metadata (`name`, `filename`, `content_type`,
  `sniffed_content_type`, `headers`). A non-multipart body sent to such a route is a `415`. A
  plain (non-file) form field alongside a file input lands in `files` too, as its raw UTF-8
  bytes, with `parts[i].filename` staying `null` — check that field to tell the two apart.
- **`bytea`** (singular) only ever receives bytes via a raw, non-multipart body — the whole
  request body becomes the single value. A `multipart/form-data` body sent to such a route is
  also a `415`, since there'd be nowhere to route the individual parts.
- A route declaring neither still receives `parts` in the request for a multipart body, even
  though it can't retrieve their bytes.

A mismatch between what the route declares and what the request actually sends is always a
`415 Unsupported Media Type`, never a silent fallback. A `bytea[]`-declaring route called with
no body genuinely gets an empty `files` array, though — that's not a mismatch, just an
upload-less call; likewise a `bytea`-declaring route called with an empty body gets an empty
byte string.

Uploads received this way are bounded by `http.max_body_size` (10 MiB by default) and
`http.max_part_count` (100) — see [Configuration](../configuration/index.md) to raise either.
Both bytes and parts must be fully received and held in memory before the route function
runs; there is no streaming mechanism for genuinely large uploads (video, multi-GB archives)
through this path. `stream_upload` below streams straight to disk instead, and is bounded by
the separate, independently-configurable `http.max_upload_size`.

## Choosing a destination without routing bytes through Postgres

If a function only needs to *decide where* an upload lands on disk, without the bytes ever
passing through Postgres, declare it `stream_upload` — rel calls it **twice**:

```sql
create function hotel.upload_lease_pdf(req jsonb, out resp jsonb, out content jsonb)
returns record language plpgsql as $$
begin
  if req->'upload'->'size' is null then
    -- First call : no bytes received yet — decide the destination from the
    -- request's own metadata (a path param, req.query, req.jwt, ...).
    if (req->'query'->>'reject') = 'true' then
      raise exception 'not accepting uploads right now' using errcode = 'RS400';
    end if;
    resp := jsonb_build_object('upload', jsonb_build_object(
      'path', 'leases/' || (req->'upload'->'part'->>'filename'),
      'mkdir', true,
      'overwrite', 'disallow'
    ));
    content := null;
  else
    -- Second call : bytes already landed on disk at the path decided above.
    resp := jsonb_build_object('status', 200);
    content := jsonb_build_object('size', req->'upload'->'size');
  end if;
end;
$$;
comment on function hotel.upload_lease_pdf(jsonb) is 'route:: path: "/hotel/leases", method: "POST", stream_upload: true';
```

A `stream_upload` function must use the full-control (two-`OUT`-column) shape — it's the only
way to return the `upload` key on the first call — and must **not** declare a `bytea`/`bytea[]`
argument at all; the whole point is that the file's bytes never enter Postgres.

- **First call**, before any bytes are received: `req.upload` is `{part}` — the upload's
  metadata (filename, claimed/sniffed content type is absent here, no `size` yet). The
  function reads whatever else it needs off `req` (path params, `req.query`, `req.jwt`, ...)
  and responds with `upload: {path?, mkdir?, overwrite?, max_size?}` — binding, not a hint.
  Only `upload` is read from this response; any other side-effecting field (`status`,
  `cookies`, `jwt`, `template`, ...) is ignored, and the second `OUT` column is discarded. It
  may reject outright (`RSxxx`) — the earliest possible rejection point, before spending time
  receiving bytes for an upload that was always going to be refused. It may also return
  `max_size` to tighten `http.max_upload_size` for this one request (a per-user quota, say) —
  since it runs before any payload byte is read, this still applies before a single byte
  streams to disk. It can only lower the cap, never raise it.
- **Second call**, after the bytes are fully written to a temporary location on disk:
  `req.upload` is `{part, size}` — `part` now also carrying `sniffed_content_type`, detected
  from the file's own head. This call's response is the one actually sent to the client,
  exactly like any other full-control route; it cannot change the destination.

Both calls happen inside the same request; there is no separate registration step and no
`__prepare`/mandatory-function pairing to declare — one function, called twice, distinguished
by whether `req.upload.size` is present.

[Middleware](index.md#middleware) covering a `stream_upload` route's path runs once, ahead of
the first call only — never repeated before the second — though any cookies/headers/jwt it set
still apply to the second call's response, the one actually sent to the client.

`upload.path`, when set, is validated against the same traversal rules [static file
serving](static-files.md) applies — no `..`, nothing resolving outside `http.static.path`'s
first directory. Leaving `path` unset is a deliberate discard: bytes are still received (so
the second call still gets an accurate `size`), but nothing is kept on disk.
`overwrite: 'disallow'` (the default) rejects with `409 Conflict`, checked before any bytes
are read, if a file already exists at `path`; `overwrite: 'allow'` replaces it. The file is
only actually swapped into place after the second call's own transaction commits — if it
raises instead, nothing is written to disk.

A route accepting `stream_upload` is scoped to exactly one file per request — a client
uploading several independently-destined files makes several requests. A JSON/text/form body
sent to a `stream_upload` route (nothing to stream) is a `415`.
