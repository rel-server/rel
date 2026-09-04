# Javascript/Typescript interactions

At its core, rel was thought up to be queried by javascript clients ; web browsers. JSON is its query language because it plays particularly nice with it.

## Goals

Especially for typescript ; provide a lightweight, ergonomic and minimalist client library along with database schema in JSON format that can :

- Provide completion while writing queries, especially on column/property names so that changing names get flagged
- Give a way to deserialize cleanly into objects with specific prototypes (in particular to allow custom accessors and methods) ; embedded children must retain prototype and type information for the result at the call site. custom prototypes must also allow for a post-deserialization hook.
- Give a way to serialize back those objects, even when creating prototypes.
- Type check column usage in expressions (might be hard and will use a lot of generics - probably a v2 concern)
- Give a json schema of the database that could be introspected by a library

Not an ORM — `.save()` is a non-goal. What matters is that a query's shape and result are known to TypeScript.

A facility to `setPrototypeOf()` transparently, so a developer can add accessors/methods to returned types and their embedded sub-types, is a primary goal. It must be minimally intrusive enough to survive regeneration of the file, and must surface an error if a change no longer applies.

Generated code may output comments telling biome/prettier to deactivate checks it might not conform to.

> Thoughts: footprint should be minimal, done almost entirely in typespace — importing a schema augments a helper function that shapes the result, rather than generating bulky per-schema code.
>
> A separate mechanism should give the querier a JSON listing of whatever they can query, schema-whitelisted (no `pg_catalog` by default, though a referenced type should still pull its definition in) — introspection output in a "palatable," less pg-flavored form (especially function arguments).

## Configuration

- `http.typescript.enable` (default false, true if dev enabled) enable serving typescript files of the server
- `http.typescript.schemas` (default empty) a whitelist of schemas that can be asked
- `http.json.enable` (default false, true if dev enabled) enable serving the JSON file describing the database

## Endpoints

Available as `GET`, both endpoints accept `schemas` as a query parameter that allows selecting which schemas will be included into the file. If not provided, all known schemas (aside from `pg_catalog`) are included into the output.

- `/rel/database.ts` : the database described as typescript types with some helpers
- `/rel/database.json` : a JSON file that describe the database exported by rel


## database.json

Based on the introspection we made, outputs a full JSON file

```typescript
interface Database {
  // can you fill that ? Maybe wait until typescript export is stabilized to offer something similar
}
```

## database.ts

The typescript file is somewhat different from the json file ; on top of serving the structure of the database, it also provide a few helper functions that make use of the generated types to help the developper.

A WIP is in `./typescript/query.ts`

## Testing

This is a bit trickier to test ; mostly this resolves around making type assertions in a test file and verifying that these assertions do not produce errors.
>: Please advise how else we can test all this
