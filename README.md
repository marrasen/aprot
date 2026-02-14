# aprot

**A**PI **P**rotocol for **R**eal-time **O**perations with **T**ypeScript

A Go library for building type-safe real-time APIs with automatic TypeScript client generation. Supports both WebSocket and SSE+HTTP transports.

> **Warning**
> This library is currently unstable and under active development. Breaking changes will occur between versions until v1.0.0 is released.

## Features

- **Type-safe handlers** - Define handlers with any number of parameters of any type, with automatic TypeScript client generation
- **Automatic TypeScript generation** - Generate fully typed client code from your Go types
- **Enum support** - Register Go enums and generate TypeScript const objects with type safety
- **React hooks** - Optional React integration with query/mutation hooks
- **Middleware support** - Add cross-cutting concerns like authentication, logging, and rate limiting
- **Connection lifecycle hooks** - React to client connect/disconnect events, reject connections
- **User-targeted push** - Send push messages to specific users across multiple connections
- **Progress reporting** - Built-in support for long-running operations with progress updates
- **Hierarchical sub-tasks** - Nested task trees with progress tracking, streamed to clients during handler execution
- **Shared tasks** - Server-wide tasks visible to all clients via push events, with cancel support
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

### 1. Define handlers (api/handlers.go)

Handler methods must accept `context.Context` as the first parameter (after receiver), followed by any number of additional parameters. They must return either `error` or `(*T, error)`:

```go
func(ctx context.Context) (*U, error)                           // No parameters
func(ctx context.Context) error                                 // No parameters, void
func(ctx context.Context, req *T) (*U, error)                   // Single struct parameter
func(ctx context.Context, name string, age int) (*U, error)     // Multiple primitives
func(ctx context.Context, items ...string) error                // Variadic
```

Parameters are positional — each Go parameter becomes a separate argument in the TypeScript client:

```typescript
import { listUsers, createUser, add } from './api/handlers';

// Go: func (h *Handlers) ListUsers(ctx context.Context) (*ListUsersResponse, error)
await listUsers(client);

// Go: func (h *Handlers) CreateUser(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error)
await createUser(client, { name: 'Alice', email: 'alice@example.com' });

// Go: func (h *Handlers) Add(ctx context.Context, a int, b int) (*SumResult, error)
await add(client, 5, 3);
```

Parameter names in the generated TypeScript are extracted from your Go source code via AST parsing — the names you choose in Go are the names your TypeScript client uses.

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

### 2. Server (server/main.go)

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

### 3. Generator (tools/generate/main.go)

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

Split handlers into separate structs by their middleware requirements. This ensures you can't accidentally forget to protect an endpoint - if a handler needs authentication, it's registered with the auth middleware.

```go
// Split handlers by their middleware requirements
registry.Register(&PublicHandlers{})                    // No middleware
registry.Register(&UserHandlers{}, authMiddleware)      // With auth
registry.Register(&AdminHandlers{}, authMiddleware, adminMiddleware)
```

Each `Register()` call creates a separate handler group with its own middleware chain and a corresponding TypeScript file (e.g., `public-handlers.ts`, `user-handlers.ts`).

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
server.PushToUser("user_123", &NotificationEvent{
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

### Sub-Tasks

Use `SubTask` inside any handler to report hierarchical progress to the calling client. Sub-tasks nest automatically via context:

```go
func (h *Handlers) Deploy(ctx context.Context, req *DeployRequest) (*DeployResponse, error) {
    err := aprot.SubTask(ctx, "Build image", func(ctx context.Context) error {
        // Nested sub-task
        return aprot.SubTask(ctx, "Compile", func(ctx context.Context) error {
            // ... do work ...
            return nil
        })
    })
    if err != nil {
        return nil, err
    }

    err = aprot.SubTask(ctx, "Push to registry", func(ctx context.Context) error {
        return nil
    })

    return &DeployResponse{Status: "ok"}, err
}
```

The client receives a `TaskNode` tree in progress messages. Each node has `id`, `title`, `status` (`running`/`completed`/`failed`), and optional `current`/`total` progress.

**Output streaming** sends text output during execution:

```go
aprot.Output(ctx, "Starting deployment...")

// Or use a writer for command output:
w := aprot.OutputWriter(ctx, "Running tests")
cmd.Stdout = w
cmd.Run()
w.Close()

// Track bytes as progress (e.g. file downloads):
pw := aprot.WriterProgress(ctx, "Downloading", fileSize)
io.Copy(pw, resp.Body)
pw.Close()
```

**TypeScript client** — the `onTaskProgress` and `onOutput` callbacks in `RequestOptions` receive these updates:

```typescript
const result = await deploy(client, req, {
    onTaskProgress: (tasks) => {
        // tasks is TaskNode[] — render a progress tree
    },
    onOutput: (output) => {
        // Append to a log view
    },
});
```

### Shared Tasks

Shared tasks are visible to **all** connected clients (not just the requesting one). They survive the originating connection's disconnect. Use them for server-wide operations like deployments, imports, or batch jobs.

#### Setup

Enable shared tasks in the registry:

```go
registry := aprot.NewRegistry()
registry.Register(&Handlers{})
registry.EnableTasks()  // Registers TaskStateEvent, TaskOutputEvent push events + CancelTask handler
```

To attach typed metadata to tasks, use `EnableTasksWithMeta` instead:

```go
type TaskMeta struct {
    UserName string `json:"userName,omitempty"`
    Error    string `json:"error,omitempty"`
}

registry.EnableTasksWithMeta(TaskMeta{})
```

This generates a typed `TaskMeta` interface in the TypeScript client and adds an optional `meta?: TaskMeta` field to `SharedTaskState` and `TaskNode`.

#### Creating Shared Tasks

```go
func (h *Handlers) StartDeploy(ctx context.Context, req *DeployRequest) (*aprot.TaskRef, error) {
    task := aprot.ShareTask(ctx, "Deploying "+req.Service)
    if task == nil {
        return nil, aprot.ErrInternal(nil)
    }

    // Attach metadata visible to all clients
    task.SetMeta(TaskMeta{UserName: "alice"})

    // Run in background — task auto-closes when fn returns
    task.Go(func(ctx context.Context) {
        task.Progress(1, 3)
        task.Output("Building image...")
        // ... build ...

        sub := task.SubTask("Push to registry")
        sub.Progress(1, 2)
        // ... push ...
        sub.Complete()

        // Sub-tasks can also nest and have metadata
        nested := sub.SubTask("Verify push")
        nested.SetMeta(TaskMeta{UserName: "bot"})
        nested.Complete()

        task.Progress(3, 3)
    })

    // Return TaskRef so the client can track/cancel this task
    return task.Ref(), nil
}
```

Key methods on `SharedTask`:
- `Go(fn)` — runs fn in a goroutine, auto-closes on return
- `Progress(current, total)` — updates progress
- `Output(msg)` — sends output text to all clients
- `SubTask(title)` — creates a child node
- `SetMeta(v)` — sets metadata broadcast to all clients
- `Close()` / `Fail()` — marks as completed/failed
- `Context()` — returns the task's cancellation context
- `Ref()` — returns a `TaskRef{TaskID}` to send to the client

Key methods on `SharedTaskSub` (child nodes):
- `Complete()` / `Fail()` — marks as completed/failed
- `Progress(current, total)` — updates progress
- `SetMeta(v)` — sets metadata on this sub-task
- `SubTask(title)` — creates a nested child node

#### TypeScript (React)

```tsx
import { useSharedTasks, useSharedTask, useTaskOutput, cancelSharedTask } from './api/client';

function TaskList() {
    const tasks = useSharedTasks();

    return (
        <ul>
            {tasks.map(task => (
                <li key={task.id}>
                    {task.title} ({task.status}) — {task.current}/{task.total}
                    <button onClick={() => cancelSharedTask(client, task.id)}>Cancel</button>
                </li>
            ))}
        </ul>
    );
}

function TaskLog({ taskId }: { taskId: string }) {
    const { lines, clear } = useTaskOutput(taskId);
    return <pre>{lines.join('\n')}</pre>;
}
```

#### TypeScript (Vanilla)

```typescript
import { cancelSharedTask } from './api/client';

client.onPush<{ tasks: SharedTaskState[] }>('TaskStateEvent', (event) => {
    console.log('Active tasks:', event.tasks);
});

client.onPush<{ taskId: string; output: string }>('TaskOutputEvent', (event) => {
    console.log(`[${event.taskId}] ${event.output}`);
});

// Cancel a shared task
await cancelSharedTask(client, taskId);
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

### Connection-Scoped State (`Set/Get/Load`)

Store arbitrary data on a connection that persists for its lifetime. This follows the `context.WithValue` convention of using unexported struct keys to avoid collisions:

```go
// Define a typed key (unexported to avoid collisions)
type principalKey struct{}

// Store a value (typically in a ConnectHook or auth middleware)
conn.Set(principalKey{}, &Principal{ID: "user_123", Role: "admin"})

// Retrieve it later (in any handler or middleware)
principal, _ := conn.Get(principalKey{}).(*Principal)

// Use Load to distinguish "not set" from "set to nil"
if v, ok := conn.Load(principalKey{}); ok {
    principal, _ = v.(*Principal)
}
```

`Set/Get/Load` are safe for concurrent use (`Get` and `Load` take a read lock). The internal map is lazily initialized — connections that never call `Set` pay zero allocation cost. Keep stored values small; they live for the entire connection lifetime.

### Authentication

aprot provides the building blocks for authentication but does not prescribe a specific strategy. The common pattern for browser clients is **cookie-session auth at connect time**:

1. **Validate at connect time** — Use an `OnConnect` hook to read session cookies and store the authenticated principal on the connection.
2. **Guard per-handler** — Use per-handler middleware to check the cached principal.

```go
type principalKey struct{}

// ConnectHook: validate session cookie and cache the user on the connection.
server.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
    for _, cookie := range conn.Info().Cookies {
        if cookie.Name == "session" {
            user, err := sessionStore.Validate(cookie.Value)
            if err != nil {
                return aprot.ErrConnectionRejected("invalid session")
            }
            conn.SetUserID(user.ID)
            conn.Set(principalKey{}, user)
            return nil
        }
    }
    return nil // allow unauthenticated connections for public endpoints
})

// Middleware: require authentication on protected handlers.
func RequireAuth() aprot.Middleware {
    return func(next aprot.Handler) aprot.Handler {
        return func(ctx context.Context, req *aprot.Request) (any, error) {
            conn := aprot.Connection(ctx)
            if conn == nil {
                return nil, aprot.ErrUnauthorized("authentication required")
            }
            if _, ok := conn.Load(principalKey{}); !ok {
                return nil, aprot.ErrUnauthorized("authentication required")
            }
            return next(ctx, req)
        }
    }
}

// Register handlers with auth middleware
registry.Register(&PublicHandlers{})
registry.Register(&ProtectedHandlers{}, RequireAuth())
```

**Note:** `conn.SetUserID` / `conn.UserID` is a *routing identity* used for push targeting (`PushToUser`). It is not a security boundary — always use the stored principal (via `Set`/`Load`) for authorization decisions in middleware.

**Origin validation** — By default, aprot accepts all origins. For production, restrict origins to prevent cross-site WebSocket hijacking (CSWSH):

```go
server.SetCheckOrigin(func(r *http.Request) bool {
    origin := r.Header.Get("Origin")
    return origin == "https://myapp.example.com"
})
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

### Graceful Shutdown

Stop the server gracefully with `Server.Stop()`. It rejects new connections (503), sends WebSocket close frames, waits for in-flight requests to complete, and runs all disconnect hooks before returning.

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
if err := server.Stop(ctx); err != nil {
    log.Printf("shutdown timed out: %v", err)
}
```

`Stop` is safe to call multiple times. If the context deadline is exceeded, it returns `ctx.Err()` and in-flight requests that haven't finished may still be running.

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

### Request Buffering

The generated TypeScript client automatically buffers requests made while a connection is being established or re-established. If you call an API method during the `connecting` or `reconnecting` state, the request is queued and sent once the connection succeeds. If the connection ultimately fails (transitions to `disconnected`), all buffered requests are rejected.

This is transparent — no configuration needed. Requests made while fully `disconnected` (no connection attempt in progress) still reject immediately with `"Not connected"`.

Buffered requests support `AbortSignal` cancellation: aborting a signal removes the request from the buffer.

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
import { createUser } from './api/public-handlers';

// WebSocket (default)
const wsClient = new ApiClient(getWebSocketUrl());

// SSE+HTTP
const sseClient = new ApiClient(getSSEUrl(), { transport: 'sse' });

// Same API for both
await sseClient.connect();
const user = await createUser(sseClient, { name: 'Alice' });
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
- `POST /rpc` — `{"connectionId":"...","id":"1","method":"Handlers.Echo","params":[...]}` → `202 Accepted`
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
import { TaskStatus, TaskStatusType } from './api/public-handlers';

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

## Type Mapping

Go types are mapped to TypeScript types during code generation:

| Go Type | TypeScript Type | Notes |
|---------|----------------|-------|
| `string` | `string` | |
| `int`, `float64`, etc. | `number` | All numeric types |
| `bool` | `boolean` | |
| `[]T` | `T[]` | |
| `map[K]V` | `Record<K, V>` | |
| `*T` | `T` (optional) | Pointer fields become optional (`?`) |
| `time.Time` | `string` | RFC 3339 format (Go's `encoding/json` default) |
| `struct` | `interface` | Named structs become TypeScript interfaces |
| Registered enum | Const object + type | See [Enum Support](#enum-support) |

`time.Time` fields (including `*time.Time`) are generated as `string` because Go's `encoding/json` marshals them as RFC 3339 strings. This applies anywhere `time.Time` appears: direct fields, slices (`[]time.Time` → `string[]`), map values, etc.

## Generated Output

The generator creates split files for better organization:

- **`client.ts`** - Base client with `ApiClient`, `ApiError`, `ErrorCode`, `getWebSocketUrl`, `getSSEUrl`
- **`{handler-name}.ts`** - Handler-specific interfaces and standalone exported functions

```
api/
├── client.ts           # Base: ApiClient, ApiError, ErrorCode, getWebSocketUrl, getSSEUrl
├── user-handlers.ts    # UserHandlers interfaces + functions
└── order-handlers.ts   # OrderHandlers interfaces + functions
```

Each handler file exports standalone functions that take `ApiClient` as the first argument. This enables tree-shaking and namespace imports when multiple handlers have overlapping method names:

```typescript
import { ApiClient, getWebSocketUrl } from './api/client';
import { createUser } from './api/user-handlers';
import { createOrder } from './api/order-handlers';

const client = new ApiClient(getWebSocketUrl());
await client.connect();

await createUser(client, { name: 'Alice' });
await createOrder(client, { items: [...] });
```

Or use namespace imports to avoid name conflicts:

```typescript
import * as users from './api/user-handlers';
import * as orders from './api/order-handlers';

await users.create(client, { name: 'Alice' });
await orders.create(client, { items: [...] });
```

Wire protocol method names are namespaced as `"HandlerStruct.MethodName"` (e.g., `"UserHandlers.CreateUser"`), allowing multiple handler groups to have methods with the same name.

For single-file output (legacy), use `GenerateTo()`:

```go
gen.GenerateTo(os.Stdout)  // Everything in one file
```

### Vanilla TypeScript

```typescript
import { ApiClient, getWebSocketUrl, getSSEUrl } from './api/client';
import { createUser, onUserCreatedEvent } from './api/public-handlers';

// WebSocket (default) — auto-detects protocol from page URL
const client = new ApiClient(getWebSocketUrl());

// Or use SSE+HTTP transport
// const client = new ApiClient(getSSEUrl(), { transport: 'sse' });

await client.connect();

const user = await createUser(client, { name: 'Alice', email: 'alice@example.com' });

onUserCreatedEvent(client, (event) => {
    console.log('User created:', event);
});
```

### React Hooks

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

    const addUser = useCallback((name: string) => {
        // mutate() runs the async action, shows loading state, then refetches
        mutate(createUser(api, { name }));
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
| client→server | request | `{"type":"request","id":"1","method":"Handlers.CreateUser","params":[{...}]}` |
| server→client | response | `{"type":"response","id":"1","result":{...}}` |
| server→client | error | `{"type":"error","id":"1","code":404,"message":"Not found"}` |
| server→client | progress | `{"type":"progress","id":"1","current":5,"total":10,"message":"..."}` |
| client→server | cancel | `{"type":"cancel","id":"1"}` |
| server→client | push | `{"type":"push","event":"UserCreatedEvent","data":{...}}` |
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

## Testing

### Go Unit Tests

```bash
go test ./...
```

### E2E Integration Tests

The `e2e/` directory contains end-to-end tests that run the generated TypeScript client against a live Go server, covering both WebSocket and SSE transports.

```bash
# Generate the TypeScript client
cd e2e/generate && go run main.go

# Install dependencies and run tests
cd .. && npm install && npm test
```

Tests cover: request/response, error handling, progress reporting, request cancellation, push events, broadcast, authentication middleware, and enum types.

### CI

GitHub Actions runs three jobs on every push/PR to `master`:

1. **go-tests** - Go unit and integration tests with race detection
2. **typescript-compile** - Verifies generated TypeScript compiles for both vanilla and React modes
3. **e2e-tests** - Runs the full E2E suite against a live server

## License

MIT
