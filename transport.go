package aprot

// transport is the internal interface for connection I/O.
// Both WebSocket and SSE transports implement this.
type transport interface {
	// Send sends data to the client. Must be safe for concurrent use.
	Send(data []byte) error
	// Close closes the transport.
	Close() error
	// CloseGracefully sends a close frame (if supported) before closing.
	CloseGracefully() error
}
