package aprot

import (
	"context"
	"errors"
	"sync/atomic"
)

// ErrDetachedConn is returned by send operations on a detached connection.
// A detached connection carries identity and per-connection values for
// request-scoped transports; it has no client to deliver frames to.
var ErrDetachedConn = errors.New("aprot: detached connection has no transport")

// detachedTransport satisfies the transport interface for connections that
// have no client channel. Every send fails with ErrDetachedConn.
type detachedTransport struct {
	noBinary
}

func (detachedTransport) Send([]byte) error                     { return ErrDetachedConn }
func (detachedTransport) SendCtx(context.Context, []byte) error { return ErrDetachedConn }
func (detachedTransport) SendDroppable([]byte) error            { return ErrDetachedConn }
func (detachedTransport) Close() error                          { return nil }
func (detachedTransport) CloseGracefully() error                { return nil }

// NewDetachedConn returns a connection that is not bound to any transport.
// It exists so request-scoped transports (REST, MCP) can satisfy middleware
// written against aprot.Connection(ctx): the per-connection value store
// (Set/Get/Load), SetUserID/UserID, and ID all work, so connection-shaped
// auth middleware runs unchanged. Attach it to a request context with
// [WithConnection].
//
// A detached connection can be scoped per request or reused per
// authenticated session — the caller owns its lifetime, and no cleanup is
// required because the server never registers it. It is excluded from push
// fan-out: SetUserID records the ID for UserID() but does not associate the
// connection with the server's user index, so PushToUser and Broadcast never
// try to deliver to it. Direct sends (Push, Progress) fail with
// [ErrDetachedConn]; [Conn.Detached] reports this up front.
//
// A detached connection carries no authentication state of its own. The
// first-message auth gate applies only to connections that read frames off
// a transport; the caller that builds a detached conn is the authority on
// whether the request behind it was authenticated, and records that with
// [WithPrincipal] or [Conn.SetPrincipalProvider].
func (s *Server) NewDetachedConn() *Conn {
	c := newConn(detachedTransport{}, s, atomic.AddUint64(&s.nextConnID, 1), ConnInfo{}, context.Background())
	c.detached = true
	// Inert here, set so the zero value never reads as "pending auth". The
	// flag is consulted only when a frame arrives off a transport, and a
	// detached conn has no read loop and is never registered. It is not an
	// authorization decision, which is why NewDetachedConn does not take it
	// as an argument — see docs/scope.md, ruling for #342.
	c.authenticated.Store(true)
	return c
}

// WithConnection returns a context carrying conn, as returned by
// [Connection]. Request-scoped transports use it to hand middleware a
// connection (typically from [Server.NewDetachedConn]) on transports that
// have no socket.
func WithConnection(ctx context.Context, conn *Conn) context.Context {
	return withConnection(ctx, conn)
}

// Detached reports whether c was created by [Server.NewDetachedConn] and so
// has no transport behind it: [Conn.Push] and [Conn.Progress] fail with
// [ErrDetachedConn], and push fan-out never reaches it.
//
// This answers "can I deliver a frame to this connection", not "did the
// caller authenticate". Connection presence is a transport fact on every
// transport; [PrincipalFrom] is the authorization input. The intended
// caller is code that picks a delivery path up front — push to a live
// socket, or fold the payload into the response — rather than calling
// [Conn.Push] and handling [ErrDetachedConn] after the fact.
func (c *Conn) Detached() bool {
	return c.detached
}
