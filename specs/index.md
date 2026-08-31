# The Rel server

## Reading order

One file per topic, no number prefixes — this list carries the reading/dependency order
instead. Each entry covers roughly what the previous ones assume.

1. [[configuration.md]] — config sources (env/flags/file), precedence, secrets (`$FILE$`), the
   "no arrays" rule.
2. [[introspection.md]] — how rel reads the Postgres schema (relations, constraints, indexes,
   functions, types) at startup and on reload.
3. [[logging.md]] — logging configuration and output.
4. [[error-handling.md]] — error codes, the `RSxxx` convention.
5. [[migrations.md]] — schema migrations via `dmut`, boot ordering, `SIGUSR1` reload.
6. [[query-engine.md]] — the core relational query language : scoping, writability, the
   Reading and Writing algorithms. Everything else query-related builds on this.
7. [[query-json.md]] — `GET /rel`'s query-string encoding of the same query shape.
8. [[query.ts]] — canonical TypeScript type reference for the query JSON shape.
9. [[well-known-queries.md]] — named, pre-parsed queries exported to the TS client.
10. [[authentication.md]] — JWT/session lifecycle, roles, SAML/OIDC/username-password auth.
11. [[rpc.md]] — `/rpc` route dispatch, request/response shapes, cookies, Postgres exceptions.
12. [[http-content.md]] — static file serving, Jet templates, CORS, CSP.
13. [[realtime.md]] — WebSockets + Postgres `LISTEN`/`NOTIFY` (reserved, not yet specified).
14. [[typescript.md]] — the generated TS/JS client export.
15. [[oauth-saml.md]] — SAML/OIDC callback endpoints specifically (reserved, not yet
    specified — `authentication.md` already covers username/password and the session/JWT
    side of SAML/OIDC).
16. [[testing.md]] — fixture/testcontainers conventions ; a developer-process doc, not part
    of understanding the running system, but referenced from the sections above wherever
    their own tests build on one of the two fixtures it describes.

[[TODO.md]], alongside this directory, tracks spec completeness against the feature list
below — not a topic of its own, consult it for what's still open in any of the above.

## Features

- Several possible configuration sources (environment variables, command line flags, TOML/YAML/HUML)
- A relational query language that spans relations with granular write control 
  - query data, modify it keeping its structure and send it back to the server for it to update it in place spanning multiple relations at once.
  - JSON protocol
  - On the server, queries are generated with plain go statements, using a lightweight library helper to help with identifier escaping/parenthesis tracking/avoiding simple syntax errors in general.
- Authentication ; OpenID, OAuth and easily configurable SAML endpoints or support for allowing username/password
  - Generic approach that must not assume any particular database layout
- JWT session
  - Access to the database is still done through roles
- Session checking support
- Static file serving
  - Possibility to configure file access control based on path and database queries
  
- Typescript/Javascript support (`typescript.md` is the authoritative spec for this, actively
  being filled in — the paths below are illustrative only, not settled)
  - The web server provides /js/query.js and /js/query.ts that house a simple querying library to interact with the API
  - The exported schemas /js/schemas/schema1.js (and .ts) give a definition of the schema that can be consumed by typescript/javascript to write queries more easily
  - Intended workflow ; the developper downloads these files and puts them in his code and then imports them to interact with the database
  - In production, these can be turned off (or kept if they want to leave the query engine open)
