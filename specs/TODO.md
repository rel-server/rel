# Spec completeness TODO

What's still missing before the specs in this directory are complete, measured against
`index.md`'s feature list. Grouped by how much they block anything downstream, not by file.
Resolved items are not tracked here — this is a todo list, not a changelog ; consult
`git log -- specs/` for history.

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
- `logging.md ## Request-scoped logging` is specified (request ID header : `X-Request-Id`,
  confirmed) but not implemented — no middleware reads/generates the header, derives the
  per-request child logger, or stores it on `context.Context` yet. `## Domain scoping`
  (the `"module"` attribute) IS implemented (`logging.For`) ; this is the other, still-open
  half of the same document.
