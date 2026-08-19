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
// [ErrDetachedConn].
func (s *Server) NewDetachedConn() *Conn {
	c := newConn(detachedTransport{}, s, atomic.AddUint64(&s.nextConnID, 1), ConnInfo{}, context.Background())
	c.detached = true
	// First-message auth gates dispatch on socket connections; a detached
	// conn is created by server-side code, which owns authentication.
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
