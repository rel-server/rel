# Typescript Protos

`relation()` and `func()` queries return plain data objects. This spec adds an optional `proto` field to `RelationQuery` that attaches behavior (getters, methods) to those objects.

## `proto` field

`RelationQuery` gains an optional `proto` field: a plain object of getters/methods, typed via `ThisType` against this node's own row shape.

`proto` is valid on any `RelationQuery` node, whether rooted by `relation()` or `func()`, and on any nested `join` entry.

> **Why:** `proto` only decorates already-produced rows with behavior ; it has no bearing on whether the node is writable, so function-rooted nodes (always read-only, `query.ts`'s `function?` field) support it the same as relation-rooted ones.

`relation()`, `func()`, and `join()` each expose their `request` parameter's `proto`-accepting form and their callback-accepting form as separate overloads, not as one signature with a union parameter type.

> **Why:** a `ThisType`-typed object literal only has its members' `this` resolved correctly when checked against a single, non-union parameter type — folded into a union with the callback form, `this` silently stops resolving. A callback-shaped `proto` field (`base_class => class extends base_class {...}`) has the same failure for a different reason : a parameter's contextual type is resolved eagerly, during inference, before the rest of the query object's sibling fields (`join` in particular) finish inferring, so `this` inside such a callback's class body can't see a sibling `join`'s embedded shape, and the callback's presence degrades the sibling fields' own inference too. Both failures were confirmed empirically before choosing the object/`ThisType` form.

## Shape

`ShapeFromRelationQuery` merges `proto`'s own members into the row shape it computes for that node, alongside the fields already derived from `select`/`join`.

`WriteShape` is unaffected by `proto` : `proto` only adds behavior on top of a row's existing fields, so a row read through a `proto` still satisfies the same `WriteShape` it would without one.

## Runtime

The Querier applies `Object.setPrototypeOf` to each row it constructs from the response, using the `proto` object of the query node that produced that row.

The Querier walks the response using the query's own `relation`/`join` tree, applying `proto` once per node : a joined relation's `proto` is applied to its own rows independently of its parent's `proto`.

Cardinality is read off the response value itself, not off any type-level source : an array is a to-many join, a non-null object is a to-one join, `null` is a to-one join with no matching row.

> **Why:** `JoinCardinality` (`shapes.ts`) only exists at the type level and is erased at compile time ; the response's own shape is the only cardinality signal available at runtime.

A to-many join applies its relation's prototype to every element of the returned array.

A to-one join applies its relation's prototype to the returned object, or does nothing when the value is `null`.

## Example

```typescript
const usr = relation("api.users", {
  proto: {
    get test() { return `${this.username} !` }
  }
})
```

```typescript
const withPosts = relation("api.users", {
  join: {
    posts: join("users", "api.posts<...", {
      proto: {
        get excerpt() { return this.body.slice(0, 100) }
      }
    })
  },
  proto: {
    // `this.posts` is already typed with the join's own `proto`-merged shape, including `excerpt`.
    get postCount() { return this.posts.length }
  }
})
```

## Reusing a Querier in a join

`Querier` gains a fourth type parameter, `Q`, carrying the literal query object it was constructed from ; its `query` field is typed `Q` instead of the widened `Query`.

`relation()` and `func()` supply their own `Q` as this fourth parameter on the `Querier` they return.

`join()`'s `request` parameter accepts a `Querier<any, any, any, Q>`, in addition to the plain query object or callback it already accepts.

When `request` is a `Querier`, `join()` uses its `query` field as the reused query object, in place of the object literal or callback's result.

`on`, `schema`, `relation`, and `shortcut` are always derived from `shortcut`, regardless of which form `request` takes.

> **Why:** a `Querier` built through `relation()` never carries `on`, since a root query isn't itself a join ; deriving `on`/`schema`/`relation` from `shortcut` unconditionally keeps that requirement uniform across all three forms of `request`.

### Example

```typescript
const postsWithExcerpt = relation("api.posts", {
  proto: {
    get excerpt() { return this.body.slice(0, 100) }
  }
})

const withPosts = relation("api.users", {
  join: {
    posts: join("users", "api.posts<...", postsWithExcerpt)
  }
})
```
