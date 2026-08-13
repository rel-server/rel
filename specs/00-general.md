# Objective of the rewrite

goserver served its purpose well, but it has several weaknesses and its query language is clumsy. This new project, named rel, aims to take it up a notch.

These documents try to describe rel by drawing comparisons to the legacy goserver. They will not stay in the final source code ; a new documentation will take its place.

For now, we will focus on specifying enough so that we can start implementing.

## Features

- Several possible configuration sources (environment variables, command line flags, TOML/YAML/HUML)
- A relational query language that spans relations with granular write control 
  - query data, modify it keeping its structure and send it back to the server for it to update it in place spanning multiple relations at once.
  - Protocol is now JSON
  - On the server, queries are generated with plain go statements, using a lightweight library helper to help with identifier escaping/parenthesis tracking/avoiding simple syntax errors in general.
- Authentication ; OpenID, OAuth and easily configurable SAML endpoints or support for allowing username/password
  - Generic approach that must not assume any particular database layout
- JWT session
  - Access to the database is still done through roles
- Session checking support
- Static file serving
  - Possibility to configure file access control based on path and database queries
  
- Typescript/Javascript support
  - The web server provides /js/query.js and /js/query.ts that house a simple querying library to interact with the API
  - The exported schemas /js/schemas/schema1.js (and .ts) give a definition of the schema that can be consumed by typescript/javascript to write queries more easily
  - Intended workflow ; the developper downloads these files and puts them in his code and then imports them to interact with the database
  - In production, these can be turned off (or kept if they want to leave the query engine open)
