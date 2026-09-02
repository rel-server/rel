# Spec completeness TODO

What's still missing before the specs in this directory are complete, measured against
`index.md`'s feature list. Grouped by how much they block anything downstream, not by file.
Resolved items are not tracked here — this is a todo list, not a changelog ; consult
`git log -- specs/` for history.

## Named but empty / not yet started

- **`oauth-saml.md` — 0 bytes.** `authentication.md` names the libraries (`crewjam/saml`,
  `go-oidc`) and how a session gets minted once authenticated, but the actual `/auth/*`
  routes are unspecified : the SAML ACS endpoint and the OIDC callback specifically, since
  those protocols mandate fixed, redirect-driven callback URLs that can't be modeled as an
  ordinary `/route/{schema}/{function}` call. Username/password login is NOT part of this gap
  — it needs no special route, just an ordinary route function using the already-specified
  `RelHttpResponse.jwt` mint mechanism.

## Known gaps in implemented features

- `pgerr.Classify`'s `PG_*` table only recognizes 5 SQLSTATEs (unique/FK/not_null/check
  violation, permission_denied). Deliberately small per the spec's own "small fixed table"
  wording — expand only if a concrete need for another class's HTTP semantics shows up.
- **Well-known queries' write-side compile/execute split** (`well-known-queries.md
  ## Behaviour`) isn't built : a well-known write query's DML is recompiled per request, same
  as a plain `/rel` write already is, rather than reusing SQL text compiled once at load
  time. Functionally complete either way (including `$param` support) — this is purely the
  performance property `## Behaviour`'s own "prepared... ready to be queried for maximum
  performance" framing promises for reads but doesn't yet deliver for writes. Whatever design
  lands here has to account for a well-known write's `__node_id`s no longer necessarily
  starting at offset 0 — a well-known query can now sit anywhere inside a larger `Query[]`
  sequence (`well-known-queries.md ## Behaviour`), so baking `__node_id` in as a load-time
  literal constant, the way the compile-once story assumes, needs a per-request offset
  applied on top, not a fixed one.
- **`$param`'s declared `type` doesn't drive its usage sites' SQL cast** (`well-known-queries.md
  ## Definition`) : `type` governs the Go-side request validation, `cast` (per usage site,
  defaulting to `::jsonb`) governs the actual SQL — currently independent, no auto-fill from
  one to the other.

## Needs your attention

- **`typescript.md`** — actively being filled in by you directly, not blocked on anything
  else. One dangling sentence as of this writing : line 87, "This file provides a simple
  function that, given schema.json," with nothing after.
- **TypeScript/JS export vs. `index.md`.** `typescript.md` is the source of truth here ;
  `index.md`'s own `/js/query.js`-style mention is illustrative only, not authoritative.
  Once `typescript.md` settles on real paths, reconcile `index.md`'s Features list to match
  rather than leaving two different-sounding descriptions — a decision for whenever
  `typescript.md` itself is considered done, not before.
