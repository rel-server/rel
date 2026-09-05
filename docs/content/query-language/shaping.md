---
icon: material/shape-outline
---

# Shaping a query

Every query names exactly one root — a table/view (`relation`) or a function (`function`),
never both:

```json
{ "relation": "properties", "schema": "hotel" }
```

`schema` is optional; when omitted, rel resolves `relation`/`function` against the connecting
role's own search path, the same way plain SQL would. `alias` names the root for use in its
own expressions and its children's — useful for a self-join (`hotel.staff.manager_id` points
back at another row in `hotel.staff`):

```json
{
  "relation": "staff", "schema": "hotel", "alias": "s",
  "join": {
    "manager": { "relation": "staff", "schema": "hotel", "on": { "id": "manager_id" } }
  }
}
```

See [Database reference](database-reference.md) for the full schema these examples query
against, and [Calling functions](functions.md) for rooting a query on a function instead of a
table.
