// PostGraphile v4 with the plugins the benchmark report lists: connection
// filter (rich where), simplified names, and nested mutations (the only way
// v4 can do the multi-level write natively).
const http = require("node:http");
const { Pool } = require("pg");
const { postgraphile } = require("postgraphile");
const PgSimplifyInflectorPlugin = require("@graphile-contrib/pg-simplify-inflector");
const ConnectionFilterPlugin = require("postgraphile-plugin-connection-filter");
const NestedMutationsPlugin = require("postgraphile-plugin-nested-mutations");

const pool = new Pool({ connectionString: process.env.DATABASE_URL, max: Number(process.env.POOL_SIZE || 20) });

http
  .createServer(
    postgraphile(pool, "hotel", {
      appendPlugins: [PgSimplifyInflectorPlugin, ConnectionFilterPlugin, NestedMutationsPlugin],
      graphileBuildOptions: { connectionFilterRelations: true },
      graphqlRoute: "/graphql",
      disableGraphiql: true,
      enhanceGraphiql: false,
      watchPg: false,
      retryOnInitFail: true,
    }),
  )
  .listen(Number(process.env.PORT || 5000), "0.0.0.0");
