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

  > Thoughts: no rationale for the current split is written down anywhere — `server/rel.go`'s
  > own top-of-file comment frames the whole handler as "the first vertical slice, scope
  > deliberately limited," so this reads as an unmade decision, not a deliberate one with a
  > hidden reason. The one real argument FOR keeping it as-is : `## Response Shape` streams
  > large results manually rather than buffering, so the response-writing phase can run for
  > as long as the client takes to read it (a slow or throttled client, a big result). Wrapping
  > that whole phase in one transaction pins a live snapshot in Postgres for that entire
  > duration — on a busy table, a long-held snapshot blocks vacuum from reclaiming dead
  > tuples and can bloat it, and a slow client reading a big result becomes a way to hold that
  > snapshot open indefinitely, which the current no-shared-transaction design avoids for
  > free. That cost is orthogonal to whether snapshot consistency is wanted, though — it comes
  > from streaming taking wall-clock time, not from opening a transaction as such. A middle
  > ground : only wrap reads in one transaction when a `Sequence` has more than one plain-read
  > item (the single-query case, by far the common one, can't have cross-read inconsistency
  > with itself, so it loses nothing) — narrows the long-snapshot risk to the genuinely
  > multi-query case without giving up consistency where it's asked for. Worth deciding with
  > this tradeoff in view rather than in the abstract.

- **Undocumented deployment prerequisite : `SET ROLE` requires role membership.** Now
  documented — `authentication.md ## Deployment prerequisite : role membership`.

## Named but empty

- **`oauth-saml.md` — 0 bytes.** `authentication.md` names the libraries (`crewjam/saml`,
  `go-oidc`) and how a session gets minted once authenticated, but the actual `/auth/*`
  routes are unspecified : the SAML ACS endpoint and the OIDC callback specifically, since
  those protocols mandate fixed, redirect-driven callback URLs that can't be modeled as an
  ordinary `/rpc/{schema}/{function}` call. Username/password login is NOT part of this gap
  — it needs no special route, just an ordinary route function using the already-specified
  `RelHttpResponse.jwt` mint mechanism.
- **TypeScript/JS export.** `typescript.md` is the source of truth for this, actively being
  filled in — `index.md`'s own `/js/query.js`-style mention is illustrative only, not
  authoritative ; once `typescript.md` settles on real paths, reconcile `index.md`'s Features
  list to match rather than leaving two different-sounding descriptions.
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
- `typescript.md` has two mid-sentence truncations (line 16, "...but also eventual
  libraries that would want to _" ; line 25, "...given schema.json," with nothing after) —
  work in progress, being filled in directly.
- `query.ts`'s `arguments` (function-relation) writability note — "this *might* be a
  problem to leave it writable, but I can't think why."

  > Thoughts: there is a real "why," and it argues for making a function-rooted node's
  > underlying-table writability ALWAYS false, not configurable. A view can only be written
  > through if Postgres itself considers it auto-updatable, or it has an `INSTEAD OF` trigger
  > — either way, Postgres guarantees the view's own defining query (its `WHERE`, its
  > `security_barrier`, whatever filtering it does) is genuinely in the path of the write, or
  > the write is refused outright. A function has no equivalent mechanism : rel's Writing
  > Algorithm targets the underlying table by name directly (`insert into target_relation
  > ...`), never "through" the function that was used to read it, so any filtering the
  > function's own SQL body does (a `where owner_id = ...`, a soft-delete filter, anything)
  > is silently bypassed for writes. Concretely : `fn_directors()` defined as `select * from
  > director where public = true` only ever shows public directors on read — but if the
  > underlying `director` table is writable through that same node, a client can upsert a row
  > by `id` that the function itself would never have exposed to them, since the write never
  > consults the function's `where` at all. rel has no way to inspect a function's body at
  > introspection time to tell "this is a safe passthrough" from "this embeds real access
  > control," so it can't safely allow writes for some functions and not others either.
  > Recommendation : make every function-rooted (or function-embedded) node unconditionally
  > read-only, the same way a non-auto-updatable, trigger-less view already effectively is.
  > Checked, not just suspected : `query/shape.go`'s `identityIsWritable` has no `IsFunction()`
  > guard at all — it only checks `node.Relation.PrimaryKey`/`OnConflictColumns`, and a `SETOF
  > director` function's `Relation` IS the real `director` table with a real PK, so this gap
  > is already live in the current implementation today, not hypothetical. If you agree, this
  > needs both a spec rule (`query-engine.md ### Function-rooted nodes`) and a
  > `query/shape.go` guard — say the word and I'll implement both.

- `logging.md ## Domain scoping` — section header only, no content.

  > Thoughts: a request-ID header is a good, standard idea, worth keeping — it's what makes
  > `logging.md ## Request-scoped logging`'s per-request child logger actually correlatable
  > end-to-end once there's more than one log line per request, and it costs nothing when
  > nobody sends one (rel just generates one). On the name : `X-Request-Id` is the pragmatic
  > default — widely recognized (Heroku, GitHub, most API gateways), trivial to generate/
  > propagate, and matches what `logging.md`'s own current TBD placeholder already suggests.
  > The other real option is the W3C Trace Context standard's `traceparent` header (part of
  > OpenTelemetry's propagation format) — heavier (a structured `version-traceid-spanid-
  > flags` value, not an opaque string) but the right choice if rel's logs are ever meant to
  > stitch together with a downstream service's own tracing rather than standing alone. Since
  > rel today is a single Postgres-backed API server with no other services to correlate
  > against, `X-Request-Id` is the simpler, sufficient choice now ; `traceparent` is the one
  > to revisit if/when multi-service tracing actually becomes a real need, not before.
  > `## Domain scoping` (module attribution — query / typescript / ...) is a separate, still
  > fully open gap, unrelated to the header name.

## Not started

- CLI / entrypoint structure. `dmut` stays signal-driven (`SIGUSR1`), not a subcommand —
  confirmed. But rel likely needs a real subcommand mechanism regardless, once TypeScript/JS
  generation exists (`typescript.md`) : generating the client files isn't naturally a
  runtime HTTP-serving behavior, and needs some invocation shape (`rel generate-ts ...` or
  similar) that doesn't exist yet. Worth deciding alongside `typescript.md` itself rather
  than in isolation, since the generation command's own inputs/outputs are exactly what that
  doc is settling.
- Testing conventions — `AGENTS.md` mandates testcontainers ; no documented fixture/schema
  convention. `pg/testdata/schema.sql` (flat movie/director) and `test/dmut`+`test/seed`
  (richer hotel/booking, dmut-based) are two ad hoc instances in active use, not a
  written-down pattern for when to reach for which.

  > Thoughts: these already serve genuinely different purposes, which is the actual axis to
  > weigh on, not "which one is better" — `pg/testdata/schema.sql` is cheap to spin up (a
  > plain SQL init script via testcontainers, no migration engine involved) and is where
  > most packages' fast, single-behavior regression tests live (a director/movie pair plus a
  > purpose-built fixture function per test) ; `test/dmut`+`test/seed` is slower to stand up
  > (a real dmut apply + a gofakeit seed pass) but is the only fixture that (a) exercises
  > dmut itself as a real integration point, (b) has genuine relationship depth/volume
  > (composite types, ranges, self-joins, FTS, hundreds of rows) for anything wanting
  > realistic query-engine stress or benchmark-grade data, per `query_bench/`'s own reason
  > for existing. Recommendation : don't collapse to one — write down the two-tier
  > convention explicitly (fast/flat fixture for ordinary unit-level regression tests ;
  > dmut+seed fixture for benchmarks, dmut integration coverage, or anything that
  > specifically wants realistic shape/volume) as the actual testing-conventions doc content,
  > rather than picking a winner. No existing spec file is a natural home for this — it'd
  > need a new one (`testing.md`?) or a section in `index.md`. Say the word if you want this
  > drafted now.
