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

// principalBox wraps the principal so a context carrying an explicitly
// resolved nil is distinguishable from one carrying nothing. The dispatch
// seam needs that distinction: it resolves a connection's provider only when
// nothing upstream has resolved the principal already, and a provider — or a
// wrapper — that legitimately resolves to nil must still count as resolved,
// or the provider would run twice (see [Server.Invoke] precedence).
type principalBox struct{ v any }

// WithPrincipal returns a context carrying p, as returned by
// [PrincipalFrom]. Request-scoped transports (REST, MCP) use it from a
// wrapping http.Handler after authenticating the request; socket
// connections normally populate it via [Conn.SetPrincipalProvider] instead.
//
// It marks the execution's principal as resolved, and takes precedence over
// any [PrincipalProvider] on a connection in the same context: the wrapper
// that authenticated the request is the authority on that execution. That
// holds for WithPrincipal(ctx, nil) too — an explicit anonymous result is
// still a result, and no provider runs to overwrite it.
func WithPrincipal(ctx context.Context, p any) context.Context {
	return context.WithValue(ctx, principalKey, principalBox{v: p})
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
	box, _ := ctx.Value(principalKey).(principalBox)
	return box.v
}

// principalResolved reports whether the principal for this execution has
// already been resolved — by a wrapper calling [WithPrincipal], or by a
// socket dispatch path that already ran the connection's provider. It is
// true even when the resolved principal is nil, which is what keeps
// [Server.invoke] from running a provider a second time.
func principalResolved(ctx context.Context) bool {
	_, ok := ctx.Value(principalKey).(principalBox)
	return ok
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
// see [PrincipalProvider] for semantics.
//
// Detached connections accept a provider too, and it runs: a request-scoped
// execution that carries a connection (attached with [WithConnection],
// typically from [Server.NewDetachedConn]) resolves it at the dispatch seam,
// so a consumer can reuse one auth setup for sockets and for REST/MCP.
// [WithPrincipal] upstream wins if both are present — the wrapper that
// authenticated the request is the authority on that execution — and the
// provider is not run at all in that case.
func (c *Conn) SetPrincipalProvider(p PrincipalProvider) {
	c.mu.Lock()
	c.principalProvider = p
	c.mu.Unlock()
}

// resolvePrincipal runs the connection's principal provider, if any, and
// returns ctx carrying the result. Called by every socket dispatch path
// (request, subscribe, refresh) after the connection is attached to the
// context, and by [Server.invoke] for request-scoped executions that carry a
// connection — so middleware and handlers see the principal uniformly on
// every path. A provider error aborts the execution; the caller maps it onto
// the wire like a handler error.
//
// Callers must not run it when [principalResolved] already holds, or the
// provider runs twice for one execution: socket unary and subscribe resolve
// here and then dispatch through invoke, which checks.
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
