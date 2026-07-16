# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

This file was introduced at v0.44.0; for the history of earlier releases see the
[git tags](https://github.com/marrasen/aprot/tags) and GitHub releases.

## [Unreleased]

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

[Unreleased]: https://github.com/marrasen/aprot/compare/v0.50.0...HEAD
[0.50.0]: https://github.com/marrasen/aprot/compare/v0.49.1...v0.50.0
[0.49.1]: https://github.com/marrasen/aprot/compare/v0.49.0...v0.49.1
[0.49.0]: https://github.com/marrasen/aprot/compare/v0.48.0...v0.49.0
[0.48.0]: https://github.com/marrasen/aprot/compare/v0.47.1...v0.48.0
[0.47.1]: https://github.com/marrasen/aprot/compare/v0.47.0...v0.47.1
[0.47.0]: https://github.com/marrasen/aprot/compare/v0.46.0...v0.47.0
[0.46.0]: https://github.com/marrasen/aprot/compare/v0.45.0...v0.46.0
[0.45.0]: https://github.com/marrasen/aprot/compare/v0.44.0...v0.45.0
