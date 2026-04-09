# React Example

Demonstrates aprot with React hooks. Covers:

- Generated `useQuery` / `useMutation` / `usePushEvent` hooks
- **Subscription refresh** — `UsersList` auto-updates whenever `CreateUser` fires `aprot.TriggerRefresh(ctx, "users")`, with no `useEffect` / refetch wiring on the client. See `api/handlers.go` and `client/src/App.tsx`.
- **Global loading indicator** — the dot next to the header title uses `useIsLoading()` to light up whenever any request is in flight anywhere in the app.
- **Cancel cause reporting** — `ProcessBatch` inspects `aprot.CancelCause(ctx)` and logs whether the cancel came from the client, a dropped connection, or server shutdown. Hit *Cancel* during a batch and check the server stdout.
- **Page visibility reconnection** — the client reconnects immediately when the tab becomes visible or the network comes back online (no heartbeat needed).
- Progress tracking, AbortController cancellation, and shared tasks with sub-task hierarchies visible to all connected clients.

## Prerequisites

- Go 1.25+
- Node.js

## Getting Started

1. **Install client dependencies**

   ```bash
   cd client && npm install
   ```

2. **Generate React TypeScript client**

   ```bash
   cd tools/generate && go run main.go
   ```

3. **Development** (two terminals)

   Terminal 1 — Go server:

   ```bash
   cd server && go run .
   ```

   Terminal 2 — Vite dev server (proxies `/ws` to `:8080`):

   ```bash
   cd client && npm run dev
   ```

   Open `http://localhost:5173`

4. **Production**

   ```bash
   cd client && npm run build
   cd server && go run .
   ```

   Open `http://localhost:8080`

## Project Structure

| Directory         | Description                                        |
| ----------------- | -------------------------------------------------- |
| `api/`            | Handler definitions, events, and types              |
| `client/`         | React app (Vite + TypeScript)                       |
| `server/`         | Go server entry point                               |
| `tools/generate/` | Code generator for the React TypeScript client      |
