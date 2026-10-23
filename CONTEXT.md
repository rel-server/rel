# rel

An HTTP server that exposes a Postgres database through a JSON query language, generated TypeScript clients, and templated routes.

## Language

**Item**:
One entry of a `/rel` request: a bare relation query, a well-known query, or a ComplexQuery wrapper.
_Avoid_: statement, sub-query

**Sequence**:
Several items sent in one request and run in one transaction.
_Avoid_: batch, bulk

**Well-known query**:
A query defined in a file on disk and referenced by name, with declared parameters.
_Avoid_: stored query, saved query

**ComplexQuery**:
An item wrapper that adds data to write and flags (`returns`, `count`, `stats`, `sql`, `query_plan`, `rollback`) around a relation query.
_Avoid_: write query

**Read-back**:
The SELECT that returns a written item's resulting rows after the write.
_Avoid_: return query

**Prepared request**:
A request's items after resolving and compiling, before any database connection is taken.
_Avoid_: compiled request

## Serving

**Snapshot**:
Everything a running server serves from, built from one introspection of the database; a reload builds a new one and replaces the old as a whole.
_Avoid_: state, registry (a registry is only part of it)

## Identity

**User**:
Whoever makes a request: signed in (a verified session) or anonymous.
_Avoid_: caller, principal, client

**Session**:
The signed token (JWT cookie) a signed-in user presents; its claims include their role.
_Avoid_: login, token (when meaning the verified whole)

**Role**:
The Postgres role a request's transaction runs as: the session's role claim, or the anonymous role for an anonymous user.
_Avoid_: permission, group
