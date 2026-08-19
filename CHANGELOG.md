# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

This file was introduced at v0.44.0; for the history of earlier releases see the
[git tags](https://github.com/marrasen/aprot/tags) and GitHub releases.

## [Unreleased]

### Added

- **MCP support** (#316). Selected handlers can be served as MCP (Model
  Context Protocol) tools, so an AI assistant calls the same handlers the
  browser does — through the same pipeline, middleware and auth. Exposure is
  per-method opt-in via `Registry.EnableMCP` with model-facing names,
  descriptions and behavior hints (`MCPTool`); the new `aprot/mcp` subpackage
  serves them as a stateless `http.Handler` implementing the MCP Streamable
  HTTP tool-serving subset (`initialize`, `ping`, `tools/list`,
  `tools/call`). Tool descriptions come from handler godoc, input schemas
  from the same generator as OpenAPI; handler errors become tool results
  with `isError`, argument problems become JSON-RPC protocol errors.

- `Server.Invoke(ctx, method, params)` — the transport-agnostic pipeline
  entry point: handler info and request context, refresh queue, server +
  group middleware, refresh flush on success. The WS/SSE dispatch, the REST
  adapter and the MCP adapter all execute through it; custom request-scoped
  transports can call it directly.

- `Server.NewDetachedConn` + `aprot.WithConnection` — a connection bound to
  no transport, so connection-shaped auth middleware (`aprot.Connection`)
  works on request-scoped transports. Value store and `SetUserID`/`UserID`
  work; push fan-out never sees it; sends fail with `ErrDetachedConn`; no
  cleanup needed. `aprot.WithHTTPRequest` is exported for custom HTTP
  transports to expose the request the way the REST adapter does.

- `Registry.SchemaFor(reflect.Type)` — the OpenAPI generator's JSON Schema
  builder as a standalone API, producing self-contained schemas (registered
  enums, embedded-struct flattening, `validate` constraints, godoc
  descriptions, recursion broken safely). OpenAPI output is unchanged.

- `GenerateSourceDocsGo` / `Registry.SetSourceDocs` — bake godoc metadata
  (handler docs, parameter names, type and field docs) into a committed,
  regenerable Go file, so OpenAPI descriptions, REST parameter names and MCP
  tool descriptions work in deployed binaries that have no source on disk.

- `aprot.MarshalWire` — the wire encoding (sql.Null flattening, `format:`
  tags) as a public function, for adapters outside the aprot package.

- `aprot.NewTestSubscriber` — test helper: a recorded, transport-less
  subscriber for asserting that mutations on other transports refresh
  subscribed clients.

### Changed

- With a `Server` built from the same registry, REST requests now also run
  server middleware (`Server.Use`), matching socket requests — previously
  they ran only adapter and group middleware. Middleware that assumes a live
  socket can use the detached-connection API; REST adapters without a server
  keep the old adapter + group chain.

## [0.57.0] - 2026-08-19

### Added

- CI now fails when the committed generated example clients are stale. The
  `typescript-compile` job regenerated the clients before compiling them, so
  it validated what the generator produces — never what is committed. A PR
  that changed `templates/` without running the generators passed every
  check; master carried 296 lines of drift that way. Both generate steps now
  run up front, followed by a `git diff` gate over the tracked generated
  artifacts (with intent-to-add, so a generated file that was never committed
  fails as a diff rather than passing as untracked). (#312, fixes #310)

### Fixed

- REST requests now run through the same request pipeline as WebSocket/SSE.
  The REST adapter built its own middleware chain without the request context
  the socket transports install, with two silent consequences: `TriggerRefresh`
  from a handler invoked over REST was a no-op — a REST mutation never
  refreshed subscribed WebSocket/SSE clients, who quietly held stale data —
  and `HandlerInfoFromContext` / `RequestFromContext` returned nil in REST
  middleware. REST handlers now get handler info, the request, and the refresh
  queue in context; triggers are batched, deduplicated, and flushed after a
  successful response, and `TriggerRefreshNow` flushes mid-handler, exactly as
  over a socket. Requires no API change: `NewServer` records itself on its
  `Registry`, and the REST adapter picks the server up from there — with no
  server built from the registry there are no subscribers and `TriggerRefresh`
  remains a no-op. `RegisterRefreshTrigger` is unchanged: it declares a
  subscription's dependencies, and REST cannot subscribe. (#317, from #316)

- Generated TypeScript function names are now escaped when they collide with
  reserved words. A handler named `Delete` generated `export function
  delete(...)` — a syntax error that surfaced only as a broken TypeScript
  build in the consumer. The reserved-word table and trailing-underscore
  escape already applied to parameter names now also cover method names, hook
  names, and push-event handler names, applied after the `NamingPlugin` runs
  so a custom plugin cannot reintroduce a reserved name. The escape is
  cosmetic: parameters are positional and methods dispatch by their qualified
  wire name, so nothing on the wire changes. (#311, fixes #309)

## [0.56.0] - 2026-08-14

### Added

- `ApiClient.onRequestError(listener)` on the generated TypeScript client
  observes every failed call the client makes — a rejected request, a
  subscription error, or a stream that throws — passing the error and the
  `{ struct, method }` source parsed from the wire name. Returns an
  unsubscribe function; multiple listeners are supported. It is an observer,
  not a handler: the error is still thrown to the caller, so per-call
  `try/catch` and per-hook `error` fields keep working. Available in vanilla
  output too, where the React provider is not. (#304)

- `TestGeneratedClientsTypecheck` compiles the generated TypeScript of every
  output mode — single-file and multi-file, vanilla and React — with `tsc`.
  Nothing compiled the generated output before, which is why the drift below
  survived. The test skips unless the React example's dependencies are
  installed (`cd example/react/client && npm ci`); CI installs them and runs
  it in the `typescript-compile` job. (#307)

### Changed

- `<ApiClientErrorProvider>` now subscribes to the client's `onRequestError`
  instead of handing components a `Proxy`-wrapped client through
  `useApiClient()`. A wrapper only covered callers who asked React for the
  client; a module that imported the client directly reported nothing. An
  observer on the client itself cannot be sidestepped that way. Visible
  difference: `useApiClient()` now returns the client itself in all cases,
  identity-stable, so code comparing it by reference or reaching past it no
  longer sees a Proxy. (#304)

### Fixed

- `Generator.GenerateTo` (single-file output) emitted a client that did not
  compile whenever the registry had a streaming handler: the generated
  functions called `client.requestStream`, and the client written by the
  single-file templates had no such method. React single-file output was worse
  — its generic `useQuery` hook called `client.subscribe`, also absent, so no
  hook worked at all. `ApiClient` existed in three hand-maintained copies; the
  two behind `GenerateTo` were reduced older versions that had drifted from the
  one behind `Generate`. Both single-file templates now render the shared
  `api-client-class` block, so the two layouts emit the same client and cannot
  drift again.

  Single-file output consequently gains what the multi-file client already had:
  `subscribe()` and `requestStream()`, the generated `subscribeX()` helpers,
  binary/Blob frames, first-message auth (`getAuthToken` / `refreshAuth`),
  `onConnectionRejected` / `reconnectOnRejected` / `getLastRejection` (accepted
  as options before but never implemented), and a function-valued client URL.
  Single-file React hooks now wire `_subscribe` and carry the `.method` tag, so
  auto-refetch and `useQuerySuspense` work there too. (#307)

## [0.55.0] - 2026-08-12

### Added

- `EnumNamer` lets a string enum say what its generated members should be
  called. `RegisterEnum` receives only the values, so a member's name is the
  value with its first letter capitalised — which reads well for `"pending"`
  and says nothing at all when the values are one character each. An enum
  stored as `"F"`, `"O"` and `"B"` generated as `DrawOption.F`, `.O` and `.B`
  while the Go constants it came from were `DrawOptionFilled`,
  `DrawOptionOutline` and `DrawOptionBoundingRect`; the names existed but not
  in the slice, so the type has to carry them. Implementing
  `EnumMemberName() string` on the enum type names the members after the
  constants. The wire value is untouched — this renames the member, not the
  data.

  Opt-in, and deliberately not `fmt.Stringer`: int enums already use that to
  name their members, but a string enum may well implement `String()` to
  return its own value, and honouring it there would rename the members of
  every such enum without anyone asking. Registration now also panics when two
  values ask for the same member name, which would otherwise generate an
  object literal with a duplicate key and make one value unreachable from the
  client. (#301)

## [0.54.0] - 2026-08-11

### Added

- `SameOriginCheck(extraOrigins ...string)` returns a ready-made origin check
  for `Server.SetCheckOrigin`, so cookie-authenticated deployments no longer
  have to hand-roll one. It accepts the server's own origin — the `Origin`
  header's host equals the request's `Host`, compared case-insensitively with
  the port included — plus any extra origins matched verbatim (also
  case-insensitive, trailing slash tolerated) for dev proxies such as Vite's
  `changeOrigin: true`, which rewrites `Host` to the backend while forwarding
  the browser's `:5173` origin. Without that escape hatch consumers tend to
  give up on the check entirely. The hand-written version is easier to get
  quietly wrong than it looks: substring or suffix matching admits
  `https://app.example.com.evil.com` against host `app.example.com`,
  `https://host:9999` must not match host `host` while `https://host:8443`
  must match `host:8443`, and `Origin: null` from a sandboxed iframe is not
  same-origin. Each of those is a place where the result is "cookies accepted
  from any origin". A missing, unparseable, or `null` Origin is rejected, and
  `null` cannot be allowlisted through `extraOrigins`. Rejecting a missing
  header is correct for browsers (they always send `Origin` on a WebSocket
  handshake) but excludes non-browser clients, so the helper is opt-in and the
  server default stays allow-all — nothing changes for existing users, and
  endpoints serving both client kinds should keep a custom func. (#298)

### Changed

- The e2e and example TypeScript packages moved to TypeScript 7 and replaced
  ESLint with oxlint. `typescript-eslint@8` declares a peer range excluding
  TS7, which held the e2e package on TS6; oxlint uses its own Rust parser and
  has no TypeScript peer dependency. Existing rules were ported to per-package
  `.oxlintrc.json`, generated client output stays excluded from linting, and
  CI now lints both example clients. Repo tooling only — the generated client
  and the library API are unaffected. (#265)

## [0.53.0] - 2026-08-08

### Added

- Per-connection opt-out from WebSocket binary frames. A client that does not
  decode binary can dial with `?binary=0` on the upgrade URL and receive `Blob`
  results as the JSON `{"$blob": {contentType, data}}` envelope instead — the
  same representation SSE and byte-stream already use — at the cost of base64
  inflation. Accepted values are `1`/`true`/`yes`/`on` and `0`/`false`/`no`/
  `off` (case-insensitive); an unrecognized value fails the upgrade with
  `400 Bad Request` rather than silently defaulting to binary, since a typo
  that re-enabled binary frames would reinstate the hang the parameter exists
  to prevent. Omitting the parameter keeps the existing behavior, so generated
  TypeScript clients are unaffected. (#279)
- `ConfigMessage.BinaryFrames` (`binaryFrames` on the wire) reports whether
  `Blob` results will arrive as binary frames on this connection. Always
  emitted, including when false, and always false on SSE and byte-stream. It
  reaches the client before any request can be made, which makes it the one
  point at which a client that cannot decode binary frames can fail loudly
  instead of hanging on the first `Blob` response — aprot cannot detect a
  dropped frame server-side. A missing field means a server predating the
  negotiation. (#279)

- `ServerOptions.AllowAnonymous` admits unauthenticated connections while an
  `OnAuth` hook is registered, for apps that mix public and protected APIs on
  one endpoint. Registering a hook otherwise puts *every* connection into the
  pending-auth state, so anonymous viewers on a public page are closed by
  `AuthTimeout` — the workaround was an empty-token dance (`getAuthToken: () =>
  ''` plus a hook treating `""` as anonymous), which is no longer needed.
  Anonymous connections skip the pending state and the auth timeout and run
  with `Conn.UserID()` `""`; a client that authenticates later upgrades the
  live session in place, and a token that is offered and rejected still closes
  an unauthenticated connection, so a bad credential never degrades into a
  working anonymous session. Admitting a connection is not authorizing the
  call — keep gating protected handlers on the user ID or middleware. (#283)
- `reconnectOnRejected` (`boolean | {delayMs, maxAttempts}`) on the generated
  TypeScript client retries a rejected connection instead of treating the
  rejection as terminal. Terminal remains the default because it is the right
  answer to a real sign-out, but with short-lived JWTs (Clerk and friends) a
  rejection is usually an expired token, session propagation lag just after
  sign-in, or clock skew — cases where retrying with a freshly minted token is
  correct. Every consumer was hand-rolling `onConnectionRejected` plus
  `setTimeout(connect, 2000)`, which is easy to get subtly wrong: the timer has
  to die on `disconnect()` or a torn-down client resurrects itself. The delay
  is fixed rather than backed off (a rejection means the server is reachable
  and answered), `maxAttempts` bounds consecutive rejections, and a retry that
  fails at the network level falls through to the normal reconnect backoff.
  (#283)
- `getConnectParams` on the generated TypeScript client returns query
  parameters merged into the connection URL, resolved fresh on every connection
  attempt — the initial connect, every auto-reconnect, every rejection retry,
  and page-wake reconnects. Carrying a short-lived token previously meant
  passing a URL *function* and hand-encoding the query, which conflates
  addressing with credentials; the base URL can now stay static. A thrown error
  fails the attempt like a transport error, so a token-service blip goes
  through the normal reconnect path instead of silently halting reconnection —
  which is what a throwing URL function used to do. (#283)
- `client.getLastRejection()` returns the `ApiError` from the most recent
  connection rejection (server rejection or failed first-message auth), or null
  after the next successful connect. Unlike `getLastConnectionError()` it is not
  overwritten by later transport failures, so a UI can keep rendering "session
  expired, sign in again" distinctly from "server unreachable" while retries
  run. React gains `useConnectionRejection()` and `useConnectionError()`, and
  `useConnection()` now also returns `error` and `rejection`. (#283)
- `client.reconnectNow()` on the generated TypeScript client: abandon a pending
  reconnect backoff and attempt a connection immediately, keeping subscriptions
  and in-flight requests. `connect()` already does this; `reconnectNow()` adds
  only the case `connect()` reads as "nothing to do" — a socket the runtime
  left half-open still reports `'connected'` until its close event lands. Until
  now the only way to cut a backoff short was `disconnect()` + `connect()`,
  which drops every subscription and rejects in-flight requests. (#287)

### Fixed

- `connect()` called during a reconnect backoff left the pending timer armed.
  It connected immediately, as intended, but up to `reconnectMaxInterval` later
  (30 s by default) the timer fired and replaced the live socket. The transport
  detaches the replaced socket's handlers, so its close was never reported and
  any request in flight at that moment never settled — it neither resolved nor
  rejected. A manual `connect()` now cancels the backoff, and connection
  attempts are serialized: at most one runs at a time, and a live socket is
  never replaced. Requests bound to a socket that *is* replaced (the page-wake
  path, where the runtime left it half-open) now reject with a
  `ConnectionError` instead of hanging. (#287)
- `useConnectionState` (and `useConnection`, which wraps it) could report a
  stale state forever. It seeded `useState` from the client at first render and
  only subscribed in an effect, so a connection that completed in between was
  missed — and because connection state is sticky, nothing later corrected it.
  The recommended wiring makes this the common case rather than a rare race:
  the client is created and connected outside React, so it is frequently
  already `connected` before the effect runs, leaving the UI permanently
  showing "Disconnected". Both hooks (and `useIsLoading`, which had the same
  subscribe-after-render gap and could strand a spinner) now read through
  `useSyncExternalStore`, which re-reads the snapshot after subscribing. (#283)

### Documentation

- The `connect()` docs said it is called "once after constructing the client"
  and "never needs to be called again". True for keeping a connection alive,
  misleading for "make sure we are connected now" — it led a consumer app to
  wrap it in a module-level `if (connected) return` cache, which no-ops exactly
  when the socket has dropped and the client is in a backoff. The symptom was a
  ~30 s sign-in with no `/ws` request in the network panel at all. `README.md`,
  `doc.go`, `APROT_AI.md`, `docs/auth.md` and the generated client's own doc
  comments now state that `connect()` is cheap and idempotent and should be
  called whenever the connection needs to be live. (#287)
- `docs/auth.md` is the recipe for authenticating the generated TypeScript
  client with short-lived JWTs (Clerk, Auth0, Firebase). Two consumer apps
  independently hit the same three traps and one shipped the broken version:
  a static URL freezes the token so every reconnect after expiry is rejected;
  a rejection is terminal, so one network blip permanently kills the session;
  and awaiting `connect()` before providing the client races React StrictMode's
  double mount into a zombie the UI waits on forever. It documents the
  per-attempt resolution of `getConnectParams` and URL functions as a
  guarantee, when terminal-on-rejection is right versus wrong, the
  StrictMode-safe provider wiring, and first-message auth including
  `AllowAnonymous` for mixed public/protected apps. Linked from README,
  `doc.go` and `APROT_AI.md`. The React example is rewired to the recommended
  pattern — it previously demonstrated the racy one — and the superseded
  internal design note `docs/issue-websocket-auth.md` is removed. (#283)
- `docs/binary-frames.md` documents the WebSocket binary frame format used for
  `Blob` results — layout, header fields, reference decoders (JS and Python),
  and the JSON `$blob` fallback — for anyone writing a WebSocket client other
  than the generated TypeScript one. A client that handles only text frames
  drops `Blob` responses silently: the call never settles, and because the
  server considers the request complete there is no error and no server-side
  trace, which is easily mistaken for a server deadlock. Subscriptions to a
  `Blob`-returning method have the same failure with no hang at all — the
  refreshes just never arrive. (#279)

## [0.52.0] - 2026-07-16

### Added

- `ServerOptions.Logger` (`*slog.Logger`; nil uses `slog.Default()`) — receives
  server-side error logs. Currently logged at error level: response-encode
  failures, with the method name and error. These were previously reported to
  the client as `CodeInternalError` but left no server-side trace, so an
  incident could produce zero log lines.

### Fixed

- Compatibility with go-json-experiment/json snapshots from 2026-06 onward,
  which made per-field `format:` struct tags opt-in. aprot's codegen requires
  such tags on some types (e.g. `json:"d,format:nano"` on `time.Duration`),
  so any consumer that resolved a newer snapshot via MVS had every response
  containing a format-tagged struct fail with ``Go struct field … has
  unsupported `format` tag option``. aprot now opts in on every path that
  marshals or unmarshals user data (response results, request params,
  push/refresh payloads, stream items, the `$blob` JSON fallback, and the
  codegen's zero-value probes) and requires the 2026-06-23 snapshot or newer.
  Consumers that pinned go-json-experiment/json back to aprot's previous
  version as a workaround can drop the pin.

## [0.51.0] - 2026-07-15

### Added

- `Registry.ReserveClientFile(base)` — marks a generated client file base name
  (without the `.ts`) as owned by a runtime's `OnGenerate` hook, so the shared
  per-package type file namer avoids it (emitting `{pkg}.types.ts` on a
  collision, the same alternate base already used for handler-file clashes).
  `tasks.Enable` and `tasks.EnableWithMeta` reserve `tasks`.

### Fixed

- A shared type returned from the `tasks` package by two or more handler groups
  (e.g. a handler returning `*tasks.TaskRef`) is no longer dropped from the
  generated client. Such a type was promoted to the shared `tasks.ts` file and
  then overwritten wholesale by the task runtime's convenience code, leaving
  every consumer importing a type nobody exported (`Module './tasks' has no
  exported member 'TaskRef'`). It is now emitted as `tasks.types.ts` and
  imported from `./tasks.types`, clear of the runtime file — the same collision
  class as the handler-file fix in #206.

## [0.50.0] - 2026-07-15

### Added

- `Registry.OverrideFieldType(owner, goFieldName, concrete)` — refine a
  dynamic (`any`/`interface{}`) struct field to a concrete type in the
  generated TypeScript. Codegen-only: the field is emitted with the concrete
  type's interface (declared and import-resolved like any other referenced
  type) while runtime serialization is untouched. Panics unless the named
  field is an exported interface-typed field declared directly on the owner
  struct.
- `tasks.EnableWithMeta[M]` now wires `M` into the generated task types:
  `TaskNode.meta` and `SharedTaskState.meta` are typed as `M`'s TypeScript
  interface instead of `any`, so client code can read `task.meta?.field`
  without casting. The meta interface moves from `tasks.ts` into
  `tasks-handler.ts` (next to the types that reference it); `tasks.ts`
  re-exports it, so existing imports keep resolving.
- `Server.DisconnectUser(userID) int` — gracefully closes every connection
  currently associated with a user id (the identity set via `Conn.SetUserID`)
  and returns the number of connections closed. Each connection gets a close
  frame where the transport supports one, its in-flight requests are canceled
  with `ErrConnectionClosed`, and disconnect hooks run through the normal
  teardown path. A no-op returning `0` for unknown ids; safe for concurrent
  use; never closes a connection that has since re-authenticated as a
  different user. Use it to evict removed users whose authenticated sockets
  would otherwise linger.

### Changed

- **Generated TypeScript contains no `any`.** Every mapping that previously
  fell back to `any` now emits `unknown`: `any`/`interface{}` fields and
  params, anonymous structs, marshaler-inferred `any[]` /
  `Record<string, any>`, and the Zod fallbacks `z.any()` (now `z.unknown()`).
  Zod validation behavior is unchanged (both schemas accept every value); the
  wire format is unchanged. Hand-written client code that dot-accessed a
  value typed `any` no longer compiles until it casts or narrows — with
  `tasks.EnableWithMeta` the task meta fields get a precise type instead, so
  no cast is needed there.

## [0.49.1] - 2026-07-14

### Fixed

- TypeScript generation no longer collects types that are only reachable
  through unexported or `json:"-"` struct fields. A registered struct with an
  unexported `mu sync.Mutex` field produced a stray `sync.ts` containing empty
  `Mutex`/`noCopy` interfaces (duplicated, since Go 1.24's `sync.Mutex` embeds
  `internal/sync.Mutex` and both package paths shorten to `sync`). `collectType`
  now applies the same exported + non-skipped field filter as interface
  emission, so only types that can actually appear in generated interfaces are
  collected (#260).

## [0.49.0] - 2026-07-13

### Added

- The generated React client now exports `selectWithPreviousData` and its
  `SubscriptionSnapshot<T>` type — the pure selector behind `useQuery`'s
  `keepPreviousData` option — so hand-written stores that call the generated
  RPC functions imperatively can reuse it instead of re-deriving the pattern.
  The invariant it centralizes: the returned snapshot carries the previous
  `data` through a params-keyed reload's null gap but always the current
  `error`/`isLoading` flags, so kept data never masks loading or error state
  (#254).

### Fixed

- WebSocket frames enqueued just before a server-side close could be dropped:
  `writePump` selects over the shutdown signal and the send queue, and when
  both were ready it sometimes exited without flushing. In practice a client
  rejected by the auth hook (or the auth timeout) could see an abnormal close
  (1006) without ever receiving its `auth_error` frame. The pump now drains
  frames queued before `Close` onto the wire during teardown (#257).

## [0.48.0] - 2026-07-09

### Added

- Pluggable shared-task cancel authorization: `tasks.WithCancelAuthorizer(fn)`
  replaces the built-in owner-only policy, receiving a `TaskCancelInfo`
  (`{ID, Title, OwnerConnID, OwnerUserID}`) and returning nil to allow or
  `aprot.ErrForbidden(...)` to deny. The default policy is keyed by connection
  ID, so a client silently loses the right to cancel its own task across a
  reconnect; an authorizer comparing `aprot.Connection(ctx).UserID()` against
  `OwnerUserID` survives one.
- `ListTasks` handler, registered by `tasks.Enable` / `tasks.EnableWithMeta`,
  returning the current shared-task snapshot with `IsOwner` evaluated against
  the calling connection.

### Fixed

- Shared-task state was empty for any client that mounted while a task was
  already running. `TaskStateEvent` is broadcast only at lifecycle boundaries
  (create/finish), so a late-joining consumer saw nothing until the next one.
  The generated React `useSharedTasks` hook now seeds itself from `ListTasks`
  on mount and on every reconnect, guarding against an in-flight seed
  clobbering a fresher snapshot.
- Shared-task state was also kept per hook instance, so a second `useSharedTasks`
  consumer mounting later started empty. State now lives in one store per
  client, attached once and shared by every hook instance via
  `useSyncExternalStore`.
- A reconnecting client whose task finished while it was away kept showing the
  stale task: the on-connect `TaskStateEvent` push was skipped when the task
  list was empty. It now fires unconditionally so the client clears its state.

## [0.47.1] - 2026-07-06

### Fixed

- Live task progress in the generated React `useSharedTasks` hook: the hook
  only folded in full `TaskStateEvent` snapshots, which the server broadcasts
  only at task lifecycle boundaries (create/finish), so the per-node
  `TaskUpdateEvent` progress ticks emitted during execution were dropped and
  progress bars showed an initial `0/N` then vanished. The hook now also
  subscribes to `TaskUpdateEvent` and folds `current`/`total` into the task
  list — recursing through nested subtasks — so consumers see live progress
  between snapshots (#246).

## [0.47.0] - 2026-07-05

### Added

- Subscription patches: `aprot.PatchSubscription(ctx, patch, keys...)` (and
  `Server.PatchSubscription` for out-of-request callers) lets a mutation push
  a small typed payload to subscribed queries instead of re-running them and
  re-sending the full result — O(patch) on the wire instead of O(list) for
  in-place updates to large subscribed collections. Clients opt in per
  subscription by registering a reducer: React hooks take
  `applyPatch(data, patch)`, applied to the shared query-cache snapshot so
  every component using the hook re-renders (patches racing ahead of the
  initial response are queued and replayed); vanilla clients pass
  `{ onPatch }` to the generated subscribe functions. The exported
  `mergeByKey(key)` helper builds the common keyed-array reducer. Subscribers
  without a reducer — older generated clients, `useQuerySuspense` — fall back
  to a full refresh automatically, and the new `Observer.PatchFanout(key,
  patched, refreshed)` event reports the split (embed `NoopObserver` to stay
  forward-compatible; direct `Observer` implementors must add the method).
  Patches deliver immediately (not batched with the request) and are meant
  for in-place updates; keep `TriggerRefresh` for structural changes (#237).
- Binary `Blob` responses: returning `aprot.Blob` (or `*aprot.Blob`) from a
  unary handler opts the result into binary delivery. Over WebSocket the
  payload is sent as a binary frame (4-byte header length + JSON header + raw
  payload — no base64 inflation); transports without binary frames (SSE,
  byte-stream) fall back to a `{"$blob": {contentType, data}}` JSON envelope.
  Generated TypeScript clients resolve a DOM `Blob` on every transport —
  methods are typed `Promise<Blob>`, and subscription refreshes deliver
  `Blob`s the same way. Only the explicit `Blob` type opts in, and only as a
  top-level result: plain `[]byte` results keep their base64 string encoding,
  and nested/streamed/parameter `Blob`s travel as ordinary JSON (#238).
- `ServerOptions.StreamChunking`: opt-in batching of streamed items. Instead
  of one wire frame per yielded item, consecutive items are batched into
  `stream_chunk` frames, flushed when any of three thresholds is reached —
  `MaxItems` (default 128), `MaxBytes` of marshaled items (default 64 KiB),
  or `MaxDelay` after the first buffered item (default 20ms), so a slow
  producer never holds delivered items back. This makes streaming viable for
  large collections (thousands of small records) where per-item framing and
  syscall overhead dominate. Batching is transparent to the generated
  TypeScript client's `AsyncIterable`, which still yields one item at a time,
  but enabling it requires a client generated from this aprot version — older
  generated clients do not understand `stream_chunk` frames. Nil (the
  default) keeps the existing per-item `stream_item` frames (#239).

### Fixed

- Fixed-size array fields (`[N]T`) no longer degrade to `any` in the generated
  TypeScript client. They map to a tuple type (`[4]float64` →
  `[number, number, number, number]`) for lengths up to 16 and to plain `T[]`
  above that; nested arrays and arrays of structs are mapped recursively, and
  struct element types get their own generated interface. Zod schemas emit
  `z.tuple([...])` (or `z.array(...).length(N)` above the cap) and OpenAPI
  schemas carry `minItems`/`maxItems`. `[N]byte` — named or not — maps to
  `string`, matching the base64 encoding go-json-experiment/json uses on the
  wire, and a `json:",format:array"` tag forces the fixed-length number-array
  shape (#240).

## [0.46.0] - 2026-07-04

### Added

- `Server.ServeStream(ctx, rw io.ReadWriteCloser, info ConnInfo)`: a
  transport-agnostic entry point that serves one connection over any byte
  stream using newline-delimited JSON framing — the stdio pipes of a child
  process (e.g. a Go backend embedded in an Electron app), a Unix domain
  socket, or a Windows named pipe. Stream connections participate fully in
  connect/disconnect hooks, first-message auth, middleware, subscriptions,
  streaming, and push. `MaxMessageSize` bounds inbound line length; the
  WebSocket keepalive/write-timeout options do not apply to raw streams (#234).
- The generated TypeScript client's `ClientTransport` interface (and
  `TransportCloseInfo`) is now exported, and `ApiClientOptions.transport`
  accepts a custom `ClientTransport` instance in addition to
  `'websocket' | 'sse'` — so the protocol can ride any message channel, such
  as an Electron preload/MessagePort bridge to a Go child process (#234).

### Changed

- `Generator.Generate()` now removes stale generated files: top-level `.ts`
  files in `OutputDir` that start with the `// Code generated by aprot. DO NOT
  EDIT.` marker but were not produced by the current run (leftovers from
  renamed or removed handler groups) are deleted so they cannot break the
  TypeScript build. Hand-written files, non-`.ts` files, and subdirectories
  are never touched (#233).
- The generated client's "never connected" rejection message now explains the
  fix: `Not connected: call client.connect() first — the client never connects
  automatically` (the `ConnectionError.reason` is unchanged). Docs and
  generated-code comments now state explicitly that `new ApiClient(...)` and
  `<ApiClientProvider>` do not open a connection — `client.connect()` is a
  required manual step (#233).

### Fixed

- A handler result that could not be marshaled (e.g. a `NaN` float, rejected
  by JSON) was silently dropped — the client received neither a response nor
  an error frame and the request hung forever. `sendResponse` now marshals up
  front and falls back to a `CodeInternalError` error frame on failure;
  `sendProtocolError` and `sendStreamEnd` resend without their `Data` payload
  when the payload is what fails to marshal, so terminal frames always reach
  the client (#235).

## [0.45.0] - 2026-07-01

### Added

- First-message authentication: `Server.OnAuth` validates a token the client
  sends over the connection — a WebSocket `auth` frame or the SSE `POST /rpc`
  body — instead of the URL, so secrets stay out of access/proxy/CDN logs.
  Includes a pending-auth state, a configurable `ServerOptions.AuthTimeout`
  (default 10s), and mid-session token refresh; the generated TS client gains a
  `getAuthToken` option and a `refreshAuth()` method (#153).
- Observability: an opt-in `Observer` (via `ServerOptions.Observer`) reporting
  connection open/close, request completion (method, duration, error code),
  subscription register/unregister, refresh fan-out, send-buffer pressure, and
  write timeouts, plus a pull-based `Server.Stats()` snapshot. No hot-path cost
  when unset; embed `NoopObserver` for forward-compatibility (#223).
- CORS support for the SSE and REST HTTP transports: `aprot.CORS(CORSOptions)`
  returns a closed-by-default `func(http.Handler) http.Handler` wrapper with
  `OPTIONS` preflight handling and credentials-safe origin echoing (#224).
- Per-connection and server-wide concurrency caps on `ServerOptions` —
  `MaxConcurrentRequests` (256), `MaxServerConcurrentRequests` (10000), and
  `MaxSubscriptions` (1024); a frame over a cap is rejected with the new
  `CodeTooManyRequests` (`-32004`) rather than spawning unbounded goroutines,
  each `-1` to disable (#222).
- Task lifecycle middleware: `tasks.Enable(registry, tasks.WithTaskMiddleware(mw))`,
  with `TaskMiddleware` / `TaskInfo` and ctx propagation through nested subtasks (#205, #211).
- Configurable request-param logging in the vanilla `LoggingMiddleware` (#212).
- Server hardening options on `ServerOptions` — `MaxMessageSize`, `WriteTimeout`,
  `PingInterval`, `PongTimeout` — plus WebSocket ping/pong keepalive (`-1` disables) (#208).
- Runtime JSON unwrapping for generic `sql.Null[T]` for the common `T`
  (`string`, `int`, `int64`, `int32`, `int16`, `float64`, `bool`, `time.Time`) (#213).
- CI: `govulncheck` and `gosec` jobs, and a Dependabot config for the Go modules,
  GitHub Actions, and the e2e / react-client npm packages (#207 P3).

### Changed

- TypeScript type mapping: a bare pointer `*T` (no `json:,omitempty`) now maps to
  `T | null` (required), `map[bool]V` to `Partial<Record<"true" | "false", V>>`,
  and `json.RawMessage` to `unknown` (#213).
- OpenAPI output is now valid 3.0.3: boolean `exclusiveMinimum`/`exclusiveMaximum`
  paired with `minimum`/`maximum`, `minItems`/`maxItems` for slice bounds, and
  `arg0`/`arg1` fallback path-param names matching the REST adapter (#213).
- `NewServer` clamps a misconfigured `PongTimeout` (≤ `PingInterval`) up to
  `2*PingInterval` so healthy connections aren't dropped (#207 P3).
- `SetCheckOrigin` (CSWSH mitigation) is now documented in README, doc.go, and APROT_AI.md (#208).

### Fixed

- TS codegen: a shared per-package enum/type file whose name collided with a
  same-named handler file was silently overwritten — dropping the enum/type
  definition and leaving dangling (self-)imports that failed `tsc`. Colliding
  shared files are now emitted as `{pkg}.types.ts` (#206).
- Stalled-client deadlock (per-frame write timeout; sends snapshotted outside the
  server lock), handler panic recovery on request/subscribe/refresh paths, and
  inbound size limits for WebSocket and SSE (#208).
- REST scalar path params and `sql.Null`-aware REST response marshaling (#208).
- Detached shared tasks (`context.WithoutCancel`) no longer auto-finalized (#208).
- Runtime lifecycle correctness: late-register races, `Server.Stop` hang, `userConns`
  leak on connect-hook rejection, subscription re-register / stale params, and small
  data races (#209).
- `tasks/` subpackage: `lastProgress` leak + trailing-flush, `ensureRoot` race,
  nil `*Task` on the REST path, `SharedSubTask` duplicate node, and owner-only
  shared-task cancel (#210).
- Zod codegen: unescaped string injection (enum values, `contains`/`startswith`/`endswith`),
  kind-aware `gt`/`lt`/`min`/`max`, `oneof` honored, and recursive schemas via `z.lazy()` (#213).
- Codegen rejects `time.Duration` fields at generation time (no default JSON
  representation in the v2 encoder) unless a `format:` option is set (#213).
- Kebab/camel acronym digit handling (`API2Handlers`) (#213).
- Invalid TypeScript identifiers are sanitized: non-identifier field keys are quoted,
  reserved-word param names are suffixed, and instantiated generic type names
  (`Box[int]`) are folded into valid identifiers everywhere they are emitted (#213).

### Security

- First-message auth keeps tokens out of URLs (and therefore out of access,
  reverse-proxy, and CDN logs); auth-hook errors are redacted to a generic
  message so internal detail isn't leaked to unauthenticated callers, and a
  mid-session refresh can't leak a prior identity's pushes (#153).
- Per-connection / server-wide concurrency and subscription caps bound the
  resource-exhaustion blast radius of a single misbehaving connection (#222).
- Static analysis (`gosec`) and vulnerability scanning (`govulncheck`) added to CI (#207 P3).

[Unreleased]: https://github.com/marrasen/aprot/compare/v0.57.0...HEAD
[0.57.0]: https://github.com/marrasen/aprot/compare/v0.56.0...v0.57.0
[0.56.0]: https://github.com/marrasen/aprot/compare/v0.55.0...v0.56.0
[0.55.0]: https://github.com/marrasen/aprot/compare/v0.54.0...v0.55.0
[0.54.0]: https://github.com/marrasen/aprot/compare/v0.53.0...v0.54.0
[0.53.0]: https://github.com/marrasen/aprot/compare/v0.52.0...v0.53.0
[0.52.0]: https://github.com/marrasen/aprot/compare/v0.51.0...v0.52.0
[0.51.0]: https://github.com/marrasen/aprot/compare/v0.50.0...v0.51.0
[0.50.0]: https://github.com/marrasen/aprot/compare/v0.49.1...v0.50.0
[0.49.1]: https://github.com/marrasen/aprot/compare/v0.49.0...v0.49.1
[0.49.0]: https://github.com/marrasen/aprot/compare/v0.48.0...v0.49.0
[0.48.0]: https://github.com/marrasen/aprot/compare/v0.47.1...v0.48.0
[0.47.1]: https://github.com/marrasen/aprot/compare/v0.47.0...v0.47.1
[0.47.0]: https://github.com/marrasen/aprot/compare/v0.46.0...v0.47.0
[0.46.0]: https://github.com/marrasen/aprot/compare/v0.45.0...v0.46.0
[0.45.0]: https://github.com/marrasen/aprot/compare/v0.44.0...v0.45.0
