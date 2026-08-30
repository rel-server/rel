# Query strings : GET /rel and /rpc's `query` field

`query.ts` specifies the JSON `Query` shape `POST /rel` accepts. This document specifies a
second, textual encoding of a *subset* of that same shape, carried in a URL query string —
for `GET /rel` (read-only, single-relation queries, easy to construct and bookmark by hand)
and, structurally only (see `## /rpc's query field` below), for `/rpc`'s `RelHttpRequest.query`.

No new JSON shape is introduced. Every query string this document describes decodes to
exactly the `Relation` (or, for well-known queries, `WellKnownQuery`) JSON that `query.ts`
already defines, then runs through the existing pass-1..4 pipeline unchanged. This is a
frontend to that shape, not a second query language.

## Why not an existing "qs"-style library

`GET /rel` needs two genuinely different things out of a query string, not one :

1. **Structure** : which relation, which joins, `limit`/`offset`/`order_by`/`select` as a
   plain list — a flat key path into a nested object, e.g. `join.actors.on.actor_id=movie_id`.
2. **Expressions** : `where` (and any computed `select` entry) is `query.ts`'s `Expression`
   type — a recursive, *variable-arity, prefix-operator* tree (`["and", ["gte", "year", 1999],
   ["like", "name", ["%needle%"]]]`). A generic "allow dots" query-string decoder (`qs`,
   `goqs`, ...) turns `a.b.c=1` into nested objects/arrays through *fixed* key paths ; it
   has no way to represent "however many operands this particular operator happens to take"
   without falling back to awkward numeric-index keys (`where.1.2.0=gte`, ...), which is
   exactly the unreadable, hard-to-hand-write outcome this feature exists to avoid.

So this spec splits the query string into two cooperating layers : a small, purpose-built
**structural decoder** (dot-path → nested JSON object, arrays via repeated keys — see
`## Structural layer`) for everything in group 1, and a dedicated **filter expression
grammar** (see `## Filter expression grammar`) for everything in group 2. The filter
grammar is a direct textual notation for `query.ts`'s own `Expression` array form — same
operators, same ambiguity rule for string-vs-column (see below) — not a separate semantic
model to keep in sync with it.

## Scope : GET /rel is read-only, single-relation

Per `querying.md ## Configuration` ("all of them MUST be POST"), only reads move to `GET`.
Concretely, a `GET /rel` query string decodes to exactly one `Relation` (nested `join`s are
still just one `Relation` tree) :

- No `WriteQuery` (no `data`), no `Query[]` sequence, no `WellKnownQuery` — WellKnownQuery
  in particular already accepts arbitrary `params`/`data` and is reachable by name alone ; a
  `GET`-friendly encoding for it, if ever wanted, is a separate, later addition, not part of
  this one.
- `write_mode`, `on_conflict`, `insert_columns`, `update_columns` are therefore never valid
  on a `GET /rel` query string — decoding one that sets any of them is a `400`, not silently
  ignored. This check runs on the fully decoded `Relation` tree (recursively, into every
  nested `join`, since `on?: {...}` and friends can appear at any depth, e.g.
  `join.actors.write_mode=merge`), not as a flat prefix/substring scan over the raw query
  string keys — a key-name scan would be trivial to word around and isn't actually checking
  the thing that matters (what got decoded), only a proxy for it.

This isn't only an implementation shortcut around what dot-notation can conveniently
express — it's what makes `GET /rel` actually safe/idempotent per HTTP's own contract for
the method, which `POST /rel` was never able to promise.

## Structural layer

Every query key is a dot-separated path into the `Relation` object `query.ts` defines,
values are plain strings unless otherwise noted below. Repeating the exact same key builds
an array (`insert_columns=a&insert_columns=b` → `["a","b"]` — irrelevant to `GET`, shown
here only because the same decoder also serves other, future array-shaped fields).

```
relation=movie
schema=api
alias=m
select=movie_id,name,actors                    # comma list ; see ## select below
where=and(gte(year,1999),like(name,'%needle%')) # ## Filter expression grammar
order_by=name,-year                             # leading "-" = desc ; see ## order_by below
limit=20
offset=0
distinct=true
distinct_on=name,year

join.actors.relation=actor
join.actors.schema=api
join.actors.on.actor_id=movie_id                # { local_column: parent_column }
join.actors.select=name
join.actors.where=eq(active,true)
join.actors.join.awards.relation=award          # joins nest arbitrarily deep, same as JSON
join.actors.join.awards.on.actor_id=actor_id
```

decodes to :

```json
{
  "relation": "movie", "schema": "api", "alias": "m",
  "select": ["movie_id", "name", "actors"],
  "where": ["and", [">=", "year", 1999], ["like", "name", ["%needle%"]]],
  "order_by": ["name", ["desc", "year"]],
  "limit": 20, "offset": 0,
  "distinct": true, "distinct_on": ["name", "year"],
  "join": {
    "actors": {
      "relation": "actor", "schema": "api",
      "on": {"actor_id": "movie_id"},
      "select": ["name"],
      "where": ["=", "active", true],
      "join": {
        "awards": { "relation": "award", "on": {"actor_id": "actor_id"} }
      }
    }
  }
}
```

`relation`/`function` (mutually exclusive, per `query.ts`) and `arguments` (for a
function-relation) follow the same dotted rules ; each `arguments` value is one filter-value
token (see `## Filter expression grammar` ## Atoms) : `arguments.0=eq(status,'open')` is an
error (arguments are values, not conditions) — `arguments.0=42`, `arguments.name='x'` are not.

### Comma lists share the expression grammar's own tokenizer

`select`, `order_by`, and `distinct_on` are each a comma-separated list of `expr` entries
(`## Filter expression grammar`'s `expr` production, not bare text) — every entry can be a
plain identifier OR a full call, e.g. `order_by=lower(name)` or
`select=movie_id,total:agg(sum,orders.amount)` are both valid. Consequently these lists must
be split by the SAME quote-and-paren-aware scanner `call`'s own argument list uses, never by
a plain `strings.Split(",")` — a literal inside a nested call can itself contain a comma
(`select=matched:in(status,'a,b')`), and a naive split would cut it in the wrong place. One
tokenizer, reused everywhere a comma-separated `expr` list appears in this grammar.

### `select`

A bare entry (no `alias:` prefix) must resolve to a plain identifier (a column/join-alias
reference, `query.ts`'s string-`Expression` form) — `select=gte(a,b)` (a non-identifier
expression with no alias) is a `400`, since the resulting object key would be undefined.
`["full-except-and", ...]`-style object/array `Expression` forms have no query-string
spelling at all (write it as a well-known query, or use `POST /rel`, instead).

An entry may be `alias:expr` to select a computed value under a chosen key :
`select=movie_id,name,total:agg(sum,orders.amount)`. The split is on the FIRST `:` that is
outside any quoted string or parenthesized call — not the first `:` in the raw text — since
a quoted literal may itself contain a colon (`label:'a:b'` must split into `label` and the
literal `'a:b'`, not `label` and `'a` and `b'`). This is the same scanner as
`## Comma lists share the expression grammar's own tokenizer` above, just also watching for
a bare top-level `:`.

### `order_by`

Each entry is an `expr`, per `## Comma lists...` above, optionally prefixed with a single
leading `-` for descending (`-created_at`, or `-lower(name)`) ; a bare entry is ascending.
Both are nulls-last, per `query.ts`'s own default. There's no query-string spelling of
`asc-nulls-first`/`desc-nulls-last` in this v1 — reach for `POST /rel` for that.

## Filter expression grammar

Textual notation for `query.ts`'s `Expression` type. Grammar (informal EBNF) :

```
expr        := call | atom
call        := identifier "(" [ expr ("," expr)* ] ")"
atom        := identifier | literal
identifier  := /[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*/    -- e.g. actors.name
literal     := "'" ( any-char-except-quote | "''" )* "'"              -- 'it''s open'
             | number                                                  -- 123, 1.5, -4
             | "true" | "false" | "null"
```

No hyphens anywhere in `identifier` — deliberately, to keep a leading `-` on a number
literal (`-4`) unambiguous : an identifier can never start with `-` or a digit, so the
lexer never has to decide whether a leading `-` opens a negative number or an operator
name. Every `query.ts` operator/keyword that's itself hyphenated (`is-null`, `not-in`,
`own-except`, ...) is spelled with an underscore here instead (`is_null`, `not_in`) — see
the table below for the full, authoritative mapping.

- **`call`'s `identifier`** names an operator/function exactly as `query.ts`'s
  `FoldedOperator`/`BinaryOperator`/`UnaryOperator`/aggregate or function name would.
  Symbolic operators (`>=`, `<=`, `<>`, `~`, ...) are NEVER written symbolically here —
  `call`'s `identifier` must be a legal unquoted identifier, so every symbolic or hyphenated
  `query.ts` operator gets exactly one word-form name below ; the compiler maps word form →
  `query.ts`'s actual operator string 1:1, a fixed table, no synonyms, no ambiguity. Two
  `query.ts` symbols collide across `UnaryOperator`/`FoldedOperator` (`-` is both negate and
  subtract ; `~` is both `UnaryOperator`'s bitwise-not and `BinaryOperator`'s regex-match) —
  each gets its own distinct word (`neg`/`sub`, `bnot`/`match`), so the collision doesn't
  carry over into this grammar at all ; the compiler still tells them apart by arity (1 vs.
  2 arguments) the same way `query.ts`'s own JSON array form already does by array length.

  | `query.ts` | word form | | `query.ts` | word form |
  |---|---|---|---|---|
  | `and` | `and` | | `or` | `or` |
  | `+` | `add` | | `-` (folded, subtract) | `sub` |
  | `*` | `mul` | | `/` | `div` |
  | `^` | `pow` | | `%` (folded, modulo) | `mod` |
  | `\|` (bitwise or) | `bor` | | `&` (bitwise and) | `band` |
  | `->` | `json_get` | | `->>` | `json_get_text` |
  | `#>` | `json_path` | | `#>>` | `json_path_text` |
  | `.` | `dot` | | `\|\|` (concat, no coalesce) | `concat` |
  | `\|\|?` | `concat_coalesce` | | `??` (folded coalesce) | `ifnull` |
  | `<=` | `lte` | | `>=` | `gte` |
  | `<` | `lt` | | `>` | `gt` |
  | `=` | `eq` | | `<>` / `!=` | `ne` |
  | `is-distinct-from` / `!==` | `is_distinct_from` | | `is-not-distinct-from` / `===` | `is_not_distinct_from` |
  | `-` (unary, negate) | `neg` | | `not` | `not` |
  | `~` (unary, bitwise not) | `bnot` | | `is-null` | `is_null` |
  | `is-true` | `is_true` | | `is-false` | `is_false` |
  | `is-not-null` | `is_not_null` | | `is-not-true` | `is_not_true` |
  | `is-not-false` | `is_not_false` | | `\|/` (sqrt) | `sqrt` |
  | `\|\|/` (cube root) | `cbrt` | | `like` | `like` |
  | `ilike` | `ilike` | | `~` (binary, regex match) | `match` |
  | `~*` (case-insensitive regex) | `imatch` | | `::` (cast) | `cast` |
  | `&&` (overlap) | `overlap` | | `<->` (distance) | `distance` |
  | `-\|-` (is adjacent) | `adjacent` | | `<<` | `shl` |
  | `>>` | `shr` | | `@>` (contains) | `contains` |
  | `<@` (contained by) | `contained_by` | | `?` (has key) | `has_key` |
  | `?\|` (has any key) | `has_any_key` | | `?&` (has all keys) | `has_all_keys` |
  | `&<` | `overlaps_or_left` | | `&>` | `overlaps_or_right` |
  | `?:` | `op_qcolon` *(name TBD)* | | `@@` (tsvector match) | `matches_ts` |
  | `in` | `in` | | `not-in` | `not_in` |
  | `any` | `any` | | `all` | `all` |
  | `between` | `between` | | `not-between` | `not_between` |
  | `agg` / `aggregate` | `agg` | | `call` | `call` |
  | `concat_ws` | `concat_ws` | | `coalesce` (variadic form) | `coalesce` |
  | `format` | `format` | | | |

  The exact word picked for each symbol is a naming choice, not a semantic one — the
  compiler maps 1:1 to `query.ts`'s literal operator string regardless, so bikeshedding a
  name (e.g. `?:`'s still-placeholder `op_qcolon`) doesn't block implementing the grammar
  itself. What DOES matter, and is fixed by this table : every operator has exactly one
  canonical word, never two spellings for the same thing.

  `call(...)` and `agg(...)` themselves (`query.ts`'s own function-call/aggregate forms)
  still take a `FunctionIdentifier` as their own first argument, written
  `call(pg_catalog.upper, name)` — a dotted identifier there means `{schema, name}`, a bare
  one means an unqualified name resolved via the search path, exactly matching
  `FunctionIdentifier`'s own two JSON forms.
- **`atom`'s `identifier`** is a column/alias reference (`query.ts`'s plain-string
  `Expression` form) — dotted for a qualified reference (`actors.name`), exactly mirroring
  how a bare JSON string already means "alias/column, resolved by scope" today. This is the
  SAME ambiguity rule `query.ts` documents for its own JSON strings ("strings always refer
  to aliases and column names... a string literal is an array of only one string") — this
  grammar doesn't introduce a new rule, it gives the existing one a textual, quoted spelling :
  **quote a value to make it a literal string** (`literal`'s `'...'` form → JSON's
  one-element `[string]` array), **leave it bare to make it a column/alias reference.** A
  string literal containing a single quote doubles it, SQL-style (`'it''s open'`) — chosen
  over backslash-escaping because backslash inside a URL is itself a source of
  double-decoding confusion (is it escaping the grammar's quote, or something the transport
  already unescaped?) ; doubling has no such ambiguity and is a rule most users already know
  from SQL string literals.
- `in`/`not-in`/`any`/`all` need no special list syntax — they're just `call` with several
  arguments, exactly matching `query.ts`'s own variadic array form :
  `in(status,'open','closed')` → `["in", "status", "open", "closed"]` (candidates are always
  literals here per `query.ts`'s own note — a bare, unquoted candidate is still an error, not
  silently treated as a column, matching the JSON form's `string | Expression` split for
  `in`'s own candidates).
- `between`/`not-between` : `between(0,age,150)` → `["between", 0, "age", 150]` — same
  positional order as `query.ts`.
- `["bigint", ...]`/`["numeric", ...]` : written `bigint('9223372036854775807')` /
  `numeric('123.456')` — a `call` whose sole argument is always a quoted string, matching
  `query.ts`'s own `value: string` field for both.
- `%` inside a quoted `like`/`ilike` pattern (`'%needle%'`) is a literal percent sign by the
  time this grammar's own parser ever sees it — standard query-string percent-decoding
  (`%25` → `%`, done by the transport before any `Expression` parsing starts) already
  resolved it, so there's no clash between a `LIKE` wildcard and URL percent-encoding to
  worry about here.
- Not covered by this grammar (write these as a well-known query, or use `POST /rel`,
  instead — no query-string spelling exists for them) : the `Expression` forms that produce
  inline objects (`{name: Expression}`, `own`/`full`/`-except`/`-and` variants, `get-set`/
  `get`/`set`), `$param`, and `arr`/`array`/`lst`/`list` literals. All of these are either
  meaningless outside a well-known query's own params (`$param`), or reachable structurally
  instead (`own`/`full`-equivalent : just omit `select` entirely, per `query.ts`'s own
  "if not specified, equivalent to select *" default) — `where`/`order_by`/computed-`select`
  filtering realistically never needs an inline-object or `own`/`full`-family `Expression`,
  which is exactly why they're the two fields this grammar targets.

## /rpc's query field

`RelHttpRequest` gains a `query` field : the request's query string decoded through the
**structural layer only** (`## Structural layer` above, generalized — not scoped to
`Relation`'s own fixed keys, since an `/rpc` query string can be any shape a given route
function's own author wants) into a plain JSON value, handed to the Postgres function
verbatim. The filter expression grammar (`## Filter expression grammar`) is specific to
`Relation`'s `where`/`select`/`order_by` fields and has no bearing here — `/rpc` route
functions receive `query`'s decoded JSON as-is and interpret it however they choose,
same as every other `RelHttpRequest` field.

## Errors

A query string that fails to decode (structural layer) or parse (filter expression grammar)
is a `400`, same status `querying.md ## Configuration` already assigns to "an unknown
relation" and any other malformed-request case on `/rel` — a query-string-specific decode
failure is not a new error class, just a new source for the same one.
