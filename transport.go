package aprot

import (
	"context"
	"errors"
)

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
	// SupportsBinary reports whether the transport has a native binary frame
	// channel. Callers must check it before using SendBinary/SendBinaryCtx;
	// binary-frame encoding is skipped entirely on transports without it.
	SupportsBinary() bool
	// SendBinary sends one binary protocol frame. Only valid when
	// SupportsBinary reports true.
	SendBinary(data []byte) error
	// SendBinaryCtx is like SendBinary but returns early if ctx is canceled.
	SendBinaryCtx(ctx context.Context, data []byte) error
	// Close closes the transport.
	Close() error
	// CloseGracefully sends a close frame (if supported) before closing.
	CloseGracefully() error
}

// errBinaryUnsupported is returned by SendBinary/SendBinaryCtx on transports
// without a native binary channel. Callers avoid it by checking
// SupportsBinary first.
var errBinaryUnsupported = errors.New("transport does not support binary frames")

// noBinary provides the binary-frame methods for transports without a native
// binary channel. Embed it to satisfy the transport interface.
type noBinary struct{}

func (noBinary) SupportsBinary() bool                        { return false }
func (noBinary) SendBinary([]byte) error                     { return errBinaryUnsupported }
func (noBinary) SendBinaryCtx(context.Context, []byte) error { return errBinaryUnsupported }
