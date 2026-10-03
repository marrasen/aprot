package client

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"iter"
	"sync"
)

// streamEntry is the client's untyped view of a running stream.
type streamEntry interface {
	push(item jsontext.Value)
	end(err error)
}

// rawStream buffers a stream's raw items. The read loop must never block, so
// the buffer is unbounded, as in the TypeScript client; the server produces
// items only as fast as the connection carries them.
type rawStream struct {
	client *Client
	id     string
	conn   wireConn

	mu         sync.Mutex
	cond       *sync.Cond
	items      []jsontext.Value
	serverDone bool  // the server ended the stream, or the connection did
	stopped    bool  // the consumer stopped, or the stream failed locally
	err        error // first error; nil for a clean end
	stopCtx    func() bool
}

func newRawStream() *rawStream {
	s := &rawStream{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *rawStream) push(item jsontext.Value) {
	s.mu.Lock()
	if !s.serverDone && !s.stopped {
		s.items = append(s.items, item)
		s.cond.Signal()
	}
	s.mu.Unlock()
}

// end records that the server side is over. Items already buffered are
// still delivered.
func (s *rawStream) end(err error) {
	s.mu.Lock()
	if !s.serverDone {
		s.serverDone = true
		if s.err == nil {
			s.err = err
		}
		s.cond.Broadcast()
	}
	s.mu.Unlock()
}

// next blocks for the next item. ok is false once the stream is over and
// the buffer is drained, or the consumer stopped.
func (s *rawStream) next() (item jsontext.Value, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.items) == 0 && !s.serverDone && !s.stopped {
		s.cond.Wait()
	}
	if s.stopped || len(s.items) == 0 {
		return nil, false
	}
	item = s.items[0]
	s.items[0] = nil
	s.items = s.items[1:]
	return item, true
}

// stop ends the stream from the client's side with err (nil when the
// consumer simply stopped), and cancels it at the server if the server has
// not ended it yet.
func (s *rawStream) stop(err error) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	if s.err == nil {
		s.err = err
	}
	running := !s.serverDone
	s.serverDone = true
	s.items = nil
	s.cond.Broadcast()
	stopCtx := s.stopCtx
	s.mu.Unlock()
	if stopCtx != nil {
		stopCtx()
	}
	if running && s.id != "" {
		c := s.client
		c.mu.Lock()
		delete(c.streams, s.id)
		c.mu.Unlock()
		_ = c.send(s.conn, outFrame{Type: "cancel", ID: s.id})
	}
}

func (s *rawStream) error() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func startStream(ctx context.Context, c *Client, method string, params []any) *rawStream {
	s := newRawStream()
	s.client = c
	raw, err := marshalParams(params)
	if err != nil {
		s.stop(err)
		return s
	}
	for {
		conn, err := c.waitConnected(ctx)
		if err != nil {
			s.stop(err)
			return s
		}
		s.id = c.newID()
		s.conn = conn
		c.mu.Lock()
		if c.conn != conn {
			c.mu.Unlock()
			continue
		}
		c.streams[s.id] = s
		c.mu.Unlock()
		break
	}
	if err := c.send(s.conn, outFrame{Type: "request", ID: s.id, Method: method, Params: raw}); err != nil {
		c.mu.Lock()
		delete(c.streams, s.id)
		c.mu.Unlock()
		if !errors.Is(err, ErrMessageTooLarge) {
			err = fmt.Errorf("%w: %v", ErrConnectionLost, err)
		}
		s.end(err)
		return s
	}
	stopCtx := context.AfterFunc(ctx, func() { s.stop(ctx.Err()) })
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		stopCtx()
	} else {
		s.stopCtx = stopCtx
		s.mu.Unlock()
	}
	return s
}

// StreamResult is a running call to a handler that returns iter.Seq[T].
//
// Read it with All, then check Err:
//
//	s := client.Stream[Row](ctx, c, "Rows.Export", nil)
//	defer s.Close()
//	for row := range s.All() {
//		...
//	}
//	if err := s.Err(); err != nil { ... }
//
// Leaving the loop early cancels the handler at the server, as does
// cancelling ctx. Streams are not resumed after a reconnect: a stream in
// flight when the connection drops ends with [ErrConnectionLost].
type StreamResult[T any] struct {
	s *rawStream
}

// Stream starts a call to a streaming handler. It waits while the client is
// reconnecting; bound the wait with ctx. A request larger than the server
// accepts ends the stream with [ErrMessageTooLarge] without being sent. A
// method that is not a streaming handler ends it with CodeInvalidRequest.
func Stream[T any](ctx context.Context, c *Client, method string, params []any) *StreamResult[T] {
	return &StreamResult[T]{s: startStream(ctx, c, method, params)}
}

// All yields each item as it arrives. It can be ranged over once.
func (r *StreamResult[T]) All() iter.Seq[T] {
	return func(yield func(T) bool) {
		defer r.s.stop(nil)
		for {
			raw, ok := r.s.next()
			if !ok {
				return
			}
			var v T
			if err := unmarshalWire(raw, &v); err != nil {
				r.s.stop(fmt.Errorf("aprot client: decoding stream item as %T: %w", v, err))
				return
			}
			if !yield(v) {
				return
			}
		}
	}
}

// Err reports why the stream ended: nil after it completed or after the
// consumer stopped early, otherwise the error.
func (r *StreamResult[T]) Err() error { return r.s.error() }

// Close cancels the stream if it is still running. Safe to call more than
// once.
func (r *StreamResult[T]) Close() { r.s.stop(nil) }

// Stream2Result is a running call to a handler that returns
// iter.Seq2[K, V]. It works like [StreamResult].
type Stream2Result[K, V any] struct {
	s *rawStream
}

// Stream2 starts a call to a streaming handler that yields key/value pairs.
func Stream2[K, V any](ctx context.Context, c *Client, method string, params []any) *Stream2Result[K, V] {
	return &Stream2Result[K, V]{s: startStream(ctx, c, method, params)}
}

// All yields each pair as it arrives. It can be ranged over once.
func (r *Stream2Result[K, V]) All() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		defer r.s.stop(nil)
		for {
			raw, ok := r.s.next()
			if !ok {
				return
			}
			var pair [2]jsontext.Value
			var k K
			var v V
			err := unmarshalWire(raw, &pair)
			if err == nil {
				err = unmarshalWire(pair[0], &k)
			}
			if err == nil {
				err = unmarshalWire(pair[1], &v)
			}
			if err != nil {
				r.s.stop(fmt.Errorf("aprot client: decoding stream pair: %w", err))
				return
			}
			if !yield(k, v) {
				return
			}
		}
	}
}

// Err reports why the stream ended. See [StreamResult.Err].
func (r *Stream2Result[K, V]) Err() error { return r.s.error() }

// Close cancels the stream if it is still running.
func (r *Stream2Result[K, V]) Close() { r.s.stop(nil) }
