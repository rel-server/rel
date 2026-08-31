# Javascript/Typescript interactions

At its core, rel was thought up to be queried by javascript clients ; web browsers. JSON is its query language because it plays particularly nice with it.

As such, it provides a few things to help developers with typescript code bases for querying rel in a typesafe manner.

Generated code may output comments telling biome/prettier to deactivate a few checks as it might not conform to all its settings.

## Schema endpoints

- `/rel/db.json`
- `/rel/db.js`
- `/rel/db.ts`
- `/rel/db/<schema>.{json,js,ts}`

These expose the database introspection, although their format differs from the internal introspection ; their role is to help a typescript API type queries and their results correctly, but also eventual libraries that would want to 

## query API endpoint

- `/rel/query.js`
- `/rel/query.ts`

`query.js` is simply the transpiled version of `query.ts`.

This file provides a simple function that, given schema.json,
