# Javascript/Typescript interactions

At its core, rel was thought up to be queried by javascript clients ; web browsers. JSON is its query language because it plays particularly nice with it.

## Goals

Especially for typescript ; provide a lightweight, ergonomic and minimalist client library along with database schema in JSON format that can :

- Provide completion while writing queries, especially on column/property names so that changing names get flagged
- Give a way to deserialize cleanly into objects with specific prototypes (in particular to allow custom accessors and methods) ; embedded children must retain prototype and type information for the result at the call site. custom prototypes must also allow for a post-deserialization hook.
- Give a way to serialize back those objects, even when creating prototypes.
- Type check column usage in expressions (might be hard and will use a lot of generics - probably a v2 concern)
- Give a json schema of the database that could be introspected by a library

## Musings

- Footprint should be absolutely minimal and done almost entirely in typespace ; importing schemas will just augment a helper function that then shapes the result correctly
- A separate mechanism should provide the querier with a json that lists whatever they can query (with a schema whitelist ; we don't want big things like pg_catalog in there, although if a type is referenced then this should trigger its inclusion) ; pretty much what was introspected in a "palatable" form with less pg-y naming (especially with function arguments and the likes.)

This is not an ORM ; having .save() is a non-goal. What matters is ensuring a query has the right shape and its result is known by typescript.

However ; providing a facility so that we can setPrototypeOf() transparently so that the developper might add accessors and methods to the returned types - and their embedded sub-types is a primary goal. The way they do so must be as minimally intrusive as possible so that it survives regenaration of the file, displays errors if the changes no longer work.

Generated code may output comments telling biome/prettier to deactivate a few checks as it might not conform to all its settings.

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
