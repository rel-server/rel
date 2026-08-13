
Rel's most important feature is its querying capabilities.

Similarly to GraphQL and PostgresT, it offers a complex query engine able to span across relations in the database to produce intricated and complex content.

Unlike GraphQL, there are no "mutations" to describe ; unlike PostgresT, the complex form that it generates can be sent back as-is to the server so that it updates accordingly.

Selecting is based on a relation. Foreign keys allow embedding of a distant resource into the result : whether from the table to another or in reverse. When embedding a remote relation that has multiple rows to the current one, embeds an array. Otherwise, stays as a simple object.

Unlike route functions, all queries are sent on `/rel`, and all of them MUST be `POST`.

When querying resources they're not allowed to access in the database, the status will be `401`. A unknown relation will result in `400`, as `/rel` will never be 404 itself.

`/rel` only returns JSON, even when it replies an error.

See `./query.ts` for the JSON query shape.
