# Javascript/Typescript interactions

At its core, rel was thought up to be queried by javascript clients ; web browsers. JSON is its query language because it plays particularly nice with it.

As such, it provides a few things to help developers with typescript code bases for querying rel in a typesafe manner.

## Schema endpoints

- `/rel/db.json`
- `/rel/db.js`
- `/rel/db.ts`
- `/rel/db/<schema>.{json,js,ts}`



## query API endpoint

- `/rel/query.js`
- `/rel/query.ts`

`query.js` is simply the transpiled version of `query.ts`.

This file provides a simple function that, given schema.json,
