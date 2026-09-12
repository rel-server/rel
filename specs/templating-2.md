# More Templating

Templating is useful and should not limited to database function returns, but also be available from static.

On top of looking for an `index.html` file when hitting a directory, `index.html.jet` is looked for if the former didn't exist.

The plain-path fallback generalizes to any extension, not just `.html`. For a request naming a file with extension `.ext`, the lookup order is the reachable file itself, then `file.ext.jet`. For an extensionless request, the lookup order is the reachable file itself, then its `.html` form, then its `.html.jet` form. A reachable file always wins ; `.jet` is the last resort, never tried ahead of a file that already exists at the requested name.

A `.jet`-rendered response's content type is derived from `.ext`, the extension immediately before `.jet` (`file.svg.jet` renders as `image/svg+xml`, `file.json.jet` as `application/json`, `file.html.jet` as `text/html`), via the same extension → MIME mapping Go's `mime` package already provides for ordinary static files. If no mime is found, `text/plain` is used.

Nonce generation and CSP-with-nonce injection apply to every `.jet` render regardless of the resulting content type, not only to `text/html` renders.

> **Why:** a non-HTML content type doesn't get CSP for free by virtue of being non-HTML — `image/svg+xml` served directly still executes inline `<script>` when navigated to.

`Stat` (`static.go:77`, feeding masking's `request.static`) reports the `.jet` source file's own `Size`/`ModifiedAt` when a request resolves to a jet fallback, not anything about the eventual render.

> **Why:** the caller's own choice of `.jet` source file already tells them a request resolves to a render ; a route function relying on `request.static` for a jet-backed path gets that source file's stats, and documentation for `rel()`/jet in templates must say so.

When hitting a `.ext.jet` file in static, the jet template mechanic is run with `Req` and `Nonce` being available.

`Req` for a static jet render is a freshly-built `RelHttpRequest`, constructed with `buildRelHttpRequest` (`route/encode.go:133`) — the same encoding function `/route` uses, not the stashed-request-JSON path `decodeRequestForTemplate` reads for database-function-return templates.

`Stat`/`ServeFile` (`static.go:77`, `static.go:165`) — the masking route function's `request.static`/`static_file` path — render a `.jet` file they're pointed at the same as `Handler` does ; they never serve `.jet` source as plain bytes.

The static `.jet` lookup and the database-function-return template lookup share a single jet loader/`Set`. `rel()` is available to database-function-return templates too, not only static ones, resolved against that request's own role/claims — a capability those templates don't have today.

Sharing one `Set` needs a custom multi-root loader ; `jet.NewOSFileSystemLoader` has one root (`http.templates.path`) and static `.jet` files live under a different, multi-directory root (`http.static.path`'s search list). That loader keys its cache so a static `foo.html.jet` and a `http.templates.path`-rooted `foo.jet` never collide. A static `.jet` render inherits the same compile-once-until-SIGUSR1-reload caching as database-function-return templates — editing a `.jet` file under a static directory has no effect on what's served until an explicit reload.

Within a static `.jet` file, an `{{include}}`/`{{extends}}`/`{{import}}` name resolves the same way jet itself already resolves one : a bare name or one starting with `./` resolves relative to that file's own physical location on disk, not through the static search-list lookup ; a name starting with `/` resolves against `http.templates.path`, the same as it does today for database-function-return templates. The upload-dir jet exclusion applies to both resolution paths — a file-relative include and a `http.templates.path`-rooted include alike are refused if they resolve into `http.upload.dir`.

A `./`-relative include's resolved absolute path must fall under a configured static directory or `http.templates.path` — the same bounding `resolveUnderDir` (`route/upload_handler.go`) already applies to upload destination paths — with the `http.upload.dir` exclusion applied on top of that.

Nonce generation, CSP-with-nonce injection, and JWT verification for a static request all run only on the code path that resolves to a jet render — the static handler triggers them inline once it has decided jet execution applies, rather than mounting `NonceMiddleware`/`jwtpkg.Middleware` unconditionally on all of `/static`.

A jet-rendered response omits the `ETag`/`Last-Modified` headers a plain static-file response gets, and sends `Cache-Control: no-store` instead, since the render is dynamic and role-scoped and must never be cached across users or roles.

> **Why:** a validator-less 200 response is still cacheable by heuristic ; an explicit `no-store` is required, not just the absence of validators.

## query functions

A `rel(query any)` function is given to the templates, which accepts a shape that can be turned into json that performs a query in the database, buildable using `map()` and `slice()` already available to the jet language. The query is run with the role of the user.

`rel()` only executes reads : write modes (INSERT/UPSERT/UPDATE/DELETE_ONLY) are disabled for it, leaving regular read queries and well-known reads.

The role of the user for a `rel()` call is resolved the same way `/rel` resolves it — same JWT verification, same claims-to-role mapping.

Each `rel()` call opens its own short-lived, role-scoped transaction via the existing `beginRoleScopedTx`, independent of any other `rel()` call in the same render and independent of a database-function-return template's own route function transaction (which has already committed by the time its template renders).

> **Why:** `rel()` in templates is a convenience for static rendering, not a mechanism for coherent multi-statement reads — a request that needs a consistent view across several queries belongs in a database function instead. A per-call transaction matches that : it's simpler to implement (reusing `beginRoleScopedTx` as-is, no render-scoped transaction plumbing to add), and it doesn't hold a pool connection for the render's full duration on `/static`, the surface least able to afford that. The trade-off — two `rel()` calls in one render can see different snapshots — is accepted because `rel()` is meant to be called rarely per render in the first place, not because the inconsistency doesn't matter.

A failed `rel()` call fails template execution the same way any other execution error does, going through the existing buffered-render-then-500 path (`writeTemplateResponse`) rather than a partial response.

No timeout, row cap, or execution-step limit is added for `rel()` calls from templates or for jet template execution itself, beyond what `/rel`/`/route` already have (none).

> **Why:** `/static` is normally the highest-traffic, least-authenticated, most-crawled surface of the site ; an unbounded `rel()` call reachable from an ordinary anonymous page load is a standing DB amplification/DoS risk. Documentation for `rel()` in templates MUST carry a prominent warning about this.

A new `http.upload.dir` configuration option names a subpath of the static write directory (`WriteDir()`, the first entry of `http.static.path`) as the root for all uploads, rather than a separate directory outside static serving. Uploaded files remain servable as ordinary static content through `/static`. `http.upload.dir` defaults unset, which disables uploads entirely. `http.max_upload_size` moves to `http.upload.max_size`. No file under `http.upload.dir`, at any depth, is ever eligible for jet execution.

`http.upload.dir`'s jet exclusion applies to the one absolute path it resolves to (`WriteDir()` joined with `http.upload.dir`), not to every `http.static.path` search-list entry — an otherwise-configured static directory is trusted by default, only upload-writable territory isn't.

> **Why:** the exclusion is only as good as the loader's own path resolution — a `./`-relative include still has to be resolved to its absolute path and checked against `http.upload.dir` before it's allowed to load, exactly like a top-level request is.

Jet exclusion under `http.upload.dir` closes the template-execution risk only ; nothing forces a safe content-type or `Content-Disposition: attachment` on uploaded files, so an uploaded `.html`/`.svg` served as its own type from the first-party origin is a stored-XSS vector left to the caller to prevent (e.g. by the route function choosing upload paths/extensions accordingly).

