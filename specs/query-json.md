# Query strings : GET /rel and /route's `query` field

`query.ts` specifies the JSON `Query` shape `POST /rel` accepts. This document specifies a
second, textual encoding of a subset of that shape, carried in a URL query string: for
`GET /rel` (read-only, single-relation queries) and, structurally only (`## /route's query
field`), for `/route`'s `RelHttpRequest.query`.

Every query string this document describes runs through the existing pass-1..4 pipeline
unchanged once decoded. A well-known query has its own, simpler `GET` encoding (`## Well-known
queries on GET /rel`), which does not use this grammar.

> Why: `GET /rel` needs two different things out of a query string: structure (which
> relation, joins, `limit`/`offset`/`order_by`/`select`) and expressions (`where`, computed
> `select` entries — `query.ts`'s recursive, variable-arity `Expression` tree). A generic
> "allow dots" query-string decoder (`qs`, `goqs`, ...) handles structure via fixed key
> paths but has no way to represent a variable-arity operator tree without awkward
> numeric-index keys (`where.1.2.0=gte`). This spec therefore splits the query string into
> a structural decoder (`## Structural layer`) and a dedicated filter expression grammar
> (`## Filter expression grammar`), the second a direct textual notation for `query.ts`'s
> own `Expression` array form rather than a separate semantic model.

## Scope : GET /rel is read-only, single-relation

- `write_mode`, `on_conflict`, `insert_columns`, `update_columns` rejection is checked on
  the fully decoded `Relation` tree, recursively into every nested `join` (these keys can
  appear at any depth, e.g. `join.actors.write_mode=merge`), not as a scan over raw
  query-string keys.

> Why: this keeps `GET /rel` genuinely safe/idempotent per HTTP's contract for the method,
> which `POST /rel` cannot promise.

## Structural layer

Every query key is a dot-separated path into the `Relation` object `query.ts` defines;
values are plain strings unless otherwise noted below. Repeating the exact same key builds
an array (`insert_columns=a&insert_columns=b` → `["a","b"]`).

```
relation=movie
schema=api
alias=m
select=movie_id,name,actors                    # comma list ; see ## select below
where=and(gte(year,1999),like(name,'%needle%')) # ## Filter expression grammar
order_by=name,-year                             # leading "-" = desc, nulls-last
limit=20
offset=0
distinct=true
distinct_on=name,year

join.actors.relation=actor
join.actors.schema=api
join.actors.on.actor_id=movie_id                # { local_column: parent_column }
join.actors.select=name
join.actors.where=and(eq(active,true),eq(m.language,'en')) # "m" = the parent's own alias
join.actors.join.awards.relation=award          # joins nest arbitrarily deep, same as JSON
join.actors.join.awards.on.actor_id=actor_id
```

`m.language` above uses `query.ts`'s own `alias` field: `actors`' own `where` reaches back
into its parent's row via the alias `movie` was given, filtering the embed by the parent's
`language`.

decodes to :

```json
{
  "relation": "movie", "schema": "api", "alias": "m",
  "select": {"movie_id": "movie_id", "name": "name", "actors": "actors"},
  "where": ["and", [">=", "year", 1999], ["like", "name", ["%needle%"]]],
  "order_by": ["name", ["desc", "year"]],
  "limit": 20, "offset": 0,
  "distinct": true, "distinct_on": ["name", "year"],
  "join": {
    "actors": {
      "relation": "actor", "schema": "api",
      "on": {"actor_id": "movie_id"},
      "select": {"name": "name"},
      "where": ["and", ["=", "active", true], ["=", "m.language", ["en"]]],
      "join": {
        "awards": { "relation": "award", "on": {"actor_id": "actor_id"} }
      }
    }
  }
}
```

`select`'s plain comma-list compiles to the OBJECT variant of `Expression`, `{[alias]:
expr}` per entry — never a bare array, since `query.ts` has no such form (`## select` below
has the full rule, including when `select=` compiles to a bare tag/array form instead).

`relation`/`function` (mutually exclusive, per `query.ts`) and `arguments` (for a
function-relation) follow the same dotted rules. Each `arguments` value is one filter-value
token (`## Filter expression grammar`'s atoms): `arguments.0=eq(status,'open')` is an error
(arguments are values, not conditions); `arguments.0=42`, `arguments.name='x'` are not.
`arguments`' positional-vs-named form (`query.ts`'s `Expression[] | {[name]: Expression}`)
is decided by its key set: if every sibling key under `arguments` is composed only of
digits, it compiles to the array form (indices, gaps filled with `null`); any other key set
compiles to the named-object form. Mixing the two (`arguments.0=x&arguments.name=y`)
compiles to the named-object form with a literal `"0"` key.

### `select`

`select=gte(a,b)` (a non-identifier expression with no alias) is a `400`. An `alias:expr`
entry's split is on the first `:` outside any quoted string or parenthesized call, using
the same quote-and-paren-aware scanner `call`'s own argument list uses, extended to also
watch for a bare top-level `:`.

> Why: the split is on the first top-level `:`, not the first `:` in the raw text,
> because a quoted literal may itself contain a colon (`label:'a:b'` must split into
> `label` and the literal `'a:b'`, not `label` and `'a` and `b'`).

### `own` / `full`

Query-string spellings of `query.ts`'s `own`/`full` `Expression` family. Hyphenated
`query.ts` names become underscored call identifiers here, same rule
`## Filter expression grammar` uses for operators.

| `select=` | compiles to |
|---|---|
| `own` | `["own"]` |
| `full` | `["full"]` |
| `own_except(a,b)` | `["own_except", ["a","b"]]` |
| `full_except(a,b)` | `["full_except", ["a","b"]]` |
| `own_and(actors,total:agg(sum,orders.amount))` | `["own_and", {"actors":"actors","total":["agg","sum",["orders.amount"]]}]` |
| `full_and(...)` | `["full_and", {...}]`, same shape as `own_and` |
| `own_except_and(a,b; total:agg(sum,orders.amount))` | `["own_except_and", ["a","b"], {"total":[...]}]` |
| `full_except_and(...)` | `["full_except_and", [...], {...}]`, same shape |

`own_and`/`full_and`'s bare argument (no `alias:` prefix) desugars to `{ident: ident}`
(self-aliased — `own_and(actors)` includes the `actors` join under its own name).

The `;` separator in `own_except_and`/`full_except_and` is special to those two calls only.

> Why: a bare comma can't separate the two argument groups, since both groups use commas
> internally.

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

No hyphens appear anywhere in `identifier` : an identifier can never start with `-` or a
digit, so the lexer never has to decide whether a leading `-` opens a negative number or an
operator name. `query.ts`'s own multi-word operator/keyword tags (`is_null`, `not_in`,
`own_except`, ...) are underscore-separated for the same reason ; the table below is the
full, authoritative mapping.

- **`call`'s `identifier`** names an operator/function exactly as `query.ts`'s
  `FoldedOperator`/`BinaryOperator`/`UnaryOperator`/aggregate or function name would.
  Symbolic operators (`>=`, `<=`, `<>`, `~`, ...) are never written symbolically here —
  `call`'s `identifier` must be a legal unquoted identifier, so every symbolic `query.ts`
  operator has exactly one word-form name below ; an already-underscore-separated
  `query.ts` tag (`is_null`, `own_except`) needs no separate word form. The compiler maps
  word form → `query.ts`'s actual operator string 1:1, a fixed table, no synonyms. Two
  `query.ts` symbols collide across `UnaryOperator`/`FoldedOperator` (`-` is both negate
  and subtract ; `~` is both `UnaryOperator`'s bitwise-not and `BinaryOperator`'s
  regex-match) ; each gets its own distinct word (`neg`/`sub`, `bnot`/`match`), and the
  compiler tells them apart by arity (1 vs. 2 arguments), same as `query.ts`'s own JSON
  array form does by array length.

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
  | `is_distinct_from` / `!==` | `is_distinct_from` | | `is_not_distinct_from` / `===` | `is_not_distinct_from` |
  | `-` (unary, negate) | `neg` | | `not` | `not` |
  | `~` (unary, bitwise not) | `bnot` | | `is_null` | `is_null` |
  | `is_true` | `is_true` | | `is_false` | `is_false` |
  | `is_not_null` | `is_not_null` | | `is_not_true` | `is_not_true` |
  | `is_not_false` | `is_not_false` | | `\|/` (sqrt) | `sqrt` |
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
  | `in` | `in` | | `not_in` | `not_in` |
  | `any` | `any` | | `all` | `all` |
  | `between` | `between` | | `not_between` | `not_between` |
  | `agg` / `aggregate` | `agg` | | `call` | `call` |
  | `concat_ws` | `concat_ws` | | `coalesce` (variadic form) | `coalesce` |
  | `format` | `format` | | | |

  The exact word picked for each symbol is a naming choice, not a semantic one — the
  compiler maps 1:1 to `query.ts`'s literal operator string regardless (`?:`'s word form,
  `op_qcolon`, is still a placeholder name). Every operator has exactly one canonical word,
  never two spellings for the same thing.

  These word forms are also an accepted synonym in `POST /rel`'s JSON body : `query.ts`'s
  own array-form operator position (`["gte", "year", 1999]` alongside the existing
  `[">=", "year", 1999]`) accepts either spelling on input, resolved against this same
  table. `query.ts`'s symbols remain the only spelling a well-known query or any other
  persisted/round-tripped JSON ever contains — pass-1 JSON parsing normalizes a word-form
  operator to its canonical symbol immediately, before scope resolution or anything else
  downstream sees it. The word form is purely an input convenience at the JSON parse
  boundary.

  `call(...)` and `agg(...)` (`query.ts`'s own function-call/aggregate forms) still take a
  `FunctionIdentifier` as their first argument, written `call(pg_catalog.upper, name)` — a
  dotted identifier means `{schema, name}`, a bare one means an unqualified name resolved
  via the search path, matching `FunctionIdentifier`'s two JSON forms. A `call` identifier
  not found in the word-form table (e.g. `lower` in `order_by=lower(name)`) desugars to
  `["call", identifier, ...arguments]` directly, `identifier` compiled the same
  dotted-vs-bare way, and goes through the same allowlist gate at resolve time as
  `query.ts`'s own explicit `call(...)`/`agg(...)` forms. `agg(...)`'s optional third
  `filter` operand (`query.ts`'s `["agg", identifier, arguments, filter?]`) has no spelling
  in this grammar — every argument after the identifier lands in `arguments` ; write a
  `POST /rel` query for an aggregate needing a filter.
- **`atom`'s `identifier`**, quoted, compiles to `query.ts`'s one-element `[string]` array
  form rather than a plain column/alias reference — same rule `query.ts` documents for its
  own JSON strings.
  > Why: doubling was chosen over backslash-escaping because a backslash inside a URL is
  > itself a source of double-decoding confusion (is it escaping the grammar's quote, or
  > something the transport already unescaped?) ; doubling has no such ambiguity and
  > matches SQL string-literal convention.
- `in`/`not_in`/`any`/`all` candidates are always literals, per `query.ts`'s own note ; a
  bare, unquoted candidate is an error, not silently treated as a column.
- `["bigint", ...]`/`["numeric", ...]` : written `bigint('9223372036854775807')` /
  `numeric('123.456')`, a `call` whose sole argument is always a quoted string, matching
  `query.ts`'s own `value: string` field for both.
- `%` inside a quoted `like`/`ilike` pattern (`'%needle%'`) is a literal percent sign by the
  time this grammar's parser sees it — standard query-string percent-decoding (`%25` → `%`)
  already resolved it before `Expression` parsing starts.

## Well-known queries on GET /rel

This section is `well-known-queries.md ## Querying`'s `GET`-specific textual encoding. It
uses `## Structural layer`'s dot-path decoding only, not `## Filter expression grammar` — a
well-known query has no query tree, only a name and a flat bag of param values.

### Shape

```
wellknown=<well-known query name>
params.<param name>=<value>
```

`wellknown`'s presence routes a `GET /rel` query string to this grammar instead of the
`Relation` one above (`decodeGETQuery`, `server/rel.go`).

An unrecognized key outside `wellknown`/`params.*` is silently ignored, unlike the
`Relation` grammar's `## Errors` rejection of an unrecognized key — a well-known query's
request tree is fixed (a name and a flat param bag), so there is no equivalent "did you
mean" check to run.

### Read-only

A request supplying `data.*` keys (or a bare `data` key — the structural layer decodes
either into the tree's `"data"` entry the same way) is rejected outright, `400`, before the
named query is looked up.

## /route's query field

`RelHttpRequest` gains a `query` field : the request's query string decoded through the
structural layer only (`## Structural layer`, generalized beyond `Relation`'s fixed keys —
an `/route` query string can be any shape a route function's author wants) into a plain
JSON value, handed to the Postgres function verbatim. `## Filter expression grammar` is
specific to `Relation`'s `where`/`select`/`order_by` fields and has no bearing here — route
functions receive `query`'s decoded JSON as-is.

## Errors

A query string that fails to decode (structural layer) or parse (filter expression
grammar) is a `400`, the same status `query-engine.md ## Errors` assigns to an
unknown relation and any other malformed-request case on `/rel`. Same for
`## Well-known queries on GET /rel` : a query string that fails to decode, or a `GET`
well-known request that supplies `data`, is a `400` through this same class.
