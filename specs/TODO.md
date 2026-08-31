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

## Found during a sqlgen (query/*.go) implementation-completeness review

Every `query.ts` `Expression`/`UnaryOperator`/`BinaryOperator`/`FoldedOperator` tag has a
parser case (`expression_parse.go`), a resolver case (`expression_resolve.go`), and a codegen
case (`sql_expr.go`) — cross-checked tag by tag, nothing missing at that level. `order_by`'s
`desc` term silently sorting nulls FIRST instead of LAST (contradicting `query.ts`'s own "asc
and desc are nulls last by default") was a real bug, not just undocumented — fixed
(`compileOrderBy`), with a regression test ; Postgres's actual default (`NULLS FIRST` for a
bare `DESC`, confirmed directly against Postgres 16) was silently relied on instead of
overridden. Composite sub-field writes — flagged as fully unimplemented despite `##
Writability` documenting them in detail — are now implemented too (`write_dml.go`'s
`writeTargetPath`/`writeColumnCase`, `write_denormalize.go`'s `extractRowData`, both keyed
on the synthetic `columnPathFlatName`, `resolved_field.go`) ; see `## Writability`'s own
"Execution, not just derivation" paragraph for the mechanism. A table-rooted node's `select`
being required to be shape-producing — flagged as unclear whether that was a real gap or an
intended restriction — turned out to be a real gap too, confirmed by the user directly (the
same "distinct shape" a scalar function root already gets, `## Response Shape`, now also
available for an ordinary table root/embed) : implemented as `## Reading Algorithm ###
Scalar-selected nodes` describes (`wrapNodeAsValue`, `sql.go`), uniformly at the root, a
to-one embed, a to-many embed, and the LATERAL-shared case.

Lower-severity, self-aware in the code (tested, with a clear reason in an existing comment)
but not cross-referenced from any spec file — worth a one-line mention in `error-handling.md`
or `query-engine.md` for discoverability, not urgent :
- `$param` (well-known query params) has no codegen yet — already tracked above.
- A child alias reached as a bare VALUE nested inside another expression (e.g. `["coalesce",
  "director", null]`) resolves but doesn't compile — `TestCompileSelect_
  EmbeddedChildAliasStillUnsupported`. Selecting the same alias as a top-level select entry
  (an ordinary embed) is unaffected ; this is specifically the nested-as-an-operand case.

## Open questions already flagged, still unresolved

- `error-handling.md` — implemented (`errcode` package, `pgerr.Classify`/`Detail`,
  `config.Dev`, `/rel`'s JSON envelope and `/rpc`'s plain-text path both wired through the
  same tiering ; `server/rel.go`'s GET/POST-check-returns-405 fix landed too). Two real
  gaps remain :
  1. `## Rel-internal codes`' query-compile-error family (`UNKNOWN_IDENTIFIER`,
     `JOIN_MISSING_INDEX`, `WRITE_FORBIDDEN`, `WRITE_FORBIDDEN_FUNCTION_ROOT`,
     `QUERY_INVALID_EXPRESSION`) is NOT wired into the `query` package's own compile passes
     yet — `server/rel.go`'s `ResolveQuery`/`ResolveExpressions`/`DeriveShapes` call sites
     currently report every failure from those as `errcode.Unclassified` rather than the
     specific code, since the `query` package doesn't tag its own errors with a `Code` at
     the point each is raised. Requires threading `oc.Code(...)` through `query`'s own
     error-construction sites, a separate, larger pass.
  2. `pgerr.Classify`'s `PG_*` table only recognizes 5 SQLSTATEs (unique/FK/not_null/check
     violation, permission_denied). Deliberately small per the spec's own "small fixed
     table" wording — expand only if a concrete need for another class's HTTP semantics
     shows up.
- `typescript.md` has two mid-sentence truncations (line 16, "...but also eventual
  libraries that would want to _" ; line 25, "...given schema.json," with nothing after) —
  work in progress, being filled in directly.
- `logging.md ## Request-scoped logging` is specified (request ID header : `X-Request-Id`,
  confirmed) but not implemented — no middleware reads/generates the header, derives the
  per-request child logger, or stores it on `context.Context` yet. `## Domain scoping`
  (the `"module"` attribute) IS implemented (`logging.For`) ; this is the other, still-open
  half of the same document.
