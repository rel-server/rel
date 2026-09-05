---
icon: material/upload
---

# File uploads

There are two separate mechanisms for a route to receive an upload, depending on whether the
function needs the bytes themselves or only needs to decide where they land on disk.

## Receiving raw bytes

A route that needs raw bytes — not just JSON — declares one or two extra parameters, beyond
`req RelHttpRequest`, matched by type, never by name:

```sql
create function hotel.upload_property_photo(req "RelHttpRequest", files bytea[])
returns "RelHttpResponse" language plpgsql as $$ ... $$;
```

Only four argument shapes are recognized route signatures at all: `()`, `(req)`,
`(req, files bytea[])`, and `(req, files bytea[], parts_headers jsonb)`. Anything else isn't
discovered as a route.

- `files` holds every uploaded part's raw bytes — a single non-multipart `POST` arrives as a
  one-element array (`files[1]`), so there's no separate single-file shape.
- `parts_headers`, when declared, is a JSON array where index `i` describes `files[i]`: its
  field `name`, `filename`, `content_type`, and full `headers`, mirroring
  `RelHttpRequest.headers`'s own shape. A plain (non-file) form field alongside a file input
  lands in `files` too, as its raw UTF-8 bytes, with `parts_headers[i].filename` staying
  `null` — check that field to tell the two apart.

A mismatch between what the route declares and what the request actually sends is always a
`415 Unsupported Media Type`, never a silent fallback: a `files`-declaring route sent a JSON
body has nowhere to put it as a separate "file", and a plain `(req)`-only route sent
`multipart/form-data` has nowhere to put the upload at all. A `files`-declaring route called
with no body genuinely gets an empty `files` array, though — that's not a mismatch, just an
upload-less call.

Uploads are bounded by `http.max_body_size` (10 MiB by default) and `http.max_part_count`
(100) — see [Configuration](../configuration/index.md) to raise either. Both bytes and parts
must be fully received and held in memory before the route function runs; there is no
streaming mechanism for genuinely large uploads (video, multi-GB archives).

## Choosing a destination without routing bytes through Postgres

If a function only needs to *decide where* an upload lands on disk, without the bytes ever
passing through Postgres, a two-function pair streams the upload straight to a file under
`http.static.path` instead. Both functions are mandatory, and share one JSON domain:

```sql
create domain "RelUpload" as jsonb;
```

- **Name**: configurable via `http.upload_domain_name` (default `RelUpload`); same
  resolution rule as `RelHttpRequest`/`RelHttpResponse` (see
  [Requests and responses](requests-responses.md)). An unresolved `RelUpload` domain just
  disables this mechanism, non-fatally — the ordinary `files bytea[]` mechanism above is
  unaffected.

```typescript
interface RelUpload {
  path?: string                      // relative to http.static.path's first directory ; omitted = discard the upload once received
  mkdir?: boolean                    // create path's parent directory if missing
  overwrite?: 'allow' | 'disallow'   // default 'disallow'
  part?: RequestPart                 // absent when returned by __prepare ; filled in by rel before the mandatory function runs
  size?: number                      // rel-filled, post-stream, the actual observed byte count
}
```

- **`<name>__prepare(req RelHttpRequest, part jsonb) returns RelUpload`** — runs *before* any
  bytes are received, from just the incoming upload's metadata (`part`, JSON `null` if the
  request carries no upload). Its `path`/`mkdir`/`overwrite` decision is binding, not a hint.
  It runs inside a real read-only transaction, so an attempted write inside it fails with a
  genuine Postgres error. It may reject outright (`RSxxx`) — the earliest possible rejection
  point, before spending time receiving bytes for an upload that was always going to be
  refused.
- **`<name>(req RelHttpRequest, upload RelUpload) returns RelHttpResponse`** — the mandatory,
  unsuffixed name; runs *after* the bytes are fully written to a temporary location on disk.
  `upload` is `__prepare`'s own returned value, with `part`/`size` filled in. This function
  cannot change the destination.

Both functions must be present for the pair to be discovered as a route at all; either one
alone is not a route. This family never composes with a `__VERB` suffix on either function.
It's scoped to exactly one file per request — a client uploading several independently-
destined files makes several requests.

`path`, when set, is validated against the same traversal rules
[static file serving](static-files.md) applies — no `..`, nothing resolving outside
`http.static.path`'s first directory. Leaving `path` unset is a deliberate discard: bytes are
still received (so the mandatory function still gets an accurate `size`), but nothing is kept
on disk. `overwrite: 'disallow'` (the default) rejects with `409 Conflict`, checked before any
bytes are read, if a file already exists at `path`; `overwrite: 'allow'` replaces it. The file
is only actually swapped into place after the mandatory function's own transaction commits —
if it raises instead, nothing is written to disk.
