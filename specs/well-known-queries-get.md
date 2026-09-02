# `GET /wellknown`'s query-string encoding

`well-known-queries.md ## Querying` defines `/wellknown`'s request shape, `{name, params?,
data?}`. This document is that shape's `GET`-specific textual encoding — the counterpart
`query-json.md` is for `GET /rel`, but a separate document : `query-json.md ## Scope`
explicitly excludes `WellKnownQuery` from its own coverage ("a `GET`-friendly encoding for
it, if ever wanted, is a separate, later addition, not part of this one" — this is that
addition), and the two encodings don't share a grammar. `query-json.md`'s structural layer
turns dot-path keys into a `Relation` tree and its own filter expression grammar handles
`where`/`select`/`order_by`. `/wellknown` has no query tree in the request at all — only a
name and a flat bag of param values — so it needs none of that machinery, just the
structural (dot-path) layer, reused as-is.

## Shape

```
name=<well-known query name>
params.<param name>=<value>
```

`name` is required — same `WELL_KNOWN_UNKNOWN_QUERY` a POST gets for an unregistered or
deactivated name. Each declared param is a separate `params.<name>=` key ; there is no
comma-list or nested-object form the way `query-json.md`'s own `select`/`join` keys have,
since a well-known query's `params` are always a flat `{name: value}` map, never a tree.

```
GET /wellknown?name=directors_by_name&params.name=Denis+Villeneuve
```

decodes to the same request a `POST /wellknown` with body
`{"name": "directors_by_name", "params": {"name": "Denis Villeneuve"}}` would send.

## `params` value coercion

Every `params.<name>=<value>` value is decoded through the structural layer as a plain
string first (`querystring.DecodeQueryField` — the same generic dot-path decoder `/rpc`'s
own free-form `query` field already uses, described in full in `query-json.md
## Structural layer`), then **opportunistically re-parsed as JSON** :

- If the raw string parses as JSON, the parsed value is used : `params.limit=5` → the
  number `5`, `params.active=true` → the boolean `true`, `params.tag=null` → `null`.
- If it doesn't parse as JSON (an ordinary bare word), the literal string is used instead :
  `params.name=bob` → the string `"bob"`.

This means a **text-typed param whose value happens to look like a number or boolean needs
an explicit, URL-encoded JSON string** to stay a string — otherwise it silently coerces to
the wrong kind and fails `well-known-queries.md ## Execution Errors`' own
`WELL_KNOWN_PARAM_TYPE_MISMATCH` check :

```
params.code=12345          -> the number 12345 (fails a text-typed "code" param)
params.code=%2212345%22    -> the string "12345" (the URL-encoded, quoted form)
```

`%22` is a URL-encoded `"` ; the raw query-string value is the four characters `"12345"`
(quotes included), which parses as JSON to the plain string `12345`. The same escape hatch
works for a literal value that would otherwise look like `true`/`false`/`null` :
`params.status=%22true%22` stays the string `"true"`, not the boolean.

An object/array-shaped param value works the same way, since a JSON object/array is just
another thing the whole-value re-parse recognizes : a URL-encoded `params.tags=[1,2,3]`
(literally the characters `[1,2,3]`, percent-encoded as needed for the transport) parses to
the array `[1, 2, 3]`. There's no dot-path nesting INTO a `params.` value the way
`query-json.md`'s own `join.actors.on...` keys nest — `params.tags.0=1&params.tags.1=2`
would decode structurally to `{"tags": {"0": "1", "1": "2"}}` (an object with string keys,
not an array), which is very unlikely to be the intended shape. A structured param value is
always written as one single, whole-value JSON literal in its own `params.<name>=` key, not
built up across several keys.

## Read-only

`GET /wellknown` never accepts `data` — matching `query-json.md ## Scope`'s own read-only
restriction on `GET /rel`. A request supplying `data.*` keys (or, in principle, a bare
`data` key — the structural layer would decode either into the tree's `"data"` entry the
same way) is rejected outright, `400`, before the named query is even looked up ; there is
no partial/silent-write behavior on `GET`.

## Errors

Same as `query-json.md ## Errors` : a query string that fails to decode, or a `GET` request
that supplies `data`, is a `400` — a new source for the same error class, not a new one.
