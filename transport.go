package aprot

import "context"

// transport is the internal interface for connection I/O.
// Both WebSocket and SSE transports implement this.
type transport interface {
	// Send sends data to the client. Must be safe for concurrent use.
	// Send blocks until the data is accepted into the transport's outbound
	// queue or the transport has been closed. It does not silently drop.
	Send(data []byte) error
	// SendCtx is like Send but also returns early if ctx is canceled. Used
	// for stream items where a canceled request should promptly stop yielding
	// even if the outbound queue is full.
	SendCtx(ctx context.Context, data []byte) error
	// Close closes the transport.
	Close() error
	// CloseGracefully sends a close frame (if supported) before closing.
	CloseGracefully() error
}
