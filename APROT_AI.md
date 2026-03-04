# aprot AI Guide

A high-performance Go library for type-safe real-time APIs with automatic TypeScript/React client generation.

## Core Concepts

- **Registry**: Central place to register handlers, enums, and push events.
- **Handlers**: Go struct methods that become TypeScript functions.
- **Transports**: Supports both WebSocket (`/ws`) and SSE+HTTP (`/sse`).
- **Middleware**: Interceptors for auth, logging, etc. (Server-level or Handler-level).
- **Tasks**: Hierarchical progress reporting and output streaming (Request-scoped or Shared).

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
```tsx
import { useQuery, useMutation } from './api/client';
import { listItems, createItem } from './api/handlers';

const { data, isLoading } = useListItems(); // Generated hook
const { mutate } = useCreateItemMutation(); // Generated hook
```

## Error Handling
- Register Go errors: `registry.RegisterError(sql.ErrNoRows, "NotFound")`.
- Built-in: `aprot.ErrUnauthorized("msg")`, `aprot.ErrInvalidParams("msg")`.
- Client: `err instanceof ApiError`, `err.isNotFound()`.
