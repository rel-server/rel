// PostGraphile v5 (Grafast) with the plugins the benchmark report lists:
// connection filter (rich where) and simplified names. No nested-mutation
// plugin exists for v5, so the multi-level write is emulated by the harness.
import { createServer } from "node:http";
import { postgraphile } from "postgraphile";
import { grafserv } from "postgraphile/grafserv/node";
import { PostGraphileAmberPreset } from "postgraphile/presets/amber";
import { makePgService } from "postgraphile/adaptors/pg";
import { PgSimplifyInflectionPreset } from "@graphile/simplify-inflection";
import { PostGraphileConnectionFilterPreset } from "postgraphile-plugin-connection-filter";

const preset = {
  extends: [PostGraphileAmberPreset, PgSimplifyInflectionPreset, PostGraphileConnectionFilterPreset],
  pgServices: [
    makePgService({
      connectionString: process.env.DATABASE_URL,
      schemas: ["hotel"],
      poolConfig: { max: Number(process.env.POOL_SIZE || 20) },
    }),
  ],
  // v5 hides filters and orderings on columns without an index by default ; the
  // other servers allow them, so turn that off to compare like with like.
  disablePlugins: ["PgIndexBehaviorsPlugin"],
  grafserv: { graphqlPath: "/graphql", graphiql: false },
};

const pgl = postgraphile(preset);
const server = createServer();
server.on("error", (e) => {
  console.error(e);
  process.exit(1);
});
await pgl.createServ(grafserv).addTo(server);
server.listen(Number(process.env.PORT || 5000), "0.0.0.0");
