# aprot AI Guide

A high-performance Go library for type-safe real-time APIs with automatic TypeScript/React client generation.

## Core Concepts

- **Registry**: Central place to register handlers, enums, and push events.
- **Handlers**: Go struct methods that become TypeScript functions.
- **Transports**: Supports both WebSocket (`/ws`) and SSE+HTTP (`/sse`).
- **Middleware**: Interceptors for auth, logging, etc. (Server-level or Handler-level).
- **Tasks**: Hierarchical progress reporting and output streaming (Request-scoped or Shared).
- **Cancel Causes**: Inspect why a request was canceled (`ErrClientCanceled`, `ErrConnectionClosed`, `ErrServerShutdown`).
- **Naming Plugins**: Customize generated TypeScript naming conventions.

## Implementation Checklist

### 1. Define API (Go)
- Handlers MUST be methods on a struct.
- Signature: `func (h *T) Method(ctx context.Context, [params]) ([res], error)`.
- Use `context.Context` as the first parameter.
- Register with `registry.Register(handlers, [middleware...])`.

### 2. TypeScript Generation
- Create a generator script using `aprot.NewGenerator(registry)`.
- Set `OutputDir` and `Mode` (`aprot.OutputReact` or `aprot.OutputVanilla`).
- Run via `go run` or `go generate`.

### 3. Server Setup
- `server := aprot.NewServer(registry)`.
- Mount WebSocket: `http.Handle("/ws", server)`.
- Mount SSE: `http.Handle("/sse", server.HTTPTransport())`.

### 4. Progress & Tasks
- **Simple**: `aprot.Progress(ctx).Update(current, total, message)`.
- **Advanced**: Use `tasks.SubTask(ctx, title, fn)` for nested progress.
- **Shared**: `tasks.StartSharedTask[Meta](ctx, title)` for server-wide jobs.
- **Enable**: Call `tasks.Enable(registry)` to use the task system.

## Common Snippets

### Middleware
```go
func MyMiddleware() aprot.Middleware {
    return func(next aprot.Handler) aprot.Handler {
        return func(ctx context.Context, req *aprot.Request) (any, error) {
            // Pre-process (e.g., auth check)
            res, err := next(ctx, req)
            // Post-process
            return res, err
        }
    }
}
```

### Accessing Connection/User
```go
conn := aprot.Connection(ctx)
userID := conn.UserID() // Set via conn.SetUserID("id") in auth middleware
```

### TypeScript Usage (React)
Per-handler hooks are generated wrapping the generic `useQuery`/`useMutation`/`usePushEvent` hooks:
```tsx
import { useListUsers, useCreateUserMutation, useUserCreatedEvent } from './api/handlers';

// Auto-fetching query hook (no params)
const { data, isLoading, refetch } = useListUsers();

// Auto-fetching query hook (with params — re-fetches when params change)
const { data: user } = useGetUser(userId);

// Imperative mutation hook
const { mutate, isLoading } = useCreateUserMutation();
mutate('Alice', 'alice@example.com');

// Server push event hook
const { lastEvent, events, clear } = useUserCreatedEvent();
```

The generic hooks can also be used directly:
```tsx
import { useQuery, useMutation, usePushEvent } from './api/client';
import { listUsers, createUser, onUserCreated } from './api/handlers';
```

## Cancel Cause Reporting
Handlers can inspect why a request was canceled:
```go
cause := aprot.CancelCause(ctx)
if errors.Is(cause, aprot.ErrClientCanceled) { /* client called AbortController.abort() */ }
if errors.Is(cause, aprot.ErrConnectionClosed) { /* client disconnected */ }
if errors.Is(cause, aprot.ErrServerShutdown) { /* server is shutting down */ }
```

## SQL Nullable Types
`database/sql` nullable types are automatically mapped in TypeScript generation:
- `sql.NullString` → `string | null`
- `sql.NullInt64`, `NullInt32`, `NullInt16` → `number | null`
- `sql.NullBool` → `boolean | null`
- `sql.NullFloat64` → `number | null`
- `sql.NullTime` → `string | null`
- `sql.Null[T]` (generic) → `T | null`

## Naming Convention Plugins
Configure how generated TypeScript names are formatted via `NamingPlugin`:
```go
gen.WithOptions(aprot.GeneratorOptions{
    OutputDir: "./client/api",
    Mode:      aprot.OutputReact,
    Naming:    aprot.DefaultNaming{FixAcronyms: true},
})
```
Built-in plugins:
- **`DefaultNaming`** — kebab-case files, camelCase methods (default). `FixAcronyms: true` keeps acronyms together (e.g., `BulkXML` → `bulk-xml`).
- **`PreserveNaming`** — keeps Go PascalCase names in TypeScript.

Implement `NamingPlugin` for custom conventions:
```go
type NamingPlugin interface {
    FileName(groupName string) string
    MethodName(name string) string
    HookName(name string) string
    HandlerName(eventName string) string
    ErrorMethodName(errorName string) string
}
```

## Error Handling
- Register Go errors: `registry.RegisterError(sql.ErrNoRows, "NotFound")`.
- Built-in: `aprot.ErrUnauthorized("msg")`, `aprot.ErrInvalidParams("msg")`.
- Client: `err instanceof ApiError`, `err.isNotFound()`.
