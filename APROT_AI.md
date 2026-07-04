# aprot AI Guide

A high-performance Go library for type-safe real-time APIs with automatic TypeScript/React client generation. WebSocket and SSE+HTTP transports; optional REST/OpenAPI on top.

This guide is a fast-reference for agents. The authoritative overview is `doc.go` (pkg.go.dev landing page).

## Core Concepts

- **Registry** — collects handler groups, push events, enums, custom errors, and a validator.
- **Handlers** — methods on a struct; one TypeScript file per group.
- **Transports** — WebSocket (`/ws`), SSE+HTTP (`/sse`), and optional REST (`NewRESTAdapter`).
- **Middleware** — `func(next) Handler` wrappers; server-level or per-handler-group.
- **Tasks** — hierarchical progress and output streaming (request-scoped or shared).
- **Subscription Refresh** — query handlers register trigger keys; mutations fire them to push fresh results.
- **Streaming** — handlers can return `iter.Seq[T]` / `iter.Seq2[K,V]`; clients consume as `AsyncIterable`.
- **Validation & Transforms** — `validate:"…"` and `transform:"…"` struct tags applied before dispatch.
- **REST + OpenAPI + Zod** — same handlers, additional surfaces opted in per registry/handler.

## Implementation Checklist

### 1. Define API (Go)
- Handler methods on a struct: `func (h *T) Method(ctx context.Context, [params]) ([res], error)`.
- Parameters are positional — each Go param becomes a separate TS argument; names come from AST.
- Return shapes supported: `error` (void), `(T, error)`, `(iter.Seq[T], error)`, `(iter.Seq2[K,V], error)`.
- Register with `registry.Register(handlers, [middleware...])`.

### 2. Server Setup
```go
server := aprot.NewServer(registry)
http.Handle("/ws", server)                   // WebSocket
http.Handle("/sse", server.HTTPTransport())  // SSE+HTTP
http.Handle("/sse/", server.HTTPTransport())
http.Handle("/api/", aprot.NewRESTAdapter(registry)) // optional REST
```

Hardening (all optional, sane defaults, `-1` disables): `aprot.ServerOptions{MaxMessageSize, WriteTimeout, PingInterval, PongTimeout, MaxConcurrentRequests, MaxServerConcurrentRequests, MaxSubscriptions}` — inbound size limit (default 4 MiB), stalled-peer write timeout (30s), WS keepalive ping (30s) and pong timeout (60s), in-flight requests per connection (256) and server-wide (10000), and active subscriptions per connection (1024). Exceeding a concurrency cap rejects the frame with `CodeTooManyRequests` (-32004; client `err.isTooManyRequests()`) instead of spawning unbounded goroutines; a streaming handler holds its request slot until the stream ends. Handler panics are recovered per request and returned as internal errors.

**Cookie-auth deployments must set an origin check** (default allows all origins — CSWSH risk): `server.SetCheckOrigin(func(r *http.Request) bool { return r.Header.Get("Origin") == "https://app.example.com" })`.

**CORS for SSE/REST** (plain HTTP, so origin check doesn't apply): wrap the transport with `aprot.CORS(aprot.CORSOptions{AllowedOrigins, AllowedMethods, AllowedHeaders, ExposedHeaders, AllowCredentials, MaxAge})` — a `func(http.Handler) http.Handler` closed by default. Handles `OPTIONS` preflight (204). `AllowCredentials: true` must pair with explicit (non-`*`) origins; with `*`+credentials it echoes the concrete origin. E.g. `http.Handle("/api/", http.StripPrefix("/api", cors(rest)))`, `http.Handle("/sse", cors(server.HTTPTransport()))`.

**First-message auth** (token over the connection, not the URL): `server.OnAuth(func(ctx, conn *aprot.Conn, token string) error { ...; conn.SetUserID(id); return nil })` — return `aprot.ErrAuthFailed(msg)` (code -32005) to reject. Registering a hook makes every new connection **pending**: it must send `{"type":"auth","token":"..."}` and get `auth_ok` before any request/subscribe runs; earlier frames get `auth_error`; a connection unauthenticated within `ServerOptions.AuthTimeout` (default 10s, `-1` disables) is closed. The same auth frame on a live connection refreshes identity (a failed refresh keeps the session — no downgrade). WebSocket sends the frame directly; SSE sends it in the first `POST /rpc` body (`{type:"auth",connectionId,token}`), with `auth_ok`/`auth_error` over the stream. No hook → unchanged (URL-token via `OnConnect` still works). TS client: `new ApiClient(url, {getAuthToken: () => token})` and `client.refreshAuth(token)`; `err.isAuthFailed()`.

**Observability**: set `ServerOptions.Observer` to an `aprot.Observer` (embed `aprot.NoopObserver`, override what you need) — nil disables it with zero hot-path cost. Events: `ConnectionOpened/Closed(*Conn)`, `RequestCompleted(RequestEvent{Method, Subscribe, Duration, Code})` (Code 0 = success; fires for client requests/subscribes, not server refreshes), `SubscriptionRegistered/Unregistered(*Conn, method, id)`, `RefreshFanout(key, matched)`, `SendBufferFull(*Conn)` (backpressure; frame not dropped), `WriteTimedOut(*Conn)`. Callbacks are synchronous on hot paths + may run concurrently — keep them fast/non-blocking. Pull-based gauges: `server.Stats()` → `ServerStats{Connections, Subscriptions}`. Streaming handlers hold their slot until stream end; `RequestCompleted.Duration` spans the full iteration.

### 3. TypeScript Generation
```go
gen := aprot.NewGenerator(registry).WithOptions(aprot.GeneratorOptions{
    OutputDir: "./client/src/api",
    Mode:      aprot.OutputReact,   // or aprot.OutputVanilla
    Naming:    aprot.DefaultNaming{FixAcronyms: true},
    Zod:       true,                // emit *.schema.ts mirrors of validate tags
})
gen.Generate()
```
Output is split: `client.ts` (base), one file per handler group, and a `{pkg}.ts` file per Go package for types/enums shared across groups. If a package's shared file would collide with a handler file (package `settings` + handler `Settings` both → `settings.ts`), the shared file is emitted as `{pkg}.types.ts` instead so neither silently overwrites the other.

`Generate()` deletes stale output: top-level `.ts` files in `OutputDir` starting with the `// Code generated by aprot. DO NOT EDIT.` marker that the current run didn't produce (leftovers from renamed/removed handler groups) are removed so they can't break the TS build. Hand-written files, non-`.ts` files, and subdirectories are untouched.

### 4. Connect the Client (TypeScript) — REQUIRED

**⚠️ The generated client NEVER connects automatically.** `new ApiClient(...)` only configures it, and React's `<ApiClientProvider>` is a plain context provider — neither opens the connection. Skipping `client.connect()` is the single most common integration mistake: every request, hook, and subscription then fails with a `ConnectionError` whose message is `'Not connected: call client.connect() first — the client never connects automatically'`.

```ts
import { ApiClient, getWebSocketUrl } from './api/client';

const client = new ApiClient(getWebSocketUrl());
client.connect(); // REQUIRED once. `await` optional: requests issued while connecting are buffered and flushed on ready.
```

React: create the client at module scope, call `client.connect()` there (or in a `useEffect`), then mount `<ApiClientProvider value={client}>`. `connect()` is a no-op when already connected/connecting; after the first successful call, auto-reconnect handles drops — never call it again for reconnection.

### 5. Progress & Tasks
- Simple: `aprot.Progress(ctx).Update(current, total, message)`.
- Hierarchical: `tasks.SubTask(ctx, title, fn)`.
- Server-wide: `tasks.StartTask[Meta](ctx, title, tasks.Shared())`. Start it on `context.WithoutCancel(ctx)` for a fire-and-forget task that outlives the handler — then the goroutine must finish it via `task.Close()` / `task.Fail()` / `task.Err()`.
- Enable: `tasks.Enable(registry)` before generation/serving.
- Lifecycle middleware (opt-in): `tasks.Enable(registry, tasks.WithTaskMiddleware(mw))`.
  - `TaskMiddleware = func(ctx context.Context, info TaskInfo, next func(context.Context) error) error` — same shape as `aprot.Middleware`. Decorate ctx, call next, observe err. The decorated ctx propagates to the task body and all nested subtasks.
  - `TaskInfo`: `{ID, Title, ParentID string}` (ParentID empty for root tasks).
  - For scope-based tasks (`SubTask`, `SharedSubTask`) the middleware runs synchronously around fn. For manual-lifecycle tasks (`StartTask`, `OutputWriter`, `WriterProgress`, `Task.SubTask`, `TaskSub.SubTask`) the middleware runs in a dedicated goroutine; `next()` blocks until `Close`/`Fail` is called on the returned task handle. Cancellation surfaces as `err.Error() == "canceled"`.
  - Subtasks created via `Task.SubTask` (no ctx parameter) inherit the parent's middleware ctx, so logger decorations chain through nested calls.
- Cancel is owner-only: `tasks.CancelSharedTask(ctx, id)` (and the generated `CancelTask` handler) only cancels a task created by the calling connection; others get `CodeForbidden`.
- `StartTask` never returns nil. On transports with no client channel (e.g. the REST adapter — no connection/manager on ctx) it returns a no-op `*Task`, so `Progress`/`Output`/`SetMeta`/`Close` are safe but undelivered.

## Handlers

### Signatures
```go
func (h *H) CreateUser(ctx context.Context, req *CreateUserRequest) (*User, error)
func (h *H) DeleteUser(ctx context.Context, id string) error                      // void
func (h *H) ListUsers(ctx context.Context) ([]User, error)                        // no params
func (h *H) Add(ctx context.Context, a int, b int) (*SumResult, error)            // arbitrary params
```

### Streaming (`iter.Seq` → `AsyncIterable`)
```go
func (h *H) ListUsers(ctx context.Context) (iter.Seq[*User], error) {
    return func(yield func(*User) bool) {
        for cursor.Next(ctx) {
            var u User
            cursor.Scan(&u)
            if !yield(&u) { return } // client canceled
        }
    }, nil
}
```
Streaming handlers cannot be exposed via REST (panics at registration). For SSE/WS only. To observe real stream end from middleware, use `aprot.OnStreamComplete(ctx, fn)` — it fires once with `(err, items)`.

## Input Transformation

`transform:"…"` ops run after JSON decoding, before validation. Statically checked at `Register` time.

```go
type SignupRequest struct {
    Email    string   `json:"email"    transform:"trim,lowercase" validate:"required,email"`
    Username string   `json:"username" transform:"trim"            validate:"required,min=3"`
    Slug     string   `json:"slug"     transform:"trimleft=/,lowercase"`
    Tags     []string `json:"tags"     transform:"trim,removeempty"`
}
```

Ops: `trim`, `trimleft[=cutset]`, `trimright[=cutset]`, `uppercase`, `lowercase`, `removeempty` (`[]string` only). Applies to `string`, `*string`, `[]string`, and recurses into nested structs/slices.

## Request Validation

Opt in per registry. Failures surface as `CodeValidationFailed` with structured `[]FieldError` payload — TS client exposes via `ApiError`.

```go
type CreateUserRequest struct {
    Name  string `json:"name"  validate:"required,min=2,max=100"`
    Email string `json:"email" validate:"required,email"`
    Age   int    `json:"age"   validate:"gte=13,lte=120"`
}

registry.Register(&Handlers{})
registry.SetValidator(aprot.NewPlaygroundValidator())
```

`Zod: true` in `GeneratorOptions` mirrors these rules into a `*.schema.ts` file per group.

## Middleware

```go
func MyMiddleware() aprot.Middleware {
    return func(next aprot.Handler) aprot.Handler {
        return func(ctx context.Context, req *aprot.Request) (any, error) {
            res, err := next(ctx, req)
            return res, err
        }
    }
}

server.Use(MyMiddleware())                          // all handlers
registry.Register(&AdminHandlers{}, AuthMiddleware()) // group-only
```

For streaming handlers, `next()` returns as soon as the iterator is constructed — register a completion callback for real end-of-stream observability:

```go
aprot.OnStreamComplete(ctx, func(err error, items int) {
    log.Printf("stream done: items=%d err=%v", items, err)
})
```

`OnStreamComplete` is a no-op on unary contexts, so the same middleware works for both kinds.

### Logging request params

`req.Params` is the raw JSON wire payload (`jsontext.Value`) — log it directly with `%s`. Two pitfalls:

- **Secrets leak.** Login / API-key / token handlers will dump plaintext credentials unless you redact.
- **Params are positional.** Wire format is a JSON array `[arg0, arg1, ...]` in source order. For `Login(username, password string)` the wire is `["alice","hunter2"]` — no `password` key to match on, so key-based redaction doesn't help. Use a method skip list instead.

The vanilla example ships a reference logger with both strategies — copy it as a starting point:

```go
// example/vanilla/api/middleware.go
server.Use(api.LoggingMiddleware(api.DefaultLoggingOptions()))

// or customize:
server.Use(api.LoggingMiddleware(api.LoggingOptions{
    LogParams:   true,
    RedactKeys:  []string{"password", "token", "secret", "api_key", "authorization"},
    SkipMethods: []string{"PublicHandlers.Login"}, // entire params hidden
    MaxParamLen: 1024,
}))
```

Behavior: `RedactKeys` matches object keys case-insensitively and recurses into nested objects/arrays, replacing the value with `"[REDACTED]"`. `SkipMethods` (fully-qualified `Struct.Method`) replaces the entire params field with `[REDACTED]`. `MaxParamLen` truncates over-long JSON with `...(truncated)`. The zero `LoggingOptions{}` (or no arg) keeps the original method/duration-only line.

## Connection Lifecycle

```go
server.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
    session, err := validateSession(conn.Info().Cookies)
    if err != nil {
        return aprot.ErrConnectionRejected("invalid session")
    }
    conn.SetUserID(session.UserID)
    conn.Set(principalKey{}, session.User)
    return nil
})
```

`Conn` exposes:
- `Info()` — HTTP request snapshot at connection time.
- `Set(key, val)` / `Get(key)` / `Load(key)` — per-connection state.
- `SetUserID(id)` / `UserID()` — push routing identity (not a security boundary; use stored principal for authz).

Inside handlers: `conn := aprot.Connection(ctx)`.

## Push Events

```go
registry.RegisterPushEventFor(&PublicHandlers{}, UserCreatedEvent{})
server.Broadcast(&UserCreatedEvent{ID: "1"})
server.PushToUser("user_123", &NotificationEvent{Message: "hello"})
```

Event name on the wire is the Go type name. Subscribed via the generated per-event hook (React) or the `onUserCreatedEvent`-style standalone (vanilla) — generated names match the Go event type name exactly.

## Subscription Refresh

Push fresh query results to subscribed clients when related data changes.

```go
// Query handler — declare the trigger keys this query depends on.
func (h *H) ListUsers(ctx context.Context) ([]User, error) {
    aprot.RegisterRefreshTrigger(ctx, "users")
    return h.db.ListUsers(ctx)
}

// Mutation handler — fire keys to refresh subscribed clients.
func (h *H) CreateUser(ctx context.Context, req *CreateReq) (*User, error) {
    user, err := h.db.CreateUser(ctx, req)
    if err != nil { return nil, err }
    aprot.TriggerRefresh(ctx, "users")
    return user, nil
}
```

- `TriggerRefresh` batches and dedupes within a request.
- `TriggerRefreshNow` flushes immediately (use in long-running handlers between observable state transitions).
- `Server.TriggerRefresh(keys...)` for background goroutines / cron / webhooks — flushes immediately, no request context required.
- `RegisterRefreshTrigger` is a no-op outside subscribe; package-level `TriggerRefresh` is a no-op outside a request context.

## REST + OpenAPI

```go
registry.Register(&UserHandlers{})       // WebSocket only
registry.RegisterREST(&TodoHandlers{})   // REST only
registry.Register(&BothHandlers{})       // WebSocket...
registry.EnableREST(&BothHandlers{})     //   ...and also REST

http.Handle("/api/", aprot.NewRESTAdapter(registry))

oag := aprot.NewOpenAPIGenerator(registry, "My API", "1.0.0")
spec, _ := oag.Generate()                // or oag.GenerateJSON()
```

HTTP method/path derive from method name (e.g. `CreateUser` → `POST /users/create-user`). Streaming handlers cannot be REST-exposed (panic at registration). Doc comments on handlers/structs/fields flow into OpenAPI `summary`/`description`/JSON Schema.

## Error Handling

### Server-side
```go
return aprot.ErrUnauthorized("invalid token")     // -32001
return aprot.ErrForbidden("access denied")        // -32003
return aprot.ErrInvalidParams("name required")    // -32602
return aprot.ErrInternal(err)                     // -32603

registry.RegisterError(sql.ErrNoRows, "NotFound") // → ApiError.isNotFound() in TS
```

### Client-side: `ApiError` vs `ConnectionError`
Two distinct error types on the generated TS client:

- **`ApiError`** — structured server-side error response. Has `.code`, `.message`, optional `.data`. Helpers: `err.isNotFound()`, `err.isUnauthorized()`, etc. Validation failures arrive here with `CodeValidationFailed` and `data: FieldError[]`.
- **`ConnectionError extends Error`** — connection-level failure. Has `.reason` for switching on user-facing UI:

  | reason            | meaning                                                                      |
  | ----------------- | ---------------------------------------------------------------------------- |
  | `offline`         | `navigator.onLine` was false at failure time                                 |
  | `server-rejected` | server sent `ApiError` with code `ConnectionRejected`; original on `.cause`  |
  | `server-closed`   | clean post-upgrade close; `.closeCode` and `.closeReason` carry CloseEvent   |
  | `network-error`   | pre-upgrade failure or close code 1006 (refused/unreachable/TLS/HTTP error)  |
  | `manual`          | caller invoked `client.disconnect()`                                         |

In-flight requests reject with a `ConnectionError` when the connection drops; calls issued while disconnected reject with the most recent one. Use:
```ts
client.onConnectionError(err => showBanner(err.reason));
const last = client.getLastConnectionError(); // or null
```
The `'server-rejected'` bucket is also surfaced via the older `onConnectionRejected` `ApiClientOption` callback (kept for backward compatibility).

## TypeScript Hooks (React Output)

aprot generates one hook per handler named after the handler itself: `useXxx()` for query/subscription handlers **and** stream handlers (e.g. stream handler `Numbers` → `useNumbers()`), plus `useXxxEvent()` for push events (named after the Go event type, e.g. `UserCreatedEvent` → `useUserCreatedEvent()`). There is **no per-handler mutation hook** — call the generated async function directly, or use the query hook's `mutate(action)` helper to compose a mutation with a refetch.

All hooks require a manually connected client: `client.connect()` must have been called (see Quickstart §4 — `<ApiClientProvider>` does not connect for you).

```tsx
import {
  useListUsers, useGetUser, createUser, deleteUser,
  useUserCreatedEvent,
} from './api/handlers';
import { useNumbers } from './api/streaming-handlers';
import { useApiClient } from './api/client';

const { data, isLoading, error, refetch, mutate, cancel } = useListUsers();
const { data: user } = useGetUser(userId);                          // re-fetches when userId changes

const { items, done, error, isLoading, restart, cancel } = useNumbers(10, 100);
const { lastEvent, events, clear } = useUserCreatedEvent();
```

### How to perform a mutation

Two patterns. Pick by intent.

**Pattern 1 — query-scoped `mutate(action)` (refetch on completion).** Every `useXxx()` query hook returns a `mutate` helper that accepts either a `Promise` or a `(client: ApiClient) => Promise<unknown>` thunk. It runs the action, captures any thrown error in `error`, and refetches this query on success — sharing the query's `isLoading` / `error` state. Use this whenever a mutation should refresh the list it lives next to:

```tsx
import { addTodo } from './api/todos';

function TodoList() {
  const { data, mutate, isLoading, error } = useListTodos();

  return (
    <>
      <button
        disabled={isLoading}
        onClick={() => mutate((client) => addTodo(client, { title: 'Buy milk' }))}
      >
        Add
      </button>
      {error && <p>{error.message}</p>}
      <ul>{data?.todos.map(t => <li key={t.id}>{t.title}</li>)}</ul>
    </>
  );
}
```

`mutate` accepts the action as `(client) => Promise<unknown>` so you don't need to call `useApiClient()` separately at the call site — the hook injects its own client. A bare `Promise<unknown>` is also accepted when you've already started the work (e.g. `mutate(Promise.all([…]))`).

Server-side `aprot.TriggerRefresh(...)` already pushes refreshed data to subscribed queries, so for many flows you don't *need* the explicit refetch. Reach for Pattern 1 specifically when you want the same component's loading state to cover both the mutation and the refresh.

**Pattern 2 — raw async function via `useApiClient()`.** Generated standalone functions throw on failure (`ApiError` for protocol-level errors, `ConnectionError` for transport-level). Use them when there is no query context (no list to refetch), or when you need conditional try/catch logic:

```tsx
import { useApiClient, ApiError } from './api/client';
import { addTodo } from './api/todos';

function AddTodoButton() {
  const client = useApiClient();
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<Error | null>(null);

  const onClick = async () => {
    setIsLoading(true);
    setError(null);
    try {
      await addTodo(client, { title: 'Buy milk' });
    } catch (err) {
      setError(err as Error);
      if (err instanceof ApiError && err.isValidationFailed()) {
        // field-level UI
      }
    } finally {
      setIsLoading(false);
    }
  };

  return <button onClick={onClick} disabled={isLoading}>Add</button>;
}
```

You manage `isLoading` / `error` / `AbortController` yourself. The trade-off is honest: the generated function is a typed `Promise<TRes>` that resolves with the actual value or throws — no hidden state machine.

**Pattern 3 — global error capture with `<ApiClientErrorProvider>`.** Drop in a provider, read errors from `useApiClientError()`. Inside the provider, `useApiClient()` returns a `Proxy`-wrapped client that reports errors from `request` / `subscribe` / `requestStream` to the provider in addition to throwing as before. Generated hooks (`useQuery`, `mutate`, `useStream`) all flow through `useApiClient()` internally, so their errors surface here too without per-hook wiring.

```tsx
import {
  ApiClient, ApiClientProvider, ApiClientErrorProvider, useApiClientError,
} from './api/client';

function App() {
  return (
    <ApiClientProvider value={client}>
      <ApiClientErrorProvider>
        <ErrorBanner />
        <Page />
      </ApiClientErrorProvider>
    </ApiClientProvider>
  );
}

function ErrorBanner() {
  const { error, source, clear } = useApiClientError();
  if (!error) return null;
  const where = source ? `${source.struct}.${source.method}` : null;
  return (
    <div role="alert">
      {where ? <strong>Error in {where}: </strong> : null}
      {error.message} <button onClick={clear}>Dismiss</button>
    </div>
  );
}
```

`source: { struct, method } | null` is parsed from the wire name on the first dot — `'Todos.CreateTodo'` becomes `{ struct: 'Todos', method: 'CreateTodo' }`. It is `null` exactly when `error` is `null`. Wire names with no dot put the full string in `method` and leave `struct` as `''`.

Latest error wins (newer replaces older); `clear()` resets both `error` and `source` to `null`. The provider does **not** swallow — calls still throw, so `try/catch` and per-hook `error` fields keep working. Without an `<ApiClientErrorProvider>` above, `useApiClient()` returns the raw client and `useApiClientError()` throws — adoption is fully opt-in.

### Migrating from `useXxxMutation()` (removed)

If your codebase still calls `useXxxMutation()`, see `MIGRATION_MUTATION_HOOKS.md` for an agent-runnable rewrite prompt. Quick summary: `useCreateUserMutation()` → either `useApiClient() + createUser(client, …)` (raw, throws) or `useListUsers().mutate((c) => createUser(c, …))` (refetch-after-mutation).

### React 19 Suspense — `useQuerySuspense`
Single generic hook; works with any generated query function. Opens a subscription, suspends until first response, swaps the promise on each push (no re-suspending).
```tsx
import { Suspense } from 'react';
import { useQuerySuspense } from './api/client';
import { listUsers, getUser } from './api/handlers';

function UsersList() {
  const data = useQuerySuspense(listUsers);   // no params
  return data.users.map(u => <li key={u.id}>{u.name}</li>);
}

function UserView({ id }: { id: string }) {
  const user = useQuerySuspense(getUser, id); // typed params
  return <h1>{user.name}</h1>;
}
```
Errors propagate to the nearest error boundary. Mutations stay on the generated async functions (or the query hook's `mutate` helper); streams stay on the generated stream hooks.

### Connection / loading hooks
```tsx
import { useConnection, useIsLoading } from './api/client';

const { isConnected, state } = useConnection(); // 'connecting' | 'connected' | 'reconnecting' | 'disconnected'
const isLoading = useIsLoading();               // any in-flight request anywhere
```

### Generic primitives
```tsx
import { useQuery, useStream, usePushEvent } from './api/client';
```

## Vanilla Output

Per-handler standalone functions plus `subscribe`/`unsubscribe` helpers for live data:

```ts
import { listUsers, subscribeListUsers, onUserCreatedEvent } from './api/handlers';

const users = await listUsers(client);
const unsub = subscribeListUsers(client, (next) => render(next));
const off = onUserCreatedEvent(client, (evt) => append(evt));
```

## Cancel Causes

Inside a handler:
```go
cause := aprot.CancelCause(ctx)
switch {
case errors.Is(cause, aprot.ErrClientCanceled):    // AbortController.abort() / unmount
case errors.Is(cause, aprot.ErrConnectionClosed):  // client disconnected
case errors.Is(cause, aprot.ErrServerShutdown):    // server.Stop in progress
}
```

## SQL Nullable Types

Auto-mapped in TS generation and unwrapped at runtime:
- `sql.NullString` → `string | null`
- `sql.NullInt64`, `NullInt32`, `NullInt16` → `number | null`
- `sql.NullBool` → `boolean | null`
- `sql.NullFloat64` → `number | null`
- `sql.NullTime` → `string | null`
- `sql.Null[T]` (generic) → `T | null` — unwrapped at runtime for the common
  `T` (`string`, `int`, `int64`, `int32`, `int16`, `float64`, `bool`,
  `time.Time`); other instantiations fall back to the `{"V":…,"Valid":…}` object.

Zod schemas mirror these as `T.nullable()`.

Other type-mapping notes:
- Bare pointer `*T` (no `json:,omitempty`) → `T | null` (always sent; null when nil). `*T` with `omitempty` stays optional `?: T`.
- `map[bool]V` → `Partial<Record<"true" | "false", V>>` (boolean isn't a valid TS index type).
- `json.RawMessage` → `unknown`.
- `time.Duration` has no default JSON representation in the v2 encoder and is **rejected at generation time** — add a json format option (e.g. `json:"d,format:nano"`) or use a different type.
- Field names and handler param names that aren't valid TS identifiers are quoted (`"my-field"`) or suffixed (`new_`) automatically.

## Naming Plugins

Configure how generated TS names are formatted:
```go
gen.WithOptions(aprot.GeneratorOptions{
    OutputDir: "./client/api",
    Mode:      aprot.OutputReact,
    Naming:    aprot.DefaultNaming{FixAcronyms: true},
})
```

Built-in:
- `DefaultNaming{FixAcronyms: bool}` — kebab-case files, camelCase methods. `FixAcronyms` keeps acronym runs together (`BulkXML` → `bulk-xml`).
- `PreserveNaming` — keeps Go PascalCase names in TS.

Custom:
```go
type NamingPlugin interface {
    FileName(groupName string) string
    MethodName(name string) string
    HookName(name string) string
    HandlerName(eventName string) string
    ErrorMethodName(errorName string) string
}
```

## Graceful Shutdown

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
server.Stop(ctx) // rejects new connections, waits for in-flight, runs OnDisconnect
```

Safe to call multiple times. In-flight handler contexts are canceled with `ErrServerShutdown`.

## Enums

```go
type Status string
const (
    StatusActive  Status = "active"
    StatusExpired Status = "expired"
)
func StatusValues() []Status { return []Status{StatusActive, StatusExpired} }

registry.RegisterEnumFor(&Handlers{}, StatusValues())
// or: registry.RegisterEnum(StatusValues()) for a shared enum
```

Struct fields with enum types generate as the TS union (not raw `string`/`number`).
