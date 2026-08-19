package aprot

import "context"

// principal.go holds the request-scoped identity seam (#330): a principal
// carried in the request context, populated per execution on every dispatch
// path. The principal is whatever the consumer's auth resolves to — aprot
// only carries it, it never inspects it.
//
// Population per transport:
//
//   - Socket (WebSocket/SSE): register a [PrincipalProvider] on the
//     connection (typically from the [Server.OnAuth] hook). The provider
//     runs once per execution — requests, subscribes, and server-driven
//     subscription refreshes — so identity changes take effect without a
//     reconnect.
//   - REST / MCP: a wrapping http.Handler that authenticates the request
//     attaches the resolved principal directly with [WithPrincipal] before
//     delegating to the adapter.
//
// The principal answers "who is asking" for authorization. It is distinct
// from [Conn.UserID], which is an address: the key push fan-out uses to
// decide where [Server.PushToUser] deliveries go. Set both when they
// coincide; never authorize on connection presence — [Connection] being
// non-nil only means there is a socket.

// PrincipalProvider resolves the caller's identity for one handler
// execution. It is registered on a connection with
// [Conn.SetPrincipalProvider] and runs before middleware on every request,
// subscribe, and subscription refresh dispatched on that connection; the
// returned principal is attached to the request context and read back with
// [PrincipalFrom].
//
// Returning an error fails the execution with that error (use the
// [ProtocolError] constructors, e.g. [ErrUnauthorized], for a typed wire
// code) and the handler never runs. On a subscription refresh the
// subscriber receives an error frame and the subscription stays registered,
// so a later refresh re-resolves.
//
// The provider runs per execution so revocation propagates without a
// reconnect; a consumer who wants fewer identity lookups memoizes inside
// the provider, keyed by credential or session with a TTL of their
// choosing. Resolving once in [Server.OnAuth] and returning the snapshot is
// the degenerate cache (TTL = connection lifetime).
type PrincipalProvider func(ctx context.Context) (any, error)

// WithPrincipal returns a context carrying p, as returned by
// [PrincipalFrom]. Request-scoped transports (REST, MCP) use it from a
// wrapping http.Handler after authenticating the request; socket
// connections normally populate it via [Conn.SetPrincipalProvider] instead.
func WithPrincipal(ctx context.Context, p any) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// PrincipalFrom returns the principal attached to the request context, or
// nil when the execution is anonymous (no provider on the connection, no
// [WithPrincipal] upstream). Consumers type-assert to their own principal
// type:
//
//	user, ok := aprot.PrincipalFrom(ctx).(*User)
//	if !ok {
//	    return nil, aprot.ErrUnauthorized("authentication required")
//	}
func PrincipalFrom(ctx context.Context) any {
	return ctx.Value(principalKey)
}

// SetPrincipalProvider registers the provider that resolves this
// connection's principal, replacing any previous one; nil clears it.
// Typically called from the [Server.OnAuth] hook, capturing the validated
// token:
//
//	server.OnAuth(func(ctx context.Context, conn *aprot.Conn, token string) error {
//	    if _, err := verify(token); err != nil {
//	        return aprot.ErrAuthFailed("invalid token")
//	    }
//	    conn.SetPrincipalProvider(func(ctx context.Context) (any, error) {
//	        return lookupUser(ctx, token) // consumer memoizes per session as desired
//	    })
//	    return nil
//	})
//
// The provider runs on every execution dispatched on this connection —
// see [PrincipalProvider] for semantics. Detached connections accept a
// provider too, but request-scoped transports never dispatch through a
// connection, so there the caller attaches the principal directly with
// [WithPrincipal].
func (c *Conn) SetPrincipalProvider(p PrincipalProvider) {
	c.mu.Lock()
	c.principalProvider = p
	c.mu.Unlock()
}

// resolvePrincipal runs the connection's principal provider, if any, and
// returns ctx carrying the result. Called by every socket dispatch path
// (request, subscribe, refresh) after the connection is attached to the
// context, so middleware and handlers see the principal uniformly. A
// provider error aborts the execution; the caller maps it onto the wire
// like a handler error.
func (c *Conn) resolvePrincipal(ctx context.Context) (context.Context, error) {
	c.mu.Lock()
	provider := c.principalProvider
	c.mu.Unlock()
	if provider == nil {
		return ctx, nil
	}
	p, err := provider(ctx)
	if err != nil {
		return ctx, err
	}
	return WithPrincipal(ctx, p), nil
}
