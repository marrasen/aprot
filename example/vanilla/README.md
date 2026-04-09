# Vanilla TypeScript Example

Demonstrates aprot with plain TypeScript (no framework). Covers:

- Authentication, middleware, and protected handlers
- Push events for one-shot notifications
- **Subscription refresh** — the users list is kept fresh via `subscribeListUsers(client, renderUsers)`. `CreateUser` fires `aprot.TriggerRefresh(ctx, "users")` and every subscribed client re-renders automatically — no manual refetch. See `api/handlers.go` and `client/static/app.ts`.
- **Global loading indicator** — the dot next to the header title uses `client.onLoadingChange()` to reflect pending request count.
- **Cancel cause reporting** — `ProcessBatch` inspects `aprot.CancelCause(ctx)` and logs whether the cancel came from the client, a dropped connection, or server shutdown. Hit *Cancel* during a batch and check the server stdout.
- **Page visibility reconnection** — the client reconnects immediately when the tab becomes visible or the network comes back online (no heartbeat needed).
- Progress tracking, sub-tasks, AbortController cancellation, and both WebSocket and SSE transports.

## Prerequisites

- Go 1.25+
- Node.js

## Getting Started

1. **Install client dependencies**

   ```bash
   cd client && npm install
   ```

2. **Generate TypeScript client**

   ```bash
   cd tools/generate && go run main.go
   ```

3. **Build client**

   ```bash
   cd client && npm run build
   ```

   Or use watch mode for development:

   ```bash
   cd client && npm run watch
   ```

4. **Run server**

   ```bash
   cd server && go run .
   ```

5. **Open** `http://localhost:8080`

## Transports

The server exposes two transports:

- **WebSocket** at `/ws`
- **SSE** at `/sse`

## Project Structure

| Directory         | Description                                      |
| ----------------- | ------------------------------------------------ |
| `api/`            | Handler definitions, middleware, events, and types |
| `client/`         | TypeScript client app (esbuild)                  |
| `server/`         | Go server entry point                            |
| `tools/generate/` | Code generator for the TypeScript client          |
