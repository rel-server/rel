
# HTTP content and security headers

Four concerns, covered together because they interlock once rel serves static files or renders HTML:

* **Static files** (`## Static files`) — serving a directory of files at a fixed URL prefix, an opt-in database-backed access-control mechanism for non-public subtrees (`### Access control`), and a way for a route function to choose where an upload lands on disk without routing its bytes through Postgres (`### Upload destinations`).
* **Templates** (`## Templates`) — `RelHttpResponse.template`, rendering dynamic HTML server-side via [Jet](github.com/CloudyKit/jet).
* **CORS** (`## CORS`, `Access-Control-*`) — which origins may make cross-origin requests to `/rel`/`/route`/`/static`.
* **CSP** (`## CSP`, `Content-Security-Policy`) — what a page rel returns is allowed to load/execute, including a per-request nonce for trusted inline `<script>`/`<style>`.

CORS and CSP apply to `/rel`, `/route`, and `/static` uniformly — no per-endpoint-family distinction.

> Why: a static subtree can serve a top-level `.html` navigation, not just subresources, so CSP is as meaningful there as for a Jet-rendered response. CORS matters less for `/static` (plain `<script src>`/`<link>` loading isn't subject to it) but a cross-origin `fetch()` of a static JSON file is, so the same policy applies rather than carving out an exception.

## Static files

`http.static.path` names a FILESYSTEM directory rel serves files FROM, not a URL prefix — the URL mount point is a fixed `/static/` (unconfigurable, same as `/rel`/`/route`).

* `http.static.path` (default `/static`) : a COLON-SEPARATED list of filesystem directories served at the fixed `/static/` URL prefix, same multi-directory convention `pg.query.wellknown_path` uses. A requested path is searched across every listed directory IN ORDER, first match wins. Directories that don't exist are silently skipped, not an error ; `/static/*` isn't mounted at all only when EVERY listed directory is missing. This check re-runs every time the inner mux is (re)built — at startup, and on every `SIGUSR1` reload (`specs/migrations.md ## Reloading` step 6) — so creating a listed directory and sending `SIGUSR1` is enough to start serving it without a full restart. `### Upload destinations` writes into the FIRST listed directory specifically.

  > Why: layering (built assets first, local overrides second) lets an override shadow the built version without merging into one tree on disk. Writing needs one unambiguous target, the same asymmetry any `PATH`-style search has.

Served via the standard library's `http.FileServer`, backed by a small `http.FileSystem` implementation that tries each listed directory's own `http.Dir` in order — never a hand-rolled path join. `http.Dir`'s own `Open` already normalizes/rejects `..` traversal at the request-path level for each directory tried ; this document only specifies the policy on top of it :

* **No directory listing.** A request for a directory with no `index.html` inside it is a `404`, never Go's default directory-listing page — unconditional, not configurable. A directory WITH an `index.html` serves it automatically, unchanged `http.FileServer` behavior.
* **Dotfiles are never served.** Any request whose path contains a segment starting with `.` (`.env`, `.git/config`, `.htpasswd`, ...) is a `404`, regardless of whether the file exists under `http.static.path`.

  > Why: `http.FileServer` has no opinion on dotfiles ; this closes the "someone dropped a `.env` next to static assets" footgun outright rather than trusting deployment hygiene.

By default, no authentication/role check applies to `/static/*` — these files are meant to be publicly reachable. `### Access control` covers the opt-in case for a subtree that isn't.

### Access control

Access control is configured as any number of NAMED, PREFIX-SCOPED rules, the same shape `blacklist.relations.<schema>.<name>` uses for "a set of named things, not an array" :

* `http.static.access.<name>.prefix` : the subpath prefix this rule gates (`"private/"`).
* `http.static.access.<name>.function` : unquoted, fully qualified name of a Postgres function called for any request whose path falls under that prefix.

`<name>` is an arbitrary operator-chosen label ; any number of rules can coexist, each scoped to its own prefix. A request under an unlisted prefix is served exactly as before.

> Why: a single flat check over the whole static tree would be too generic to reason about, and would run on every asset request including the common, genuinely-public ones.

Required signature, same interaction shape as `http.functions.check_session` (`specs/authentication.md ## Session invalidation`) : a configured function name, called with a `jsonb` payload, `RSxxx` to reject, returning normally to allow.

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

`payload` is `{"path": "<the request path, relative to http.static.path>", "jwt": JWT | null}`. A plain, unguarded `/static/*` request has no connection/transaction/JWT-verification plumbing around it at all (`## Static files` — no auth check applies). A request under a GATED prefix gains just enough of that machinery to run the one check : Verify (populating `payload->'jwt'`, `specs/authentication.md ## Lifecycle` step 2 — no DB needed), then a connection acquire for the single `check_static_access` call, then the actual file read. There is no `SET LOCAL ROLE`/Apply-role step here (unlike `check_session`) — a static file read is never itself a database operation needing a role, and `check_static_access` runs `security definer` so it doesn't need one either.

With anonymous access disabled (`specs/authentication.md ## Anonymous role existence`), an unauthenticated request to a GATED prefix gets the same uniform `401` every other unauthenticated request gets under that condition — BEFORE the existence check below. An UNGATED prefix is unaffected either way.

> Why: matches the existing "before route lookup, before any request body is read, before a pool connection is acquired" ordering `/rel`/`/route` already use. Stat-then-401 would leak whether a gated file exists to a caller who was never going to be let past the gate either way — the leak matters most in exactly this locked-down configuration.

**Existence checked first**, in the remaining ordinary case (anonymous access enabled, or the request is authenticated). A request for a path that doesn't exist under `http.static.path` is a plain `404` BEFORE any configured access-control function for that prefix runs.

> Why: cheap filesystem `stat`, no DB round trip wasted on a request that was never going to succeed either way.
>
> Caveat : if a file's mere EXISTENCE is sensitive (e.g. "does user 47 have a private report at all"), checking existence first leaks that to an unauthorized caller — a 404 for "doesn't exist" is indistinguishable from a 404 an access-control function might otherwise have returned for "exists, but you can't see it." Static files whose existence must stay private need either: not being under `http.static.path` at all (served indirectly via a route function), or accepting this tradeoff. Not solved further here.

### Upload destinations

The upload mechanism `specs/route.md ## Request bodies` already has (`files bytea[]`) always routes the actual bytes THROUGH Postgres, as a `bytea[]` argument. This section adds a second, narrower mechanism for a function that only wants to decide WHERE an upload lands on disk under `http.static.path`, without ever receiving its bytes : the database never receives the file, only makes a binding placement decision.

> Why: `bytea[]` is right when a function wants to inspect/store the bytes itself. It's wrong when a function only decides placement — routing bytes through Postgres regardless means paying for a round trip (encoding, connection, transaction) for data that's just written straight back out to a file.

This is a TWO-FUNCTION mechanism, both mandatory.

> Why: a single-invocation design has an unavoidable ordering problem — decide the destination before receiving bytes (real streaming, but the decision is made blind, before rel knows the upload will complete or is shaped correctly) or after (safe, but back to fully buffering first, the cost this mechanism exists to avoid). Splitting into two calls resolves this. Once split, the destination-deciding half can't be optional — nothing else in the mechanism runs before bytes are received, so skipping it leaves nothing to stream toward.

Both functions share one JSON domain, `RelUpload` (`jsonb`-underlying, `http.upload_domain_name`, default `RelUpload`, resolved via `specs/route.md ## Configuration`'s domain-name resolution rule, same as `RelHttpRequest`/`RelHttpResponse`) :

```ts
interface RelUpload {
  path?: string                      // relative to http.static.path's first directory ; omitted = discard the upload once received, don't keep it anywhere
  mkdir?: boolean                    // create path's parent directory if missing ("mkdir -p" semantics)
  overwrite?: 'allow' | 'disallow'   // default 'disallow' — see ### Upload destinations' ordering, step 3, for what each does
  part?: RequestPart                 // absent when returned by __prepare (bytes not read yet) ; filled in by rel before the mandatory function runs
  size?: number                      // same — rel-filled, post-stream, the ACTUAL observed byte count
}
```

`__prepare` only ever sets `path`/`mkdir`/`overwrite` ; rel is the only thing that ever sets `part`/`size`. One domain name, not two — `RelUpload` already isn't `RelHttpResponse` or a mimetype domain, which is all that's needed to keep `__prepare` out of generic route discovery.

* **`schema.<name>__prepare(req RelHttpRequest, part jsonb) returns RelUpload`** — MANDATORY. Runs BEFORE any bytes are received, from JUST the incoming upload's metadata (`part`, `null` if the request carries no upload — see below). Declared `stable` (or `immutable`) ; rel runs this call inside a real read-only transaction (`BEGIN READ ONLY`), so an attempted write inside it fails with a real Postgres error, not just a code-review-time convention. rel also warns non-fatally, same treatment `## Static files`'s `PUBLIC`-executable warning gets, if a discovered `__prepare` function is declared `volatile`. Its `path`/`mkdir`/`overwrite` decision is BINDING, not a hint — see the ordering below. It may reject outright (`RSxxx`), the earliest possible rejection point — a quota/content-type check here avoids spending time receiving bytes for an upload that was always going to be refused.
* **`schema.<name>(req RelHttpRequest, upload RelUpload) returns RelHttpResponse`** — MANDATORY, the base (unsuffixed) name. Runs AFTER the incoming bytes are fully, successfully written to a temporary location on disk — the ONLY function in this mechanism that writes to the database. `upload` is `__prepare`'s own returned value, with `part`/`size` filled in by rel ; `size` is the actual observed byte count of what's now on disk, which `__prepare` could not know and a client's declared `Content-Length` can't be trusted to state honestly. This function cannot change the destination — the bytes are already streamed to a temp location chosen by `__prepare`.

This family does not compose with `__VERB` (`specs/route.md`'s verb-suffix rule) : neither `<name>__prepare` nor the mandatory `<name>` is ever verb-suffixed ; a function named `upload__POST` is not discovered as a member of this family (it can still match a different shape under ordinary `__VERB` rules). `__prepare` is RESERVED and stripped off during route discovery BEFORE `__VERB` interpretation runs for any shape — a function literally named `receive_upload__prepare` does not become base name `receive_upload`, verb `PREPARE`.

Discovery requires BOTH functions present for the pair to be a route at all : a discovered `<name>__prepare` with no matching unsuffixed `<name>` sibling isn't a route, and a discovered `<name>(req, upload RelUpload) returns RelHttpResponse` with no matching `<name>__prepare` sibling isn't a route either. Both cases get the same non-fatal discovery-time warning as the existing ambiguous-route and `PUBLIC`-executable warnings.

**Signature matching.** Both are a NEW route-function shape family, alongside the four `## Request bodies` already defines, matched purely by TYPE SEQUENCE : `(req, part jsonb) returns RelUpload` and `(req, upload RelUpload) returns RelHttpResponse` are each unambiguous against every other recognized shape, including each other. Neither shape's return type is ever `RelHttpResponse` on `__prepare`'s side or a mimetype domain on either side, so this family never collides with a plain route or a binary/text-download route.

`part`/`upload.part` is a SINGLE value shaped like `## Request bodies`' existing `RequestPart` interface, never an array. This mechanism is scoped to ONE file per request, not a multi-file batch. A client uploading several files that each need their own destination decision makes several requests.

> Why: Go's multipart reader can't expose a later part's headers without first consuming every earlier part's body, so streaming several independently-destined files in one request needs either full buffering up front (defeats the point) or a manifest-part convention — more complexity than warranted for a first pass.

**A request carrying ANY sibling field alongside the file input is out of scope for this mechanism, unlike `files bytea[]`.** `## Request bodies` handles mixed plain-field-plus-file-part multipart submissions for that shape ; this one does not. A plain `<input type="text">` alongside a file input produces a second multipart part, caught and rejected before either function's own destination decision is treated as final (see the ordering below). Metadata that needs to travel alongside an upload through this mechanism goes in the URL's own query string (`req.query`, `specs/query-json.md`) instead of a sibling form field.

`part` is JSON `null` when the request carries no upload at all (no body, or a body that doesn't parse as either multipart or a single raw upload) — NOT a `415`, same "absence isn't a shape mismatch, only a WRONG shape is" rule `## Request bodies` uses for `files`. `__prepare` still runs in this case, with `part: null`, and may still reject via `RSxxx`. If it doesn't reject : whatever `path`/`mkdir`/`overwrite` it returned is never acted on — the ordering below's step 3 placement checks and steps 4/5/9 (streaming, the multipart second-part check, the final swap) are skipped entirely. The mandatory function still runs (steps 6-8), with `upload` equal to `__prepare`'s returned value plus `part: null`/`size: null`. `RelUpload` itself is never `null` — `__prepare` always returns a real value, even with `path` unset. A request that IS JSON/text/form-urlencoded (a real `body`-shaped request) is a `415`, checked BEFORE invoking either function, from the request's own `Content-Type` alone, same as every other shape's mismatch rule.

**Placement.** `path`, when set, is relative to `http.static.path`'s FIRST listed directory specifically, validated against the same traversal/escaping rules `## Static files` applies to serving (no `..`, no absolute path, nothing resolving outside that directory after cleaning ; a violation is a hard `500`, logged, nothing written, checked as soon as `__prepare` returns it — before any bytes are read). `path` omitted : the upload is a deliberate discard — bytes are still received (bounded by `http.max_body_size` same as any other request) so the mandatory function still gets an accurate `size`, but nothing is kept on disk once that function's transaction resolves ; not an error condition. `overwrite` (default `'disallow'`) governs what happens when a file already exists at `path` — see the ordering below for when it's enforced. Collision avoidance in the first place (a hash/uuid in the path) remains `__prepare`'s own responsibility ; `overwrite: 'allow'` is for a deliberate replace, not a substitute for generating non-colliding paths.

**Anonymous-route-authorization.** This is one route with two functions, not two independently reachable routes — `specs/route.md ## Anonymous route authorization`'s existence/EXECUTE check must pass for BOTH `<name>__prepare` and the mandatory `<name>` for the pair to be reachable anonymously at all ; fail-closed on either.

**Ordering.** Every other route shape resolves its entire request body before the route function is invoked. This mechanism resolves the body BEFORE the one function that writes to the database runs — the opposite ordering from `__prepare`, which runs before any bytes at all — and the file changes on disk only AFTER that function's transaction has committed :

1. Route lookup, Verify, anonymous-route-authorization checks — unchanged from `/route`'s existing ordering (`specs/authentication.md ## Lifecycle`'s request-handling-order paragraph).
2. Read ONLY the incoming upload's headers (the one multipart part's header, or — for a raw single POST — the request's own `Content-Type`/headers directly) : enough to build `part`, without touching the payload.
3. Connection acquire ; `check_session` (the first, and — per `specs/authentication.md`'s "called once per request" rule — the only transaction of the request, run as a plain statement BEFORE `BEGIN READ ONLY` opens, so a `check_session` implementation that itself writes is never constrained by `__prepare`'s own read-only transaction) ; renew if due ; `BEGIN READ ONLY` ; `SET LOCAL ROLE` to the session's role (or `pg.query.anonymous_role` if anonymous) ; invoke `<name>__prepare(req, part)` → `RelUpload` ; `COMMIT` ; connection released. A rejection here (`RSxxx`, from either `check_session` or `__prepare`) ends the request now, before a single byte of the actual upload is read.

   Immediately after, still before any bytes are read : if `path` is set and traverses outside `http.static.path`'s first directory after cleaning, that's a hard `500`, logged, request ends here. If `path` is set, `overwrite` is `'disallow'` (the default), and a file already exists there, reject now with `409 Conflict` — a fast-fail, not a guarantee (see step 9 for the race this doesn't fully close). If `mkdir` was requested, the parent directory is created now — a failure here (permissions, invalid path) is also a `500`, logged, before any bytes are read.
4. Stream the upload's bytes into a TEMP file whose NAME is always dot-prefixed (`.upload-<random>`) — in the SAME directory as the resolved `path` when one was set, otherwise (`path` omitted) into a generic staging location inside `http.static.path`'s first directory. `## Static files`'s dotfile-blocking rule already makes the temp file unservable. Bounded by the same `http.max_body_size`/`http.MaxBytesReader` every other body-reading path uses.

   > Why: same-directory placement makes the eventual move in step 9 a cheap same-filesystem rename, not a copy. The dot-prefix keeps a partially-written upload unservable mid-stream even though the directory is otherwise served.
5. For multipart specifically, rel then calls `NextPart()` ONE more time after the stream ends : finding a second part means the request didn't match the single-part shape this mechanism assumes — the temp file is deleted, and the request is rejected with `415` HERE, before the mandatory function is ever invoked and before any database write.
6. Connection acquire, `BEGIN`, `SET LOCAL ROLE` (reapplying the role step 3 already resolved — `check_session`/renew are NOT repeated here).
7. Invoke `<name>(req, upload)` — `upload` is step 3's `RelUpload` with `part`/`size` now filled in.
8. `COMMIT`, connection released immediately after — nothing in this mechanism holds a pool connection open while bytes are moving (steps 4-5 run entirely outside any transaction). If the mandatory function raises (no commit) : the temp file is deleted, nothing further happens.
9. ONLY on a successful commit, and ONLY if `path` was set : finalize the file swap. If a file already exists at `path`, rename it aside to a dot-prefixed backup name in the same directory ; rename the temp file into place at `path` ; on success, delete the backup (if one was made). If the swap itself fails partway (disk full, permissions, ...) : restore the backup into place if one was made, clean up the temp file, `500`, logged. The mandatory function's own database-side effects remain committed regardless of what happens in this step — file writes are not part of Postgres's own transaction and cannot be made atomic with it, same documented limitation as `specs/route.md`'s "Known limitation : no true streaming to Postgres." `overwrite: 'disallow'` is not re-checked here — if a file appeared at `path` between step 3's early check and this step, the swap still proceeds, last-write-wins, logged as a warning (refusing the swap here would leave the database referencing a file that was never written, worse than overwriting).

   ONLY IF `path` was omitted (the discard case) : the temp file is deleted here instead, nothing served — not an error.

   > A process crash at any point between step 4 and the end of step 9 can leave a dot-prefixed temp or backup file behind with nothing to clean it up — unservable, per the dotfile rule, so not a correctness problem, but real disk usage with no automatic sweep in this pass. Known gap, not solved here.

## Templates

`RelHttpResponse.template` already exists in `specs/route.md ## Responses`'s TypeScript shape (`template?: string`) but is parsed and never acted on (`specs/TODO.md`). This document gives it real behavior.

* `http.templates.path` (renamed from `http.templatesdir`, default kept at `/template`) : filesystem directory Jet templates are loaded from, via `jet.NewOSFileSystemLoader(http.templates.path)` + `jet.NewSet(loader)`.

`RelHttpResponse` also gains `template_data?: unknown`, a new field carrying the template's data context. `content` is unused/ignored whenever `template` is set — not an error to also set both, just pointless.

> Why: overloading `content`'s meaning based on whether `template` is set would be implicit coupling ; `content` already has its own established meaning (the literal response body when `template` is unset).

When a route function's `RelHttpResponse.template` is a non-empty string, rel treats it as a template PATH relative to `http.templates.path` (`"dashboard.jet"`, `"emails/welcome.jet"`) :

1. `set.GetTemplate(resp.template)` loads and caches the named template unconditionally. A template that fails to load (missing file, parse error) is a `500`, logged with the template path — never a silent fallback to raw `resp.content`.
2. The template EXECUTES with Jet's own positional `data` argument always `nil` ; everything a template needs is exposed through NAMED variables (Jet's `VarMap`, `Template.Execute(w, variables, data)`'s second argument), giving one access pattern throughout (`{{ Name }}`) :
   * `Data` — `resp.template_data` (JSON `null` if unset).
   * `Req` — the exact same `RelHttpRequest` JSON value the route function itself received, decoded into a plain Go map/slice tree, so nested access (`{{ Req.jwt.role }}`, `{{ Req.uri }}`) works the same way Jet handles any Go map/struct.
   * `Nonce` — `req.csp_nonce` (`## CSP ### Nonce`), also available as `Req.csp_nonce`.
3. The template's own rendered output becomes the response body, replacing whatever `resp.content` would otherwise have serialized to. `resp.content_type` is not implied/overridden by using `template` — a function returning HTML via a template still sets `content_type: "text/html"` itself, same as returning raw HTML directly.
4. A runtime error DURING execution (a template referencing a field `Data`/`Req` doesn't have) is also a `500`, same treatment as a load failure — never a partially-written body.

**Escaping.** Jet's default `SafeWriter` is `html/template`'s own `template.HTMLEscape` — every `{{ value }}` interpolation is HTML-escaped automatically unless the template explicitly pipes it through `| unsafe` (or `| raw`, an alias). This is a single, context-blind escaper, not `html/template`'s full context-aware auto-escaping. Concretely : `{{ Nonce }}` inside a `nonce="..."` attribute is fine. Interpolating a route function's own dynamic data INSIDE a `<script>` block is the case this gets wrong — HTML-escaping a value destined for JavaScript source doesn't produce valid/safe JS. The safe pattern for getting dynamic data into an inline, nonce'd script is a JSON island, not raw interpolation inside the script body :

```html
<script type="application/json" id="page-data">{{ json(Data) | raw }}</script>
<script nonce="{{ Nonce }}">
  const data = JSON.parse(document.getElementById('page-data').textContent);
</script>
```

`json(...)` is one of Jet's own built-in global functions (`encoding/json.Marshal` under the hood), already available in every template. `| raw` is required on top of it : `json`'s own output is already correctly escaped for a JSON-typed `<script>` body (Go's `encoding/json` escapes `<`, `>`, and `&` inside strings by default, so a `</script>`-shaped value can't break out of the block) ; piping it through the default `HTMLEscape` on top would mangle those escapes a second time.

Not :

```html
<script nonce="{{ Nonce }}">const data = {{ Data }};</script> {{/* wrong escaper for this context */}}
```

**Reload.** The Jet `Set` (and its template cache) is rebuilt as part of the same `SIGUSR1` reload sequence `specs/migrations.md ## Reloading` already specifies — a changed `.jet` file on disk takes effect the next time an operator reloads, same as a schema change. `jet.InDevelopmentMode()` (disables Jet's own template cache, reparsing from disk every render) is NOT used.

## CORS

rel's session is a cookie (`specs/authentication.md`), so cross-origin CORS here means cross-origin COOKIE-CARRYING requests specifically — the CORS spec forbids combining a wildcard origin (`Access-Control-Allow-Origin: *`) with `Access-Control-Allow-Credentials: true`.

### Configuration

* `http.cors.allowed_origins` (default empty) : comma-separated list of EXACT origins (`scheme://host[:port]`, no wildcards/patterns within an entry) allowed to make cross-origin requests, or the literal `*` (see below). Empty (the default) means CORS is fully closed : rel sends no `Access-Control-*` headers at all, and browsers enforce same-origin only.

  > Why: security-inclined default, same posture as `pg.query.anonymous_role`'s own existence check (`specs/authentication.md ## Anonymous role existence`) — nothing reachable from outside the deployment's own origin until explicitly configured.
* `http.cors.allowed_methods` (default `GET, POST, PUT, PATCH, DELETE, OPTIONS`) : methods a preflight may approve — matches the full set of verb suffixes a route function can declare (`specs/route.md`'s `__VERB` suffix rule), not just the two `/rel` itself accepts.
* `http.cors.allowed_headers` (default `Content-Type`) : request headers a preflight may approve, beyond the small set every browser always allows (`Accept`, `Accept-Language`, `Content-Language`, simple `Content-Type` values).
* `http.cors.max_age` (default `600`, seconds) : how long a browser may cache one preflight response before re-checking.

There is no separate `http.cors.allow_credentials` toggle. Whenever the request's `Origin` matches an entry in `allowed_origins` (the non-`*` case), rel always sends `Access-Control-Allow-Credentials: true` alongside it.

> Why: the only reason to allowlist an origin at all is to let an authenticated cross-origin frontend work — unauthenticated cross-origin reads need no CORS configuration to begin with. A browser still lets a cross-origin caller attempt an unauthenticated request either way ; CORS governs whether the caller's own JavaScript can read the response, not whether the request happens.

### `*` as an explicit value

Setting `http.cors.allowed_origins` to the literal `*` (the whole value, not a comma-list containing it) allows every origin, but credentials are NEVER sent in that mode : `Access-Control-Allow-Credentials` is omitted, and the JWT cookie is therefore useless to a cross-origin caller. rel does not reflect the literal request `Origin` back when `*` is configured.

> Why: `*` is meant for a genuinely public, read-only, anonymous-role-only API. `Access-Control-Allow-Origin: *` is simpler/cacheable and equivalent to reflection once credentials are off the table.

### Preflight handling

For any non-"simple" cross-origin request (a JSON body, a custom header, or a method outside `GET`/`HEAD`/`POST`+simple-content-type), the browser sends an `OPTIONS` preflight before the real request — answered entirely by rel itself, never by invoking a route function. rel answers a CORS preflight for any `/rel`, `/route/{schema}/{function}`, or `/static/*` path whether or not a route actually exists there yet : if the request's `Origin` is allowed (per the rules above), rel responds `204 No Content` with the `Access-Control-*` headers describing what's approved ; if not, rel responds with no `Access-Control-*` headers at all (still `204`, never an error status — the browser itself then blocks the real request).

rel distinguishes a preflight from an ordinary `OPTIONS` request the standard way — a real browser preflight always carries both an `Origin` header and an `Access-Control-Request-Method` header ; an `OPTIONS` request missing either one proceeds to normal route dispatch instead.

> Why this matters: `__VERB` suffixes are case-insensitive and unrestricted (`specs/route.md`), so a route function named `fn__options` is a legal route today — the preflight responder must not silently shadow it.

The actual (non-preflight) response also carries the same `Access-Control-Allow-Origin`/`-Allow-Credentials` headers when the request was cross-origin and allowed. When `allowed_origins` is a real allowlist (not the `*` case), every response carrying `Access-Control-Allow-Origin` also carries `Vary: Origin`.

> Why: browsers check the real response's own CORS headers independently of the preflight. The allowed origin varies per caller, so a shared/intermediate cache must not serve one origin's reflected value to a different origin.

## CSP

### Configuration

* `http.csp.default_src`, `http.csp.script_src`, `http.csp.style_src`, `http.csp.img_src`, `http.csp.font_src`, `http.csp.connect_src`, `http.csp.object_src`, `http.csp.frame_ancestors`, `http.csp.base_uri`, `http.csp.form_action` — one config key per CSP directive, each a literal space-separated source-list value exactly as it appears in the header (`"'self' https://cdn.example.com"`), set independently. Only `default_src` has a value by default (`'self'`) ; every other directive is unset by default and falls back to `default-src` per the CSP spec's own fallback rule.
* `http.csp.policy` (default empty) : a full, raw `Content-Security-Policy` header value, semicolon-separated directives exactly as the header itself is written. When set, this REPLACES every individual `http.csp.*` directive entirely — mutually exclusive as the source of directive values, not merged. Nonce injection (below) still applies to a raw `http.csp.policy` value : rel splits it on `;` and matches each segment's directive NAME as a whole token (`script-src`, not a substring match — `script-src-elem`/`script-src-attr` are distinct real CSP directives), appending the nonce source to `script-src`/`style-src` wherever found, leaving every other segment untouched.

CSP is ON by default (`default-src 'self'`) — a real `Content-Security-Policy` header is sent on every `/rel`/`/route`/`/static` response even with zero configuration, EXCEPT a CORS preflight response (`## CORS ### Preflight handling`), which is a bodyless `204` answered before any route function runs.

> Why `default-src 'self'` as the default: standard, widely-adopted baseline (helmet.js, Django's CSP middleware, Rails' default all ship the same starting point) ; does nothing to a pure JSON API, since `default-src` only constrains what a returned PAGE may itself load. A deployment already relying on inline scripts or external assets in HTML a route function returns will see them start being blocked on upgrade.

### Nonce

rel generates a fresh, cryptographically random nonce for EVERY request, before invoking the route function : `RelHttpRequest.csp_nonce` (a base64 string — `RelHttpRequest`'s authoritative TypeScript shape lives in `specs/route.md ## Request`, which gains this field as a companion edit). A route function returning hand-built HTML embeds it directly (illustrated here as plpgsql string-building ; `## Templates` covers the `{{ Nonce }}` spelling for Jet) :

```plpgsql
content := '<script nonce="' || req->>'csp_nonce' || '">/* trusted inline code */</script>';
```

rel appends `'nonce-<value>'` to BOTH the `script-src` and `style-src` directives of the CSP header for that response (whether assembled from the individual `http.csp.*` directives or from a raw `http.csp.policy`).

**Synthesis when the directive is absent.** Under the default config (`default-src 'self'` only, no explicit `script-src`/`style-src`), rel synthesizes `script-src`/`style-src` as a copy of the effective `default-src` value plus the nonce (`script-src 'self' 'nonce-<value>'`) before appending, WHENEVER `default-src` itself is present to synthesize from — never as the nonce alone.

> Why: CSP's fallback rule (an unset directive inherits `default-src`) stops applying the instant `script-src` exists at all. Injecting a bare `script-src 'nonce-x'` with no `default-src` copied in would silently block same-origin file scripts a `'self'` `default-src` was otherwise allowing.

If `default-src` is ALSO absent from the assembled directive set (a raw `http.csp.policy` or `resp.csp` override that omits it entirely), synthesis is skipped for whichever of `script-src`/`style-src` is still missing.

> Why: CSP's browser-level fallback for a directive that's entirely absent is "unrestricted." Injecting a nonce-only directive in that case would silently narrow it below what the base policy asked for.

A `script-src`/`style-src` that IS already present (empty or not) always gets the nonce appended regardless of `default-src` ; only the synthesize-a-new-directive case is gated on `default-src` existing.

The nonce is generated and available on `RelHttpRequest` regardless of whether the route's own response uses it.

### Per-response override

`RelHttpResponse`'s own authoritative TypeScript shape (`specs/route.md ## Responses`) gains an optional `csp` field as a companion edit (`string | undefined`, TypeScript-optional, not `| null` — there is no "explicitly disable CSP for this response" case, only "use the default" vs "use this instead"). When a route function sets it, this raw policy string REPLACES the process-wide default CSP header for that one response only — same semantics as `http.csp.policy` above (whole-token `script-src`/`style-src` matching, and the same synthesize-if-absent rule from `### Nonce`, scoped to that response's own effective `default-src`). A route with no reason to override anything simply never sets `resp.csp`, and gets the process-wide default.

> Why this is possible for CSP but not CORS: nothing about CSP is decided before the route function runs — there is no preflight-equivalent step reading a static, pre-computed policy.

### Why CORS has no equivalent override

A CORS preflight is answered before any route function ever executes (`## CORS ### Preflight handling`) — there is no response payload at that point to read a per-route CORS policy from, since the route function that would produce one is never invoked for a preflight. CORS policy is therefore static, process-wide, and resolved once per request from config alone.
