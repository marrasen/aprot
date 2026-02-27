# React Example

Demonstrates aprot with React hooks. Covers generated `useQuery`/`useMutation`/`usePushEvent` hooks, progress tracking, cancellation, and shared tasks with sub-task hierarchies visible to all connected clients.

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
