# Spec completeness TODO

What's still missing before the specs in this directory are complete, measured against
`index.md`'s feature list. Grouped by how much they block anything downstream, not by file.
Resolved items are not tracked here — this is a todo list, not a changelog ; consult
`git log -- specs/` for history.

## Blocking / foundational

- **Read-only `Sequence` transaction sharing.** Several *pure reads* in one `Sequence` (no
  write item at all) do not currently share a snapshot with each other — each read runs as
  its own autocommit statement. Only writes-vs-writes (and a write's own reread) get that
  guarantee today (`server/rel.go`). If cross-read snapshot consistency for a read-only
  `Sequence` turns out to matter, wrapping the whole request (not just its write phase) in
  one transaction is the fix — noted in `query-engine.md ## Transactions` but not yet
  decided either way.
- **Undocumented deployment prerequisite : `SET ROLE` requires role membership.**
  `SET ROLE`/`SET LOCAL ROLE` only succeeds if the connecting role is a MEMBER of the
  target role. Every local/testcontainer run so far connects as a superuser, which can
  `SET ROLE` to anything, masking this. In production the connecting role is
  `pg.query.user`, deliberately NOT a superuser — so it must be granted membership in
  `pg.query.anonymous_role` AND every role any JWT in the deployment may carry, for both
  `/rel` and `/rpc`, or every request 500s with "permission denied to set role" the moment
  it's deployed against a properly-locked-down connecting role. PostgREST documents the
  identical prerequisite for its own `authenticator`/`web_anon` pattern ; neither
  `query-engine.md` nor `authentication.md` states it yet for rel — needs to be written
  into one of them (`authentication.md ## Roles` is the more natural home).

## Named but empty

- **`oauth-saml.md` — 0 bytes.** `authentication.md` names the libraries (`crewjam/saml`,
  `go-oidc`) and how a session gets minted once authenticated, but the actual `/auth/*`
  routes are unspecified : the SAML ACS endpoint and the OIDC callback specifically, since
  those protocols mandate fixed, redirect-driven callback URLs that can't be modeled as an
  ordinary `/rpc/{schema}/{function}` call. Username/password login is NOT part of this gap
  — it needs no special route, just an ordinary route function using the already-specified
  `RelHttpResponse.jwt` mint mechanism.
- **TypeScript/JS export** (`/js/query.js`, `/js/schemas/*.ts`, or `/rel/...` — see the
  path disagreement below). Named as a feature in `index.md`. No spec on how types are
  generated from introspection + well-known queries, or what the runtime query-building
  helper actually does.
- **Well-known queries.** Named in `query-engine.md ## Configuration` (`$param`, prepared
  statements, exported to the TS client) but never given its own section : where they're
  defined/stored, `$param` casting rules, caching and versioning across schema reloads.

## Open questions already flagged, still unresolved

- `query-engine.md ## Errors` — `RelErrorResponse.error` is always the underlying Go
  error's own message text, not a stable, machine-readable code. No taxonomy exists yet.
- `error-handling.md ## Error Codes` — needs a decision on the `X-Rel-Errorcode` response
  header / `errorcode` JSON property an earlier draft specified : abandoned deliberately,
  or still-intended work that hasn't landed ? Neither exists in the code today. If the
  latter, it belongs back in the doc as an explicit "not yet implemented" item.
- `index.md` vs. `typescript.md` disagree on where the generated TS/JS client files live —
  `index.md` says `/js/query.js`/`/js/query.ts`/`/js/schemas/schema1.js` ; `typescript.md`
  says `/rel/query.js`/`/rel/query.ts`/`/rel/db.json`/`/rel/db/<schema>.json`. Neither path
  is implemented in code, so nothing today favors one over the other — needs a decision.
- `typescript.md` has two mid-sentence truncations (line 16, "...but also eventual
  libraries that would want to _" ; line 25, "...given schema.json," with nothing after) —
  needs the redactor's own original intent, can't be completed by inference.
- `query.ts`'s `arguments` (function-relation) writability note — "this *might* be a
  problem to leave it writable, but I can't think why." Never promoted to a tracked
  `> Question:`, still just sitting in a comment.
- `logging.md ## Domain scoping` — section header only, no content. Request-ID header
  name also marked TBD.

## Not started

- CLI / entrypoint structure — the `dmut` subcommand-vs-signal question (is `dmut` ever
  invoked as its own CLI subcommand, or purely `SIGUSR1`-driven as currently implemented ?)
  is still open, along with the remaining auth/`SET LOCAL ROLE` wiring detail (see the
  deployment-prerequisite item above).
- Testing conventions — `AGENTS.md` mandates testcontainers ; no documented fixture/schema
  convention. `pg/testdata/schema.sql` (flat movie/director) and `test/dmut`+`test/seed`
  (richer hotel/booking, dmut-based) are two ad hoc instances in active use, not a
  written-down pattern for when to reach for which.
