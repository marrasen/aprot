# aprot

A Go library for building type-safe WebSocket APIs with automatic TypeScript client generation.

> **Warning**
> This library is currently unstable and under active development. Breaking changes will occur between versions until v1.0.0 is released.

## Features

- **Type-safe handlers** - Define request/response types as Go structs
- **Automatic TypeScript generation** - Generate fully typed client code from your Go types
- **React hooks** - Optional React integration with query/mutation hooks
- **Progress reporting** - Built-in support for long-running operations with progress updates
- **Request cancellation** - Clients can cancel in-flight requests via AbortController
- **Server push** - Broadcast events to connected clients
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
```

### 4. Create registry (api/registry.go)

```go
package api

import "github.com/marrasen/aprot"

func NewRegistry() *aprot.Registry {
    registry := aprot.NewRegistry()
    registry.Register(&Handlers{})
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
    registry := api.NewRegistry()
    server := aprot.NewServer(registry)

    handlers := api.NewHandlers()
    handlers.SetBroadcaster(server)
    registry.Register(handlers)

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
