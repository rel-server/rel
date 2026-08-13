# Legacy Websocket Subsystem

Source module: `sales-way.com/server` (legacy Go server at `/home/chris/Code/rel/_legacy`).
Files covered: `websocket.go`, `websocket-session.go`, plus wiring in `main.go`, `env.go`, `jwt.go`.

## 1. Purpose / role

The `main.go` header comment (`main.go:1-18`) lists websockets as one of the "stuff that would have to be
handled" items:

- "Websocket support for queries which allows for several in parallel (and works with a custom client)" (`main.go:13-14`)
- "Websocket NOTIFY and LISTEN with filters" (`main.go:15`)

**Reality check against the actual code:** only the second bullet is implemented, and only partially/differently
than "with filters" suggests. There is **no query-execution protocol** over the websocket at all — see §6.
What actually exists is a Postgres `LISTEN`/`NOTIFY` pub-sub relay plus a small set of DB-defined lifecycle hooks
(`onconnect`, `onsubscribe_<pattern>`, `onunsubscribe_<pattern>`, `ondisconnect` — though `ondisconnect` is
detected but never invoked, see §9). The websocket endpoint lets a client:

- Open one long-lived connection and subscribe/unsubscribe to named Postgres `NOTIFY` channels (`S`/`U` commands, `websocket-session.go:237-249`).
- Publish a `pg_notify` on a channel it is currently subscribed to (`N` command, `websocket-session.go:250-263`).
- Receive push messages whenever Postgres emits a `NOTIFY` on a channel the connection has subscribed to (`websocket.go:324-333`, `websocket-session.go:188-208`).
- Optionally run custom SQL-defined "hook" functions on connect/subscribe/unsubscribe, which can gate or react to those actions (`websocket.go:92-186`, `websocket-session.go:148-162`).

It does **not** let a client submit arbitrary REST-style queries over the socket the way the HTTP query endpoints
(`query`/`rel` packages, used by `pg2.go`/`pg3.go`) do. "Several [queries] in parallel... with a custom client" is
aspirational text that was never built out here.

## 2. Environment variables

### `SW_WEBSOCKETS_DISABLE`

Read at `main.go:171`:

```go
if getenvOrDefault("SW_WEBSOCKETS_DISABLE") != "" {
    session_should_log = getenvOrDefault("SW_WEBSOCKETS_LOG") != ""
    if err = setupWebsockets(&srv); err != nil { ... }
}
```

`getenvOrDefault` (`env.go:12-24`) is variadic: every argument except the *last* is treated as an environment
variable name to probe with `os.Getenv`; the last argument is the fallback **literal default value returned if
none of the env vars were set** — it is never itself passed to `os.Getenv`. With only one argument, `l == 1`, so
the loop's first iteration (`i == 0 == l-1`) immediately hits the "return the literal" branch and **`os.Getenv` is
never called at all**. Concretely: `getenvOrDefault("SW_WEBSOCKETS_DISABLE")` always returns the literal string
`"SW_WEBSOCKETS_DISABLE"` — a non-empty string — regardless of whether the real environment variable
`SW_WEBSOCKETS_DISABLE` is set, unset, empty, or `"false"`.

**Net effect: this check is always true.** `setupWebsockets(&srv)` runs unconditionally on every server start;
the env var has no observable effect on whether websockets are enabled. Despite its name implying "set this to
disable websockets," setting it (or not setting it, or setting it to any value) makes no difference — websockets
are always on. This looks like a straightforward bug in how `getenvOrDefault` was called (it needed a second,
empty-string default argument, e.g. `getenvOrDefault("SW_WEBSOCKETS_DISABLE", "")`, to actually consult the OS
environment). The same single-argument misuse pattern also appears at `plugins.go:13`
(`getenvOrDefault("SW_PLUGINS_DIR")`), so this is a systemic footgun with the helper, not a one-off typo.

### `SW_WEBSOCKETS_LOG`

Read at `main.go:172`, same helper, same bug: `getenvOrDefault("SW_WEBSOCKETS_LOG") != ""` is likewise always
true, so `session_should_log` (`websocket-session.go:164`) is always set to `true` whenever the (always-executed)
websocket setup block runs. In practice this means the per-message debug logging in `session.log(...)`
(`websocket-session.go:166-173`, emitting colored `☢`/`⇧`/`⇩` lines via `log.Println`) is **always active** in
the shipped binary, not opt-in as the name suggests.

**Flag for the rewrite:** neither env var currently does anything functional; both conditions always evaluate to
true because of the variadic-helper misuse. If the intent was "set `SW_WEBSOCKETS_DISABLE=1` to turn websockets
off," the code needs to be inverted (`== ""` check) and fixed to actually read the OS environment (pass a real
second/default argument, or call `os.Getenv` directly).

## 3. The websocket endpoint

- Route: `GET /ws`, registered on `srv.Router` inside `setupWebsockets` (`websocket.go:35`). No versioning/path
  parameters.
- Handler: `wsHandlerFn(&notif)` (`websocket-session.go:71-130`) returns a standard `http.HandlerFunc`.
- Upgrade: uses `github.com/gorilla/websocket` (`v1.5.0`, confirmed in `go.mod`) via a package-level
  `var upgrader = websocket.Upgrader{}` (`websocket.go:18`) — a zero-value `Upgrader`, so **no explicit
  `CheckOrigin`, no `ReadBufferSize`/`WriteBufferSize` tuning, no subprotocol negotiation**. Zero-value
  `Upgrader.CheckOrigin` falls back to gorilla's default same-origin check (compares `Origin` header host to
  request host), so cross-origin websocket connections are rejected unless `Origin` matches the host.
- Auth: **before** the HTTP-to-websocket upgrade even happens, the handler calls
  `jwtGetRoleFromRequest(r)` (`websocket-session.go:79-84`, delegating to `jwt.go:151-158`). This reads the same
  JWT cookie used by plain HTTP routes (cookie name from `JWT_COOKIE`, i.e. env `SW_JWT_COOKIE` /
  `PGRST_JWT_COOKIE`, default `"accesstoken"` — `env.go:37`). If the cookie is missing or invalid, the session
  role falls back to the anonymous role (`anon` package var, `websocket-session.go:65`, from
  `PGRST_DB_ANON_ROLE` default `"@unauthenticated"`) rather than rejecting the connection — i.e. **auth failure
  does not block the websocket handshake**, it just downgrades the session to the anonymous Postgres role. Actual
  authorization then happens implicitly through whatever the `ws.*` SQL functions (running `SET ROLE`-style as
  that role, presumably via the same Postgres role-switch mechanism used elsewhere in the server) allow.
- `session.doInit()` (`websocket-session.go:149-162`) runs *before* `upgrader.Upgrade` is called
  (`websocket-session.go:86-97`): if the DB has a `ws.onconnect` function, it's invoked with `(role, session_id)`
  and can return channel names (`supl_channels`, though notably **this returned value is discarded** — scanned
  into a local variable that's never used, `websocket-session.go:150-158`). If `doInit` errors, the connection is
  logged and the function returns **without ever upgrading** the HTTP connection (`websocket-session.go:86-90`) —
  the client just gets a failed/hanging HTTP response, not a websocket close frame.
- `session.Id` is set to the chi request-ID middleware value (`middleware.GetReqID(r.Context())`,
  `websocket-session.go:75`), not a client-supplied identifier.

## 4. Session lifecycle (`websocket-session.go`)

Per-connection state, `SwWebsocketSession` (`websocket-session.go:134-146`):

- `Id string` — chi request ID, used as a stable per-connection identifier for logging and passed into SQL hook
  functions.
- `Role string` — resolved once at handshake time (JWT role or anon), fixed for the connection's lifetime.
- `Conn *websocket.Conn` — the gorilla connection.
- `Notifier *SwWebsocketPostgresNotifier` — back-reference to the single shared notifier (one per server, not
  per connection).
- `Req *http.Request` — kept around only to source `session.context()` (`websocket-session.go:175-177`), i.e.
  every Postgres call for this session uses the *original HTTP request's context*, whose lifetime is tied to the
  handler goroutine, not to anything explicitly cancelled on disconnect.
- `sendLock sync.Mutex` — serializes writes to the socket (needed because both the read loop and the async PG
  notify dispatcher call `Send`).
- `Channels utils.Set[string]` — plain `map[string]struct{}` wrapper, **not internally synchronized**; it is only
  ever mutated (`Add`/`Remove`) while the notifier's `listeningLock` is held (`websocket.go:220`, `:251`), and
  only read without a lock during the deferred cleanup in the same read-loop goroutine
  (`websocket-session.go:101-106`) — so its safety depends on that external convention, not on the type itself.

**Multiplexing / correlation:** there is no true concurrent multiplexing. `JSONCommand.Id` (`websocket-session.go:23`)
is echoed back verbatim in the `JSONAnswer.Id` field of the ack (`JSONCommand.ok()`/`.error()`,
`websocket-session.go:29-47`) purely so the client can correlate a request with its response — but the server
processes exactly one inbound message at a time on a single goroutine per connection (the `for { conn.ReadMessage() }`
loop in `wsHandlerFn`, `websocket-session.go:109-128`); `handleMessage` runs synchronously and blocks that loop
(including any Postgres round-trip) until it returns and the ack/error is sent. So client "ids" support response
correlation but **not** true parallel in-flight requests on one socket — the "several in parallel" aspiration
from the header comment is not realized.

**Error surfacing:** every inbound `JSONCommand` gets exactly one `JSONAnswer` back, `{id, ok, message}}`
(`websocket-session.go:49-53`), via a `defer` in `handleMessage` (`websocket-session.go:224-230`) that inspects
whatever `error` var is set by the switch branch. There is no partial/streaming response and no distinct
error-message type — success and failure share `JSONAnswer`, distinguished by `Ok`.

**Disconnect:** when `conn.ReadMessage()` returns an error (client closed the socket, network drop, etc.), the
loop breaks (`websocket-session.go:110-114`), the deferred cleanup unsubscribes the session from every channel it
was still in (`websocket-session.go:101-106`, calling `notifier.unsubscribe` per channel), and `defer conn.Close()`
runs (`websocket-session.go:98`). There is **no `ondisconnect` invocation** despite the notifier detecting
`HasOnDisconnect` from the DB schema (`websocket.go:69-70`, `:113`) — the field is populated but never read
anywhere else in either file (confirmed by grep — no other reference to `HasOnDisconnect`). `SwWebsocketSession.Cleanup()`
(`websocket-session.go:272-274`) exists but is a no-op and is never called from anywhere.

**Reconnect:** there is no session persistence/resumption — a new connection creates a brand-new
`SwWebsocketSession` with a new chi request ID and starts from zero subscriptions; there is no concept of
resuming a previous session's channel list.

## 5. Message protocol

All frames are `websocket.TextMessage` JSON. Ping/Pong/Close are handled at the frame level, not as JSON
(`websocket-session.go:116-127`): a `PingMessage` gets an immediate `PongMessage` echo of the same payload;
`CloseMessage` just falls through to end the loop; anything else (binary, etc.) is silently ignored (no `default`
case).

### Client → server: `JSONCommand` (`websocket-session.go:22-27`)

```json
{ "id": 1, "command": "S", "channels": ["room:42"], "payload": "" }
```

- `id` (int) — client-chosen correlation id, echoed back in the ack.
- `command` (string) — one of `"S"` (subscribe), `"U"` (unsubscribe), `"N"` (notify/publish). Anything else →
  error `unknown command %s` (`websocket-session.go:264-266`).
- `channels` ([]string) — must be non-empty or the whole message errors with `channels are empty`
  (`websocket-session.go:232-235`); for `S`/`U` this is the list of channel names acted on; for `N` this is the
  list of channels to `pg_notify` (each must already be in `session.Channels`, i.e. the session must currently be
  subscribed to a channel to publish on it — `websocket-session.go:252-256`).
- `payload` (string) — free-form string sent as the `NOTIFY` payload for `N` commands (`websocket-session.go:257-262`).
  Not used for `S`/`U`.

### Server → client: two shapes

1. Ack/error for a processed command, `JSONAnswer` (`websocket-session.go:49-53`):
   ```json
   { "id": 1, "ok": true, "message": "Ok" }
   ```
2. Push notification when a subscribed Postgres channel fires, shape depends on whether the NOTIFY payload is
   valid JSON (`session.Notify`, `websocket-session.go:188-208`):
   - If payload is valid JSON: `JSONNotification` — `{"channel": "room:42", "payload": <raw json>}` where
     `payload` is embedded as raw JSON (`json.RawMessage`), not a string (`websocket-session.go:55-58`).
   - If payload is not valid JSON: `JSONNotificationString` — `{"channel": "room:42", "payload": "some text"}`,
     payload as a plain JSON string (`websocket-session.go:60-63`, `:190-198`).

  There is no explicit type discriminator field distinguishing "ack" from "push notification" messages — a client
  must distinguish by shape (`id`+`ok`+`message` vs. `channel`+`payload`).

## 6. Query execution over websocket

**Not implemented.** Neither `websocket.go` nor `websocket-session.go` imports the `query` or `rel` packages
(confirmed by grep — those packages are only imported from `pg2.go` and `pg3.go`, the plain-HTTP query/REST
routes). The only SQL the websocket subsystem executes is:

- The one-time schema introspection query against `information_schema.routines` in the `ws` schema
  (`websocket.go:105-123`) to discover `onconnect`/`onsubscribe_*`/`onunsubscribe_*`/`ondisconnect` functions.
- Direct calls to those discovered `ws.*` functions via `srv.PgSimpleQueryRow` / the pool (`websocket.go:203`,
  `websocket-session.go:152`).
- `pg_notify(...)` calls for control-channel bookkeeping and for relaying client `N` commands
  (`websocket.go:215`, `:244`, `websocket-session.go:257-262`).

There is no generic "run this arbitrary query/RPC and stream back rows" command in the `JSONCommand` switch — only
`S`/`U`/`N`. Any actual data querying a client wants to do over this transport would have to be implemented as a
custom `ws.onsubscribe_<pattern>`/`ws.onconnect` SQL function that itself does work as a side effect of
subscribing, since the client cannot ask the server to run a general query message.

## 7. LISTEN/NOTIFY support

**Implemented**, as a channel-based pub/sub relay, though not with per-message "filters" beyond channel name
matching:

- One dedicated Postgres connection is held for the server's entire lifetime specifically to run `LISTEN`
  (`notifier.listenPg`, `websocket.go:258-339`), acquired once from the pool and never released
  (`websocket.go:266-272`, comment: "This should only be released when the program ends").
- The notifier also listens on its own private **control channel** (`"__gs_ctrl_" + uniuri.New()`,
  `websocket.go:30`) used purely to tell that one dedicated connection when to `LISTEN "<channel>"` / `UNLISTEN "<channel>"`,
  triggered via `pg_notify` from *other* pool connections when the first subscriber joins / last subscriber leaves
  a channel (`websocket.go:213-217` for subscribe, `:243-246` for unsubscribe, dispatched at `:296-322`). This
  indirection exists because Postgres `LISTEN` is connection-scoped, so all real channel listening has to happen
  on that one long-lived connection regardless of which pooled connection handled the client's `S`/`U` request.
- Fan-out: when a `NOTIFY` arrives on any channel other than the control channel, the notifier looks up which
  live sessions are subscribed (`notifier.ListeningTo`, a `utils.MapSet[string, *SwWebsocketSession]`) and calls
  `session.Notify(channel, payload)` on each (`websocket.go:326-333`), which writes a JSON frame to that
  session's socket (guarded by `sendLock`).
- Subscription bookkeeping: `SwWebsocketPostgresNotifier.subscribe`/`unsubscribe` (`websocket.go:188-255`) hold a
  server-wide `listeningLock sync.RWMutex` (`websocket.go:79`) around all mutations of `ListeningTo`, run the
  optional DB-defined `onsubscribe_*`/`onunsubscribe_*` hook first (which can fail and abort the
  subscribe/unsubscribe), and only emit the `LISTEN`/`UNLISTEN` control message on the first subscriber / last
  unsubscriber for a given channel (reference counting via `ListeningTo.SizeForKey`).
- "Filters": the closest thing to filtering is that channel names can be matched against DB-registered regex
  patterns (`onSubscribeReg`/`onUnsubscribeReg`, compiled from `information_schema.routines` naming convention
  `onsubscribe_<pattern>` / `onunsubscribe_<pattern>`, `websocket.go:141-156`) so that a subscribe/unsubscribe to
  a dynamically-named channel (e.g. `room:42`) can run a matching SQL hook with regex capture groups
  (`fns.subscribe`/`fns.unsubscribe` are the regex submatches, passed as an array param — `websocket.go:203`).
  This is authorization/side-effect hooking, not payload filtering — every subscriber to a channel gets every
  `NOTIFY` on it verbatim; there's no server-side content filtering of notifications per subscriber.

So: LISTEN/NOTIFY itself is fully implemented and reasonably careful about connection lifecycle and reference
counting; "filters" is realized only as connect/subscribe/unsubscribe-time SQL hooks keyed by channel-name regex,
not as per-message payload filtering.

## 8. Concurrency / goroutine model

- **Per server process:** exactly one extra long-lived goroutine, `go notif.listenPg()` (`websocket.go:38`),
  running the single control-channel `WaitForNotification` loop for the whole server's lifetime — not one per
  connection.
- **Per connection:** exactly one goroutine, the HTTP handler goroutine running `wsHandlerFn`'s
  `for { conn.ReadMessage() }` loop (`websocket-session.go:109-128`). There is no separate writer goroutine; writes
  happen either synchronously inside that same goroutine (for acks) or from the shared `listenPg` goroutine (for
  pushed notifications), both funneled through `session.Send` under `sendLock` (`websocket-session.go:179-186`).
- **Shared mutable state / locks:**
  - `SwWebsocketPostgresNotifier.listeningLock sync.RWMutex` (`websocket.go:79`) guards `ListeningTo` and
    `channelSubscribeFns`; write-locked during subscribe/unsubscribe, read-locked during fan-out dispatch.
  - `SwWebsocketSession.sendLock sync.Mutex` (`websocket-session.go:142`) guards individual socket writes.
  - No buffered channel or queue for outbound messages — `conn.WriteMessage` is called directly and synchronously
    under the lock; a slow client's TCP backpressure will therefore block whichever goroutine is trying to send
    to it (potentially blocking the shared `listenPg` dispatch loop for *all* sessions on that channel if any one
    subscriber is slow, since fan-out at `websocket.go:326-333` iterates synchronously and calls `Notify` in-line
    while holding `listeningLock.RLock()`).
  - Postgres access is via `srv.Pool` (`pgxpool.Pool`), so most calls (subscribe/unsubscribe/notify/hook
    invocations) acquire-and-release a pooled connection per call (`sw/defs.go:64-`, `:94-`), except the one
    dedicated `LISTEN` connection held forever by `listenPg`.
- No worker pool, no per-message goroutine spawning, no explicit backpressure/rate-limiting on inbound commands
  or outbound notifications beyond OS/TCP-level flow control.

## 9. Notes / things to reconsider for the rewrite

- **`SW_WEBSOCKETS_DISABLE` and `SW_WEBSOCKETS_LOG` are both dead/always-true** because `getenvOrDefault` is
  called with a single argument at `main.go:171-172`. With one argument, `getenvOrDefault` never calls
  `os.Getenv` — it just returns the literal string passed in (always non-empty). Net result: websockets are
  **always enabled** and debug logging is **always on** in the legacy binary, no matter what these env vars are
  set to. Worth deciding for the rewrite whether the intended semantics were "set to disable" (current name) or
  "set to enable" — the name strongly suggests the former, but as written the check (`!= ""` gating *setup*)
  would have made setting the var *enable* websockets, i.e. inverted-from-name even if the env lookup had worked.
  Recommend an explicit boolean env parsing helper instead of the variadic string-default trick for anything gate-like.
- **`ondisconnect` is detected but never called.** `HasOnDisconnect` is populated from the DB introspection query
  (`websocket.go:69,113`) but there is no code path anywhere that invokes a `ws.ondisconnect` function — dead
  feature, half-wired.
- **`onconnect`'s return value is discarded.** `doInit` scans a `supl_channels []string` from `ws.onconnect(...)`
  (`websocket-session.go:150-158`) but never uses it — looks like it was meant to auto-subscribe the session to
  channels returned by the connect hook, but that wiring was never finished.
- **No true "parallel queries over one connection."** Commands are processed strictly one-at-a-time per
  connection (`websocket-session.go:109-128`); the `id` field only supports response correlation, not concurrent
  in-flight requests. If the rewrite wants genuine parallelism, it needs either per-message goroutines with
  careful ordering/backpressure, or an explicit statement that ordering is serial-per-connection by design.
- **No generic query/RPC command exists.** The header comment's promise of websocket query support was never
  implemented; only `S`/`U`/`N` (subscribe/unsubscribe/notify) exist. A rewrite that wants "queries over
  websocket" is starting from zero here, not extending existing plumbing.
- **Auth-soft-fail on handshake.** An invalid/missing JWT cookie silently downgrades to the anonymous role rather
  than rejecting the websocket handshake (`websocket-session.go:79-84`). Depending on what the anonymous
  Postgres role can do, this may be intentional (parity with anonymous HTTP requests) or worth tightening.
  Same-origin `CheckOrigin` is gorilla's implicit default since `upgrader` is a zero-value `Upgrader`
  (`websocket.go:18`) — no CORS-style allowlist for websocket origins as there is for plain HTTP responses
  (compare the explicit `Access-Control-Allow-Origin` middleware in `main.go:159-169`, which does not apply to
  the `/ws` upgrade path in the same way).
- **One dedicated LISTEN connection, held forever, never released, no reconnect-on-drop logic.** If that
  connection dies (network blip, DB restart), `listenPg` just logs and returns (`websocket.go:284-288`), and
  nothing re-establishes it — the entire NOTIFY relay for the process silently stops working until restart. Worth
  adding a reconnect/retry loop in the rewrite.
- **Backpressure/slow-consumer risk.** Fan-out to subscribers happens synchronously while holding
  `listeningLock.RLock()` (`websocket.go:326-333`) and each `Send` blocks on `conn.WriteMessage` under a
  per-session mutex with no write deadline set (`upgrader`/`conn` have no `SetWriteDeadline` calls anywhere in
  either file) — a single stalled client can stall notification delivery to every other subscriber on that
  channel, and there's no timeout to evict it.
- **No message size limits, no `SetReadLimit`/`SetPongHandler`/idle-timeout configuration** anywhere on `conn`
  before or after upgrade — worth adding explicit deadlines/limits in the rewrite rather than relying on library
  defaults.
- **Ack/notification frames share no discriminator field**, so a client must infer message type from which keys
  are present (`id`/`ok`/`message` vs `channel`/`payload`). A rewrite protocol should probably add an explicit
  `type` field.
- Underlying library confirmed: `github.com/gorilla/websocket v1.5.0` (`go.mod`, `go.sum:131-132`) — a
  maintenance-mode but still widely used library; worth deciding in the rewrite whether to stay on it or move to
  `nhooyr.io/websocket` / `coder/websocket` or stdlib `net/http`'s (as of newer Go versions) websocket support if
  available.
