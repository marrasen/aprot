# aprot

A Go library for building type-safe WebSocket APIs with automatic TypeScript client generation.

## Features

- **Type-safe handlers** - Define request/response types as Go structs
- **Automatic TypeScript generation** - Generate fully typed client code from your Go types
- **Progress reporting** - Built-in support for long-running operations with progress updates
- **Request cancellation** - Clients can cancel in-flight requests via AbortController
- **Server push** - Broadcast events to connected clients
- **JSON-RPC style protocol** - Simple, debuggable wire format

## Installation

```bash
go get aprot
```

## Quick Start

### 1. Define your types

```go
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

### 2. Create handlers

```go
type Handlers struct{}

func (h *Handlers) CreateUser(ctx *aprot.Context, req *CreateUserRequest) (*CreateUserResponse, error) {
    // Your logic here
    return &CreateUserResponse{
        ID:    "123",
        Name:  req.Name,
        Email: req.Email,
    }, nil
}
```

### 3. Register and serve

```go
registry := aprot.NewRegistry()
registry.Register(&Handlers{})

server := aprot.NewServer(registry)
http.Handle("/ws", server)
http.ListenAndServe(":8080", nil)
```

### 4. Generate TypeScript client

```go
//go:generate go run aprot/cmd/aprot-gen
```

Or programmatically:

```go
gen := aprot.NewGenerator(registry)
gen.RegisterPushEvent("UserCreated", UserCreatedEvent{})

f, _ := os.Create("client.ts")
gen.Generate(f)
```

## Protocol

Messages are JSON objects with a `type` field:

### Request (client → server)
```json
{"type": "request", "id": "1", "method": "CreateUser", "params": {"name": "Alice"}}
```

### Response (server → client)
```json
{"type": "response", "id": "1", "result": {"id": "123", "name": "Alice"}}
```

### Error (server → client)
```json
{"type": "error", "id": "1", "code": 404, "message": "User not found"}
```

### Progress (server → client)
```json
{"type": "progress", "id": "1", "current": 5, "total": 10, "message": "Processing..."}
```

### Cancel (client → server)
```json
{"type": "cancel", "id": "1"}
```

### Push (server → client)
```json
{"type": "push", "event": "UserCreated", "data": {"id": "123", "name": "Alice"}}
```

## Handler Features

### Progress Reporting

```go
func (h *Handlers) ProcessBatch(ctx *aprot.Context, req *ProcessBatchRequest) (*ProcessBatchResponse, error) {
    for i, item := range req.Items {
        ctx.Progress(i, len(req.Items), "Processing "+item)
        // do work
    }
    return &ProcessBatchResponse{Processed: len(req.Items)}, nil
}
```

### Cancellation

```go
func (h *Handlers) LongTask(ctx *aprot.Context, req *LongTaskRequest) (*LongTaskResponse, error) {
    for {
        select {
        case <-ctx.Done():
            return nil, ctx.Err() // Request was cancelled
        default:
            // do work
        }
    }
}
```

### Server Push

```go
// Push to single connection
ctx.Push("Notification", &NotificationEvent{Message: "Hello"})

// Broadcast to all connections
server.Broadcast("SystemAlert", &AlertEvent{Level: "warning"})
```

### Custom Errors

```go
func (h *Handlers) GetUser(ctx *aprot.Context, req *GetUserRequest) (*GetUserResponse, error) {
    user, ok := h.users[req.ID]
    if !ok {
        return nil, aprot.NewError(404, "User not found")
    }
    return user, nil
}
```

## Generated TypeScript Client

```typescript
const client = new ApiClient('ws://localhost:8080/ws');
await client.connect();

// Type-safe method calls
const user = await client.createUser({ name: 'Alice', email: 'alice@example.com' });

// Progress tracking
const result = await client.processBatch(
    { items: ['a', 'b', 'c'] },
    { onProgress: (current, total, msg) => console.log(`${current}/${total}: ${msg}`) }
);

// Cancellation
const controller = new AbortController();
const promise = client.longTask({ ... }, { signal: controller.signal });
controller.abort(); // Cancel the request

// Push event handlers
const unsubscribe = client.onUserCreated((event) => {
    console.log('User created:', event);
});
unsubscribe(); // Stop listening

client.disconnect();
```

## Example

See the [example](./example) directory for a complete working example with:
- User CRUD operations
- Progress reporting
- Push notifications
- Web UI

Run it:

```bash
cd example
go generate ./...    # Generate TypeScript client
npm install          # Install build dependencies
npm run build        # Compile TypeScript to JavaScript
go run .             # Start the server
```

Then open http://localhost:8080 in your browser.

For development, use `npm run watch` to automatically rebuild on changes.

## License

MIT
