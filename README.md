# aprot

A Go library for building type-safe WebSocket APIs with automatic TypeScript client generation.

> **Warning**
> This library is currently unstable and under active development. Breaking changes will occur between versions until v1.0.0 is released.

## Features

- **Type-safe handlers** - Define request/response types as Go structs
- **Automatic TypeScript generation** - Generate fully typed client code from your Go types
- **React hooks** - Optional React integration with query/mutation hooks
- **Middleware support** - Add cross-cutting concerns like authentication, logging, and rate limiting
- **User-targeted push** - Send push messages to specific users across multiple connections
- **Progress reporting** - Built-in support for long-running operations with progress updates
- **Request cancellation** - Clients can cancel in-flight requests via AbortController
- **Server push** - Broadcast events to all connected clients
- **JSON-RPC style protocol** - Simple, debuggable wire format

## Installation

```bash
go get github.com/marrasen/aprot
```

## Project Structure

For real-world applications, we recommend separating concerns:

```
myapp/
├── api/                      # Shared Go types package
│   ├── types.go              # Request/response structs
│   ├── events.go             # Push event types
│   ├── handlers.go           # Handler implementations
│   ├── middleware.go         # Custom middleware (optional)
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

## Quick Start

### 1. Define your types (api/types.go)

```go
package api

type CreateUserRequest struct {
    Name  string `json:"name"`
    Email string `json:"email"`
}

type CreateUserResponse struct {
    ID    string `json:"id"`
    Name  string `json:"name"`
    Email string `json:"email"`
}
```

### 2. Define push events (api/events.go)

```go
package api

type UserCreatedEvent struct {
    ID   string `json:"id"`
    Name string `json:"name"`
}
```

### 3. Implement handlers (api/handlers.go)

```go
package api

import (
    "context"

    "github.com/marrasen/aprot"
)

type Handlers struct {
    broadcaster aprot.Broadcaster
}

func NewHandlers() *Handlers {
    return &Handlers{}
}

func (h *Handlers) SetBroadcaster(b aprot.Broadcaster) {
    h.broadcaster = b
}

func (h *Handlers) CreateUser(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error) {
    // Implementation...
    h.broadcaster.Broadcast("UserCreated", &UserCreatedEvent{...})
    return &CreateUserResponse{...}, nil
}

func (h *Handlers) DeleteUser(ctx context.Context, req *DeleteUserRequest) (*DeleteUserResponse, error) {
    // This method requires auth (see registry.go)
    // Access connection if needed:
    conn := aprot.Connection(ctx)
    userID := conn.UserID() // Set by auth middleware
    // ...
    return &DeleteUserResponse{}, nil
}
```

### 4. Create registry (api/registry.go)

```go
package api

import "github.com/marrasen/aprot"

func NewRegistry(handlers *Handlers) *aprot.Registry {
    registry := aprot.NewRegistry()

    // Register with per-method options
    registry.RegisterWithOptions(handlers, map[string][]aprot.Option{
        "CreateUser": {},                 // Public
        "GetUser":    {},                 // Public
        "DeleteUser": {aprot.WithAuth()}, // Requires auth
    })

    registry.RegisterPushEvent("UserCreated", UserCreatedEvent{})
    return registry
}
```

### 5. Server entry point (server/main.go)

```go
package main

import (
    "net/http"

    "github.com/marrasen/aprot"
    "myapp/api"
)

func main() {
    handlers := api.NewHandlers()
    registry := api.NewRegistry(handlers)
    server := aprot.NewServer(registry)

    handlers.SetBroadcaster(server)

    // Add middleware (optional)
    server.Use(
        api.LoggingMiddleware(),
        api.AuthMiddleware(),
    )

    http.Handle("/ws", server)
    http.ListenAndServe(":8080", nil)
}
```

### 6. Generator (tools/generate/main.go)

```go
//go:build ignore

package main

import (
    "github.com/marrasen/aprot"
    "myapp/api"
)

func main() {
    gen := aprot.NewGenerator(api.NewRegistry()).WithOptions(aprot.GeneratorOptions{
        OutputDir: "../../client/src/api",
        Mode:      aprot.OutputReact, // or aprot.OutputVanilla
    })
    gen.Generate()
}
```

Add a go:generate directive in `tools/generate/doc.go`:

```go
//go:generate go run main.go
package main
```

## Middleware

Middleware allows you to add cross-cutting concerns like authentication, logging, and rate limiting to your handlers.

### Defining Middleware

```go
func LoggingMiddleware() aprot.Middleware {
    return func(next aprot.Handler) aprot.Handler {
        return func(ctx context.Context, req *aprot.Request) (any, error) {
            start := time.Now()
            result, err := next(ctx, req)
            log.Printf("[%s] %s completed in %v", req.ID, req.Method, time.Since(start))
            return result, err
        }
    }
}
```

### Using Middleware

```go
server := aprot.NewServer(registry)
server.Use(
    LoggingMiddleware(),
    AuthMiddleware(),
)
```

Middleware executes in the order added, wrapping inward (first middleware is outermost).

### Handler Options

Mark handlers with options for middleware to inspect:

```go
registry.RegisterWithOptions(handlers, map[string][]aprot.Option{
    "Login":       {},                                    // Public
    "GetProfile":  {aprot.WithAuth()},                    // Requires auth
    "DeleteUser":  {aprot.WithAuth(), aprot.WithTags("admin")},
    "RateLimit":   {aprot.WithCustom("requests_per_min", 100)},
})
```

Access options in middleware:

```go
func AuthMiddleware() aprot.Middleware {
    return func(next aprot.Handler) aprot.Handler {
        return func(ctx context.Context, req *aprot.Request) (any, error) {
            info := aprot.HandlerInfoFromContext(ctx)
            if info != nil && info.Options.RequireAuth {
                // Validate auth token...
                if !valid {
                    return nil, aprot.ErrUnauthorized("invalid token")
                }
            }
            return next(ctx, req)
        }
    }
}
```

### User-Targeted Push

Associate connections with user IDs to send push messages to specific users:

```go
// In auth middleware, after validating the user:
conn := aprot.Connection(ctx)
if conn != nil {
    conn.SetUserID(user.ID)  // User can have multiple connections
}

// Later, send push to specific user (e.g., from a background job):
server.PushToUser("user_123", "Notification", &NotificationEvent{
    Message: "You have a new message",
})
```

### Context Helpers

Access request metadata in handlers and middleware:

```go
info := aprot.HandlerInfoFromContext(ctx)  // Handler metadata and options
req := aprot.RequestFromContext(ctx)       // Request ID, method, params
conn := aprot.Connection(ctx)              // WebSocket connection
progress := aprot.Progress(ctx)            // Progress reporter
```

### Connection Info

Each connection has a unique ID and HTTP request info captured at connection time:

```go
conn := aprot.Connection(ctx)
conn.ID()          // uint64 - unique connection ID (increments per connection)
conn.RemoteAddr()  // string - client's remote address
conn.UserID()      // string - associated user ID (set via SetUserID)

// Full HTTP info from the upgrade request
info := conn.Info()
info.RemoteAddr    // "192.168.1.100:54321"
info.Header        // http.Header - all request headers
info.Cookies       // []*http.Cookie - parsed cookies
info.URL           // "/ws?token=abc"
info.Host          // "example.com"
```

Example logging middleware using connection info:

```go
func LoggingMiddleware() aprot.Middleware {
    return func(next aprot.Handler) aprot.Handler {
        return func(ctx context.Context, req *aprot.Request) (any, error) {
            conn := aprot.Connection(ctx)
            start := time.Now()

            result, err := next(ctx, req)

            log.Printf("[conn:%d %s] %s - %v",
                conn.ID(), conn.RemoteAddr(), req.Method, time.Since(start))
            return result, err
        }
    }
}
```

### Error Handling

#### Server-side (Go)

Use `ProtocolError` to return typed errors to clients:

```go
// Built-in error helpers
aprot.ErrUnauthorized("invalid token")     // Code: -32001
aprot.ErrForbidden("access denied")        // Code: -32003
aprot.ErrInvalidParams("name is required") // Code: -32602
aprot.ErrInternal(err)                     // Code: -32603

// Custom errors with your own codes
aprot.NewError(1001, "insufficient balance")
aprot.NewError(1002, "item out of stock")

// Wrap existing errors
aprot.WrapError(1003, "database error", err)
```

Standard error codes:
| Code | Constant | Description |
|------|----------|-------------|
| -32700 | `CodeParseError` | Invalid JSON |
| -32600 | `CodeInvalidRequest` | Invalid request structure |
| -32601 | `CodeMethodNotFound` | Method not found |
| -32602 | `CodeInvalidParams` | Invalid parameters |
| -32603 | `CodeInternalError` | Internal server error |
| -32800 | `CodeCanceled` | Request canceled |
| -32001 | `CodeUnauthorized` | Not authenticated |
| -32003 | `CodeForbidden` | Not authorized |

#### Client-side (TypeScript)

The generated client throws `ApiError` with a `code` property:

```typescript
import { ApiError, ErrorCode } from './api/client';

try {
    await client.createUser({ name: '', email: '' });
} catch (err) {
    if (err instanceof ApiError) {
        // Check specific error codes
        if (err.isUnauthorized()) {
            // Redirect to login
        } else if (err.isInvalidParams()) {
            // Show validation error
        } else if (err.code === 1001) {
            // Handle custom error code
        }
        console.log(`Error ${err.code}: ${err.message}`);
    }
}
```

Available `ErrorCode` constants and helper methods:

```typescript
// Constants
ErrorCode.Unauthorized  // -32001
ErrorCode.Forbidden     // -32003
ErrorCode.InvalidParams // -32602
ErrorCode.MethodNotFound // -32601
// ... etc

// Helper methods on ApiError
err.isUnauthorized()  // err.code === -32001
err.isForbidden()     // err.code === -32003
err.isInvalidParams() // err.code === -32602
err.isNotFound()      // err.code === -32601
err.isCanceled()      // err.code === -32800
```

## Generated Output

### Vanilla TypeScript

```typescript
import { ApiClient, getWebSocketUrl } from './api/client';

// getWebSocketUrl() automatically uses the current page's host
// - http://localhost:8080 → ws://localhost:8080/ws
// - https://myapp.com → wss://myapp.com/ws
const client = new ApiClient(getWebSocketUrl());
await client.connect();

const user = await client.createUser({ name: 'Alice', email: 'alice@example.com' });

client.onUserCreated((event) => {
    console.log('User created:', event);
});
```

### React Hooks

```tsx
import { ApiClient, ApiClientProvider, getWebSocketUrl, useListUsers, useCreateUserMutation, useUserCreated } from './api/client';

const client = new ApiClient(getWebSocketUrl());

function App() {
    return (
        <ApiClientProvider value={client}>
            <UsersList />
        </ApiClientProvider>
    );
}

function UsersList() {
    const { data, isLoading, refetch } = useListUsers({ params: {} });
    const { mutate } = useCreateUserMutation();
    const { lastEvent } = useUserCreated();

    useEffect(() => {
        if (lastEvent) refetch();
    }, [lastEvent]);

    return <ul>{data?.users.map(u => <li key={u.id}>{u.name}</li>)}</ul>;
}
```

## Protocol

Messages are JSON with a `type` field:

| Direction | Type | Example |
|-----------|------|---------|
| client→server | request | `{"type":"request","id":"1","method":"CreateUser","params":{...}}` |
| server→client | response | `{"type":"response","id":"1","result":{...}}` |
| server→client | error | `{"type":"error","id":"1","code":404,"message":"Not found"}` |
| server→client | progress | `{"type":"progress","id":"1","current":5,"total":10,"message":"..."}` |
| client→server | cancel | `{"type":"cancel","id":"1"}` |
| server→client | push | `{"type":"push","event":"UserCreated","data":{...}}` |

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

## License

MIT
