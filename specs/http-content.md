
# HTTP content and security headers

Several mechanically distinct concerns, covered together in one document because they interlock in
practice — the moment rel can serve a static `app.js` or render dynamic HTML via a Jet template,
the security-header sections below stop being purely theoretical and start actually mattering :

* **Static files** (`## Static files`) — serving a directory of files (an `app.js`, a stylesheet,
  an image) at a fixed URL prefix, including an opt-in, database-backed access-control mechanism
  for subtrees that aren't meant to be fully public (`### Access control`), and a way for a route
  function to decide WHERE an upload lands on disk without ever receiving its bytes through
  Postgres (`### Upload destinations`).
* **Templates** (`## Templates`) — `RelHttpResponse.template`, rendering dynamic HTML server-side
  via [Jet](github.com/CloudyKit/jet) instead of a function hand-building an HTML string.
* **CORS** (`## CORS`, `Access-Control-*`) — which origins may make cross-origin requests to
  `/rel`/`/route`/`/static` at all.
* **CSP** (`## CSP`, `Content-Security-Policy`) — what a page rel returns is allowed to load/
  execute, including a per-request nonce for trusted inline `<script>`/`<style>` — the mechanism
  that makes a Jet-rendered `<script>` block usable without loosening CSP for anything else.

CORS and CSP apply to `/rel`, `/route`, AND `/static` uniformly — there is no per-endpoint-family
distinction. (Static files can legitimately include an `.html` document served as a top-level
navigation, not just subresources like `.js`/`.css` — CSP is meaningful there the same as for a
Jet-rendered response ; CORS matters less for `/static`, ordinary `<script src>`/`<link>`
resource loading isn't subject to it, but a cross-origin `fetch()` of a static JSON file is,
so the same policy applies for consistency rather than carving out an exception.)

## Static files

`http.static.path` already exists as a placeholder key (`specs/TODO.md`'s "Named but empty" —
config only, nothing served it yet). This document gives it a concrete meaning, fixing one
ambiguity in its own prior doc comment along the way : it names a FILESYSTEM directory rel serves
files FROM, not a URL prefix — the URL mount point is a fixed `/static/` (unconfigurable, same as
`/rel`/`/route` are also fixed, never dotted-config-driven paths). The prior "Path prefix for static
file serving" wording read as if `.path` meant the URL side ; it was always meant to mean the same
kind of thing `dmut.path`/`http.templates.path` (below) mean — a directory on disk — and is
corrected here rather than left to drift, per this session's own propagate-the-fix convention.

* `http.static.path` (default `/static`) : a COLON-SEPARATED list of filesystem directories
  served at the fixed `/static/` URL prefix — same convention `pg.query.wellknown_path` already
  uses for "search several directories for a named thing," reused rather than inventing a second
  one. A requested path is searched across every listed directory IN ORDER, first match wins
  (layering — e.g. a directory of built assets first, a directory of local overrides second, so an
  override shadows the built version without needing to be merged into the same tree on disk).
  Directories that don't exist are silently skipped in the search, not an error ; `/static/*` isn't
  mounted at ALL only when EVERY listed directory is missing — same "feature not in use, not a
  misconfiguration" treatment `specs/migrations.md ## Execution` already gives a missing `dmut.path`,
  extended here to "not a single one of them exists" rather than "the one directory doesn't exist."
  This check re-runs every time the inner mux is (re)built — at startup, AND on every `SIGUSR1`
  reload (`specs/migrations.md ## Reloading` step 6, which already rebuilds the whole mux from
  scratch) — so creating a listed directory and sending `SIGUSR1` is enough to make `/static` start
  being served (or start finding a path it couldn't before) without a full process restart, same as
  a schema change. `### Upload destinations` below writes into the FIRST listed directory
  specifically — searching many but writing to one canonical location is the same asymmetry a
  `PATH`-style search always has, and needs a single unambiguous answer for where a write goes.

Served via the standard library's `http.FileServer`, backed by a small `http.FileSystem`
implementation that tries each listed directory's own `http.Dir` in order (the layering above) —
never a hand-rolled path join for any individual directory. `http.Dir`'s own `Open` already
normalizes/rejects `..` traversal attempts at the request-path level before any filesystem call
happens, for each directory tried ; this document doesn't re-specify that mechanism, only the
policy on top of it :

* **No directory listing.** A request for a directory with no `index.html` inside it is a `404`,
  never Go's own default directory-listing page — an unconditional rel behavior, not configurable,
  matching this session's own "secure by default, minimal flags" pattern for new features (see
  `## CORS`/`## CSP` below, neither of which grew a bypass toggle either). A directory WITH an
  `index.html` serves it automatically, standard `http.FileServer` behavior, unchanged.
* **Dotfiles are never served.** Any request whose path contains a segment starting with `.`
  (`.env`, `.git/config`, `.htpasswd`, ...) is a `404`, regardless of whether such a file actually
  exists under `http.static.path` — `http.FileServer` itself has no opinion on dotfiles one way or
  the other, so this is rel's own added check, closing the classic "someone dropped a `.env` next
  to their static assets" footgun outright rather than trusting every deployment's own directory
  hygiene.
By default, no authentication/role check applies to `/static/*` — these are, by definition, files
meant to be publicly reachable (an `app.js` a browser needs to load before any session exists).
`### Access control` below covers the opt-in case for a subtree that isn't.

### Access control

Some static subtrees legitimately need gating — a `private/` directory of per-user files, say.
Rather than a single flat check applying to the entire static tree (too generic to reason about,
and would run on every single asset request including the common, genuinely-public ones), access
control is configured as any number of NAMED, PREFIX-SCOPED rules — the same shape
`blacklist.relations.<schema>.<name>` already uses for "a set of named things, not an array"
(config can't hold arrays at all, per its own stated rule) :

* `http.static.access.<name>.prefix` : the subpath prefix this rule gates (`"private/"`).
* `http.static.access.<name>.function` : unquoted, fully qualified name of a Postgres function
  called for any request whose path falls under that prefix.

`<name>` is an arbitrary operator-chosen label ; any number of rules can coexist, each scoped to
its own prefix — a request under an unlisted prefix is served exactly as before (a rule is opt-in
per-subtree, never a global gate everything routes through, however many rules exist).

Required signature, identical interaction shape to `http.functions.check_session`
(`specs/authentication.md ## Session invalidation`) — a configured function name, called with
a `jsonb` payload, `RSxxx` to reject, returning normally to allow — deliberately reusing that
convention rather than inventing a second one :

```sql
create function schema.check_static_access(payload jsonb) returns void
language plpgsql
security definer
as $$
begin
  if <this session (payload->'jwt') isn't allowed at payload->>'path'> then
    raise exception 'Not found' using errcode = 'RS404';
  end if;
end;
$$;
```

`payload` is `{"path": "<the request path, relative to http.static.path>", "jwt": JWT | null}`.
A plain, unguarded `/static/*` request has no connection/transaction/JWT-verification plumbing
around it at all today (`## Static files`' own opening paragraph — no auth check applies). A
request under a GATED prefix gains just enough of that machinery to run the one check : Verify
(populating `payload->'jwt'`, `specs/authentication.md ## Lifecycle` step 2 — no DB needed),
then a connection acquire for the single `check_static_access` call, then the actual file read.
There is no `SET LOCAL ROLE`/Apply-role step here at all (unlike `check_session`) — a static file
read is never itself a database operation needing a role, only the gate check is, and that check
runs `security definer` same as `check_session` does, precisely so it doesn't need one.

With anonymous access disabled (`specs/authentication.md ## Anonymous role existence` — the
configured anonymous role doesn't exist in the database), an unauthenticated request to a GATED
prefix gets the same uniform `401` every other unauthenticated request gets under that condition —
BEFORE the existence check below, not after : matching the existing "before route lookup, before
any request body is read, before a pool connection is acquired" ordering that rule already uses
for `/rel`/`/route`, and for the same reason the existence-first ordering below carries its own
caveat about — stat-then-401 would leak whether a gated file exists to a caller who was never going
to be let past the gate either way, in precisely the locked-down configuration where that leak
matters most. An UNGATED prefix is unaffected either way — it was never gated by role/session state
to begin with, anonymous-disabled or not.

**Existence checked first** (among the remaining, ordinary case — anonymous access enabled, or the
request is authenticated). A request for a path that doesn't exist under `http.static.path` at
all is a plain `404` BEFORE any configured access-control function for that prefix runs — cheap
filesystem `stat`, no DB round trip wasted on a request that was never going to succeed either
way. One caveat worth being deliberate about, not silently defaulting past : if a file's mere
EXISTENCE is itself sensitive (e.g. "does user 47 have a private report at all"), checking
existence first leaks that to an unauthorized caller (a 404 for "doesn't exist" is
indistinguishable from a 404 an access-control function might otherwise have chosen to return for
"exists, but you can't see it, and I don't want you to know it exists either") before the function
ever gets a say. Static files whose EXISTENCE is meant to stay private need one of : the file
genuinely not being under `http.static.path` at all (served indirectly, e.g. via a route function
that streams it from elsewhere), or accepting this ordering's tradeoff consciously. Not solved
further here — named as a real caveat of the existence-first design, not an oversight.

### Upload destinations

The upload mechanism `specs/route.md ## Request bodies` already has (`files
bytea[]`) always routes the actual bytes THROUGH Postgres, as a `bytea[]` argument. That's the
right shape when a function wants to inspect/store the bytes itself (`pg_largeobject`, a `bytea`
column, ...). It's the wrong shape when a function only wants to decide WHERE an upload should
land on disk under `http.static.path` and never needs the bytes at all — routing them through
Postgres regardless would mean paying for a round trip through the database (bytea encoding, the
connection, the transaction) for data that's just going to be written straight back out to a
file. This section adds a second, narrower upload mechanism for exactly that case : the database
never receives the file, only makes a binding decision about where it goes.

This is a TWO-FUNCTION mechanism, BOTH mandatory — not a single function, and not one-mandatory-
one-optional either. A single-invocation design has an unavoidable ordering problem : either decide
the destination BEFORE receiving bytes (genuine streaming, but the decision — and any DB work tied
to it — is made blind, before rel even knows whether the upload will actually complete or turn out
to be shaped wrong) or AFTER (safe, but back to fully buffering first, the exact cost this
mechanism exists to avoid). Splitting the decision into two calls resolves this rather than picking
one side of the tradeoff — and once split, the destination-deciding half can't be optional : nothing
else in this mechanism ever runs before bytes are received, so if it's skipped there is structurally
nothing to stream toward. Both functions share one JSON domain, `RelUpload` (`jsonb`-underlying,
`http.upload_domain_name`, default `RelUpload`, resolved via `specs/route.md
## Configuration`'s domain-name resolution rule, same as `RelHttpRequest`/`RelHttpResponse`) :

```ts
interface RelUpload {
  path?: string                      // relative to http.static.path's first directory ; omitted = discard the upload once received, don't keep it anywhere
  mkdir?: boolean                    // create path's parent directory if missing ("mkdir -p" semantics)
  overwrite?: 'allow' | 'disallow'   // default 'disallow' — see ### Upload destinations' ordering, step 3, for what each does
  part?: RequestPart                 // absent when returned by __prepare (bytes not read yet) ; filled in by rel before the mandatory function runs
  size?: number                      // same — rel-filled, post-stream, the ACTUAL observed byte count
}
```

`__prepare` only ever sets `path`/`mkdir`/`overwrite` (its job is a placement decision, made before
any bytes exist) ; rel is the only thing that ever sets `part`/`size` (nothing to report until the
bytes are actually on disk). One name, reused progressively rather than two similar-but-distinct
domains, since the only actual reason for a separate type would be keeping `__prepare` out of
generic route discovery — and that's already achieved by `RelUpload` simply not being
`RelHttpResponse` or a mimetype domain, no second name required.

* **`schema.<name>__prepare(req RelHttpRequest, part jsonb) returns RelUpload`** — MANDATORY.
  Runs BEFORE any bytes are received, from JUST the incoming upload's metadata (`part`, `null` if
  the request carries no upload at all — see below). Declared `stable` (or `immutable`) — and this
  isn't a naming convention rel merely checks and trusts : rel runs this call inside a REAL
  read-only transaction (`BEGIN READ ONLY`), so an attempted write inside it fails with an actual
  Postgres error immediately, not a code-review-time hope. (rel also warns, non-fatally, same
  treatment `## Static files`'s `PUBLIC`-executable warning already gets, if a discovered
  `__prepare` function is declared `volatile` — a free, introspection-time signal that something's
  probably wrong, on top of the enforcement that doesn't depend on it.) Its `path`/`mkdir`/
  `overwrite` decision is BINDING, not a hint — see the ordering below for exactly how rel acts on
  it. It may also reject outright (`RSxxx`), the earliest possible rejection point : a
  quota/content-type check here avoids ever spending time receiving bytes for an upload that was
  always going to be refused.
* **`schema.<name>(req RelHttpRequest, upload RelUpload) returns RelHttpResponse`** — MANDATORY,
  the base (unsuffixed) name. Runs AFTER the incoming bytes are fully, successfully written to a
  temporary location on disk — the ONLY function in this mechanism that ever writes to the
  database. `upload` is `__prepare`'s own returned value, with `part`/`size` now filled in by rel —
  `size` is the ACTUAL observed byte count of what's now safely on disk, something `__prepare`
  could never know and a client's declared `Content-Length` can't be trusted to state honestly.
  This function does NOT get to change the destination — by the time it runs, the bytes are already
  streamed to a temp location chosen from `__prepare`'s own decision ; its role is purely the
  database write, not a second placement opinion.

This whole family doesn't compose with `__VERB` (`specs/route.md`'s verb-suffix rule)
in this pass — neither `<name>__prepare` nor the mandatory `<name>` is ever verb-suffixed ; a
function named `upload__POST` is simply not discovered as a member of this family (it isn't
excluded from OTHER discovery either — if it happens to also match a different shape, e.g. a plain
`(req) returns RelHttpResponse`, ordinary `__VERB` rules apply to that unrelated match as usual).
`__prepare` itself is RESERVED and stripped off during route discovery BEFORE `__VERB`
interpretation ever runs for any shape — it is never itself treated as a verb suffix (a Postgres
function literally named `receive_upload__prepare` does not become base name `receive_upload`,
verb `PREPARE`).

Both halves are now mandatory, and discovery requires BOTH to be present for the pair to be a
route at all : a discovered `<name>__prepare` with no matching unsuffixed `<name>` sibling isn't a
route (nothing to invoke once bytes arrive), and — the reverse, now equally possible since the base
function alone used to be a complete route and no longer is — a discovered `<name>(req, upload
RelUpload) returns RelHttpResponse` with no matching `<name>__prepare` sibling isn't a route
either (nothing exists to make the placement decision before bytes arrive, and there is no `RelUpload`
value to fill in in the first place). Both cases get the same non-fatal discovery-time warning,
same treatment as the existing ambiguous-route and `PUBLIC`-executable warnings.

**Signature matching.** Both are a NEW route-function shape family, alongside the four `##
Request bodies` already defines, matched purely by TYPE SEQUENCE, no naming convention needed to
tell the families apart : `(req, part jsonb) returns RelUpload` and `(req, upload RelUpload)
returns RelHttpResponse` are each unambiguous against every other recognized shape, including each
other — one takes a bare `jsonb` second argument and returns the new domain, the other takes the
new domain itself as its second argument and returns `RelHttpResponse`. `__prepare` vs. the base
name is what distinguishes the two functions WITHIN this family, same suffix mechanism `__VERB`
already uses. Neither shape's return type is ever `RelHttpResponse` on `__prepare`'s side or a
mimetype domain on either side, so this family can never collide with a plain route or a
binary/text-download route — a mimetype domain's return value IS its raw response body, with
nowhere to carry a placement decision alongside it, and `RelUpload` itself is deliberately never
`RelHttpResponse` or a mimetype domain, so `__prepare` is excluded from plain route discovery
categorically, not by suffix-checking alone.

`part`/`upload.part` is a SINGLE value shaped like `## Request bodies`' existing `RequestPart`
interface (reused as-is, not a new shape) — never an array. This mechanism is deliberately scoped
to ONE file per request, not a multi-file batch : Go's own multipart reader can't expose a later
part's headers without first having consumed every earlier part's body, so genuinely streaming
several independently-destined files in one request isn't achievable without either buffering
everything up front (defeating the point) or a manifest-part convention (real, usable, but
meaningfully more complexity than this is worth for a first pass). A client uploading several files
that each need their own destination decision makes several requests — same "singular isn't a
special case" precedent `## Request bodies`' own single-raw-binary-POST-as-a-one-element-`files`-
array rule already established, resolved the other direction here (one request, one file, no array
at all) since there's no bytes-array to collapse into.

**This means a request carrying ANY sibling field alongside the file input is out of scope for
this mechanism, unlike `files bytea[]`.** `## Request bodies` explicitly handles mixed plain-field-
plus-file-part multipart submissions for THAT shape — this one, deliberately, does not : a plain
`<input type="text">` alongside a file input produces a second multipart part, which — per the
ordering below — is now caught and rejected BEFORE either function's own destination decision is
treated as final, but it's still rejected, not folded in as a second thing this mechanism also
describes. Metadata that needs to travel alongside an upload through THIS mechanism goes in the
URL's own query string (`req.query`, `specs/query-json.md`) instead of a sibling form field.

`part` is JSON `null` when the request carries no upload at all (no body, or a body that doesn't
parse as either multipart or a single raw upload) — NOT a `415`, same "absence isn't a shape
mismatch, only a WRONG shape is" rule `## Request bodies` already uses for `files`. `__prepare`
still runs in this case, with `part: null`, and may still reject via `RSxxx` (e.g. a route that
requires a file this time). If it doesn't reject : whatever `path`/`mkdir`/`overwrite` it returned
is simply never acted on — with no bytes coming, the ordering below's step 3 placement checks
(traversal validation, the `409` existence check, `mkdir`) and steps 4/5/9 (streaming, the
multipart second-part check, the final swap) are all skipped entirely, not executed-but-vacuous.
The mandatory function still runs (step 6-8), with `upload` equal to `__prepare`'s own returned
value plus `part: null`/`size: null`. `RelUpload` itself is never `null` — `__prepare` always
returns a real value, even when its `path` field is left unset. A request that IS JSON/text/form-
urlencoded (a real `body`-shaped request) is a `415` — a mismatch against a route that declared it
wants exactly one opaque upload ; checked BEFORE invoking either function, from the request's own
`Content-Type` alone, same as every other shape's mismatch rule.

**Placement.** `path`, when set, is relative to `http.static.path`'s FIRST listed directory
specifically (the one, unambiguous write target — see `## Static files`'s own config bullet
above), validated against the exact same traversal/escaping rules `## Static files` already
applies to serving (no `..`, no absolute path, nothing resolving outside that one directory after
cleaning ; a violation is a hard `500`, logged, nothing written, checked as soon as `__prepare`
returns it — before any bytes are read). `path` omitted : the upload is a deliberate discard —
bytes are still received (bounded by `http.max_body_size` same as any other request) so the
mandatory function still gets an accurate `size`, but nothing is ever kept on disk once that
function's transaction resolves ; not an error condition on its own. `overwrite` (default
`'disallow'`) governs what happens when a file already exists at `path` — see the ordering below
for exactly when and how it's enforced (once, early, as a fast-fail — not a hard guarantee against
a genuine last-moment race, called out explicitly there). Collision avoidance in the first place
(a hash/uuid in the path, say) remains `__prepare`'s own responsibility ; `overwrite: 'allow'` is
for the deliberate-replace case, not a substitute for generating non-colliding paths up front.

**Anonymous-route-authorization.** This is one route with two functions, not two independently
reachable routes — `specs/route.md ## Anonymous route authorization`'s
existence/EXECUTE check must pass for BOTH `<name>__prepare` and the mandatory `<name>` (both are
now always present — see above) for the pair to be reachable anonymously at all ; fail-closed on
either, don't cache reachability off the base name alone.

**Ordering — genuinely different from every other route shape, and the reason the multi-part
mismatch problem goes away entirely rather than only shrinking.** Every other shape resolves its
ENTIRE request body before the route function is ever invoked. This mechanism resolves the body
BEFORE the one function that actually writes anything to the database ever runs — the exact
opposite ordering from `__prepare` (which runs before any bytes at all) — and the file only ever
changes on disk AFTER that function's transaction has actually committed, never before :

1. Route lookup, Verify, anonymous-route-authorization checks — unchanged from `/route`'s existing
   ordering (`specs/authentication.md ## Lifecycle`'s request-handling-order paragraph).
2. Read ONLY the incoming upload's headers (the one multipart part's header, or — for a raw single
   POST — the request's own `Content-Type`/headers directly) : enough to build `part`, without
   touching a single byte of the actual payload.
3. Connection acquire ; `check_session` (this is the FIRST — and, per `specs/authentication.md`'s
   "called once per request" rule, the ONLY — transaction of the request this runs on, as a plain
   statement BEFORE `BEGIN READ ONLY` opens, specifically so a `check_session` implementation that
   itself writes — nothing today forbids one — is never constrained by `__prepare`'s own read-only
   transaction ; the two are deliberately not sharing one transaction, unlike `/route`'s existing
   single-transaction request handling) ; renew if due ; `BEGIN READ ONLY` ; `SET LOCAL ROLE` to the
   session's role (or `pg.query.anonymous_role` if anonymous) — `SET LOCAL` is scoped to the CURRENT
   transaction only, so this happens again in step 6 regardless, and a role switch is legal inside a
   read-only transaction ; invoke `<name>__prepare(req, part)` → `RelUpload` ; `COMMIT` (a read-only
   transaction commits trivially — nothing was written either way) ; connection released. A
   rejection here (`RSxxx`, from either `check_session` or `__prepare` itself) ends the request now,
   before a single byte of the actual upload is read.
   Immediately after, still before any bytes are read : if `path` is set and traverses outside
   `http.static.path`'s first directory after cleaning, that's a hard `500`, logged, request ends
   here. If `path` is set, `overwrite` is `'disallow'` (the default), and a file already exists
   there, reject now with `409 Conflict` — a deliberate fast-fail, not a guarantee (see step 9 for
   what happens if a file appears at `path` between this check and the eventual swap : a genuine
   concurrent-request race this one early check doesn't fully close, called out honestly rather than
   pretending `overwrite: 'disallow'` is airtight). If `mkdir` was requested, the parent directory is
   created now — a failure here (permissions, invalid path) is also a `500`, logged, before any
   bytes are read.
4. Stream the upload's bytes into a TEMP file whose NAME is always dot-prefixed (`.upload-<random>`)
   regardless of which directory it lands in — in the SAME directory as the resolved `path` when one
   was set (so the eventual move in step 9 is a cheap same-filesystem rename, not a copy ; the
   dot-prefix is what keeps a partially-written upload unservable mid-stream even though that
   directory is otherwise a served, non-staging location) ; otherwise (`path` omitted — the
   deliberate-discard case) into a generic staging location inside `http.static.path`'s first
   directory. Either way `## Static files`'s own dotfile-blocking rule already makes the temp file's
   own name unservable, no separate "don't serve this" mechanism needed. Bounded by the SAME
   `http.max_body_size`/`http.MaxBytesReader` wrapping every other body-reading path already uses.
5. For multipart specifically, rel then calls `NextPart()` ONE more time after the stream ends :
   finding a second part means the request didn't actually match the single-part shape this
   mechanism assumes — the temp file is deleted, and the request is rejected with `415` HERE,
   before the mandatory function is ever invoked and BEFORE any database write has happened at all.
   This is the actual improvement this two-function split buys over a single-invocation design : the
   mismatch is now caught in exactly the same "before invoke" position every other shape's mismatch
   rule already uses, not discovered after a commit.
6. Connection acquire, `BEGIN`, `SET LOCAL ROLE` (reapplying the same role step 3 already resolved —
   `check_session`/renew are NOT repeated here, they already ran exactly once, in step 3, which now
   unconditionally runs since `__prepare` is mandatory ; there is no longer a conditional branch
   here the way there would be if `__prepare` could be absent).
7. Invoke `<name>(req, upload)` — `upload` is step 3's `RelUpload` with `part`/`size` now filled in
   — the mandatory function's own database work happens here, with full knowledge that the bytes
   are genuinely, completely, correctly-shaped already on disk, at their still-temporary path.
8. `COMMIT`, connection released immediately after — same as every other route shape ; nothing in
   this mechanism holds a pool connection open while bytes are moving (steps 4-5, the only
   genuinely slow part, run entirely outside any transaction). If the mandatory function raises
   (no commit) : the temp file is deleted, nothing further happens — the public path was never
   touched, so there is nothing to undo there.
9. ONLY on a successful commit, and ONLY if `path` was set : finalize the file swap. If a file
   already exists at `path`, rename it aside to a dot-prefixed backup name in the same directory ;
   rename the temp file into place at `path` ; on success, delete the backup (if one was made). If
   the swap itself fails partway (disk full, permissions, ...) : restore the backup into place if
   one was made, clean up the temp file, `500`, logged. This is now the ONLY residual failure
   window in the whole mechanism, and it's a narrow one deliberately : by the time this step runs,
   rel has already proven it can write the (potentially large) temp file and — for the overwrite
   case — already renamed the old file aside, both of which need the same permissions and disk
   headroom the final rename does ; a failure specifically at this last, cheap, same-filesystem
   rename is a genuinely rare case (near-simultaneous disk exhaustion or a permissions change mid-
   request), not the routine path. The mandatory function's own database-side effects remain
   committed regardless of what happens in this step — file writes are not part of Postgres's own
   transaction and cannot be made atomic together with it, the same honestly-documented-limitation
   treatment `specs/route.md`'s own "Known limitation : no true streaming to Postgres"
   note already sets precedent for. `overwrite: 'disallow'` is not re-checked here — if a file
   appeared at `path` between step 3's early check and this step (the race step 3 flagged), the swap
   still proceeds, last-write-wins, logged as a warning ; the DB write already committed by this
   point, so refusing the swap here would leave the database referencing a file that was never
   actually written, a strictly worse outcome than overwriting.
   ONLY IF `path` was omitted (the discard case) : the temp file is deleted here instead, nothing
   served — not an error.

   A process crash at any point between step 4 and the end of step 9 (not just a filesystem-level
   failure inside step 9 itself) can leave a dot-prefixed temp or backup file behind with nothing
   left to clean it up — unservable either way, per the same dotfile rule, so not a correctness
   problem, but real disk usage with no automatic sweep in this pass. Documented as a known gap,
   same honesty-over-completeness treatment as the rest of this section, not solved here.

## Templates

`RelHttpResponse.template` already exists in `specs/route.md ## Responses`'
TypeScript shape (`template?: string`) but is parsed and never acted on (`specs/TODO.md`). This
document gives it real behavior.

* `http.templates.path` (renamed from the pre-existing, inconsistently-named `http.templatesdir`
  — never implemented, so renaming it now costs nothing ; matches this document's own `.path`
  convention for "a filesystem directory," same as `http.static.path` above and `dmut.path`,
  default kept at `/template`, unchanged) : filesystem directory Jet templates are loaded from,
  via `jet.NewOSFileSystemLoader(http.templates.path)` + `jet.NewSet(loader)`.

`RelHttpResponse` also gains `template_data?: unknown` — a new field, replacing what an earlier
draft of this section had `content` itself double as (the template's data context) : overloading
one field's meaning based on whether a SIBLING field is set is exactly the kind of implicit
coupling worth avoiding, and `content` already has its own, different, well-established meaning
(the literal response body when `template` is unset). `content` is simply unused/ignored whenever
`template` is set — not an error to also set both, just pointless.

When a route function's `RelHttpResponse.template` is a non-empty string, rel treats it as a
template PATH relative to `http.templates.path` (`"dashboard.jet"`, `"emails/welcome.jet"`) :

1. `set.GetTemplate(resp.template)` loads and caches the named template — unconditionally, not
   gated on any logging level (see `**Reload**` below for why : Jet's own development mode, which
   would disable this caching, is deliberately not used). A template that fails to load (missing
   file, parse error) is a `500`, logged with the template path — never a silent fallback to raw
   `resp.content`.
2. The template EXECUTES with Jet's own positional `data` argument always `nil` — rel doesn't use
   it at all — and instead exposes everything a template needs through NAMED variables (Jet's
   `VarMap`, `Template.Execute(w, variables, data)`'s second argument), so there's exactly ONE
   access pattern throughout (`{{ Name }}`) rather than mixing Jet's dot-context convention for one
   value and named variables for another :
   * `Data` — `resp.template_data` (JSON `null` if unset), the general-purpose "whatever this
     template needs to render" value, standing in for what `content` would otherwise have
     serialized to directly.
   * `Req` — the exact same `RelHttpRequest` JSON value the route function itself received (decoded
     into a plain Go map/slice tree so ordinary nested access — `{{ Req.jwt.role }}`, `{{ Req.uri
     }}` — works the same way Jet already handles any Go map/struct) : a template building a
     canonical URL, checking the caller's role, or reading a cookie doesn't need the route function
     to manually re-thread any of that through `template_data` first.
   * `Nonce` — `req.csp_nonce` (`## CSP ### Nonce` below), kept as its own short variable — same
     value `Req.csp_nonce` already carries, but the single highest-frequency lookup this whole
     nonce mechanism exists for is worth a dedicated short name (`{{ Nonce }}` vs `{{ Req.csp_nonce
     }}`) rather than making every template spell out the full path.
3. The template's OWN rendered output becomes the actual response body, replacing whatever
   `resp.content` would otherwise have serialized to. `resp.content_type` is not implied/
   overridden by using `template` — a function returning HTML via a template still sets
   `content_type: "text/html"` itself, same as it would returning raw HTML directly ; rel doesn't
   guess a content type from the fact that `template` was used, matching the existing
   "conforming to web semantics is the function author's responsibility, rel does not enforce it"
   precedent already established for `__VERB` suffixes.
4. A runtime error DURING execution (a template referencing a field `Data`/`Req` doesn't have,
   say) is also a `500`, same treatment as a load failure — never a partially-written body.

**Escaping.** Jet's default `SafeWriter` is `html/template`'s own `template.HTMLEscape` — every
`{{ value }}` interpolation is HTML-escaped automatically UNLESS the template explicitly pipes it
through `| unsafe` (or `| raw`, an alias). This is a SINGLE, context-blind escaper, not `html/
template`'s full context-aware auto-escaping (which knows it's inside an attribute vs. a `<script>`
block vs. plain text and escapes differently for each) — Jet trades that away for its own template
syntax. Concretely : `{{ Nonce }}` inside a `nonce="..."` attribute is fine (HTML-attribute
escaping is what's needed there, and that's what HTMLEscape does). Interpolating a route function's
OWN dynamic data INSIDE a `<script>` block is the case this gets wrong — HTML-escaping a value
destined for JavaScript source doesn't produce valid/safe JS, and the `<script>` block being
nonce'd only proves the block itself wasn't attacker-injected, not that everything interpolated
inside it is safe to interpolate that way. The safe pattern for getting dynamic data into an
inline, nonce'd script is a JSON island, not raw interpolation inside the script body :

```html
<script type="application/json" id="page-data">{{ json(Data) | raw }}</script>
<script nonce="{{ Nonce }}">
  const data = JSON.parse(document.getElementById('page-data').textContent);
</script>
```

`json(...)` is one of Jet's own built-in global functions (`encoding/json.Marshal` under the hood)
— not something rel registers, already available in every template. `| raw` is required on top of
it : `json`'s OWN output is already the exactly-correct escaping for a JSON-typed `<script>` body
(Go's `encoding/json` escapes `<`, `>`, and `&` inside strings BY DEFAULT specifically so a
`</script>`-shaped value can never break out of the block it's embedded in) — piping it through the
DEFAULT `HTMLEscape` on top would mangle those already-correct escapes a second time, so `raw`
(Jet's "skip the escaper for this one value" pipe, an alias for `unsafe`) is what makes this
correct, not a shortcut around safety.

Not :

```html
<script nonce="{{ Nonce }}">const data = {{ Data }};</script> {{/* wrong escaper for this context */}}
```

This document states the contract (default-escaped, single non-context-aware escaper, the
JSON-island pattern for script-embedded data) ; it isn't a Jet tutorial and doesn't re-derive
`html/template`'s own well-known escaping caveats beyond what's specific to rel's integration here.

**Reload.** The Jet `Set` (and its template cache) is rebuilt as part of the SAME `SIGUSR1` reload
sequence `specs/migrations.md ## Reloading` already specifies — no separate template-watching
mechanism ; a changed `.jet` file on disk takes effect the next time an operator reloads for any
reason, same as a schema change does. `jet.InDevelopmentMode()` (disables Jet's own template
cache, reparsing from disk every render) is NOT used — reload already exists as the one mechanism
for "pick up a change without a full restart," and a second, independent one (per-render disk
reads) would just be two ways to do the same thing.

## CORS

rel's session is a cookie (`specs/authentication.md`), so cross-origin CORS here means
cross-origin COOKIE-CARRYING requests specifically — the CORS spec forbids combining a wildcard
origin (`Access-Control-Allow-Origin: *`) with `Access-Control-Allow-Credentials: true`, so
"allow any origin" and "allow authenticated cross-origin calls" are mutually exclusive by
construction, not a rel-specific restriction.

### Configuration

* `http.cors.allowed_origins` (default empty) : comma-separated list of EXACT origins
  (`scheme://host[:port]`, no wildcards/patterns within an entry) allowed to make cross-origin
  requests, or the literal `*` (see below). Empty (the default) means CORS is fully closed : rel
  sends no `Access-Control-*` headers at all, and browsers enforce same-origin only — no
  cross-origin frontend can call `/rel`/`/route`, authenticated or not. This is the security-inclined
  default : opt-in only, same posture as `pg.query.anonymous_role`'s own existence check
  (`specs/authentication.md ## Anonymous role existence`) — nothing is reachable from outside
  the deployment's own origin until explicitly configured otherwise.
* `http.cors.allowed_methods` (default `GET, POST, PUT, PATCH, DELETE, OPTIONS`) : methods a
  preflight may approve — matches the full set of verb suffixes a route function can declare
  (`specs/route.md`'s `__VERB` suffix rule), not just the two `/rel` itself accepts.
* `http.cors.allowed_headers` (default `Content-Type`) : request headers a preflight may approve,
  beyond the small set every browser always allows regardless (`Accept`, `Accept-Language`,
  `Content-Language`, and simple `Content-Type` values).
* `http.cors.max_age` (default `600`, seconds) : how long a browser may cache one preflight
  response before re-checking.

There is no separate `http.cors.allow_credentials` toggle. Whenever the request's `Origin` matches
an entry in `allowed_origins` (the non-`*` case), rel always sends
`Access-Control-Allow-Credentials: true` alongside it — there is no meaningful "cross-origin
allowed, but never send the session cookie" case for rel specifically : the only reason to
allowlist an origin at all is to let an authenticated cross-origin frontend work, since
unauthenticated cross-origin reads need no CORS configuration to begin with (a browser still lets
a cross-origin caller ATTEMPT an unauthenticated request either way — CORS governs whether the
CALLER'S OWN JAVASCRIPT can read the response, not whether the request happens at all — so an
anonymous integration that doesn't care about reading the response back in-browser was never
blocked by CORS regardless of this configuration).

### `*` as an explicit value

Setting `http.cors.allowed_origins` to the literal `*` (not a comma-list containing it, the
WHOLE value) allows every origin, but per the spec-mandated restriction above, credentials are
NEVER sent in that mode : `Access-Control-Allow-Credentials` is omitted, and the JWT cookie is
therefore useless to a cross-origin caller in this mode — this is meant for a genuinely public,
read-only, anonymous-role-only API, not a general "allow everything" escape hatch. rel does not
reflect the literal request `Origin` back when `*` is configured (unnecessary — the two are
equivalent without credentials, and `Access-Control-Allow-Origin: *` is simpler/cacheable).

### Preflight handling

For any non-"simple" cross-origin request (anything with a JSON body, a custom header, or a
method outside `GET`/`HEAD`/`POST`+simple-content-type), the browser sends an `OPTIONS` preflight
BEFORE the real request — answered entirely by rel itself, never by invoking a route function
(there is no request body / route logic to run for a preflight, and no result to read an
override from — this is *why* CORS has no per-response override the way CSP does, see below).
rel answers a CORS preflight for any `/rel`, `/route/{schema}/{function}`, or `/static/*` path
whether or not a
route actually exists there yet (so a preflight for a not-yet-deployed route function doesn't
depend on introspection state) : if the request's `Origin` is allowed (per the rules above), rel
responds `204 No Content` with the `Access-Control-*` headers describing what's approved ; if not,
rel responds with no `Access-Control-*` headers at all (still `204`, never an error status — the
browser itself is what then blocks the real request from being sent, rel doesn't need to reject
the preflight outright).

rel distinguishes a preflight from an ordinary `OPTIONS` request the standard way — a real browser
preflight always carries BOTH an `Origin` header and an `Access-Control-Request-Method` header ;
an `OPTIONS` request missing either one proceeds to normal route dispatch instead. This matters
because `__VERB` suffixes are case-insensitive and unrestricted (`specs/route.md`),
so a route function named `fn__options` is a perfectly legal, real route today — the preflight
responder must not silently shadow it.

The actual (non-preflight) response also carries the same `Access-Control-Allow-Origin`/
`-Allow-Credentials` headers when the request was cross-origin and allowed — browsers check the
real response's own CORS headers independently of the preflight, not just the preflight itself.
When `allowed_origins` is a real allowlist (the reflect-the-matching-origin case, not the `*`
case), every response carrying `Access-Control-Allow-Origin` also carries `Vary: Origin` — the
allowed origin varies per caller, so a shared/intermediate cache must not serve one origin's
reflected value to a different origin.

## CSP

### Configuration

* `http.csp.default_src`, `http.csp.script_src`, `http.csp.style_src`, `http.csp.img_src`,
  `http.csp.font_src`, `http.csp.connect_src`, `http.csp.object_src`, `http.csp.frame_ancestors`,
  `http.csp.base_uri`, `http.csp.form_action` — one config key per CSP directive, each a literal
  space-separated source-list value exactly as it appears in the header (`"'self' https://
  cdn.example.com"`), set independently. Only `default_src` has a value by default (`'self'`,
  the standard OWASP baseline — see below) ; every other directive is unset by default, meaning
  it simply doesn't appear in the assembled header and falls back to `default-src` per the CSP
  spec's own fallback rule (this is standard CSP behavior, not a rel invention).
* `http.csp.policy` (default empty) : a full, raw `Content-Security-Policy` header value,
  semicolon-separated directives exactly as the header itself is written. When set, this REPLACES
  every individual `http.csp.*` directive above entirely (mutually exclusive as the source of
  directive values, not merged — avoids "which one wins" ambiguity) — for anything the ten
  named directives above don't cover (`worker-src`, `manifest-src`, `report-to`, ...). Nonce
  injection (below) still applies to a raw `http.csp.policy` value : rel splits it on `;` and
  matches each segment's directive NAME as a whole token (`script-src`, not a substring match —
  `script-src-elem`/`script-src-attr` are distinct real CSP directives and must not be matched by
  a bare `script-src` search), appending the nonce source to `script-src`/`style-src` wherever
  found, leaving every other segment untouched.

CSP is ON by default (`default-src 'self'`) — a real `Content-Security-Policy` header is sent on
every `/rel`/`/route`/`/static` response even with zero configuration, EXCEPT a CORS preflight
response (`## CORS ### Preflight handling`) : a preflight is a bodyless `204`, answered before any
route function runs, with no `RelHttpResponse` to read a per-response `csp` override from either
way, so there is nothing for a CSP header to usefully govern there. This is the one place this document
picks a default that can change existing behavior on upgrade rather than only ever being
stricter-by-omission : a deployment already relying on inline scripts or external assets in HTML
a route function returns will see them start being blocked. `default-src 'self'` is nonetheless
the standard, widely-adopted baseline (helmet.js, Django's CSP middleware, Rails' default all
ship the same starting point) and does nothing at all to a pure JSON API — `default-src` only
constrains what a returned PAGE may itself load, so a `/route` route returning `RelHttpResponse`
JSON is unaffected either way ; the header is sent uniformly (defense in depth against any future
route that starts returning HTML), but only has teeth once one actually does.

### Nonce

rel generates a fresh, cryptographically random nonce for EVERY request (cheap — one `crypto/
rand` read — so this happens unconditionally, not only for routes that end up using it), before
invoking the route function : `RelHttpRequest.csp_nonce` (a base64 string — `RelHttpRequest`'s
authoritative TypeScript shape lives in `specs/route.md ## Request`, which gains this
field as a companion edit alongside this document ; `query.ts` is the unrelated query-language
grammar and does not describe HTTP request/response shapes at all). A route function returning
hand-built HTML embeds it directly — illustrated here as plpgsql string-building, not Jet syntax
(`## Templates` below covers the `{{ Nonce }}` spelling for a route using a Jet template instead
of hand-building its own HTML string ; the two are different mechanisms reaching for the same
underlying value) :

```plpgsql
content := '<script nonce="' || req->>'csp_nonce' || '">/* trusted inline code */</script>';
```

rel appends `'nonce-<value>'` to BOTH the `script-src` and `style-src` directives of the CSP
header it sends for that same response (whether assembled from the individual `http.csp.*`
directives or from a raw `http.csp.policy` — see above) — covering inline `<style>`/`style=`
alongside `<script>` costs nothing extra (same value, one more directive) and both are real,
if asymmetric, injection surfaces once a route is emitting raw HTML strings rather than using an
auto-escaping template engine. An attacker who manages to inject `<script>`/`<style>` content into
that HTML cannot guess the per-request nonce, so CSP blocks their injected content even though it
is syntactically inline — this is nonce-based CSP's entire purpose, standard practice, not a rel
invention.

**Synthesis when the directive is absent.** Under the DEFAULT config (`default-src 'self'` only,
no explicit `script-src`/`style-src`), there is no existing `script-src` directive to append the
nonce onto — and injecting a bare `script-src 'nonce-x'` in that case would be actively wrong :
CSP's own fallback rule (an unset directive inherits `default-src`) stops applying the instant
`script-src` exists at all, which would silently BLOCK same-origin file scripts a `'self'`
`default-src` was otherwise allowing. So nonce injection ensures `script-src`/`style-src`
exist in the assembled directive set before appending the nonce to them, WHENEVER `default-src`
itself is present to synthesize from — synthesizing each missing one as a copy of the effective
`default-src` value plus the nonce (`script-src 'self' 'nonce-<value>'` under the zero-config
default), never as the nonce alone. This is what makes `req.csp_nonce` actually usable out of the
box, with no `http.csp.*` configuration at all — the user-facing case this document opened with.

If `default-src` is ALSO absent from the assembled directive set (a raw `http.csp.policy` or
`resp.csp` override that omits it entirely), synthesis is skipped for whichever of
`script-src`/`style-src` is still missing — nonce injection simply does nothing for that
directive, rather than adding it as a nonce-only value. A nonce-only `script-src 'nonce-x'` with
no `default-src` behind it would be strictly MORE restrictive than the base policy asked for : CSP's
browser-level fallback for a directive that's entirely absent is "unrestricted," and injecting a
nonce-only directive in that case would silently narrow it. A `script-src`/`style-src` that IS
already present (empty or not) still always gets the nonce appended regardless of `default-src` ;
only the synthesize-a-new-directive case is gated on `default-src` existing.

The nonce is generated and available on `RelHttpRequest` regardless of whether the route's own
response actually uses it — an ordinary JSON-returning `/route` route simply never reads
`req.csp_nonce`, at no cost.

### Per-response override

`RelHttpResponse`'s own authoritative TypeScript shape (`specs/route.md
## Responses`) gains an optional `csp` field as a companion edit alongside this document
(`string | undefined`, TypeScript-optional, not `| null` — there is no "explicitly disable CSP
for this response" case, only "use the default" vs "use this instead") : when a route function
sets it, this raw policy string REPLACES the
process-wide default CSP header for that one response only — same semantics as `http.csp.policy`
above in every respect (whole-token `script-src`/`style-src` matching, and the same synthesize-
if-absent rule from ### Nonce above : a `resp.csp` with no `script-src` of its own still gets one
synthesized from ITS OWN effective `default-src`, not silently left nonce-less), just scoped to a
single response instead of the whole process. This is possible for CSP specifically
because, unlike CORS, nothing about CSP is decided before the route function runs — there is no
preflight-equivalent step reading a static, pre-computed policy. A route with no reason to
override anything simply never sets `resp.csp`, and gets the process-wide default.

### Why CORS has no equivalent override

A CORS preflight is answered BEFORE any route function ever executes (see ## CORS ### Preflight
handling above) — there is simply no response payload yet at that point to read a per-route CORS
policy from, since the route function that would produce one is never invoked for a preflight.
So CORS policy is necessarily static, process-wide, and resolved once per request from config
alone, never from a route function's own logic. This isn't an arbitrary asymmetry between the two
sections above ; it falls directly out of how each mechanism is actually specified and enforced
by browsers.
