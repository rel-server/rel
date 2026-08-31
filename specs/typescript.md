# Javascript/Typescript interactions

At its core, rel was thought up to be queried by javascript clients ; web browsers. JSON is its query language because it plays particularly nice with it.

## Goals

Especially for typescript ; provide a lightweight, ergonomic and minimalist client library along with database schema in JSON format that can :

- Provide completion while writing queries, especially on column/property names so that changing names get flagged
- Give a way to deserialize cleanly into objects with specific prototypes (in particular to allow custom accessors and methods) ; embedded children must retain prototype and type information for the result at the call site. custom prototypes must also allow for a post-deserialization hook.
- Give a way to serialize back those objects, even when creating prototypes.
- Type check column usage in expressions (might be hard and will use a lot of generics - probably a v2 concern)

This is not exaclty an ORM ; having .save() is a non-goal.

Exported database symbols must be tree-shakable.

Rel must offer both to generate a whole javascript/typescript tree where told as well as offer these files over https for dynamic clients (it might provide a generic web-admin at some point).

Generated code may output comments telling biome/prettier to deactivate a few checks as it might not conform to all its settings.

## Configuration

- `http.javascript.enable` (default false) enable serving typescript/javascript/json files about the
- `http.javascript.prefix` (default `/gen`)

## Endpoints

- `$prefix/db.json`
- `$prefix/db.js`
- `$prefix/db.ts`
- `$prefix/db/<schema>.{json,js,ts}`

These expose the database introspection, although their format differs from the internal introspection.

```typescript
interface Relation {
  name: string
}

interface Function {
  
}
````

## query API endpoint

- `$prefix/query.js`
- `$prefix/query.ts`

`query.js` is simply the transpiled version of `query.ts`.

This file provides a simple function that, given schema.json,

## API

```typescript
// rel will need some helper types
export type QueryResult<Q extends Query> = /* todo */;

async function read<T>(query_object): Promise<T> { }
async function write<T>(query_object, data): Promise<T> { }

```
