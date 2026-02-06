# aprot

**A**PI **P**rotocol for **R**eal-time **O**perations with **T**ypeScript

A Go library for building type-safe real-time APIs with automatic TypeScript client generation. Supports both WebSocket and SSE+HTTP transports.

> **Warning**
> This library is currently unstable and under active development. Breaking changes will occur between versions until v1.0.0 is released.

## Features

- **Type-safe handlers** - Define request/response types as Go structs, with void handler support
- **Automatic TypeScript generation** - Generate fully typed client code from your Go types
- **Enum support** - Register Go enums and generate TypeScript const objects with type safety
- **React hooks** - Optional React integration with query/mutation hooks
- **Middleware support** - Add cross-cutting concerns like authentication, logging, and rate limiting
- **Connection lifecycle hooks** - React to client connect/disconnect events, reject connections
- **User-targeted push** - Send push messages to specific users across multiple connections
- **Progress reporting** - Built-in support for long-running operations with progress updates
- **Request cancellation** - Clients can cancel in-flight requests via AbortController
- **Server push** - Broadcast events to all connected clients
- **Server-pushed config** - Automatically configure client reconnect/heartbeat settings
- **Dual transport** - WebSocket and SSE+HTTP transports with identical API
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

Split handlers by their middleware requirements. Handlers support two signatures:

```go
// Standard handler - returns a response
func(ctx context.Context, req *T) (*U, error)

// Void handler - no response data (generates Promise<void> in TypeScript)
func(ctx context.Context, req *T) error
```

Both signatures can be mixed on the same handler struct:

```go
package api

import (
    "context"

    "github.com/marrasen/aprot"
)

// Shared state between handler groups
type SharedState struct {
    Broadcaster aprot.Broadcaster
}

// PublicHandlers - no authentication required
type PublicHandlers struct {
    state *SharedState
}

func (h *PublicHandlers) CreateUser(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error) {
    // Implementation...
    h.state.Broadcaster.Broadcast("UserCreated", &UserCreatedEvent{...})
    return &CreateUserResponse{...}, nil
}

// ProtectedHandlers - requires authentication middleware
type ProtectedHandlers struct {
    state *SharedState
}

// Void handler - no response needed
func (h *ProtectedHandlers) DeleteUser(ctx context.Context, req *DeleteUserRequest) error {
    // Auth middleware has already validated the user
    conn := aprot.Connection(ctx)
    userID := conn.UserID() // Set by auth middleware
    // ... delete user ...
    return nil
}
```

### 4. Create registry (api/registry.go)

```go
package api

import "github.com/marrasen/aprot"

func NewRegistry(publicHandlers *PublicHandlers, protectedHandlers *ProtectedHandlers, authMiddleware aprot.Middleware) *aprot.Registry {
    registry := aprot.NewRegistry()

    // Register public handlers (no middleware)
    registry.Register(publicHandlers)

    // Register protected handlers (with auth middleware)
    registry.Register(protectedHandlers, authMiddleware)

    registry.RegisterPushEvent(UserCreatedEvent{})
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
    // Create shared state and handlers
    state := &api.SharedState{}
    publicHandlers := &api.PublicHandlers{State: state}
    protectedHandlers := &api.ProtectedHandlers{State: state}

    // Create registry with per-handler middleware
    authMiddleware := api.AuthMiddleware()
    registry := api.NewRegistry(publicHandlers, protectedHandlers, authMiddleware)

    server := aprot.NewServer(registry)
    state.Broadcaster = server

    // Server-level middleware (applies to all handlers)
    server.Use(api.LoggingMiddleware())

    // Transports
    http.Handle("/ws", server)               // WebSocket
    http.Handle("/sse", server.HTTPTransport())  // SSE+HTTP
    http.Handle("/sse/", server.HTTPTransport()) // SSE+HTTP sub-routes
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
    // Create minimal instances for code generation (no actual state needed)
    state := &api.SharedState{}
    registry := api.NewRegistry(
        &api.PublicHandlers{State: state},
        &api.ProtectedHandlers{State: state},
        api.AuthMiddleware(),
    )

    gen := aprot.NewGenerator(registry).WithOptions(aprot.GeneratorOptions{
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

### Per-Handler Middleware

Apply middleware at the handler group level for safety. This ensures you can't accidentally forget to protect an endpoint - if a handler needs authentication, it's registered with the auth middleware.

```go
// Split handlers by their middleware requirements
registry.Register(&PublicHandlers{})                    // No middleware
registry.Register(&UserHandlers{}, authMiddleware)      // With auth
registry.Register(&AdminHandlers{}, authMiddleware, adminMiddleware)
```

Both server-level and handler-level middleware can be used together:
- **Server middleware** applies to all handlers (e.g., logging)
- **Handler middleware** applies only to that handler group (e.g., auth)

```go
// Server-level middleware (applies to all handlers)
server.Use(LoggingMiddleware())

// Handler-level middleware (applies only to protected handlers)
registry.Register(&ProtectedHandlers{}, AuthMiddleware(tokenStore))
```

Execution order: server middleware (outer) → handler middleware (inner) → actual handler

```go
func AuthMiddleware(tokenStore *TokenStore) aprot.Middleware {
    return func(next aprot.Handler) aprot.Handler {
        return func(ctx context.Context, req *aprot.Request) (any, error) {
            // Extract and validate token
            // ...
            if !valid {
                return nil, aprot.ErrUnauthorized("invalid token")
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

### Connection Lifecycle Hooks

React to connection events with `OnConnect` and `OnDisconnect` hooks:

```go
server := aprot.NewServer(registry)

// Called when a client connects (before message processing starts)
server.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
    log.Printf("Client connected: %d from %s", conn.ID(), conn.RemoteAddr())

    // Reject connection by returning an error
    if server.ConnectionCount() >= maxConnections {
        return aprot.ErrConnectionRejected("max connections reached")
    }

    // Access HTTP request context (useful for zerolog integration)
    logger := log.Ctx(conn.Context())
    logger.Info().Msg("new connection")

    return nil
})

// Called when a client disconnects (UserID still available)
server.OnDisconnect(func(ctx context.Context, conn *aprot.Conn) {
    log.Printf("Client disconnected: %d (user: %s)", conn.ID(), conn.UserID())
})
```

Multiple hooks can be registered and are called in order. If an `OnConnect` hook returns an error, the connection is rejected and subsequent hooks are not called.

### Server Options

Configure client reconnection and heartbeat behavior:

```go
server := aprot.NewServer(registry, aprot.ServerOptions{
    ReconnectInterval:    2000,  // Initial reconnect delay (ms), default: 1000
    ReconnectMaxInterval: 60000, // Max reconnect delay (ms), default: 30000
    ReconnectMaxAttempts: 10,    // Max attempts (0=unlimited), default: 0
    HeartbeatInterval:    15000, // Heartbeat interval (ms), default: 30000
    HeartbeatTimeout:     3000,  // Heartbeat timeout (ms), default: 5000
})
```

The server automatically sends this configuration to clients on connect. TypeScript clients apply it automatically, overriding any client-side defaults.

### Transport

aprot supports two transports that share the same handler dispatch, middleware, connection management, and generated client API:

| | WebSocket | SSE+HTTP |
|---|-----------|----------|
| **Server→Client** | WebSocket frames | SSE event stream |
| **Client→Server** | WebSocket frames | HTTP POST |
| **Heartbeat** | Client ping/server pong | Server-sent SSE comments |
| **Best for** | Full-duplex, low latency | Environments where WebSocket is blocked |

#### Server Setup

```go
server := aprot.NewServer(registry)

// WebSocket transport (existing)
http.Handle("/ws", server)

// SSE+HTTP transport
// Routes: GET /sse (event stream), POST /sse/rpc (calls), POST /sse/cancel (cancellation)
http.Handle("/sse", server.HTTPTransport())
http.Handle("/sse/", server.HTTPTransport())
```

Both transports can run simultaneously. Connections from both transports are tracked together — `Broadcast()`, `PushToUser()`, and `ConnectionCount()` work across all connections regardless of transport.

#### Client Usage

```typescript
import { ApiClient, getWebSocketUrl, getSSEUrl } from './api/client';

// WebSocket (default)
const wsClient = new ApiClient(getWebSocketUrl());

// SSE+HTTP
const sseClient = new ApiClient(getSSEUrl(), { transport: 'sse' });

// Same API for both
await sseClient.connect();
const user = await sseClient.createUser({ name: 'Alice' });
```

#### SSE Protocol

The SSE stream uses named events (`event:` field) for message routing:

| SSE Event | Data | Description |
|-----------|------|-------------|
| `connected` | `{"type":"connected","connectionId":"abc123"}` | First event, provides connection ID |
| `config` | `{"type":"config",...}` | Server configuration |
| `response` | `{"type":"response","id":"1","result":{...}}` | RPC response |
| `error` | `{"type":"error","id":"1","code":...,"message":...}` | RPC error |
| `progress` | `{"type":"progress","id":"1",...}` | Progress update |
| `push` | `{"type":"push","event":"...","data":{...}}` | Push event |

HTTP endpoints:
- `POST /rpc` — `{"connectionId":"...","id":"1","method":"Echo","params":{...}}` → `202 Accepted`
- `POST /cancel` — `{"connectionId":"...","id":"1"}` → `200 OK`

### Error Handling

#### Server-side (Go)

**Register Go errors for automatic conversion:**

```go
// Register standard Go errors - they'll be auto-converted when returned
// Codes are auto-assigned starting at 1000
registry.RegisterError(io.EOF, "EndOfFile")
registry.RegisterError(sql.ErrNoRows, "NotFound")
registry.RegisterError(context.DeadlineExceeded, "Timeout")

// Register code-only (for manual use with NewError)
insufficientBalanceCode := registry.RegisterErrorCode("InsufficientBalance")
```

Now handlers can return standard Go errors:

```go
func (h *Handlers) ReadData(ctx context.Context, req *ReadRequest) (*ReadResponse, error) {
    data, err := h.reader.Read()
    if err != nil {
        return nil, err  // io.EOF automatically becomes code 1000
    }
    return &ReadResponse{Data: data}, nil
}
```

**Built-in error helpers:**

```go
aprot.ErrUnauthorized("invalid token")     // Code: -32001
aprot.ErrForbidden("access denied")        // Code: -32003
aprot.ErrInvalidParams("name is required") // Code: -32602
aprot.ErrInternal(err)                     // Code: -32603

// Manual custom errors
aprot.NewError(code, "message")
aprot.WrapError(code, "message", cause)
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
| -32002 | `CodeConnectionRejected` | Connection rejected by hook |
| -32003 | `CodeForbidden` | Not authorized |

#### Client-side (TypeScript)

The generated client throws `ApiError` with a `code` property. Custom error codes registered with `RegisterError` are automatically included:

```typescript
import { ApiError, ErrorCode } from './api/client';

try {
    await client.readData({ id: '123' });
} catch (err) {
    if (err instanceof ApiError) {
        // Check standard errors
        if (err.isUnauthorized()) {
            // Redirect to login
        }

        // Check custom registered errors (auto-generated)
        if (err.isEndOfFile()) {
            // Handle EOF
        } else if (err.isNotFound()) {
            // Handle not found
        }

        console.log(`Error ${err.code}: ${err.message}`);
    }
}
```

Generated `ErrorCode` constants include both standard and custom codes:

```typescript
export const ErrorCode = {
    // Standard codes
    Unauthorized: -32001,
    Forbidden: -32003,
    InvalidParams: -32602,
    // ...

    // Custom codes (from RegisterError/RegisterErrorCode)
    EndOfFile: 1000,
    NotFound: 1001,
    Timeout: 1002,
    InsufficientBalance: 1003,
} as const;
```

Helper methods are generated for all error types:

```typescript
err.isUnauthorized()        // standard
err.isEndOfFile()           // custom
err.isNotFound()            // custom
err.isInsufficientBalance() // custom
```

### Enum Support

Register Go enum types to generate TypeScript const objects with full type safety.

#### Defining Enums (Go)

```go
// String-based enum
type TaskStatus string

const (
    TaskStatusPending   TaskStatus = "pending"
    TaskStatusRunning   TaskStatus = "running"
    TaskStatusCompleted TaskStatus = "completed"
    TaskStatusFailed    TaskStatus = "failed"
)

// Required: Values() function returning all enum values
func TaskStatusValues() []TaskStatus {
    return []TaskStatus{
        TaskStatusPending,
        TaskStatusRunning,
        TaskStatusCompleted,
        TaskStatusFailed,
    }
}
```

For int-based enums, implement the `Stringer` interface to provide names:

```go
type Priority int

const (
    PriorityLow Priority = iota
    PriorityMedium
    PriorityHigh
)

func (p Priority) String() string {
    switch p {
    case PriorityLow:
        return "Low"
    case PriorityMedium:
        return "Medium"
    case PriorityHigh:
        return "High"
    default:
        return "Unknown"
    }
}

func PriorityValues() []Priority {
    return []Priority{PriorityLow, PriorityMedium, PriorityHigh}
}
```

#### Registering Enums

```go
registry := aprot.NewRegistry()
registry.RegisterEnum(TaskStatusValues())
registry.RegisterEnum(PriorityValues())
```

#### Using Enums in Types

```go
type Task struct {
    ID       string     `json:"id"`
    Name     string     `json:"name"`
    Status   TaskStatus `json:"status"`
    Priority Priority   `json:"priority"`
}
```

#### Generated TypeScript

```typescript
// String-based enum - names derived by capitalizing value
export const TaskStatus = {
    Pending: "pending",
    Running: "running",
    Completed: "completed",
    Failed: "failed",
} as const;
export type TaskStatusType = typeof TaskStatus[keyof typeof TaskStatus];

// Int-based enum - names from String() method
export const Priority = {
    Low: 0,
    Medium: 1,
    High: 2,
} as const;
export type PriorityType = typeof Priority[keyof typeof Priority];

// Struct fields use enum types instead of string/number
export interface Task {
    id: string;
    name: string;
    status: TaskStatusType;   // not string!
    priority: PriorityType;   // not number!
}
```

#### Using Enums (TypeScript)

```typescript
import { TaskStatus, TaskStatusType } from './api/handlers';

// Type-safe comparison
if (task.status === TaskStatus.Running) {
    console.log('Task is running');
}

// Type-safe switch with exhaustive checking
function getStatusLabel(status: TaskStatusType): string {
    switch (status) {
        case TaskStatus.Pending:
            return 'Pending';
        case TaskStatus.Running:
            return 'Running';
        case TaskStatus.Completed:
            return 'Completed';
        case TaskStatus.Failed:
            return 'Failed';
    }
}
```

## Generated Output

The generator creates split files for better organization:

- **`client.ts`** - Base client with `ApiClient`, `ApiError`, `ErrorCode`, `getWebSocketUrl`, `getSSEUrl`
- **`{handler-name}.ts`** - Handler-specific interfaces and methods (extends ApiClient via module augmentation)

```
api/
├── client.ts           # Base: ApiClient, ApiError, ErrorCode, getWebSocketUrl, getSSEUrl
├── user-handlers.ts    # UserHandlers interfaces + methods
└── order-handlers.ts   # OrderHandlers interfaces + methods
```

Import and use:

```typescript
import { ApiClient, ApiError, ErrorCode, getWebSocketUrl } from './api/client';
import './api/user-handlers';   // Adds user methods to ApiClient
import './api/order-handlers';  // Adds order methods to ApiClient

const client = new ApiClient(getWebSocketUrl());
await client.connect();

// Methods from all imported handlers are available
await client.createUser({ name: 'Alice' });
await client.createOrder({ items: [...] });
```

For single-file output (legacy), use `GenerateTo()`:

```go
gen.GenerateTo(os.Stdout)  // Everything in one file
```

### Vanilla TypeScript

```typescript
import { ApiClient, getWebSocketUrl, getSSEUrl } from './api/client';

// WebSocket (default) — auto-detects protocol from page URL
const client = new ApiClient(getWebSocketUrl());

// Or use SSE+HTTP transport
// const client = new ApiClient(getSSEUrl(), { transport: 'sse' });

await client.connect();

const user = await client.createUser({ name: 'Alice', email: 'alice@example.com' });

client.onUserCreated((event) => {
    console.log('User created:', event);
});
```

### React Hooks

```tsx
import { ApiClient, ApiClientProvider, getWebSocketUrl, useApiClient } from './api/client';
import { useListUsers } from './api/handlers';

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
    const { data, isLoading, error, mutate } = useListUsers({ params: {} });

    const addUser = useCallback((name: string) => {
        // mutate() runs the async action, shows loading state, then refetches
        mutate(api.createUser({ name }));
    }, [mutate, api]);

    if (error) return <div>Error: {error.message}</div>;
    if (isLoading) return <div>Loading...</div>;

    return (
        <>
            <button onClick={() => addUser('New User')}>Add User</button>
            <ul>{data?.users.map(u => <li key={u.id}>{u.name}</li>)}</ul>
        </>
    );
}
```

The `mutate` function accepts a Promise or async function. It sets `isLoading=true`, awaits the action, refetches data on success, or sets `error` on failure. This consolidates loading/error state for all operations on the resource.

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
| server→client | config | `{"type":"config","reconnectInterval":1000,"heartbeatInterval":30000,...}` |
| server→client | connected | `{"type":"connected","connectionId":"abc123"}` (SSE only) |

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
