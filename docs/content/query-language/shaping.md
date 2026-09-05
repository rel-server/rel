---
icon: material/shape-outline
---

# Shaping a query

Every query is a tree of nodes shaped like the `Relation` type in the [Query shape
reference](reference.md) — one root, plus a `join` for each nested node. Every node, root or
nested, carries the same fields and follows the same rules; this page covers the ones that
decide what a node *is* and what it can *see* — `relation`/`function`, `schema`, `alias`, and
scope. [Filtering](filtering.md), [selecting](selecting.md), [joining](joining.md), and
[writing](writing.md) each cover the rest of `Relation`'s fields on their own page.

## One root, table or function

Every query names exactly one root — a table/view (`relation`) or a function (`function`),
never both:

```json
{ "relation": "properties", "schema": "hotel" }
```

The same rule applies to a `join` target: it's a nested node, not a different kind of thing,
so it also names exactly one of `relation`/`function`. See [Calling
functions](functions.md) for rooting a query on a function, or joining into one, instead of a
table.

## Schema resolution

`schema` is optional; when omitted, rel resolves `relation`/`function` against the connecting
role's own search path, the same way plain SQL would.

Two schemas are never queryable, `relation`/`schema` or not: `pg_catalog` and
`information_schema`. Both are readable by `PUBLIC` in a default Postgres install and would
otherwise be reachable through an ordinary query the same as any real table.

## Aliases and self-joins

`alias` names the root (or any joined node) for use in its own expressions and its children's —
useful for a self-join, where a relation joins back into itself, such as
`hotel.staff.manager_id` pointing back at another row in `hotel.staff`:

```json
{
  "relation": "staff", "schema": "hotel", "alias": "s",
  "join": {
    "manager": { "relation": "staff", "schema": "hotel", "on": { "id": "manager_id" } }
  }
}
```

## What a node can see

A node's own expressions (`where`, `select`, and the rest) can reference: its own physical
columns, its own `alias` (self-referencing its own columns), and the alias of each of its
direct `join` children. Nothing else is in scope — not the parent node's columns or alias, and
not a sibling's. A bare name that matches more than one of these at once (a column and a child
alias, say) is a hard error rather than being resolved by some fixed precedence order — a query
that could silently mean either of two things never runs at all.

## Depth and disallowed constructs

A query's nesting depth is capped (`pg.query.max_depth`, default `6`) — see
[Configuration](../configuration/index.md). `GROUP BY` and window functions have no equivalent
in the query language at all; model that shape as a database view and query the view instead.

See [Database reference](database-reference.md) for the full schema these examples query
against.
