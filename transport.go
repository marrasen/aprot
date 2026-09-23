package aprot

import (
	"context"
	"errors"
)

// transport is the internal interface for connection I/O.
// Both the WebSocket and byte-stream transports implement this.
type transport interface {
	// Send sends data to the client. Must be safe for concurrent use.
	// Send blocks until the data is accepted into the transport's outbound
	// queue or the transport has been closed. It does not silently drop.
	Send(data []byte) error
	// SendCtx is like Send but also returns early if ctx is canceled. Used
	// for stream items where a canceled request should promptly stop yielding
	// even if the outbound queue is full.
	SendCtx(ctx context.Context, data []byte) error
	// SendDroppable sends data that may be skipped rather than queued. It is
	// the opposite trade from Send: it never blocks, and it returns
	// ErrPushDropped when the connection already has maxQueuedDroppable
	// droppable frames waiting for the write pump. Only frames whose push
	// event opted in via Droppable take this path.
	SendDroppable(data []byte) error
	// SupportsBinary reports whether the transport has a native binary frame
	// channel. Callers must check it before using SendBinary/SendBinaryCtx;
	// binary-frame encoding is skipped entirely on transports without it.
	SupportsBinary() bool
	// SendBinary sends one binary protocol frame. Only valid when
	// SupportsBinary reports true.
	SendBinary(data []byte) error
	// SendBinaryCtx is like SendBinary but returns early if ctx is canceled.
	SendBinaryCtx(ctx context.Context, data []byte) error
	// SendBinaryDroppable is SendDroppable for one binary protocol frame.
	// Only valid when SupportsBinary reports true.
	SendBinaryDroppable(data []byte) error
	// Close closes the transport.
	Close() error
	// CloseGracefully sends a close frame (if supported) before closing.
	CloseGracefully() error
}

// maxQueuedDroppable is how many droppable frames one connection may have
// waiting for its write pump before further droppable frames are skipped.
//
// One, because that is what "droppable" is asking for: send this frame if the
// client has kept up, skip it otherwise. A frame holds its slot until it is on
// the wire, so the next droppable frame is accepted only once its predecessor
// has been written; anything produced in between is skipped, and whatever
// comes after is newer anyway. The effect is delivery at whatever rate the
// connection sustains, always with the freshest frame available.
//
// It is a constant rather than a ServerOptions knob on purpose. The number is
// not a tuning parameter, it is the semantics: raising it buys smoothness by
// adding exactly that many stale frames of latency, and for the live-preview
// case that motivated droppable pushes (#387) latency is the whole point.
//
// The allowance is per connection, not per event: every droppable event on a
// connection shares the one slot, because they share one write pump and one
// wire. Two droppable streams at full rate therefore halve each other rather
// than each getting a slot — which is the honest outcome, since a per-event
// allowance would only move the contention onto the queue and add a frame of
// head-of-line delay for every control message behind it.
const maxQueuedDroppable = 1

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
func (noBinary) SendBinaryDroppable([]byte) error            { return errBinaryUnsupported }
