# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build and Test Commands

```bash
# Run all tests
go test ./...

# Run a single test
go test -run TestName ./...

# Generate TypeScript clients for examples
cd example/vanilla/tools/generate && go run main.go
cd example/react/tools/generate && go run main.go

# Run vanilla example server
cd example/vanilla/server && go run .

# Run react example (needs both server and client)
cd example/react/server && go run .
cd example/react/client && npm run dev
```

## Architecture Overview

aprot is a Go library that generates type-safe TypeScript WebSocket clients from Go handler structs. The core flow:

1. **Handler Registration** (`handler.go`): Go structs with methods matching `func(ctx, *Req) (*Resp, error)` or `func(ctx, *Req) error` (void) are registered via `Registry.Register()`. Methods are validated via reflection and stored as `HandlerInfo`.

2. **Code Generation** (`generate.go`): `Generator` uses reflection to extract types from registered handlers, converts Go types to TypeScript, and executes embedded templates (`templates/*.tmpl`) to produce client code.

3. **WebSocket Server** (`server.go`, `connection.go`): `Server` manages WebSocket connections via gorilla/websocket. Each connection runs read/write pumps. Incoming messages are dispatched to handlers through the middleware chain.

4. **Protocol** (`protocol.go`): JSON-RPC style messages with types: `request`, `response`, `error`, `progress`, `push`, `ping`, `pong`, `cancel`.

### Key Design Patterns

- **Per-handler middleware**: Middleware is passed to `Register()` and stored per handler group, enabling different auth requirements for different handler structs
- **Module augmentation**: Generated TypeScript uses `declare module './client'` to extend `ApiClient` with handler-specific methods
- **Split output**: `Generate()` produces separate files: `client.ts` (base) + `{handler}.ts` per handler group

### Template System

Templates use Go's `text/template` with shared blocks in `_client-common.ts.tmpl`:
- `error-codes`, `api-error`, `types` - Core TypeScript types
- `api-client-class` - WebSocket client with auto-reconnect/heartbeat
- `react-context`, `react-hook-types` - React-specific code

### Type Flow

Go types → `collectType()` extracts struct fields → `goTypeToTS()` maps to TS → Templates render interfaces/methods

Enums: Registered via `RegisterEnum(Values())` → detected in `collectType()` → rendered as `const {...} as const` with companion type

## Important: Always Update README

When adding or changing user-facing features (new types, new API patterns, new options, etc.), **always update `README.md`** to document the change. This is a required step for every feature PR — do not skip it.

## Common Patterns

### Adding a new protocol message type

1. Add constant to `protocol.go` (e.g., `TypeFoo`)
2. Add struct for the message
3. Handle in `connection.go` `handleMessage()` for incoming, or add send method for outgoing
4. Update client templates if needed

### Modifying generated TypeScript

1. Edit templates in `templates/` directory
2. Shared code goes in `_client-common.ts.tmpl` as `{{define "block-name"}}`
3. **Always** regenerate and verify TypeScript compiles after template changes:
   ```bash
   cd example/vanilla/tools/generate && go run main.go
   cd example/react/tools/generate && go run main.go
   cd example/react/client && npx tsc --noEmit
   ```
