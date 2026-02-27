# Vanilla TypeScript Example

Demonstrates aprot with plain TypeScript (no framework). Covers authentication, middleware, protected handlers, push events, progress tracking, sub-tasks, and both WebSocket and SSE transports.

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
