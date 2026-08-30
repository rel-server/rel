# Well Known Queries

They're basically what views achieve in SQL, but in rel, with the write operations planned for.

- rel reads them on startup, but also when receiving `SIGUSR2`
- rel reevaluates them whenever the database is introspected

## Configuration

* `pg.query.wellknown_path` (default `'/wellknown'`) : a colon `:` separated list of directories containing well-known queries in json, yaml or ruml format.

## Behaviour

Rel reads the directories of `pg.query.wellknown_path` recursively and consider every .json, .yml, .yaml, .ruml files whose name doesn't start with '_'.

If a query has an error, a warning is displayed, and the query is deactivated. If a query introduces a name already existing, rel prints a warning a deactivates all queries on that name, replying instead an error when it is queries.

Well-known queries are not meant to be mixed-and-matched with other queries ; they're evaluated once and their statements are prepared, ready to be queried.

In their implementation, the params will be supplied with `$1::jsonb`, from which the `["$param"... ]` operator will extract the correct version.

## Definition

A well-known file contains either one `WellKnownQuery` or `WellKnownQuery[]`. It defines a query in the Rel JSON Query Language


```typescript
interface WellKnownQuery {
  name: string
  query: QueryNode
  params: {
    [name: string]: WellKnownParam
  }
}

interface WellKnownParam {
  type: string // any postgres type that the json value will then cast to
  default?: unknown // must be of the specified type or castable. Can be null to indicate non-requiredness
}
```

> Note: I think that the [$param ] operator had semantics a little different

## Compilation Errors

- `RW001` : duplicate well-known name
  Two well-known queries intended to register the same name
- `RW002` : unused param
  The query declares a parameter it doesn't use
- `RW003` : non-existing param
  A `[$param]` statement calls a non-existing param

## Execution Errors

- `RW010` : a supplied param was of the wrong type
- `RW011` : the user did not specify a param that did not have a default
