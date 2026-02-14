# Feature: Connection-Scoped State for WebSocket Authentication

## Background / Motivation

Browser-based WebSocket clients commonly authenticate via **session cookies**. The browser automatically attaches cookies on the HTTP upgrade request, so the server can validate the session once at connection time rather than requiring a token in every message.

aprot already provides most of the infrastructure needed for this pattern:

- `ConnectHook` — runs before message processing, has access to the HTTP upgrade request
- `ConnInfo` — exposes cookies, headers, and other HTTP metadata from the upgrade request
- `SetCheckOrigin` — controls the WebSocket origin check
- Per-handler middleware — different auth requirements per handler group
- `ErrUnauthorized` / `ErrForbidden` — standard error codes for auth failures
- `ErrConnectionRejected` — reject connections from hooks

The **one missing primitive** is a way to store arbitrary connection-scoped data (e.g. an authenticated principal, roles, tenant ID) that persists for the connection's lifetime without re-loading on every request. Currently `conn.UserID()` is the only connection-level state, and richer data must be re-fetched from a store on every handler call.

## Threat Model

### Cross-Site WebSocket Hijacking (CSWSH)

When using cookie-based auth, the browser sends cookies on the upgrade request **regardless of the page's origin**. A malicious page can open a WebSocket to your server and the cookies will be attached automatically.

**Mitigation:** Use `server.SetCheckOrigin()` to restrict allowed origins. aprot allows all origins by default (like gorilla/websocket) — this is safe for token-based auth but **must be restricted** for cookie-based auth.

```go
server.SetCheckOrigin(func(r *http.Request) bool {
    origin := r.Header.Get("Origin")
    return origin == "https://myapp.example.com"
})
```

### Cookie Security

Session cookies used for WebSocket auth should be configured with:

- `HttpOnly` — prevents JavaScript access (XSS mitigation)
- `Secure` — sent only over HTTPS
- `SameSite=Strict` or `SameSite=Lax` — additional CSWSH protection
- Short expiry / sliding window — limits replay window

### Replay / Session Fixation

A WebSocket connection is long-lived. If a session is revoked server-side, existing connections authenticated with that session remain active until they disconnect. Applications that need immediate revocation should either:

1. Track active connections by session ID and force-disconnect on revocation
2. Use short-lived sessions with periodic re-validation in middleware

### What This Feature Does NOT Do

This feature adds a generic key-value store on connections. It does **not** introduce any authentication logic, session management, or security policy. Those remain application-level concerns. The feature merely makes it possible to cache connection-scoped data efficiently.

## What Already Exists

| Feature | Location | Description |
|---------|----------|-------------|
| `ConnectHook` | `server.go` | Runs at connection time, can reject |
| `ConnInfo` | `connection.go` | HTTP cookies, headers, URL, host, remote addr |
| `SetCheckOrigin` | `server.go` | Configures WebSocket origin validation |
| Per-handler middleware | `handler.go` | Different middleware per handler group |
| `ErrUnauthorized` / `ErrForbidden` | `protocol.go` | Standard auth error codes |
| `conn.SetUserID` / `conn.UserID` | `connection.go` | String user ID for push targeting |

## What's Missing

A generic connection-scoped key-value store:

```go
conn.Set(key, value any)  // Store a value for the connection's lifetime
conn.Get(key any) any     // Retrieve a value (nil if not set)
```

## Proposed API Changes

Two new methods on `*Conn`:

```go
// Set stores a value on the connection, keyed by an arbitrary key.
// The map is lazily initialized on first call.
// Safe for concurrent use.
func (c *Conn) Set(key, value any)

// Get retrieves a value previously stored with Set.
// Returns nil if the key was never set.
// Safe for concurrent use.
func (c *Conn) Get(key any) any
```

Implementation details:
- New field: `values map[any]any` on `Conn` (nil until first `Set`)
- Both methods acquire the existing `c.mu` mutex
- Zero-cost for connections that never call `Set` (nil map check, no allocation)

## Server-Side Usage Examples

### Cookie-Session Auth at Connect Time

```go
type principalKey struct{}

type Principal struct {
    ID   string
    Role string
}

server.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
    for _, cookie := range conn.Info().Cookies {
        if cookie.Name == "session" {
            principal, err := sessionStore.Validate(cookie.Value)
            if err != nil {
                return aprot.ErrConnectionRejected("invalid session")
            }
            conn.SetUserID(principal.ID)
            conn.Set(principalKey{}, principal)
            return nil
        }
    }
    // No cookie — allow for public endpoints
    return nil
})
```

### RequireAuth Middleware

```go
func RequireAuth() aprot.Middleware {
    return func(next aprot.Handler) aprot.Handler {
        return func(ctx context.Context, req *aprot.Request) (any, error) {
            conn := aprot.Connection(ctx)
            if conn == nil || conn.Get(principalKey{}) == nil {
                return nil, aprot.ErrUnauthorized("authentication required")
            }
            return next(ctx, req)
        }
    }
}
```

### RequireRole Middleware

```go
func RequireRole(role string) aprot.Middleware {
    return func(next aprot.Handler) aprot.Handler {
        return func(ctx context.Context, req *aprot.Request) (any, error) {
            conn := aprot.Connection(ctx)
            p, _ := conn.Get(principalKey{}).(*Principal)
            if p == nil || p.Role != role {
                return nil, aprot.ErrForbidden("requires role: " + role)
            }
            return next(ctx, req)
        }
    }
}

// Registration
registry.Register(&PublicHandlers{})
registry.Register(&UserHandlers{}, RequireAuth())
registry.Register(&AdminHandlers{}, RequireAuth(), RequireRole("admin"))
```

## Compatibility

- **Zero breaking changes** — additive only
- No changes to the wire protocol
- No changes to generated TypeScript
- No changes to existing `Conn` methods
- Connections that don't use `Set/Get` have zero additional cost

## Test Plan

### Unit Tests

- `TestConnSetGet` — Set in ConnectHook, retrieve in middleware
- `TestConnGetBeforeSet` — Returns nil for unset keys
- `TestConnSetOverwrite` — Second Set replaces first value
- `TestConnSetGetConcurrent` — No data race under `-race` flag

### Integration Tests (existing, unchanged)

- `TestOnConnectHookCalled` — ConnectHook executes
- `TestOnConnectHookRejectsConnection` — Connection rejected with error
- `TestOnDisconnectHookHasUserID` — UserID persists through disconnect

## Out of Scope

The following are explicitly **not** part of this feature:

- **OIDC/OAuth flows** — Application-level concern
- **Session store implementation** — Application-level concern
- **Token refresh/rotation** — Application-level concern
- **Built-in RequireAuth/RequireRole** — Shown as patterns in examples, not library code
- **Default origin restriction** — Would be a breaking change; documented as best practice instead
- **Changes to request context propagation** — Request contexts still derive from `context.Background()`
