# aprot

**A**PI **P**rotocol for **R**eal-time **O**perations with **T**ypeScript

A Go library for building type-safe real-time APIs with automatic TypeScript client generation. Supports both WebSocket and SSE+HTTP transports.

> **Warning**
> This library is currently unstable and under active development. Breaking changes will occur between versions until v1.0.0 is released.

## Showcase

A taste of what writing an aprot app looks like. Define typed handlers in Go, declare refresh triggers, and your React components auto-update — no event wiring, no manual refetch.

**Go — `api/handlers.go`**

```go
// Enums are plain Go types. A Values() function exposes them to codegen,
// which emits a TypeScript const object + union type.
type JobStatus string

const (
    JobStatusPending JobStatus = "pending"
    JobStatusRunning JobStatus = "running"
    JobStatusDone    JobStatus = "done"
)

func JobStatusValues() []JobStatus {
    return []JobStatus{JobStatusPending, JobStatusRunning, JobStatusDone}
}

type Job struct {
    ID     string    `json:"id"`
    Title  string    `json:"title"`
    Status JobStatus `json:"status"`
}

type Handlers struct{ store *Store }

// Query — any client that calls useListJobs() is subscribed to the "jobs"
// trigger key and will auto-refresh whenever it fires.
func (h *Handlers) ListJobs(ctx context.Context) ([]Job, error) {
    aprot.RegisterRefreshTrigger(ctx, "jobs")
    return h.store.All(), nil
}

// Mutation — validation, typed errors, and a refresh that fans out to every
// subscribed client with zero client-side code.
func (h *Handlers) CreateJob(ctx context.Context, title string) (*Job, error) {
    if title == "" {
        return nil, aprot.ErrInvalidParams("title is required")
    }
    job := h.store.Add(title, JobStatusPending)
    aprot.TriggerRefresh(ctx, "jobs")
    return job, nil
}

// Long-running mutation. TriggerRefreshNow flushes the refresh queue
// mid-handler so subscribers observe the "running" state immediately;
// progress.Update streams progress to the caller via onProgress; and the
// final TriggerRefresh is batched to fire when the handler returns.
func (h *Handlers) RunJob(ctx context.Context, id string) error {
    progress := aprot.Progress(ctx)

    h.store.SetStatus(id, JobStatusRunning)
    aprot.TriggerRefreshNow(ctx, "jobs") // subscribers re-render with status=running

    for i := 1; i <= 5; i++ {
        progress.Update(i, 5, fmt.Sprintf("step %d/5", i))
        time.Sleep(200 * time.Millisecond)
    }

    h.store.SetStatus(id, JobStatusDone)
    aprot.TriggerRefresh(ctx, "jobs") // batched; flushed on return
    return nil
}
```

**React — `Jobs.tsx`**

```tsx
import {
    useListJobs,
    createJob,
    runJob,
    JobStatus,
    type JobStatusType,
} from './api/handlers'

export function Jobs() {
    // Subscribed query. Re-renders automatically whenever the server calls
    // TriggerRefresh(ctx, "jobs") — no useEffect, no event listener, no refetch.
    // The hook also exposes `mutate(action)`: a helper that runs an async
    // action and refetches this query on completion, so we can wire the
    // "Add job" button without juggling a separate mutation hook.
    const { data: jobs, isLoading, mutate } = useListJobs()

    if (isLoading) return <p>Loading…</p>

    return (
        <div>
            <button onClick={() => mutate((client) => createJob(client, 'Write the README'))}>
                Add job
            </button>

            <ul>
                {jobs?.map((job) => (
                    <li key={job.id}>
                        <strong>{job.title}</strong> — {labelFor(job.status)}
                        {job.status === JobStatus.Pending && (
                            <button onClick={() => mutate((client) => runJob(client, job.id, {
                                onProgress: (cur, total, msg) => console.log(`${msg} (${cur}/${total})`),
                            }))}>
                                Run
                            </button>
                        )}
                    </li>
                ))}
            </ul>
        </div>
    )
}

function labelFor(status: JobStatusType) {
    switch (status) {
        case JobStatus.Pending: return 'pending'
        case JobStatus.Running: return 'running'
        case JobStatus.Done:    return 'done'
    }
}
```

Open the component in two browser tabs, click "Add job" in one, and the other updates instantly.

## Features

- **Type-safe handlers** — define handlers with any signature; parameters become TypeScript arguments
- **Automatic TypeScript generation** — standalone functions, React hooks, typed errors, enum const objects
- **Streaming handlers** — return `iter.Seq[T]` / `iter.Seq2[K, V]` from Go and the generated client exposes `AsyncIterable<T>`, so UIs can populate lists item-by-item as results arrive
- **Subscription refresh** — server-driven auto-refresh: query handlers declare trigger keys, mutation handlers fire them to push updates to all subscribed clients
- **Query cache** — multiple React components using the same hook share a single server subscription and receive data from a shared cache; configurable per-hook or globally via `setQueryCacheEnabled`
- **Middleware** — server-level and per-handler middleware chains
- **Push events** — broadcast to all clients or target specific users
- **Hierarchical tasks** — nested task trees with progress tracking, streamed to clients (see [`tasks`](https://pkg.go.dev/github.com/marrasen/aprot/tasks) subpackage)
- **Shared tasks** — server-wide tasks visible to all clients with typed metadata
- **Task middleware** — opt-in `WithTaskMiddleware` wraps every task with a single `func(ctx, info, next) error` callback (mirrors `aprot.Middleware`); decorate ctx with your logger of choice (slog, zerolog, zap), observe start/completion/failure in one place
- **Progress reporting** — built-in support for long-running operations
- **Request cancellation** — clients cancel via AbortController; handlers see cancel cause
- **Connection lifecycle** — hooks for connect/disconnect, connection-scoped state, user targeting
- **Dual transport** — WebSocket and SSE+HTTP with identical API
- **Connection hardening** — per-request panic recovery, inbound message size limits, write timeouts that drop stalled clients, WebSocket keepalive pings, and per-connection / server-wide concurrency and subscription caps — all configurable via `ServerOptions`
- **Cross-origin control** — WebSocket origin checking (`SetCheckOrigin`) plus a closed-by-default `CORS` middleware for the SSE and REST HTTP transports
- **First-message auth** — authenticate with a token sent over the connection (`OnAuth`) instead of in the URL, with a pending-auth timeout and mid-session token refresh; works over WebSocket and SSE
- **Observability** — opt-in `Observer` hooks (connections, request latency/errors, subscriptions, refresh fan-out, send-buffer pressure) plus a pull-based `Stats()` snapshot, with zero hot-path cost when unset
- **Automatic reconnection** — page visibility + network-aware, with exponential backoff; supports dynamic URL functions for token refresh on reconnect
- **Struct validation** — opt-in server-side validation via `go-playground/validator` struct tags, automatically enforced before handler dispatch
- **Input transformation** — declarative `transform` struct tags (`trim`, `trimleft`, `trimright`, `uppercase`, `lowercase`, `removeempty`) normalize fields before validation runs
- **Zod schema generation** — opt-in generation of Zod validation schemas alongside TypeScript interfaces
- **REST adapter** — serve handlers as REST/HTTP endpoints alongside WebSocket, with convention-based HTTP method detection and path parameter mapping
- **OpenAPI generation** — opt-in OpenAPI 3.0 spec generation from handler metadata, including validation constraints from struct tags

## Installation

```bash
go get github.com/marrasen/aprot
```

## Documentation

**Go API** — the full reference, usage patterns, and examples live on pkg.go.dev:

- **[`aprot`](https://pkg.go.dev/github.com/marrasen/aprot)** — core library: handlers, registry, server, middleware, subscriptions, code generation
- **[`aprot/tasks`](https://pkg.go.dev/github.com/marrasen/aprot/tasks)** — hierarchical task trees, shared tasks, output streaming

**Generated TypeScript** — the examples include committed generated code you can browse directly:

- **[React client](example/react/client/src/api/)** — query / stream / push hooks plus standalone async functions ([`client.ts`](example/react/client/src/api/client.ts), [`handlers.ts`](example/react/client/src/api/handlers.ts))
- **[Vanilla client](example/vanilla/client/static/api/)** — standalone functions, subscribe helpers ([`client.ts`](example/vanilla/client/static/api/client.ts), [`public-handlers.ts`](example/vanilla/client/static/api/public-handlers.ts))

## Quick Start

### 1. Define handlers

Handler methods accept `context.Context` as the first parameter and return either `error` or `(T, error)`:

```go
package api

import (
    "context"
    "github.com/marrasen/aprot"
)

type CreateUserRequest struct {
    Name  string `json:"name"`
    Email string `json:"email"`
}

type CreateUserResponse struct {
    ID    string `json:"id"`
    Name  string `json:"name"`
    Email string `json:"email"`
}

type UserCreatedEvent struct {
    ID   string `json:"id"`
    Name string `json:"name"`
}

type Handlers struct {
    Broadcaster aprot.Broadcaster
}

func (h *Handlers) CreateUser(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error) {
    resp := &CreateUserResponse{ID: "1", Name: req.Name, Email: req.Email}
    h.Broadcaster.Broadcast(&UserCreatedEvent{ID: resp.ID, Name: resp.Name})
    return resp, nil
}
```

Parameters are positional — each Go parameter becomes a separate TypeScript argument. Names are extracted from Go source via AST parsing.

### 2. Create the server

```go
package main

import (
    "net/http"
    "github.com/marrasen/aprot"
    "myapp/api"
)

func main() {
    handlers := &api.Handlers{}

    registry := aprot.NewRegistry()
    registry.Register(handlers)
    registry.RegisterPushEventFor(handlers, api.UserCreatedEvent{})

    server := aprot.NewServer(registry)
    handlers.Broadcaster = server

    http.Handle("/ws", server)                    // WebSocket
    http.Handle("/sse", server.HTTPTransport())   // SSE+HTTP
    http.Handle("/sse/", server.HTTPTransport())
    http.ListenAndServe(":8080", nil)
}
```

### 3. Generate the TypeScript client

```go
//go:build ignore

package main

import (
    "github.com/marrasen/aprot"
    "myapp/api"
)

func main() {
    handlers := &api.Handlers{}
    registry := aprot.NewRegistry()
    registry.Register(handlers)
    registry.RegisterPushEventFor(handlers, api.UserCreatedEvent{})

    gen := aprot.NewGenerator(registry).WithOptions(aprot.GeneratorOptions{
        OutputDir: "../../client/src/api",
        Mode:      aprot.OutputReact, // or aprot.OutputVanilla
    })
    gen.Generate()
}
```

`Generate()` also removes stale output: any top-level `.ts` file in `OutputDir` that starts with the `// Code generated by aprot. DO NOT EDIT.` marker but wasn't produced by the current run (e.g. from a handler group you renamed or removed) is deleted, so leftovers can't break the TypeScript build. Hand-written files, non-`.ts` files, and subdirectories are never touched.

### 4. Use from TypeScript

**React:**

```tsx
import { ApiClient, ApiClientProvider, getWebSocketUrl } from './api/client';
import { useListUsers, createUser } from './api/handlers';

const client = new ApiClient(getWebSocketUrl());
client.connect(); // REQUIRED — the client never connects automatically

function App() {
    return (
        <ApiClientProvider value={client}>
            <UsersList />
        </ApiClientProvider>
    );
}

function UsersList() {
    // useListUsers() returns the live query plus a `mutate(action)` helper.
    // mutate runs an async action and refetches this query on completion;
    // its error/loading state is shared with the query itself.
    const { data, isLoading, error, mutate } = useListUsers();

    if (error) return <div>Error: {error.message}</div>;
    if (isLoading) return <div>Loading...</div>;

    return (
        <>
            <button onClick={() => mutate((c) => createUser(c, { name: 'New User' }))}>
                Add User
            </button>
            <ul>{data?.users.map(u => <li key={u.id}>{u.name}</li>)}</ul>
        </>
    );
}
```

> **⚠️ The client never connects automatically.** `new ApiClient(...)` only configures it, and `<ApiClientProvider>` is a plain context provider — neither opens the connection. If you skip `client.connect()`, nothing works: every hook and request fails with a `ConnectionError` (`'Not connected'`). Calling `client.connect()` without `await` (e.g. at module scope, as above) is fine — requests issued while connecting are buffered and flushed once the connection is ready. After the first successful `connect()`, auto-reconnect handles drops.

> aprot does **not** generate per-handler mutation hooks. Either compose mutations through a query hook's `mutate(action)` helper (above), or call the generated function directly with `useApiClient()` (see [TypeScript Mutation Patterns](#typescript-mutation-patterns)). If you're upgrading from a version that generated `useXxxMutation()`, see [`MIGRATION_MUTATION_HOOKS.md`](MIGRATION_MUTATION_HOOKS.md) for an agent-runnable rewrite prompt.

**React Suspense (`useQuerySuspense`)** — pairs aprot's generated query functions with React 19's `use()` hook for components that prefer declarative loading boundaries:

```tsx
import { Suspense } from 'react';
import { ApiClient, ApiClientProvider, getWebSocketUrl, useQuerySuspense } from './api/client';
import { listUsers, getUser } from './api/handlers';

function UsersList() {
    const data = useQuerySuspense(listUsers);            // no params
    return <ul>{data.users.map(u => <li key={u.id}>{u.name}</li>)}</ul>;
}

function UserView({ id }: { id: string }) {
    const user = useQuerySuspense(getUser, id);          // typed params
    return <h1>{user.name}</h1>;
}

function App() {
    return (
        <ApiClientProvider value={client}>
            <Suspense fallback={<p>Loading…</p>}>
                <UsersList />
            </Suspense>
        </ApiClientProvider>
    );
}
```

A single generic hook handles every handler — no per-method Suspense hook is generated. The hook opens a server subscription on first read (using the same `TriggerRefresh` machinery as `useQuery`), suspends until the first response arrives, then replaces the cached promise on each subsequent push so live updates flow without re-suspending. Errors propagate to the nearest error boundary. Requires React 19+.

**With auth tokens** (dynamic URL for automatic token refresh on reconnect):

```typescript
const client = new ApiClient(async () => {
    const token = await getAuthToken();
    return `${getWebSocketUrl()}?token=${encodeURIComponent(token)}`;
});
await client.connect();
```

**Vanilla:**

```typescript
import { ApiClient, getWebSocketUrl } from './api/client';
import { createUser, onUserCreatedEvent } from './api/public-handlers';

const client = new ApiClient(getWebSocketUrl());
await client.connect(); // REQUIRED — the client never connects automatically

const user = await createUser(client, { name: 'Alice', email: 'alice@example.com' });

onUserCreatedEvent(client, (event) => {
    console.log('User created:', event);
});
```

## TypeScript Mutation Patterns

aprot deliberately generates **only query / stream / push hooks** — there is no `useXxxMutation()` hook. Two patterns cover every mutation:

### Pattern 1 — query-scoped `mutate(action)` (refetch on completion)

Every `useXxx()` query hook returns a `mutate` helper alongside `data` / `isLoading` / `error`. It accepts either a `Promise` or a `(client: ApiClient) => Promise<unknown>` thunk, runs the action, and refetches the query on success. The action's errors are captured in the hook's `error` field, and `isLoading` covers both the action and the subsequent refetch.

```tsx
import { useListTodos } from './api/todos';
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

The thunk receives the `ApiClient` instance the hook is already bound to, so you don't need to call `useApiClient()` separately. A bare `Promise` is also accepted — useful when composing multiple operations: `mutate(Promise.all([addTodo(c, a), addTodo(c, b)]))`.

Server-side `aprot.TriggerRefresh(...)` already pushes refreshed data to subscribed queries, so for many flows you don't need the explicit refetch. Reach for Pattern 1 when you want the same component's loading indicator to cover both the action and the refresh.

### Pattern 2 — raw async function via `useApiClient()`

When there's no surrounding query (no list to refetch), call the generated function directly. Generated standalone functions are typed `Promise<TRes>` that throw `ApiError` (protocol error) or `ConnectionError` (transport error) on failure — no hidden state machine, no ambiguous return values.

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
            const todo = await addTodo(client, { title: 'Buy milk' });
            toast(`Created ${todo.id}`);
        } catch (err) {
            setError(err as Error);
            if (err instanceof ApiError && err.isValidationFailed()) {
                // field-level error UI
            }
        } finally {
            setIsLoading(false);
        }
    };

    return <button onClick={onClick} disabled={isLoading}>Add</button>;
}
```

You manage `isLoading` / `error` / `AbortController` yourself. The trade-off is honest: the function does what its type says.

### Why no mutation hooks?

A previous version of aprot generated `useXxxMutation()` hooks whose `mutate()` swallowed errors and returned `undefined as TRes` on failure. The `Promise<TRes>` type lied at runtime; `void` mutations couldn't distinguish success from "not yet called"; and the only correct after-success pattern (`useEffect([data])`) was non-obvious. The two patterns above cover the same ground without the foot-guns. See [`MIGRATION_MUTATION_HOOKS.md`](MIGRATION_MUTATION_HOOKS.md) for a rewrite prompt that AI agents can run against an existing codebase.

### Catching errors globally with `<ApiClientErrorProvider>`

Patterns 1 and 2 wire errors per call site. When a component (or app) fans out to many hooks and ad-hoc client calls and just wants a single place to surface failures, drop in `<ApiClientErrorProvider>` and read from `useApiClientError()`:

```tsx
import {
  ApiClient, ApiClientProvider, ApiClientErrorProvider,
  useApiClient, useApiClientError,
} from './api/client';

const client = new ApiClient(`ws://${location.host}/ws`);

function App() {
    return (
        <ApiClientProvider value={client}>
            <ApiClientErrorProvider>
                <ErrorBanner />
                <UsersPanel />
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

Inside the provider, `useApiClient()` returns a `Proxy`-wrapped client whose `request`, `subscribe`, and `requestStream` calls report errors to the provider — in addition to throwing / re-surfacing them as before. Generated hooks (`useListUsers`, `useQuery`, `mutate`, `useStream`) all retrieve their client through `useApiClient()` internally, so their errors flow up too without any per-hook wiring.

Alongside `error`, `useApiClientError()` returns a `source: { struct, method } | null` parsed from the wire name (e.g. `'Todos.CreateTodo'` → `{ struct: 'Todos', method: 'CreateTodo' }`), so a banner can name the call that failed. `source` is `null` exactly when `error` is `null`. If a caller invokes the client with a wire name that has no dot, `struct` is `''` and `method` holds the full name.

Only the latest error is held; `clear()` resets it. The provider observes errors but does **not** swallow them — wrapped client calls still throw, so per-hook `error` fields and explicit `try/catch` keep working. Without an `<ApiClientErrorProvider>` above, `useApiClient()` returns the raw client unchanged and `useApiClientError()` throws — adoption is fully opt-in.

## Streaming Handlers

Handlers can return `iter.Seq[T]` (Go 1.23+) and each yielded value is delivered to the client as a separate websocket message. The generated TypeScript function returns an `AsyncIterable<T>`, so you can consume results with `for await` and render them as they arrive — ideal for progressive list population, large result sets, or any handler whose output is naturally lazy.

```go
import "iter"

type User struct {
    ID   string `json:"id"`
    Name string `json:"name"`
}

func (h *Handlers) ListUsers(ctx context.Context) (iter.Seq[*User], error) {
    return func(yield func(*User) bool) {
        for rows.Next() {
            var u User
            if err := rows.Scan(&u.ID, &u.Name); err != nil {
                return
            }
            if !yield(&u) {
                return // client canceled
            }
        }
    }, nil
}
```

```typescript
import { listUsers } from './api/handlers';

for await (const user of listUsers(client)) {
    appendUserToList(user); // renders each row as it arrives
}
```

The generated function name and signature reflect the Go method's return type — `iter.Seq[T]` produces `AsyncIterable<T>` instead of `Promise<T>`. There's no naming prefix; consumers see `listUsers()` for both unary and streaming handlers, and TypeScript distinguishes them by return type.

React variant — the generated `useListUsers()` hook accumulates items into local state and restarts the stream on parameter change:

```tsx
function UserList() {
    const { items, done, error, isLoading } = useListUsers();
    return (
        <ul>
            {items.map((u) => <li key={u.id}>{u.name}</li>)}
            {isLoading && <li>Loading more…</li>}
            {error && <li>Error: {error.message}</li>}
        </ul>
    );
}
```

Cancellation works exactly like any other request: break out of the `for await` loop, pass an `AbortSignal`, or unmount the React component, and the handler's `ctx` is canceled — the next `yield` returns `false` and the iterator stops. Streaming handlers also support `iter.Seq2[K, V]`, which surfaces as `AsyncIterable<[K, V]>` on the TypeScript side.

Streaming handlers are WebSocket/SSE only. Registering one via `RegisterREST` panics at registration time since REST cannot deliver multi-message responses over a single HTTP request.

### Middleware and streaming handlers

Middleware sees a streaming handler return as soon as the iterator value is in hand — *not* after every row has been streamed. For unary handlers that's the normal post-handler hook point; for streams, the duration and success you see at that moment reflect the time it took to produce the iterator, not the time to drain it. To observe the real end of a stream (duration, item count, cancellation cause, panic), register a callback via `aprot.OnStreamComplete(ctx, fn)`:

```go
func Logging(next aprot.Handler) aprot.Handler {
    return func(ctx context.Context, req *aprot.Request) (any, error) {
        start := time.Now()
        aprot.OnStreamComplete(ctx, func(err error, items int) {
            slog.Info("stream done",
                "method", req.Method,
                "dur", time.Since(start),
                "items", items,
                "err", err)
        })
        result, err := next(ctx, req)
        if info := aprot.HandlerInfoFromContext(ctx); info != nil &&
            info.Kind != aprot.HandlerKindUnary {
            // Streaming handler: the hook above will fire when iteration
            // finishes. Preflight errors (handler returned (nil, err)) never
            // invoke the hook — log them here instead.
            if err != nil {
                slog.Error("stream preflight error",
                    "method", req.Method, "dur", time.Since(start), "err", err)
            }
            return result, err
        }
        slog.Info("unary done",
            "method", req.Method, "dur", time.Since(start), "err", err)
        return result, err
    }
}
```

The `err` passed to the hook distinguishes every termination path:

| Termination | `err` value |
|---|---|
| Clean completion (iterator returned normally) | `nil` |
| Client canceled (`break`, `AbortSignal`, `cancel()`) | `aprot.ErrClientCanceled` |
| Client disconnected mid-stream | `aprot.ErrConnectionClosed` |
| Server shutdown | `aprot.ErrServerShutdown` |
| Handler panicked mid-stream | wrapped recover value |
| Transport send failure | underlying transport error |

Use `errors.Is(err, aprot.ErrClientCanceled)` etc. to distinguish. The hook also receives `items int` — the number of elements successfully yielded to the client (for `iter.Seq2`, each `(key, value)` pair counts as one).

`OnStreamComplete` is a no-op on a unary handler's context — the hooks slot is only populated when the dispatcher sees a streaming return type. Middleware that calls it on every request (as in the example above) works uniformly across both.

## Validation

Add `validate` struct tags to request structs and enable validation on the registry. Validation is opt-in — nothing changes unless you call `SetValidator`.

```go
type CreateUserRequest struct {
    Name  string `json:"name"  validate:"required,min=2,max=100"`
    Email string `json:"email" validate:"required,email"`
    Age   int    `json:"age"   validate:"gte=13,lte=120"`
}

// Enable validation
registry.SetValidator(aprot.NewPlaygroundValidator())
```

Invalid requests are automatically rejected with structured errors (code `-32604`) before reaching your handler. Uses [go-playground/validator](https://github.com/go-playground/validator) — see their docs for the full tag reference.

The structured error payload (a `[]FieldError`) flows through to the generated TypeScript client. Catch `ApiError` and use the exported `getValidationErrors` helper to map server-side failures to per-field UI state without re-implementing the type-narrowing dance:

```typescript
import { ApiError, getValidationErrors } from "./api/client";

try {
    await api.users.update(input);
} catch (err) {
    const fields = getValidationErrors(err);
    if (fields) {
        for (const f of fields) setFieldError(f.field, f.message);
        return;
    }
    if (err instanceof ApiError) {
        // Other server error — generic toast, etc.
    }
    throw err;
}
```

`getValidationErrors` returns `null` for any error that isn't a `CodeValidationFailed` response, so the same form-submit catch block can handle both validation failures and other errors with a single check. The `FieldError` interface mirrors the Go `FieldError` struct in `validate.go`.

### Input transformation

Normalize incoming fields with `transform` struct tags. Transforms run **after** JSON decoding and **before** validation, so rules like `required,min=1` see the cleaned value. No registry setup required — a field is transformed iff it carries a `transform:""` tag.

```go
type SignupRequest struct {
    Email    string   `json:"email"    transform:"trim,lowercase" validate:"required,email"`
    Username string   `json:"username" transform:"trim"            validate:"required,min=3"`
    Slug     string   `json:"slug"     transform:"trimleft=/,lowercase"`
    Tags     []string `json:"tags"     transform:"trim,removeempty"`
}
```

Supported ops, applied in the order listed:

| Op                       | Type         | Behavior                                          |
|--------------------------|--------------|---------------------------------------------------|
| `trim`                   | string       | `strings.TrimSpace`                               |
| `trimleft` / `trimright` | string       | `TrimLeft` / `TrimRight`; optional `=cutset` (defaults to whitespace) |
| `uppercase` / `lowercase`| string       | `strings.ToUpper` / `strings.ToLower`             |
| `removeempty`            | `[]string`   | drop empty elements (apply after `trim` to also drop whitespace-only) |

Transforms recurse into nested structs and slices of structs, and they handle `*string` fields (nil pointers are left alone).

### Zod Schemas

Enable Zod to generate TypeScript validation schemas from the same struct tags:

```go
gen := aprot.NewGenerator(registry).WithOptions(aprot.GeneratorOptions{
    OutputDir: "client/src/api",
    Mode:      aprot.OutputReact,
    Zod:       true, // generates .schema.ts files
})
```

This produces `{handler}.schema.ts` files with Zod schemas for any struct that has `validate` tags:

```typescript
import { z } from 'zod';

export const CreateUserRequestSchema = z.object({
    name: z.string().min(2).max(100),
    email: z.string().min(1).email(),
    age: z.number().int().min(13).max(120),
});
```

A few validate-tag semantics worth knowing when designing your request structs:

- **`omitempty` on a string** (`validate:"omitempty,url,max=500"`) mirrors `go-playground/validator`'s server behavior: the empty string is the Go zero value, so subsequent rules are skipped. The generated Zod schema wraps the chain in `z.union([z.literal(""), ...]).optional()`, so `""` and `undefined` both pass — useful for optional form fields where the browser submits `""` rather than omitting the key. `omitempty` on non-string kinds just appends `.optional()`.
- **`sql.NullString` / `sql.NullInt64` / `sql.NullTime` / `sql.NullBool` / `sql.Null[T]`** are unwrapped on both sides: the TypeScript type becomes `T | null` and the Zod schema becomes `z.<base>().nullable()`, with any `validate` tag constraints still applied to the inner type. The generic `sql.Null[T]` is unwrapped at runtime for the common `T` (`string`, `int`, `int64`, `int32`, `int16`, `float64`, `bool`, `time.Time`); exotic instantiations fall back to the raw `{"V":…,"Valid":…}` object.
- **`oneof` becomes a literal union.** `validate:"oneof=red green blue"` generates `z.enum(["red", "green", "blue"])` (or a `z.union` of `z.literal(...)` for numeric kinds), and `gt`/`lt` on strings and slices are translated to the equivalent length bounds (`z.string()` has no numeric `.gt()`). Map size constraints are dropped (`z.record()` has no length method).
- **`url` and `email` are absolute-only** because that's how `go-playground/validator` defines them upstream. `validate:"url"` rejects app-internal paths like `/images/123/mobile`, and `email` is strict about the formats it accepts. Not an aprot choice — just a footgun to know about so you don't fight it for half an hour. If you need relative URLs, fall back to `validate:"max=500"` or a custom validator.
- **Slice and map element types are substituted into parent schemas.** A field of type `[]EventLink` whose element struct has its own `validate` tags becomes `Links: z.array(EventLinkSchema)` in the parent — not `z.array(z.any())`. Combined with `validate:"dive"` on the parent slice, this means the same `dive` tag drives both server-side element validation (via `go-playground/validator`) and client-side element validation (via the substituted Zod schema). Primitive elements (`[]string`, `[]int`, `map[string]bool`) get a typed `z.array(z.string())` / `z.record(z.string(), z.boolean())` etc. Self-referential and mutually recursive struct references are wrapped in `z.lazy(() => XSchema)` so they load without a temporal-dead-zone error. Slices of slices, slices of maps, and anonymous-struct elements still fall through to `z.any()`.

A few type-mapping rules worth knowing: a bare pointer `*T` (no `json:,omitempty`) becomes `T | null` (it is always sent, as `null` when nil), while `*T` with `omitempty` stays optional `?: T`; `map[bool]V` becomes `Partial<Record<"true" | "false", V>>`; `json.RawMessage` becomes `unknown`; and `time.Duration` is **rejected at generation time** (it has no default JSON representation in the v2 encoder) — add a json format option such as `json:"d,format:nano"` or use a different type.

## Connection Errors

Connection-level failures surface as a structured `ConnectionError` (separate from `ApiError`, which represents a structured server-side error response). It extends `Error`, so existing `instanceof Error` catches keep working — switch on `err.reason` to render appropriate UI:

```typescript
import { ConnectionError } from './api/client';

try {
    await addTodo(client, { title: 'Buy milk' });
} catch (err) {
    if (err instanceof ConnectionError) {
        switch (err.reason) {
            case 'offline':         return showBanner('You appear to be offline.');
            case 'server-rejected': return showError('Session expired. Please sign in.');
            case 'server-closed':   return showError(`Server closed: ${err.closeReason ?? err.closeCode}`);
            case 'network-error':   return showError('Connection failed. Retrying…');
            case 'manual':          return; // we initiated the disconnect
        }
    }
    throw err;
}
```

Reasons:

| Reason | When |
|---|---|
| `offline` | `navigator.onLine` was `false` at failure time. |
| `server-rejected` | The server sent an `ApiError` with code `ConnectionRejected` (e.g. invalid session) before closing. The original `ApiError` is attached as `err.cause`. |
| `server-closed` | Transport closed cleanly after the WebSocket upgrade completed. `err.closeCode` and `err.closeReason` carry the WebSocket `CloseEvent` fields. |
| `network-error` | Pre-upgrade failure or close code 1006 — refused, unreachable, TLS, or HTTP error during the WebSocket upgrade. Browsers deliberately collapse these into one bucket; finer classification is not achievable from JS. |
| `manual` | The caller invoked `client.disconnect()`. |

The same `ConnectionError` instance is delivered to:

- **In-flight `request` / `requestStream` calls** — pending promises reject with it when the connection drops.
- **`request()` issued while disconnected** — rejects synchronously with the most recent `ConnectionError`, falling back to `'offline'` (when `navigator.onLine === false`) or `'manual'`.
- **`onConnectionError(listener)`** — register a listener to drive UI like an "Offline" banner. Multiple listeners supported; returns an unsubscribe function. `getLastConnectionError()` returns the most recently observed error or `null`.

```typescript
const off = client.onConnectionError((err) => {
    if (err.isOffline()) showOfflineBanner();
    else hideOfflineBanner();
});
```

## Server Hardening & Security

The server protects itself from misbehaving clients out of the box, and every limit is configurable through `ServerOptions`:

```go
server := aprot.NewServer(registry, aprot.ServerOptions{
    MaxMessageSize: 1 << 20,          // max inbound WS frame / SSE body size (default 4 MiB)
    WriteTimeout:   10 * time.Second, // drop peers that stop reading (default 30s)
    PingInterval:   30 * time.Second, // WebSocket keepalive ping interval (default 30s)
    PongTimeout:    60 * time.Second, // drop peers with no inbound traffic (default 60s)

    MaxConcurrentRequests:       256,   // in-flight requests per connection (default 256)
    MaxServerConcurrentRequests: 10000, // in-flight requests across all connections (default 10000)
    MaxSubscriptions:            1024,  // active subscriptions per connection (default 1024)
})
```

Set any of these to `-1` to disable it. Defaults apply when the field is zero.

- **Panic recovery** — a panic in a handler (or middleware) is recovered per request and sent to the client as an internal error, mirroring `net/http`. One buggy handler cannot take down the process.
- **Message size limits** — oversized WebSocket frames close the connection; oversized SSE RPC bodies get HTTP 413.
- **Write timeout** — a client that stops reading is disconnected once a write blocks longer than `WriteTimeout`, so it cannot back-pressure broadcasts or other connections.
- **Keepalive** — the server pings on `PingInterval` and drops connections with no inbound traffic for `PongTimeout`, so half-open connections (NATs, dropped Wi-Fi) are cleaned up. `PongTimeout` must exceed `PingInterval`; if it's set lower, `NewServer` clamps it to `2*PingInterval` rather than dropping healthy connections.
- **Concurrency caps** — a single connection can otherwise pin unbounded work: every inbound frame runs on its own goroutine and a connection can register unlimited subscriptions (each amplifying server-side `TriggerRefresh` fan-out). `MaxConcurrentRequests` bounds in-flight requests per connection, `MaxServerConcurrentRequests` bounds them across the whole server, and `MaxSubscriptions` bounds active subscriptions per connection. A frame over a cap is rejected with `CodeTooManyRequests` (`-32004`; the TS client exposes `err.isTooManyRequests()`) rather than spawning more goroutines. A streaming handler holds its request slot until the stream ends.

### Origin checking (cross-site WebSocket hijacking)

By default the WebSocket upgrader accepts **any** `Origin` header so non-browser clients work out of the box. If your deployment authenticates browsers with **cookies**, you must restrict origins — otherwise any website a logged-in user visits can open an authenticated WebSocket to your server from their browser:

```go
server.SetCheckOrigin(func(r *http.Request) bool {
    return r.Header.Get("Origin") == "https://app.example.com"
})
```

Token-based auth (e.g. a token passed via the connection URL) is not affected by this, but setting an origin check is still good hygiene for browser-facing deployments.

### CORS for SSE & REST (cross-origin browser clients)

The SSE and REST endpoints are plain HTTP, so a cross-origin browser app calling them needs CORS response headers and `OPTIONS` preflight handling — the HTTP-transport counterpart to WebSocket origin checking. `aprot.CORS` returns a standard `func(http.Handler) http.Handler` wrapper you can put in front of any transport:

```go
cors := aprot.CORS(aprot.CORSOptions{
    AllowedOrigins:   []string{"https://app.example.com"},
    AllowCredentials: true, // required for cookie-authenticated browsers
    // AllowedMethods / AllowedHeaders / ExposedHeaders / MaxAge optional
})

http.Handle("/api/", http.StripPrefix("/api", cors(rest)))   // REST adapter
http.Handle("/sse", cors(server.HTTPTransport()))            // SSE handler
```

- **Closed by default** — construct the wrapper only where you want cross-origin access; nothing is loosened otherwise. An `Origin` that isn't allowed receives no CORS headers, so the browser blocks the response while same-origin and non-browser clients are unaffected.
- **Credentials caveat** — cookie/`Authorization` requests need `AllowCredentials: true` **paired with explicit origins**. The browser rejects `Access-Control-Allow-Origin: *` alongside credentials, so `CORS` echoes the exact matched origin instead of `*` in that case (same class of concern as the CSWSH note above).
- **Preflight** — `OPTIONS` requests carrying `Access-Control-Request-Method` are answered directly (`204`) with the allowed methods/headers and `Access-Control-Max-Age`; they never reach your handlers.

### First-message authentication

Instead of putting a token in the WebSocket URL (`ws://…/ws?token=<jwt>`) — where it leaks into access/proxy/CDN logs — the client can send it *over the connection* as its first message. Register an `OnAuth` hook to validate it:

```go
server.OnAuth(func(ctx context.Context, conn *aprot.Conn, token string) error {
    claims, err := verify(token)
    if err != nil {
        return aprot.ErrAuthFailed("invalid token")
    }
    conn.SetUserID(claims.Subject)
    return nil
})
```

On the client, supply a token callback — the generated client sends `{type:'auth',token}` and waits for `auth_ok` before flushing any requests:

```typescript
const client = new ApiClient(getWebSocketUrl(), { getAuthToken: () => getToken() });
// mid-session refresh, no reconnect:
await client.refreshAuth(freshToken);
```

- **Pending-auth state** — with a hook registered, a new connection must authenticate before any request/subscribe runs; earlier frames are rejected with `auth_error`, and a connection that doesn't authenticate within `ServerOptions.AuthTimeout` (default 10s, `-1` disables) is closed.
- **Mid-session refresh** — the same `auth` frame on a live connection updates the token/identity without reconnecting. A *failed* refresh keeps the existing session (a live connection is never downgraded).
- **Both transports** — WebSocket sends the `auth` frame directly; SSE sends it in the first `POST /rpc` body (the `EventSource` GET can't set headers). `auth_ok`/`auth_error` arrive over the stream either way.
- **Backward compatible** — with no `OnAuth` hook, connections behave exactly as before (URL-token via `OnConnect` still works). `ErrAuthFailed` uses code `-32005`; the TS client exposes `err.isAuthFailed()`.

## Observability

Wire aprot into Prometheus, OpenTelemetry, or structured logs by registering an `Observer` — aprot takes no dependency on a metrics library, it just calls your callbacks on key events. Observation is **opt-in**: with no observer the hot path allocates nothing.

```go
type metrics struct{ aprot.NoopObserver } // embed NoopObserver, override what you need

func (metrics) RequestCompleted(e aprot.RequestEvent) {
    requestDuration.WithLabelValues(e.Method, strconv.Itoa(e.Code)).Observe(e.Duration.Seconds())
}
func (metrics) SendBufferFull(*aprot.Conn) { sendBufferFull.Inc() } // early backpressure signal

server := aprot.NewServer(registry, aprot.ServerOptions{Observer: metrics{}})
```

Events: `ConnectionOpened` / `ConnectionClosed`, `RequestCompleted` (method, subscribe flag, duration, error code), `SubscriptionRegistered` / `SubscriptionUnregistered`, `RefreshFanout` (trigger key + fan-out size), `SendBufferFull` (slow-consumer backpressure — the precursor to a stalled-client drop), and `WriteTimedOut`.

- **Embed `NoopObserver`** so you implement only the events you care about and stay forward-compatible as new ones are added.
- **Hot-path discipline** — callbacks run synchronously on the server's hot paths and may fire concurrently, so keep them fast and non-blocking; offload heavy work to a goroutine.
- **Cardinality** — `Method` is bounded by your handler set, but per-user / per-connection labels can explode a metrics backend; aggregate those.
- **Gauges** — `server.Stats()` returns a pull-based snapshot (`Connections`, `Subscriptions`) for periodic scraping, rather than tracking those from events.

## REST Adapter

Serve your handlers as REST/HTTP endpoints alongside WebSocket. Use `RegisterREST` for REST-only handlers, or `EnableREST` to expose a WebSocket handler via REST as well:

```go
registry.Register(&handlers)          // WebSocket only
registry.RegisterREST(&todos)         // REST only
registry.Register(&shared)            // WebSocket...
registry.EnableREST(&shared)          // ...and also REST

rest := aprot.NewRESTAdapter(registry)
http.Handle("/api/", http.StripPrefix("/api", rest))
```

Mapping conventions:
- **Group name** → path prefix: `Users` → `/users`
- **Method name** → path segment: `UpdateUser` → `/update-user`
- **Primitive params** → path parameters: `func(ctx, id string, ...)` → `/{id}`
- **Struct param** → JSON request body
- **HTTP method** inferred from name prefix: `Get`/`List` → GET, `Create`/`Add` → POST, `Update` → PUT, `Set` → PATCH, `Delete`/`Remove` → DELETE

Example: `func (h *Users) UpdateUser(ctx context.Context, id string, req *UpdateReq) error` becomes `PUT /users/update-user/{id}` with JSON body.

Access the HTTP request in middleware via `aprot.HTTPRequestFromContext(ctx)`.

## OpenAPI Generation

Generate an OpenAPI 3.0 spec from your handlers. Only handlers registered with `RegisterREST` are included:

```go
oag := aprot.NewOpenAPIGenerator(registry, "My API", "1.0.0")
spec, _ := oag.Generate()     // *OpenAPISpec
data, _ := oag.GenerateJSON() // []byte
```

Use `WithBasePath` when the API is mounted behind a proxy or at a non-root path:

```go
oag := aprot.NewOpenAPIGenerator(registry, "My API", "1.0.0").
    WithBasePath("/rest/api/v1.0")
// paths: "/rest/api/v1.0/todos/create-todo", etc.
```

Validation tags flow into the spec: `validate:"gte=12,lte=110"` → `minimum: 12, maximum: 110`, `validate:"email"` → `format: "email"`.

## Byte-Stream Transport (Electron, stdio, sockets)

`Server.ServeStream` serves a single connection over any `io.ReadWriteCloser` using newline-delimited JSON framing — one protocol message per line, in both directions. It reaches byte streams the HTTP handlers can't: the stdio pipes of a child process, a Unix domain socket, or a Windows named pipe. Stream connections participate fully in hooks, first-message auth, middleware, subscriptions, streaming, and push.

```go
// stdio: the parent process (e.g. an Electron main process that spawned
// this binary) is the single client.
server.ServeStream(ctx, struct {
    io.Reader
    io.WriteCloser
}{os.Stdin, os.Stdout}, aprot.ConnInfo{})

// or one connection per accepted socket (Unix domain socket, named pipe, TCP):
for {
    c, err := ln.Accept()
    if err != nil {
        break
    }
    go server.ServeStream(ctx, c, aprot.ConnInfo{RemoteAddr: c.RemoteAddr().String()})
}
```

`ServeStream` blocks until the connection ends: it returns `nil` on a normal close (peer EOF, `ctx` canceled, server `Stop`), or the rejection error when a connect hook refused the connection. `MaxMessageSize` bounds inbound line length, but the WebSocket keepalive/write-timeout options don't apply — a raw byte stream has no ping frames or deadlines, so liveness is the stream's own lifetime (manage the peer process or socket, and cancel `ctx` to end the connection).

On the client side, the generated `ApiClientOptions.transport` accepts a custom `ClientTransport` instance in addition to the `'websocket' | 'sse'` strings, so the same protocol can ride any message channel:

```ts
import { ApiClient, type ClientTransport } from './api/client';

class BridgeTransport implements ClientTransport {
    // e.g. forward over an Electron preload/MessagePort bridge to the
    // Go child process; deliver inbound lines to onMessage as strings.
    ...
}

const client = new ApiClient('bridge:', { transport: new BridgeTransport() });
client.connect();
```

**Electron note:** a renderer can open `ws://127.0.0.1:<port>` directly, so for most Electron apps the simplest wiring is still a loopback WebSocket to the spawned Go process — pair it with `OnAuth` (a spawn-time random token) and `SetCheckOrigin` so other local processes and browser tabs can't connect. Use `ServeStream` + a custom `ClientTransport` when you want no listening port at all: Go child speaks NDJSON on stdio to the main process, which relays to the renderer over a `MessagePort`.

## Project Structure

```
myapp/
├── api/                      # Shared Go types package
│   ├── types.go              # Request/response structs
│   ├── events.go             # Push event types
│   ├── handlers.go           # Handler implementations
│   ├── middleware.go          # Custom middleware (optional)
│   └── registry.go           # NewRegistry() function
├── server/
│   └── main.go               # Server entry point
├── client/                   # Frontend (separate npm project)
│   ├── package.json
│   ├── src/
│   │   └── api/              # Generated code destination
│   └── ...
└── tools/
    └── generate/
        ├── doc.go            # //go:generate directive
        └── main.go           # Generator script
```

## Examples

### Vanilla Example

```bash
cd example/vanilla
go mod tidy
cd tools/generate && go run main.go    # Generate TypeScript
cd ../../client && npm install && npm run build
cd ../server && go run .
```

### React Example

```bash
cd example/react
go mod tidy
cd tools/generate && go run main.go    # Generate React hooks
cd ../../client && npm install
npm run dev                             # Start Vite dev server
# In another terminal:
cd ../server && go run .
```

## Testing

### Go Unit Tests

```bash
go test ./...
```

### E2E Integration Tests

The `e2e/` directory contains end-to-end tests that run the generated TypeScript client against a live Go server, covering both WebSocket and SSE transports.

```bash
cd e2e/generate && go run main.go       # Generate client
cd .. && npm install && npm test
```

### Formatting

A pre-commit hook ensures Go files are formatted before each commit. Enable it once after cloning:

```bash
git config core.hooksPath .githooks
```

### Linting

```bash
# Go (requires golangci-lint v2)
golangci-lint run ./...

# TypeScript (E2E tests)
cd e2e && npm run lint
```

### CI

GitHub Actions runs five jobs on every push/PR to `master`:

1. **go-fmt** — verifies all Go files are formatted with `gofmt`
2. **go-tests** — Go unit and integration tests with race detection
3. **go-lint** — runs golangci-lint across all Go modules
4. **typescript-compile** — verifies generated TypeScript compiles for both vanilla and React modes
5. **e2e-tests** — runs the full E2E suite (with ESLint) against a live server

## License

MIT
