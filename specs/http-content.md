
# HTTP content and security headers

Implementation detail for static file serving (`## Static files`), Jet template rendering
(`## Templates`), CORS (`## CORS`), and CSP (`## CSP`) — user-facing behavior for all four is
in `docs/content/http/static-files.md`, `docs/content/http/templates.md`, and
`docs/content/http/cors-csp.md`.

> Why CORS/CSP apply uniformly to `/rel`, `/route`, and `/static` : a static subtree can
> serve a top-level `.html` navigation, not just subresources, so CSP is as meaningful there
> as for a Jet-rendered response. CORS matters less for `/static` (plain `<script
> src>`/`<link>` loading isn't subject to it) but a cross-origin `fetch()` of a static JSON
> file is, so the same policy applies rather than carving out an exception.

## Static files

`http.static.path` names a FILESYSTEM directory rel serves files FROM, not a URL prefix — the URL mount point is a fixed `/static/` (unconfigurable, same as `/rel`/`/route`).

* `http.static.path` (default `/static`) : a COLON-SEPARATED list of filesystem directories served at the fixed `/static/` URL prefix, same multi-directory convention `pg.query.wellknown_path` uses. `/static/*` isn't mounted at all only when EVERY listed directory is missing. This check re-runs every time the inner mux is (re)built — at startup, and on every `SIGUSR1` reload (`specs/migrations.md ## Reloading` step 6) — so creating a listed directory and sending `SIGUSR1` is enough to start serving it without a full restart. `### Upload destinations` writes into the FIRST listed directory specifically.

  > Why: layering (built assets first, local overrides second) lets an override shadow the built version without merging into one tree on disk. Writing needs one unambiguous target, the same asymmetry any `PATH`-style search has.

Served via the standard library's `http.FileServer`, backed by a small `http.FileSystem` implementation that tries each listed directory's own `http.Dir` in order — never a hand-rolled path join. `http.Dir`'s own `Open` already normalizes/rejects `..` traversal at the request-path level for each directory tried ; this document only specifies the policy on top of it :

* No directory listing, and dotfile blocking, are unconditional — behavior is in `docs/content/http/static-files.md`.

  > Why dotfiles are blocked here rather than left to deployment hygiene: `http.FileServer` has no opinion on dotfiles ; this closes the "someone dropped a `.env` next to static assets" footgun outright.

### Access control

Access control is configured as any number of NAMED, PREFIX-SCOPED rules, the same shape `blacklist.relations.<schema>.<name>` uses for "a set of named things, not an array" — keys and the required function signature are in `docs/content/http/static-files.md ## Restricting access to part of the tree`.

> Why: a single flat check over the whole static tree would be too generic to reason about, and would run on every asset request including the common, genuinely-public ones.

A plain, unguarded `/static/*` request has no connection/transaction/JWT-verification plumbing around it at all. A request under a GATED prefix gains just enough of that machinery to run the one check : Verify (populating `payload->'jwt'`, `specs/authentication.md ## Lifecycle` step 2 — no DB needed), then a connection acquire for the single `check_static_access` call, then the actual file read. There is no `SET LOCAL ROLE`/Apply-role step here (unlike `check_session`) — a static file read is never itself a database operation needing a role, and `check_static_access` runs `security definer` so it doesn't need one either.

With anonymous access disabled (`specs/authentication.md ## Anonymous role existence`), an unauthenticated request to a GATED prefix gets the same uniform `401` every other unauthenticated request gets under that condition — BEFORE the existence check below. An UNGATED prefix is unaffected either way.

> Why: matches the existing "before route lookup, before any request body is read, before a pool connection is acquired" ordering `/rel`/`/route` already use. Stat-then-401 would leak whether a gated file exists to a caller who was never going to be let past the gate either way — the leak matters most in exactly this locked-down configuration.

**Existence checked first**, in the remaining ordinary case (anonymous access enabled, or the request is authenticated).

> Why: cheap filesystem `stat`, no DB round trip wasted on a request that was never going to succeed either way.
>
> Caveat : if a file's mere EXISTENCE is sensitive (e.g. "does user 47 have a private report at all"), checking existence first leaks that to an unauthorized caller — a 404 for "doesn't exist" is indistinguishable from a 404 an access-control function might otherwise have returned for "exists, but you can't see it." Static files whose existence must stay private need either: not being under `http.static.path` at all (served indirectly via a route function), or accepting this tradeoff. Not solved further here.

### Upload destinations

The upload mechanism `specs/route.md ## Request bodies` already has (`files bytea[]`) always routes the actual bytes THROUGH Postgres, as a `bytea[]` argument. This section adds a second, narrower mechanism for a function that only wants to decide WHERE an upload lands on disk under `http.static.path`, without ever receiving its bytes : the database never receives the file, only makes a binding placement decision. The two-function shape, the `RelUpload` domain's fields, and the placement/discard/overwrite rules are documented in `docs/content/http/uploads.md`.

> Why: `bytea[]` is right when a function wants to inspect/store the bytes itself. It's wrong when a function only decides placement — routing bytes through Postgres regardless means paying for a round trip (encoding, connection, transaction) for data that's just written straight back out to a file.

> Why two functions, not one: a single-invocation design has an unavoidable ordering problem — decide the destination before receiving bytes (real streaming, but the decision is made blind, before rel knows the upload will complete or is shaped correctly) or after (safe, but back to fully buffering first, the cost this mechanism exists to avoid). Splitting into two calls resolves this. Once split, the destination-deciding half can't be optional — nothing else in the mechanism runs before bytes are received, so skipping it leaves nothing to stream toward.

`__prepare` only ever sets `path`/`mkdir`/`overwrite` ; rel is the only thing that ever sets `part`/`size`. One domain name, not two — `RelUpload` already isn't `RelHttpResponse` or a mimetype domain, which is all that's needed to keep `__prepare` out of generic route discovery.

`__prepare` is declared `stable` (or `immutable`) ; rel runs this call inside a real read-only transaction (`BEGIN READ ONLY`), so an attempted write inside it fails with a real Postgres error, not just a code-review-time convention. rel also warns non-fatally, same treatment `## Static files`'s `PUBLIC`-executable warning gets, if a discovered `__prepare` function is declared `volatile`.

This family does not compose with `__VERB` (`specs/route.md`'s verb-suffix rule) : neither `<name>__prepare` nor the mandatory `<name>` is ever verb-suffixed ; a function named `upload__POST` is not discovered as a member of this family (it can still match a different shape under ordinary `__VERB` rules). `__prepare` is RESERVED and stripped off during route discovery BEFORE `__VERB` interpretation runs for any shape — a function literally named `receive_upload__prepare` does not become base name `receive_upload`, verb `PREPARE`.

Discovery requires BOTH functions present for the pair to be a route at all : a discovered `<name>__prepare` with no matching unsuffixed `<name>` sibling isn't a route, and a discovered `<name>(req, upload RelUpload) returns RelHttpResponse` with no matching `<name>__prepare` sibling isn't a route either. Both cases get the same non-fatal discovery-time warning as the existing ambiguous-route and `PUBLIC`-executable warnings.

**Signature matching.** Both are a NEW route-function shape family, alongside the four route-signature shapes (`specs/route.md ## Request bodies`), matched purely by TYPE SEQUENCE : `(req, part jsonb) returns RelUpload` and `(req, upload RelUpload) returns RelHttpResponse` are each unambiguous against every other recognized shape, including each other. Neither shape's return type is ever `RelHttpResponse` on `__prepare`'s side or a mimetype domain on either side, so this family never collides with a plain route or a binary/text-download route.

> Why scoped to one file per request: Go's multipart reader can't expose a later part's headers without first consuming every earlier part's body, so streaming several independently-destined files in one request needs either full buffering up front (defeats the point) or a manifest-part convention — more complexity than warranted for a first pass.

**A request carrying ANY sibling field alongside the file input is out of scope for this mechanism, unlike `files bytea[]`.** A plain `<input type="text">` alongside a file input produces a second multipart part, caught and rejected before either function's own destination decision is treated as final (see the ordering below). Metadata that needs to travel alongside an upload through this mechanism goes in the URL's own query string (`req.query`, `specs/query-json.md`) instead of a sibling form field.

`part` is JSON `null` when the request carries no upload at all (no body, or a body that doesn't parse as either multipart or a single raw upload) — NOT a `415`, same "absence isn't a shape mismatch, only a WRONG shape is" rule the four-shape upload mechanism uses for `files`. `__prepare` still runs in this case, with `part: null`, and may still reject via `RSxxx`. If it doesn't reject : whatever `path`/`mkdir`/`overwrite` it returned is never acted on — the ordering below's step 3 placement checks and steps 4/5/9 (streaming, the multipart second-part check, the final swap) are skipped entirely. The mandatory function still runs (steps 6-8), with `upload` equal to `__prepare`'s returned value plus `part: null`/`size: null`. `RelUpload` itself is never `null` — `__prepare` always returns a real value, even with `path` unset. A request that IS JSON/text/form-urlencoded (a real `body`-shaped request) is a `415`, checked BEFORE invoking either function, from the request's own `Content-Type` alone, same as every other shape's mismatch rule.

**Placement.** `path`, when set, is validated against the same traversal/escaping rules `## Static files` applies to serving (no `..`, no absolute path, nothing resolving outside that directory after cleaning ; a violation is a hard `500`, logged, nothing written, checked as soon as `__prepare` returns it — before any bytes are read). Collision avoidance in the first place (a hash/uuid in the path) remains `__prepare`'s own responsibility ; `overwrite: 'allow'` is for a deliberate replace, not a substitute for generating non-colliding paths.

**Anonymous-route-authorization.** This is one route with two functions, not two independently reachable routes — `specs/route.md ## Anonymous route authorization`'s existence/EXECUTE check must pass for BOTH `<name>__prepare` and the mandatory `<name>` for the pair to be reachable anonymously at all ; fail-closed on either.

**Ordering.** Every other route shape resolves its entire request body before the route function is invoked. This mechanism resolves the body BEFORE the one function that writes to the database runs — the opposite ordering from `__prepare`, which runs before any bytes at all — and the file changes on disk only AFTER that function's transaction has committed :

1. Route lookup, Verify, anonymous-route-authorization checks — unchanged from `/route`'s existing ordering (`specs/authentication.md ## Lifecycle`'s request-handling-order paragraph).
2. Read ONLY the incoming upload's headers (the one multipart part's header, or — for a raw single POST — the request's own `Content-Type`/headers directly) : enough to build `part`, without touching the payload.
3. Connection acquire ; `check_session` (called once per request, `docs/content/http/authentication.md` — the first, and the only, transaction of the request, run as a plain statement BEFORE `BEGIN READ ONLY` opens, so a `check_session` implementation that itself writes is never constrained by `__prepare`'s own read-only transaction) ; renew if due ; `BEGIN READ ONLY` ; `SET LOCAL ROLE` to the session's role (or `pg.query.anonymous_role` if anonymous) ; invoke `<name>__prepare(req, part)` → `RelUpload` ; `COMMIT` ; connection released. A rejection here (`RSxxx`, from either `check_session` or `__prepare`) ends the request now, before a single byte of the actual upload is read.

   Immediately after, still before any bytes are read : if `path` is set and traverses outside `http.static.path`'s first directory after cleaning, that's a hard `500`, logged, request ends here. If `path` is set, `overwrite` is `'disallow'` (the default), and a file already exists there, reject now with `409 Conflict` — a fast-fail, not a guarantee (see step 9 for the race this doesn't fully close). If `mkdir` was requested, the parent directory is created now — a failure here (permissions, invalid path) is also a `500`, logged, before any bytes are read.
4. Stream the upload's bytes into a TEMP file whose NAME is always dot-prefixed (`.upload-<random>`) — in the SAME directory as the resolved `path` when one was set, otherwise (`path` omitted) into a generic staging location inside `http.static.path`'s first directory. `## Static files`'s dotfile-blocking rule already makes the temp file unservable. Bounded by the same `http.max_body_size`/`http.MaxBytesReader` every other body-reading path uses.

   > Why: same-directory placement makes the eventual move in step 9 a cheap same-filesystem rename, not a copy. The dot-prefix keeps a partially-written upload unservable mid-stream even though the directory is otherwise served.
5. For multipart specifically, rel then calls `NextPart()` ONE more time after the stream ends : finding a second part means the request didn't match the single-part shape this mechanism assumes — the temp file is deleted, and the request is rejected with `415` HERE, before the mandatory function is ever invoked and before any database write.
6. Connection acquire, `BEGIN`, `SET LOCAL ROLE` (reapplying the role step 3 already resolved — `check_session`/renew are NOT repeated here).
7. Invoke `<name>(req, upload)` — `upload` is step 3's `RelUpload` with `part`/`size` now filled in.
8. `COMMIT`, connection released immediately after — nothing in this mechanism holds a pool connection open while bytes are moving (steps 4-5 run entirely outside any transaction). If the mandatory function raises (no commit) : the temp file is deleted, nothing further happens.
9. ONLY on a successful commit, and ONLY if `path` was set : finalize the file swap. If a file already exists at `path`, rename it aside to a dot-prefixed backup name in the same directory ; rename the temp file into place at `path` ; on success, delete the backup (if one was made). If the swap itself fails partway (disk full, permissions, ...) : restore the backup into place if one was made, clean up the temp file, `500`, logged. The mandatory function's own database-side effects remain committed regardless of what happens in this step — file writes are not part of Postgres's own transaction and cannot be made atomic with it, same documented limitation as `specs/route.md ## Request bodies ### Known limitation : no true streaming to Postgres`. `overwrite: 'disallow'` is not re-checked here — if a file appeared at `path` between step 3's early check and this step, the swap still proceeds, last-write-wins, logged as a warning (refusing the swap here would leave the database referencing a file that was never written, worse than overwriting).

   ONLY IF `path` was omitted (the discard case) : the temp file is deleted here instead, nothing served — not an error.

   > A process crash at any point between step 4 and the end of step 9 can leave a dot-prefixed temp or backup file behind with nothing to clean it up — unservable, per the dotfile rule, so not a correctness problem, but real disk usage with no automatic sweep in this pass. Known gap, not solved here.

## Templates

`RelHttpResponse.template`'s user-facing behavior (`Data`/`Req`/`Nonce` variables, `extends`/`block`/`import`/`include`, the JSON-island escaping pattern, reload timing) is in `docs/content/http/templates.md`. Implementation only :

* `http.templates.path` (renamed from `http.templatesdir`, default kept at `/template`) : filesystem directory Jet templates are loaded from, via `jet.NewOSFileSystemLoader(http.templates.path)` + `jet.NewSet(loader)`.

> Why `content`/`template` don't overload one field: overloading `content`'s meaning based on whether `template` is set would be implicit coupling ; `content` already has its own established meaning (the literal response body when `template` is unset).

`set.GetTemplate(resp.template)` loads and caches the named template unconditionally. The template executes with Jet's own positional `data` argument always `nil` ; everything a template needs is exposed through NAMED variables (Jet's `VarMap`, `Template.Execute(w, variables, data)`'s second argument) — `Req` is the request JSON, decoded into a plain Go map/slice tree, so nested access (`{{ Req.jwt.role }}`) works the same way Jet handles any Go map/struct.

**Escaping.** Jet's default `SafeWriter` is `html/template`'s own `template.HTMLEscape` — a single, context-blind escaper, not `html/template`'s full context-aware auto-escaping (it does not know it's inside a `<script>` block versus an attribute versus text).

**Reload.** `jet.InDevelopmentMode()` (disables Jet's own template cache, reparsing from disk every render) is NOT used.

## CORS

### Configuration

Keys, defaults, and the `*` / preflight / credentials behavior are in `docs/content/http/cors-csp.md ## CORS`.

### Preflight handling

rel distinguishes a preflight from an ordinary `OPTIONS` request the standard way — a real browser preflight always carries both an `Origin` header and an `Access-Control-Request-Method` header ; an `OPTIONS` request missing either one proceeds to normal route dispatch instead.

> Why this matters: `__VERB` suffixes are case-insensitive and unrestricted (`specs/route.md`), so a route function named `fn__options` is a legal route today — the preflight responder must not silently shadow it.

rel answers a CORS preflight for any `/rel`, `/route/{schema}/{function}`, or `/static/*` path whether or not a route actually exists there yet : if the request's `Origin` is allowed, rel responds `204 No Content` with the `Access-Control-*` headers describing what's approved ; if not, rel responds with no `Access-Control-*` headers at all (still `204`, never an error status — the browser itself then blocks the real request).

The actual (non-preflight) response also carries the same `Access-Control-Allow-Origin`/`-Allow-Credentials` headers when the request was cross-origin and allowed. When `allowed_origins` is a real allowlist (not the `*` case), every response carrying `Access-Control-Allow-Origin` also carries `Vary: Origin`.

> Why: browsers check the real response's own CORS headers independently of the preflight. The allowed origin varies per caller, so a shared/intermediate cache must not serve one origin's reflected value to a different origin.

## CSP

### Configuration

Directive keys and `http.csp.policy` are in `docs/content/http/cors-csp.md ## CSP`. Nonce injection into a raw `http.csp.policy` value splits it on `;` and matches each segment's directive NAME as a whole token (`script-src`, not a substring match — `script-src-elem`/`script-src-attr` are distinct real CSP directives), appending the nonce source to `script-src`/`style-src` wherever found, leaving every other segment untouched.

A `Content-Security-Policy` header is sent on every response by default, EXCEPT a CORS preflight response (`## CORS ### Preflight handling`), which is a bodyless `204` answered before any route function runs.

> Why `default-src 'self'` as the default: standard, widely-adopted baseline (helmet.js, Django's CSP middleware, Rails' default all ship the same starting point) ; does nothing to a pure JSON API, since `default-src` only constrains what a returned PAGE may itself load. A deployment already relying on inline scripts or external assets in HTML a route function returns will see them start being blocked on upgrade.

### Nonce

**Scope : `/route` and `/auth` only, never `/rel` or `/static`.** `websec.Middleware` sets the
base CSP header (no nonce) uniformly across every path ; `websec.NonceMiddleware`, mounted only
around `/route/*` and the SSO mount (`boot.BuildMux`'s `chi.Group`, since `sso.Mount`'s own
route patterns are already absolute and can't be nested under a path-prefixed sub-router),
generates the nonce and overwrites that header again with it appended. `/rel` only ever answers
JSON and `/static` serves files as-is with no per-request templating to inject a nonce into —
neither can ever consume one, so neither gets `NonceMiddleware` in its chain, and neither pays
for the `crypto/rand` read. `docs/content/http/cors-csp.md ## CSP ### Nonce` covers the
user-facing version of this rule.

> Why `/auth` needs it too : `sso/claims.go`'s `invokeCallback` (the SSO callback function's
> response) goes through the exact same `route.WriteRelHttpResponse`/Jet-template path a
> `/route` function's response does, so a callback rendering an HTML landing page (a common
> OIDC/SAML popup-flow pattern — a small page that closes itself or `postMessage`s back to the
> opener) needs the nonce exactly the way a `/route` template does. The callback function itself
> never reads the nonce directly, though — it only ever receives `claims jsonb`, never
> `RelHttpRequest` (`oauth-saml.md ## Callback function`), so `{{ Nonce }}` inside its own Jet
> template is the only path it has to it ; the value still has to exist in context for that
> template render to work.

`InjectNonce` treats an EMPTY nonce as "no nonce facility active," not a degenerate nonce value
to inject — directives pass through completely untouched rather than gaining a hollow
`'nonce-'` token. This is what lets `websec.Middleware`'s own baseline call
(`Policy(cfg.Http.Csp, "", "")`) produce a genuinely nonce-less policy for `/rel`/`/static`,
rather than a fake one.

**Synthesis when the directive is absent.** Under the default config (`default-src 'self'` only, no explicit `script-src`/`style-src`), rel synthesizes `script-src`/`style-src` as a copy of the effective `default-src` value plus the nonce (`script-src 'self' 'nonce-<value>'`) before appending, WHENEVER `default-src` itself is present to synthesize from — never as the nonce alone.

> Why: CSP's fallback rule (an unset directive inherits `default-src`) stops applying the instant `script-src` exists at all. Injecting a bare `script-src 'nonce-x'` with no `default-src` copied in would silently block same-origin file scripts a `'self'` `default-src` was otherwise allowing.

If `default-src` is ALSO absent from the assembled directive set (a raw `http.csp.policy` or `resp.csp` override that omits it entirely), synthesis is skipped for whichever of `script-src`/`style-src` is still missing.

> Why: CSP's browser-level fallback for a directive that's entirely absent is "unrestricted." Injecting a nonce-only directive in that case would silently narrow it below what the base policy asked for.

A `script-src`/`style-src` that IS already present (empty or not) always gets the nonce appended regardless of `default-src` ; only the synthesize-a-new-directive case is gated on `default-src` existing.
