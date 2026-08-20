# Authenticating the TypeScript client

This is the recipe for connecting the generated TypeScript client to an authenticated backend, with an emphasis on **short-lived JWTs** — Clerk, Auth0, Firebase and friends, whose session tokens expire in about a minute.

Short-lived tokens surface three problems that long-lived credentials never do: the token that worked at page load is dead by the first reconnect, a rejection may be transient rather than final, and React StrictMode can strand a connection you awaited. Each has a one-line answer below.

If your credential is a session cookie, or a long-lived token you can bake into a static URL, you need very little of this — connect and move on. Skip to [Choosing a flavor](#choosing-a-flavor) to confirm.

## Two ways to present a token

**Query token.** The client puts the credential in the connection URL and the server validates it in an `OnConnect` hook. Simple, works with any transport, but the token lands in access, proxy and CDN logs.

**First-message auth.** The client sends `{type:'auth',token}` as its first message and the server validates it in an `OnAuth` hook. The token never appears in a URL, and it can be refreshed on a live connection without reconnecting. See [First-message auth](#first-message-auth).

Both re-run per connection attempt, so both keep working across reconnects. The rest of this document applies to either.

## Connection params are re-evaluated on every attempt

The credential must be minted fresh for **each** connection attempt, because auto-reconnect replays whatever the client last had. `getConnectParams` is the intended way to do that: the base URL stays static and the callback runs on every attempt — initial connect, every auto-reconnect, every rejection retry, and page-wake reconnects.

```typescript
import { ApiClient, getWebSocketUrl } from './api/client';

const client = new ApiClient(getWebSocketUrl(), {
    getConnectParams: async () => ({ token: await getToken({ skipCache: true }) }),
});
client.connect();
```

Values are URL-encoded for you and override same-named parameters already present in the URL.

> **Use `skipCache: true` (or your provider's equivalent).** Clerk caches tokens for roughly 50 seconds and hands back whatever it has. On a reconnect that can be a token with a second of life left, which then expires mid-handshake — the failure looks intermittent and is miserable to debug.

If the callback throws, the attempt fails like a transport error and the normal reconnect backoff applies — a token service blip does not permanently stop reconnection.

The same per-attempt guarantee holds for a **URL function**, which is the older form of this recipe and still supported:

```typescript
// Equivalent to the above; getConnectParams is preferred because it keeps
// addressing and credentials separate and handles encoding for you.
const client = new ApiClient(async () => {
    const token = await getToken({ skipCache: true });
    return `${getWebSocketUrl()}?token=${encodeURIComponent(token)}`;
});
```

A plain string URL is the trap: `new ApiClient(getWebSocketUrl() + '?token=' + jwt)` freezes the token at construction time. The first connect succeeds and every reconnect after expiry is rejected.

## Rejection is terminal by default — and when it shouldn't be

When the server rejects a connection (`ConnectionRejected`, or a failed first-message auth), the client stops auto-reconnecting. That default is deliberate: after a real sign-out or a revoked session, retrying with a dead credential can never succeed and only hammers the server.

For short-lived tokens the same rejection is usually transient — a token that expired in flight, session propagation lag just after sign-in, or clock skew. Retrying with a freshly minted token is exactly right, so opt in:

```typescript
const client = new ApiClient(getWebSocketUrl(), {
    getConnectParams: async () => ({ token: await getToken({ skipCache: true }) }),
    reconnectOnRejected: { delayMs: 2000, maxAttempts: 5 },
});
```

- `true` is shorthand for `{ delayMs: 2000, maxAttempts: 0 }` (unlimited).
- The delay is fixed, not backed off: a rejection means the server is reachable and answered, so this is a credential problem, not congestion.
- `maxAttempts` bounds *consecutive* rejections; any other kind of close resets the streak, as does a manual `connect()`.
- `onConnectionRejected` still fires on every rejection, and a retry that fails at the network level falls through to the normal reconnect backoff.
- Cancellation is handled for you: `disconnect()` cancels a pending retry, so a torn-down client cannot resurrect itself.

Distinguish "signed out" from "server unreachable" when rendering:

```typescript
const rejection = client.getLastRejection();   // ApiError | null
if (rejection) showSignInPrompt(rejection.message);
else if (client.getLastConnectionError()?.isOffline()) showOfflineBanner();
```

`getLastRejection()` keeps the rejection until the next successful connect, so it survives the transport failures that overwrite `getLastConnectionError()`. In React, `useConnection()` exposes the same thing as `rejection`.

## React and StrictMode

Create the client, start connecting, and provide it **synchronously**. Never gate rendering on the connect promise.

```tsx
// Module scope: one client for the app's lifetime.
const client = new ApiClient(getWebSocketUrl(), {
    getConnectParams: async () => ({ token: await getToken({ skipCache: true }) }),
    reconnectOnRejected: true,
});
client.connect();   // fire-and-forget

export default function App() {
    return (
        <ApiClientProvider value={client}>
            <ConnectionBanner />
            <Routes />
        </ApiClientProvider>
    );
}

function ConnectionBanner() {
    const { state, rejection } = useConnection();
    if (state === 'connected') return null;
    if (rejection) return <Banner>Session ended — please sign in again.</Banner>;
    return <Banner>Reconnecting…</Banner>;
}
```

> **The anti-pattern:** `client.connect().then(() => setClient(client))` — or any `setConnected(true)` that gates the tree. Under StrictMode's double mount, the effect runs twice and a disconnected client can win the race, leaving the UI waiting on a zombie forever. It is also unnecessary: requests issued while connecting are **buffered** and flushed once the connection is ready, so nothing is lost by rendering immediately.

Prefer a slim banner over unmounting the app while disconnected: the UI stays interactive, in-flight work is preserved, and reconnects become invisible. `connect()` is idempotent and auto-reconnect handles drops, so it does not need calling again — though calling it again is safe and is the right move whenever the connection needs to be live *now* (right after signing in, say). What is not safe is wrapping it in a module-level `if (connected) return` cache: that skips the call exactly when the socket has dropped and the client is sitting in a backoff, so nothing opens a socket at all and the UI waits with no `/ws` request in the network panel. Call `connect()` itself — it cancels the pending backoff and attempts immediately.

Deriving the client from React state (a user id, a workspace) is fine — build it in a `useMemo`, call `connect()` in the same effect that creates it, and `disconnect()` in the cleanup. The rule is only that you never *wait* on the promise before providing the client.

## First-message auth

Keeps the token out of URLs and logs, and supports refresh without reconnecting.

```go
server.OnAuth(func(ctx context.Context, conn *aprot.Conn, token string) error {
    claims, err := verify(token)
    if err != nil {
        return aprot.ErrAuthFailed("invalid token")
    }
    conn.SetUserID(claims.Subject) // push-routing address (PushToUser / DisconnectUser)
    conn.SetPrincipalProvider(func(ctx context.Context) (any, error) {
        return lookupUser(ctx, claims.Subject) // who is asking, for authorization
    })
    return nil
})
```

The hook sets two different things, and the distinction matters:

- **`SetUserID` is an address** — the key `PushToUser` and `DisconnectUser` use to find this connection. It is not an authorization input.
- **`SetPrincipalProvider` supplies the identity** handlers and middleware authorize on, read back with `aprot.PrincipalFrom(ctx)`. The provider runs once per execution — every request, subscribe, and **server-driven subscription refresh** — so revoking a role takes effect on the next refresh, not the next reconnect. A provider error (e.g. `aprot.ErrUnauthorized`) fails the execution before middleware runs.

Per-execution resolution does not mean a database hit per request: memoize `lookupUser` per session or credential with a TTL of your choosing, and revocation takes effect within that TTL on every transport at once. Returning a value captured in the hook is the degenerate cache (TTL = connection lifetime) — supported, but a per-session cache is the better default. See the [scope document](scope.md) for why the cache itself stays on your side of the line.

If your wrapper installs a detached connection so connection-shaped middleware keeps working (`aprot.WithConnection` with `server.NewDetachedConn()`), you can register the same `PrincipalProvider` on it instead of calling `WithPrincipal` — aprot resolves it for the request, so one auth setup covers sockets and REST/MCP. When both are present the explicit `WithPrincipal` wins and the provider does not run: the wrapper that authenticated the request is the authority on that execution. `aprot.WithPrincipal(ctx, nil)` counts as resolved too, so an explicit anonymous result is never overwritten by a provider.

Both values are readable the same way on every transport. `aprot.PrincipalFrom(ctx)` returns the principal; `aprot.UserID(ctx)` returns the address, reading through to the connection on sockets. Over REST and MCP there is no connection, so the wrapping `http.Handler` that authenticates the request attaches both itself:

```go
func withAuth(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        user, err := authenticate(r)
        if err != nil {
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
        ctx := aprot.WithPrincipal(r.Context(), user) // who is asking
        ctx = aprot.WithUserID(ctx, user.ID)         // where to reach them
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

Set both when they coincide; aprot never derives one from the other. Two properties of the address are worth knowing: it is read through rather than snapshotted at dispatch, so it can change while a handler runs (read it once into a local when you need a consistent value) — that is deliberate, because a refresh after a mid-session re-authentication should fan out to where the user is now. And it is never an authorization input: a non-empty address is not evidence that the caller authenticated.

```typescript
const client = new ApiClient(getWebSocketUrl(), {
    getAuthToken: () => getToken({ skipCache: true }),   // re-run on every reconnect
    reconnectOnRejected: true,                            // auth_error is a rejection
});
client.connect();

// Mid-session refresh, no reconnect. A failed refresh keeps the live session.
await client.refreshAuth(await getToken({ skipCache: true }));
```

The client waits for `auth_ok` before flushing requests, so nothing runs unauthenticated. An `auth_error` during connect is classified as a connection rejection — which is why `reconnectOnRejected` matters here too, and why `getLastRejection()` reports it.

### Mixed public and protected APIs

Registering `OnAuth` puts **every** connection into pending-auth: anonymous visitors on a public page never send an auth frame, so they are closed by `AuthTimeout`. Set `AllowAnonymous` to admit them:

```go
server := aprot.NewServer(registry, aprot.ServerOptions{AllowAnonymous: true})
server.OnAuth(authHook)   // still validates any token that *is* offered
```

Anonymous connections work immediately with no principal provider, no user ID, and no auth timeout; a client that authenticates later upgrades the live session in place. On the client, an anonymous viewer simply omits `getAuthToken`.

- **Still gate your handlers.** Admitting the connection is not authorizing the call — check the principal on protected handlers or in middleware. `aprot.PrincipalFrom(ctx)` is nil for anonymous callers, and the same check works over REST and MCP (where a wrapping `http.Handler` attaches the principal with `aprot.WithPrincipal`):

  ```go
  func requireUser(next aprot.Handler) aprot.Handler {
      return func(ctx context.Context, req *aprot.Request) (any, error) {
          if _, ok := aprot.PrincipalFrom(ctx).(*User); !ok {
              return nil, aprot.ErrUnauthorized("authentication required")
          }
          return next(ctx, req)
      }
  }
  ```
- **A bad token still closes the connection.** Offering a credential that fails does not silently degrade into an anonymous session.
- **Anonymous connections carry no user ID**, so `PushToUser` and `DisconnectUser` cannot reach them until they authenticate. `Broadcast` and `ForEachConn` still do.
- **No auth timeout is armed**, so keep `SetCheckOrigin` and the concurrency/subscription caps in place on public endpoints.

Before `AllowAnonymous`, the workaround was an empty-token dance — `getAuthToken: () => ''` with the hook treating an empty token as anonymous. It is no longer needed.

## Choosing a flavor

| | Query token (`getConnectParams`) | First-message auth (`getAuthToken`) |
|---|---|---|
| Token visibility | In the URL — access, proxy and CDN logs | In-band only |
| Server hook | `OnConnect` | `OnAuth` |
| Freshness | Per connection attempt | Per connection attempt |
| Mid-session refresh | Reconnect required | `refreshAuth()`, no reconnect |
| Anonymous connections | Open by default; gate in the hook | `ServerOptions.AllowAnonymous` |
| SSE | Query on the `EventSource` GET | First `POST /rpc` body |
| Rejection surfaces as | `ConnectionRejected` | `auth_error` (`-32005`) |

Both are retryable with `reconnectOnRejected` and both report through `getLastRejection()`. If you have no reason to prefer one, first-message auth is the better default — it keeps credentials out of logs and refreshes in place.

## Guarantees

The recipe leans on these documented behaviors:

- A URL **function** and `getConnectParams` are both resolved on **every** connection attempt, including auto-reconnects, rejection retries, and page-wake reconnects.
- `getAuthToken` is likewise re-invoked on every reconnect, and buffered work flushes only after `auth_ok`.
- Requests issued while `connecting` / `reconnecting` are buffered and flushed once ready; requests issued while fully disconnected reject with a `ConnectionError`.
- `connect()` is idempotent — a no-op while connected or connecting — and does not need to be called again after the first success. Calling it during a reconnect backoff cancels the backoff and attempts immediately, so it is also the way to say "connect now" after a token refresh. `reconnectNow()` is the same without the connected/connecting no-op.
- A rejection stops auto-reconnect unless `reconnectOnRejected` is set; `disconnect()` always cancels a pending retry.
- `getLastRejection()` is cleared only by the next successful connect.
