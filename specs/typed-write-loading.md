# Typed write loading

A write request carries its rows as JSON or CBOR. Today `query/write_denormalize.go` flattens them into one shared `_data` temp table whose `data` and `keys` columns are `jsonb`, and `query/write_dml.go` pulls every value back out of the jsonb with `jsonb_populate_record` and `->>` casts. This document replaces `_data` with one typed temp table per destination table, loaded from binary Postgres values built directly from the request's `Node` tree (`specs/cbor.md ## Node interface`). No JSON text is produced for CBOR input, except for `json`/`jsonb` target columns and text-mode columns given a map or array (`## Column mode`).

The write semantics (`docs/content/query-language/writing.md`), the phase order, `stats`, `sql`, `query_plan` and the response shape are unchanged. Only how rows reach Postgres, and how generated keys flow between nodes, changes.

## Terms

- **Destination table**: the relation a write node inserts into or updates, identified by its relation OID.
- **Slot**: one position in a destination table's presence layout (see `## Presence slots`).
- **Mask word**: one `bigint` holding 64 slots.
- **General path**: the write path through temp tables, used for every write the fast path does not take.
- **Fast path**: the single-node write of `query/write_fast.go`, which answers from the write statement's own `RETURNING` rows.

## Value source

The row reader walks the request's `Node` tree (JSON or CBOR adapter) alongside the resolved `QueryNode` tree, the way `denormalize` walks the sonic tree today. It reads each value with the `Strict*` accessors and encodes it straight into the binary field for the column's Postgres type. It never builds an intermediate Go value per field and never passes a field through `any`.

`json` and `jsonb` target columns, and text-mode columns given a JSON object or array, are the exceptions: the field's JSON text is written as the value. A JSON source copies the subtree's source text. A CBOR source is converted to JSON text for that field only (`## json and jsonb columns`).

> **Why:** pgx's `CopyFrom` and parameter encoding box every field in `any` and look up an encode plan per field. Measured on 8 columns: 282 ns per row against 17 ns for direct byte writes, 16 times slower. pgx's `numeric` encoder alone costs about 290 ns per value because it goes through `big.Int`, so `numeric` has its own encoder. The benchmark is repeated under `## Tests`.

## Presence slots

A node needs to tell a column the row supplied from one it left out: an absent column takes its `DEFAULT` on insert and keeps its current value on update. Presence travels as bits in mask words stored next to the row's values.

### Layout

A destination table has an ordered slot list, built once from introspection:

1. Each column, in attribute order, gets one slot.
2. After a column whose type is a composite (or a domain over one), its sub-fields get slots depth-first, each sub-field immediately followed by its own sub-fields. Array types are not expanded.

Slot `i` lives in mask word `i / 64`, at bit `i % 64`. A table has `ceil(slots / 64)` mask words, at least one. A set bit means the row supplied the column or sub-field, an explicit `null` included.

The layout is a method set on `pg.Relation`: `MaskSlots()`, `MaskWords()` and a lookup from a `ColumnPath` to `(word, bit)`. It is computed once per relation and is stable until the next introspection.

### Filling

- The reader sets a slot's bit when the payload names the column or sub-field.
- A column filled from a parent's or an outgoing child's recovered key is never read from the payload. Its bit is not set, and the SQL for such a column does not test it.
- Masks are always built and always stored. When the generated SQL has no use for a table's masks, it simply does not read them.

> **Why:** deciding per request whether masks are needed means inspecting every row first. Filling an 8-byte word per 64 slots while the row is already being encoded costs nothing measurable, and the fast-path measurements showed a mask word is within noise of no mask word.

### Use in SQL

- **Insert and the insert side of upsert**: a column with a `DefaultExpression` resolves to `case when <bit set> then <value> else <default expression> end`. For a `NOT NULL` column the condition also requires the value to be non-null, so an explicit `null` takes the default. A column without a default reads its value directly: absent and `null` are the same.
- **`update-existing` and `merge-update`**: every column and sub-field in the update list resolves to `case when <bit set> then <resolved value> else <current value> end`. The resolved value follows the insert rule above for `NOT NULL` columns with a default. A sub-field is set with its dotted target path, as `writeTargetPath` does today.
- **`do update` of an upsert**: reads `excluded.<col>` unchanged. `excluded` already holds the values prepared by the insert side.
- **`update` (patch)**: the patch names exactly the columns to set, so no mask is tested.

> **Why:** composite sub-fields get slots because an update must tell "leave `home.city` alone" from "set it to NULL". Composite attributes cannot have defaults (`create type … (a text default 'x')` is a syntax error, and a domain default is not applied to a missing field), so insert never tests a sub-field slot.

## Column encoders

An encoder maps a `Node` and the column's type (`pg.Type`, domains unwrapped through `BaseType`) to the binary field. A type without an encoder puts its column in text mode (`## Column mode`).

### Scalar types

| Postgres type | JSON forms accepted | CBOR forms accepted |
|---|---|---|
| `int2`, `int4`, `int8` | integer number, or a string of optional sign and digits | integer, bignum (tag 2/3) when it fits |
| `float4`, `float8` | number, or a string in Postgres float syntax including `NaN`, `Infinity`, `-Infinity` | float, integer |
| `numeric` | number (digits taken from the source text), or a numeric string including `NaN`, `Infinity`, `-Infinity` | integer, bignum, tag 4, float |
| `bool` | `true`, `false`, the numbers `0` and `1`, and the strings `bool_in` accepts | `true`, `false` |
| `text`, `varchar`, `bpchar`, `name` | string | text string |
| `uuid` | string: hyphenated, 32 hex digits, or in braces, any case | tag 37 (16 bytes), or the same strings |
| `bytea` | string in `\x` hex form | byte string |
| `date` | `YYYY-MM-DD` string, `infinity`, `-infinity` | tag 100 (integer days), or the same strings |
| `timestamp` | ISO 8601 string (see below) | text string |
| `timestamptz` | ISO 8601 string (see below) | tag 1 (integer or float seconds), tag 0 text, or the same strings |
| enums | the label as a string | text string |

A JSON number for an integer type must be a plain integer: `1.0`, `1.5`, `1e3` and `true` are errors, as they are today. Integer values are parsed from digits and never go through `float64`.

Each encoder is covered by a differential test against Postgres' own parser (`## Tests`). A string the encoder accepts must yield the value Postgres yields for the same text.

### Rejected forms

Postgres reads each of these, and the encoders reject them. The differential tests pin the list.

| Type | Rejected |
|---|---|
| `text`, `varchar`, `bpchar`, `name`, enums | a number or a boolean (`42` is not accepted for a text column) |
| integers | surrounding whitespace, `0x`/`0o`/`0b` prefixes, `_` separators |
| `float4`, `float8`, `numeric` | surrounding whitespace, hexadecimal floats, `_` separators, `0x` prefixes |
| `uuid` | other hyphen placements : Postgres reads a hyphen after any group of four hex digits (`a0ee-bc99-…`), while only the hyphenated 8-4-4-4-12 form and the bare 32 digits are read, each with optional braces |
| `bytea` | the escape form (`abc`, `\000`), whitespace between hex pairs |
| `date` | `DateStyle` forms (`10/11/2026`), `today` and the other special words, compact `20261018`, one-digit month or day, a time suffix, surrounding whitespace |
| `timestamp`, `timestamptz` | more than 6 fractional digits, seconds `60`, a trailing `.` with no digits, the special words `now`, `today`, `epoch`, zone names and abbreviations in the string, `DateStyle` forms, compact `20261018T143000`, two spaces between date and time, surrounding whitespace, an offset with seconds (`+02:00:30`) |

> **Why:** a number for a text column is the one deliberate narrowing of a write that works today (`jsonb_populate_record` turns `42` into `'42'`). The value forms are specified per type so a client sees an error naming the column instead of a silently coerced value.

### Date and time strings

For `timestamp`, `timestamptz` and `date` the accepted text is ISO 8601 / RFC 3339: `YYYY-MM-DD`, then optionally `T` or a space and `hh:mm[:ss[.f]]`, then for `timestamptz` an optional `Z` or `±hh[:mm]` (`±hhmm` is read too), optionally after one space. Further rules:

- Up to 6 fractional digits. More is an error.
- `24:00:00` is the start of the next day. Seconds `60` are an error.
- `infinity` and `-infinity` are accepted for all three types.
- `timestamp` ignores an offset suffix.
- A `timestamptz` without an offset is read in the connection's session `TimeZone`, taken from the `TimeZone` parameter the server reports for the connection (`pgconn.PgConn.ParameterStatus`). A zone name Go's `time.LoadLocation` cannot load is an error.
- The year has 4 or more digits. A ` BC` suffix after the time or offset marks a year before 1 AD: calendar year Y BC is year 1 − Y of the proleptic Gregorian calendar. This is the form rel's own output uses (`0044-03-15T00:00:00+00:00 BC`).
- `now`, `today`, `tomorrow`, `yesterday`, `epoch`, zone names or abbreviations inside the string, and `DateStyle`-dependent forms such as `10/11/2026` are errors.
- Values outside Postgres' range (4714-11-24 BC to 294276-12-31 AD for timestamps, to 5874897-12-31 AD for dates) are errors. For `timestamptz` the range applies to the instant, so the same text can be valid in one session zone and out of range in another.
- A wall-clock reading skipped by a spring-forward gap gets the offset in force before the change, and a reading that occurs twice in a fall-back overlap gets the offset in force after it. This is Postgres' rule, implemented explicitly because Go's `time.Date` picks differently.

> **Why:** the session `TimeZone` is not pinned anywhere in rel, so an offset-less `timestamptz` string means whatever the server or role default says today. Reading it from the connection keeps that meaning. About 2 million wall-clock readings around every clock change from 1900 to 2060, in 19 zones, were compared with Postgres, and the tests repeat that.

> **Question:** an offset-less `timestamptz` in a non-UTC session zone depends on Go and the server having the same time zone database. They differ on this machine in three zones (a 1925 Dublin change, a 1979 Tehran change, a 2026 Casablanca change): near those changes the reading gets an instant an hour away from the one Postgres gives. Options: accept it as a documented limit (UTC sessions and any text with an offset are unaffected), embed Go's `time/tzdata` (still not the server's), or have Postgres read such values (a text-mode `timestamptz` column, giving up the integer path for CBOR tag 1). Not decided.

### Arrays

A JSON or CBOR array encodes as a Postgres binary array whose element type comes from `Type.ElementType`. If the first element is itself an array, every element must be an array and they become extra dimensions, which must be rectangular. This holds whatever the element type, `json` and `jsonb` included, as `jsonb_populate_record` reads it. An empty array, or one whose first sub-arrays are all empty, has zero dimensions. A `null` element is a NULL element. Elements follow the scalar rules of their own type.

A string given for an array column in binary mode (`"{1,2}"`) is an error.

### Composite values

In binary mode, a composite column or an array element of composite type encodes as a binary record: field count, then for each field its type OID, length and bytes, in the order `Type.Relation` lists them. A key in the object that is not a field of the type is ignored. A missing key is NULL.

A composite column is encoded from whichever of its paths the node's extractors supply: the whole object, or individual sub-fields (the existing rule that a request never supplies both stays). Sub-fields not supplied are NULL.

### Column mode

A column, array element or composite field is in **binary mode** when its type has an encoder. A composite or array column is in binary mode only when every type in its tree has an encoder. Every other column is in **text mode**: it is loaded as `text` and cast, without modifiers, in the statement that reads it (`## Type modifiers and domains`). The mode is fixed from introspection, not from the data.

> **Why:** inside a binary record or array every field must be in that field type's binary format, so one field without an encoder (an `interval`, a range, a PostGIS `geometry`) forces its whole composite or array into text mode. Postgres then parses each field itself.

### json and jsonb columns

`jsonb` is sent as the byte `0x01` followed by the JSON text. `json` is sent as the JSON text.

A CBOR value converts to JSON text by these rules: maps with text keys become objects, arrays become arrays, text strings, integers, booleans and `null` map directly, finite floats become numbers, tag 4 and bignums become numbers from their exact digits. Any other value (a byte string, another tag, a non-finite float, a non-text key) is an error.

### Text mode

A text-mode column is written as the text Postgres' input function for the type reads:

- A JSON or CBOR string as is.
- A number as its digit text. A boolean as `true` or `false`.
- A CBOR integer, float or bignum as its decimal text, a byte string as `\x` and hex digits, a tag 37 value as a hyphenated UUID, a tag 1 value as an ISO 8601 `timestamptz` string, a tag 100 value as an ISO 8601 date, and a tag 4 value as its decimal text.
- A JSON object or array as its JSON text, and a CBOR map or array as the JSON text its conversion gives (`## json and jsonb columns`). The type's input function decides whether it accepts it.
- A CBOR value with no such text form (another tag, a non-finite float where the type has no such value) is an error.

A composite or array column in text mode is written as a Postgres literal built recursively from its elements, each element rendered by these same rules:

- An array is `{e1,e2,…}`. An element is quoted when it is empty, equals `NULL` in any case, or contains `{`, `}`, `,`, `"`, `\` or whitespace. A NULL element is `NULL` unquoted.
- A composite is `(f1,f2,…)`. A field is quoted when it is empty or contains `(`, `)`, `,`, `"`, `\` or whitespace. A NULL field is empty and unquoted.
- A quoted element or field escapes `"` and `\` with a backslash.

Typical text-mode types: `interval`, `time`, `timetz`, `inet`, `cidr`, `macaddr`, `money`, `bit`, `varbit`, range and multirange types, geometric types, `tsvector`, `xml`, and extension types such as PostGIS' `geometry` and `geography`.

Text mode accepts what Postgres' own `json_to_record`, `jsonb_to_record`, `jsonb_to_recordset` and `jsonb_populate_record` accept for the same type, and rejects what they reject. No rule is specific to an extension.

An extension type needs no registration. Introspection already lists it as a `pg.Type` (name, schema and OID, with no `ElementType`, `BaseType` or `Relation`), so it is a base type with no encoder and lands in text mode.

> **Why:** Postgres parses these types from text itself, so nothing accepted today is rejected and no format has to be reimplemented. `jsonb_populate_record` already hands a JSON object's text to the input function of a non-JSON type: a GeoJSON object written to a `geometry` column works today (checked on PostGIS 3.4: the `crs` member gives the SRID), and text mode keeps doing exactly that. A `geography` column does not accept a GeoJSON object, today or in text mode; it takes the text forms its input function reads (WKT, EWKT, hex EWKB).

### Type modifiers and domains

In binary mode a temp column or row-type field carries the target column's full declared type including modifiers (`varchar(20)`, `numeric(10,2)`, `Column.DeclaredType`). Length, precision and domain constraints are therefore checked when the rows are loaded, not when they are inserted. The error code and message stay those Postgres reports.

In text mode the temp column is `text`, and the statement that reads it casts it to the column's type without modifiers (`::bit`, never `::bit(4)`). The `insert` or `update` then assigns the value to the column, which applies the modifiers with assignment semantics : a value that does not fit is an error, as it is today.

> **Why:** an explicit cast to `bit(4)` or `varchar(5)` pads or truncates silently (`'101'` becomes `1010`), while assignment, like `jsonb_populate_record`, rejects it. The differential tests pin this.

## Temp row types

For each destination table, two temp tables exist per connection. They are used for their row types. Only the first holds rows.

| Temp table | Columns | Used by |
|---|---|---|
| `pg_temp.__rel_w_<oid>` | the metadata columns, the table's columns, the mask words | general path |
| `pg_temp.__rel_f_<oid>` | the table's columns, the mask words | fast path |

`<oid>` is the destination table's relation OID.

### Columns

- A table column keeps its name, with no `NOT NULL`, default, primary key, unique or check constraint. A text-mode column is declared `text`.
- The metadata columns are `<prefix>row_id int`, `<prefix>node_id int`, `<prefix>parent_id int`, and the mask words `<prefix>mask0`, `<prefix>mask1`, … as `bigint`.
- `<prefix>` is `__rel_`. If any column of the table starts with the prefix, the prefix gets one more `_` until none does.
- There are no indexes. Parent and child lookups by `row_id` are joins over the request's own rows.
- `__rel_w_<oid>` is created `on commit delete rows`. `__rel_f_<oid>` never receives rows.

### Row ids

`row_id` is unique across every temp table of a request. `WriteState` keeps the allocator across the items of a sequence, as it does now. `node_id` identifies the query node, so a table appearing at several nodes keeps its rows apart.

### Generated keys

An insert or upsert writes the keys it generated back into the same row's columns in the general temp table (`update … set <key columns> = dml.<key columns> from dml where row_id = dml.row_id`). A child resolves a foreign key from its parent's temp table by `parent_id = row_id`, typed, with no cast. The key columns are `keysColumns(node)` (`query/write_dml.go`).

> **Why:** a node's parent is fixed by the query tree, so the SQL for a child names its parent's temp table directly. Key columns are few and typed, so the write-back avoids building and parsing a jsonb object per row.

## Connection lifecycle

- A connection creates the two temp tables of a destination table lazily, the first time a write on that connection touches it, in one pipelined batch together with the OID lookup of both row types and their array types, and a grant to `public`. The batch runs before the request's transaction begins.
- After that batch, the connection's pgx type map gets a codec registered for each array type OID. The codec accepts the `rawBinary` parameter type and writes its bytes unchanged in binary format.
- A temp table exists as long as its connection. Rel rebuilds the pool before it re-introspects the database, so a temp table never outlives the schema it was built from.
- The grant mirrors the one `server/rel.go` gives `_data` today, so the role switched to by `SET LOCAL ROLE` can read and write the tables.
- Transaction-level connection poolers in front of Postgres are unsupported, as they are for `_data` today.

> **Why:** creating a temp table is a catalog write that waits for a WAL flush (`server/rel.go` notes this for `_data`). Lazy creation moves that cost to the first write per table per connection. Creating the tables of every writable relation at connection setup would cost two catalog entries per relation per connection, a few hundred for a database like the test fixture.

## Loading

Rows reach Postgres as one binary array parameter per temp table, in the same statement batch as the statements that read them:

```sql
insert into pg_temp.__rel_w_16401 select * from unnest($1::pg_temp.__rel_w_16401[])
```

The parameter is a `rawBinary` value: a Postgres binary array of composites, built in one buffer. The array header carries the row type's OID. Each element is a record with one field per column of the temp table, unsupplied columns as NULL.

> **Why:** `COPY` cannot travel in a `pgx.Batch`, which is why `loadData` already inserts instead. A single flat composite with the mask words as fields ran within noise of sending no flags at all (41 columns, 50000 rows: 378 ms against 379 ms). Zipping a second `int8[]` array of masks cost 5 to 10 % more, and nesting the table's own row type inside a wrapper composite cost 33 to 43 % more.

## General path

The phases of `query/write.go` keep their order. What changes in the SQL:

- **Load**: one `insert … unnest` per temp table, replacing `loadData`'s `jsonb_to_recordset`.
- **`resolved` CTE** (`writeResolvedCTE`): reads the node's rows from its temp table by `node_id`, with typed columns, in place of `jsonb_populate_record(null::rel, tmp.data)`. Text-mode columns are cast. Default, update-keeps and key-linkage rules are as in `## Use in SQL`.
- **Key write-back**: `## Generated keys`. `recoverKeys` (`merge-new`'s do-nothing branch) matches the supplied identity columns of the temp row against the target table, typed.
- **`runUpdateWhere`** (`update` with a patch): the patch is one row in the table's general temp table. The rows it updates are recorded as new rows of the same table, with fresh row ids.
- **Read-back** (`CompileSelectForDataNode`): scopes the root relation to the rows of its own temp table where `parent_id` is null and `node_id` is the root's, matched on the identity columns, in place of the `_data` join.
- **`DataTableDDL`, `DataTableDDLPerTransaction`, `dataTables` and `denormalize`'s jsonb output** are removed.

`stats.submitted` counts the rows loaded for a node, as it does now.

## Fast path

Eligibility is unchanged (`fastWriteEligible`). The rows come in as one binary array parameter of the fast row type `pg_temp.__rel_f_<oid>`, and the statement reads from it:

```sql
insert into t (a, b, c)
select z.a, case when (z.__rel_mask0 & 4) <> 0 then z.b else <default> end, z.c
from unnest($1::pg_temp.__rel_f_16401[]) z
returning *
```

Where `runInsert` orders by `__row_id` today to keep `RETURNING` in payload order, the statement uses `unnest(…) with ordinality` and orders by it. The response is built from the `RETURNING` rows as it is now. A write that cannot take the fast path takes the general path with the same encoders.

## Errors

An encoding failure reports the node (path of join aliases), the row index in the payload, the column, the expected Postgres type and the received kind (`string`, `number`, `byte string`, …), with the `errcode` the current writes use for a bad payload. No encoding failure reaches Postgres.

## Tests

All tests are integration tests against a real Postgres (testcontainers, as in `query/write_test.go`) or plain Go tests for pure code. Tests use Postgres' built-in types and the contrib extensions the official image ships (`citext`, `hstore`, `ltree`), never a third-party extension. The existing write tests (`write_test.go`, `write_fast_test.go`, `server` write tests) stay and must pass unchanged, except where they read `_data`.

Every value-level test runs for each source (JSON, and CBOR once the CBOR adapter exists) and for each path (general and fast, where eligible), and compares both paths with each other.

### Layout and masks

- Tables with 1, 63, 64, 65, 128 and 129 slots; composite expansion order, including nested composites and a domain over a composite; lookup round trip; arrays not expanded.
- A table with more than 64 columns written end to end through both paths, with bits set in every word.
- The mask word count and bit positions stay identical across two introspections of the same schema.

### Encoders

- **Differential, per type**: for every type with an encoder (`int2`, `int4`, `int8`, `float4`, `float8`, `numeric`, `bool`, `text`, `varchar`, `bpchar`, `name`, `uuid`, `bytea`, `date`, `timestamp`, `timestamptz`, enums, `json`, `jsonb`) a corpus of accepted and rejected inputs. Whatever an encoder accepts is stored, read back and compared with `select '<text>'::<type>` on Postgres, and the two must be equal. A corpus entry Postgres accepts but the encoder rejects must appear in the documented rejection list of `## Column encoders`.
- **Boundaries**: minimum and maximum of each integer type, one past each, `numeric` with 0, 1, 4, 5, 1000 digits, negative scale and exponent forms, `NaN`, `Infinity`; floats with subnormal values and `-0`; empty and 1 MB strings; strings with `\u0000` (rejected as Postgres rejects them), multi-byte characters and invalid UTF-8 (CBOR).
- **Modifiers and domains**: `varchar(n)` overflow, `numeric(p,s)` rounding and overflow, `bpchar(n)` padding, a domain with a `CHECK`, a `NOT NULL` domain, a domain over an array; the error code matches the one an `insert` reports.
- **Arrays**: empty, one-dimensional, two-dimensional rectangular, ragged (rejected), with `null` elements, of every encoder type, and an array given as a Postgres literal string (rejected in binary mode).
- **Wrong kinds**: for each encoder, a value of every other JSON and CBOR kind (object, array, number, string, bool, null, byte string, tags) either accepted as documented or rejected with an error naming node, row, column, expected type and received kind.
- **json and jsonb**: nested documents, large numbers kept exact, duplicate keys, a CBOR document with each rejected kind (byte string, tag, non-finite float, non-text key).

### Date and time

- Session `TimeZone` set to `UTC`, `Europe/Paris` and an offset zone; daylight-saving gap and overlap; `infinity` and `-infinity` for all three types.
- Fractional digits 0 to 7 (7 rejected); `24:00:00` accepted, seconds `60` rejected; offset ignored by `timestamp`, honoured by `timestamptz`; each rejected form of `## Date and time strings`.
- Range edges: 4713 BC, 294276 AD, one past each.
- `BC` suffix and 5-digit years, round-tripped from rel's own output for each of the three types.
- CBOR tag 0, tag 1 (integer and float seconds, with microsecond precision at several magnitudes), tag 100.

### Absent versus null

- Each of insert, upsert, `update-existing`, `merge-update` with a column absent, `null`, and supplied.
- `NOT NULL` with default and explicit `null`; a column without default; a generated-by-default identity column.
- Composite sub-field update with only one sub-field supplied, then with a sibling set to `null`.
- Upsert whose row already exists, with an absent defaulted column (it is overwritten by the default, as documented).
- Mixed rows in one payload, supplying different columns.
- `insert_columns` and `update_columns` allowlists.

### Text mode

- Built-in types without an encoder: `interval`, `time`, `timetz`, `inet`, `cidr`, `macaddr`, `money`, `bit`, `varbit`, `int4range`, `tsrange`, a multirange, `point`, `box`, `tsvector`, `xml`, and a contrib extension type (`citext`, `hstore`, `ltree`).
- For each, a corpus of strings, numbers, booleans, objects, arrays, and (for CBOR) integers, floats, bignums, byte strings and the supported tags. Each value is compared with what `jsonb_populate_record` does for the same JSON value: the same stored value, or both reject.
- Strings sent as JSON strings and as CBOR text strings give the same stored value.
- Text-mode arrays (`interval[]`, `inet[]`, `int4range[]`) with elements that need quoting: empty, `NULL`, braces, commas, quotes, backslashes, whitespace, multi-byte characters, `null` elements.
- Text-mode composites: a composite with an `interval` field, a composite nested in an array, an array inside a composite, quoting of each special character at each nesting level.

### Composite values

- Composite column whole and by sub-field, composites nested in composites, array of composites, `null` composite, missing and extra keys.
- Each case compared against the same write through `_data` before it is removed.

### Key flow and structure

- Three-level nested writes, an outgoing child, a table appearing at two nodes, a self-referencing table, a non-primary-key `on_conflict`, `merge-new` recovery, `merge` deleting absent children, a `GENERATED ALWAYS` identity.
- A sequence of several write items sharing a connection and one `WriteState`; row ids stay unique across items.
- `stats`, `sql` and `query_plan` output unchanged against the existing tests.

### Connection and lifecycle

- **Role switch**: a write under `SET LOCAL ROLE` to a role without access to the pool user's temp objects.
- **Pool rebuild**: re-introspection after `alter table … add column`, then a write on a connection that had the old temp tables.
- **Lazy creation**: the first write to a table on a connection creates its temp tables and later writes do not; a rolled-back request leaves usable temp tables behind.
- **Column name clash**: a table with a column named `__rel_row_id`, and one named `__rel__mask0`.
- **Wide tables**: a table near the 1600-column limit fails with a clear error, not a Postgres internal one.

### Scale and performance

- Payloads of 1, 20, 1000 and 50000 rows, and one of 1 million small rows, compared with the `_data` path for result and for time.
- Benchmarks: `query/write_bench_test.go` extended to compare against the `_data` path at those sizes, a benchmark of the encoders against pgx's `any` path, and a benchmark of the packed row types (flat composite, zipped `int8[]`, wrapper composite).

## Docs

`docs/content/query-language/writing.md` gets the accepted value forms per type, the datetime rules, and the text-mode behaviour, including how extension types such as PostGIS' are written. `specs/cbor.md ## Node interface`'s question about `Raw()` is answered by `## Value source`.
