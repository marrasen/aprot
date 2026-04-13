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
    useRunJobMutation,
    JobStatus,
    type JobStatusType,
} from './api/handlers'
import { useApiClient } from './api/client'

export function Jobs() {
    const client = useApiClient()

    // Subscribed query. Re-renders automatically whenever the server calls
    // TriggerRefresh(ctx, "jobs") — no useEffect, no event listener, no refetch.
    const { data: jobs, isLoading } = useListJobs()

    const runJob = useRunJobMutation({
        onProgress: (current, total, message) =>
            console.log(`${message} (${current}/${total})`),
    })

    if (isLoading) return <p>Loading…</p>

    return (
        <div>
            <button onClick={() => createJob(client, 'Write the README')}>
                Add job
            </button>

            <ul>
                {jobs?.map((job) => (
                    <li key={job.id}>
                        <strong>{job.title}</strong> — {labelFor(job.status)}
                        {job.status === JobStatus.Pending && (
                            <button onClick={() => runJob.mutate(job.id)}>Run</button>
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
- **Progress reporting** — built-in support for long-running operations
- **Request cancellation** — clients cancel via AbortController; handlers see cancel cause
- **Connection lifecycle** — hooks for connect/disconnect, connection-scoped state, user targeting
- **Dual transport** — WebSocket and SSE+HTTP with identical API
- **Automatic reconnection** — page visibility + network-aware, with exponential backoff; supports dynamic URL functions for token refresh on reconnect
- **Struct validation** — opt-in server-side validation via `go-playground/validator` struct tags, automatically enforced before handler dispatch
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

- **[React client](example/react/client/src/api/)** — hooks, mutation helpers, push event subscriptions ([`client.ts`](example/react/client/src/api/client.ts), [`handlers.ts`](example/react/client/src/api/handlers.ts))
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

### 4. Use from TypeScript

**React:**

```tsx
import { ApiClient, ApiClientProvider, getWebSocketUrl, useApiClient } from './api/client';
import { createUser, useListUsers } from './api/handlers';

const client = new ApiClient(getWebSocketUrl());

function App() {
    return (
        <ApiClientProvider value={client}>
            <UsersList />
        </ApiClientProvider>
    );
}

function UsersList() {
    const api = useApiClient();
    const { data, isLoading, error, mutate } = useListUsers();

    if (error) return <div>Error: {error.message}</div>;
    if (isLoading) return <div>Loading...</div>;

    return (
        <>
            <button onClick={() => mutate(createUser(api, { name: 'New User' }))}>
                Add User
            </button>
            <ul>{data?.users.map(u => <li key={u.id}>{u.name}</li>)}</ul>
        </>
    );
}
```

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
await client.connect();

const user = await createUser(client, { name: 'Alice', email: 'alice@example.com' });

onUserCreatedEvent(client, (event) => {
    console.log('User created:', event);
});
```

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
import { streamListUsers } from './api/handlers';

for await (const user of streamListUsers(client)) {
    appendUserToList(user); // renders each row as it arrives
}
```

React variant — the generated `useStreamListUsers()` hook accumulates items into local state and restarts the stream on parameter change:

```tsx
function UserList() {
    const { items, done, error, isLoading } = useStreamListUsers();
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
- **`sql.NullString` / `sql.NullInt64` / `sql.NullTime` / `sql.NullBool` / `sql.Null[T]`** are unwrapped on both sides: the TypeScript type becomes `T | null` and the Zod schema becomes `z.<base>().nullable()`, with any `validate` tag constraints still applied to the inner type.
- **`url` and `email` are absolute-only** because that's how `go-playground/validator` defines them upstream. `validate:"url"` rejects app-internal paths like `/images/123/mobile`, and `email` is strict about the formats it accepts. Not an aprot choice — just a footgun to know about so you don't fight it for half an hour. If you need relative URLs, fall back to `validate:"max=500"` or a custom validator.
- **Slice and map element types are substituted into parent schemas.** A field of type `[]EventLink` whose element struct has its own `validate` tags becomes `Links: z.array(EventLinkSchema)` in the parent — not `z.array(z.any())`. Combined with `validate:"dive"` on the parent slice, this means the same `dive` tag drives both server-side element validation (via `go-playground/validator`) and client-side element validation (via the substituted Zod schema). Primitive elements (`[]string`, `[]int`, `map[string]bool`) get a typed `z.array(z.string())` / `z.record(z.string(), z.boolean())` etc. Slices of slices, slices of maps, and anonymous-struct elements still fall through to `z.any()`.

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
