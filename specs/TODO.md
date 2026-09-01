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
  ordinary `/rpc/{schema}/{function}` call. Username/password login is NOT part of this gap
  — it needs no special route, just an ordinary route function using the already-specified
  `RelHttpResponse.jwt` mint mechanism.
- **Well-known queries.** Named in `query-engine.md ## Configuration` (`$param`, prepared
  statements, exported to the TS client) but never given its own section : where they're
  defined/stored, `$param` casting rules, caching and versioning across schema reloads.
  Codegen for `$param` doesn't exist yet either (`sql_expr.go` errors outright), and `/rel`
  rejects `ParsedQuery.WellKnown` outright — both waiting on this design, not separate gaps.

## Known gaps in implemented features

- A child alias reached as a bare VALUE nested inside another expression (e.g. `["coalesce",
  "director", null]`) resolves but doesn't compile (`query/sql_expr.go`) — `TestCompileSelect_
  EmbeddedChildAliasStillUnsupported`. Selecting the same alias as a top-level select entry
  (an ordinary embed) is unaffected ; this is specifically the nested-as-an-operand case.
- `pgerr.Classify`'s `PG_*` table only recognizes 5 SQLSTATEs (unique/FK/not_null/check
  violation, permission_denied). Deliberately small per the spec's own "small fixed table"
  wording — expand only if a concrete need for another class's HTTP semantics shows up.

## Needs your attention

- **`typescript.md`** — actively being filled in by you directly, not blocked on anything
  else. One dangling sentence as of this writing : line 87, "This file provides a simple
  function that, given schema.json," with nothing after.
- **TypeScript/JS export vs. `index.md`.** `typescript.md` is the source of truth here ;
  `index.md`'s own `/js/query.js`-style mention is illustrative only, not authoritative.
  Once `typescript.md` settles on real paths, reconcile `index.md`'s Features list to match
  rather than leaving two different-sounding descriptions — a decision for whenever
  `typescript.md` itself is considered done, not before.
