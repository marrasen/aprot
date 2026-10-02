package client

import (
	"context"
	"encoding/json/jsontext"
)

// Progress is one progress report from a running handler.
type Progress struct {
	Current *int
	Total   *int
	Message string
}

type progressKey struct{}

// WithProgress returns a context that makes [Call] report the handler's
// progress updates to fn. fn runs on the client's read goroutine, so it must
// return quickly and must not make calls on the same client.
func WithProgress(ctx context.Context, fn func(Progress)) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

type callResult struct {
	raw  jsontext.Value
	blob *Blob
	err  error
}

type pendingCall struct {
	// ch receives exactly one result. Whoever removes the call from
	// Client.pending sends it, so the buffer of one never blocks.
	ch       chan callResult
	progress func(Progress)
}

// Call calls method with params and decodes the result into T. method is the
// wire name, "Group.Method". Use struct{} as T for a handler that returns
// only an error.
//
// While the client is reconnecting, Call waits for the connection; bound the
// wait with ctx. A call in flight when the connection drops fails with
// [ErrConnectionLost] and is not retried. Cancelling ctx sends a cancel to
// the server and returns ctx.Err().
func Call[T any](ctx context.Context, c *Client, method string, params []any) (T, error) {
	var zero T
	raw, blob, err := c.call(ctx, method, params)
	if err != nil {
		return zero, err
	}
	return decodeResult[T](raw, blob)
}

func (c *Client) call(ctx context.Context, method string, params []any) (jsontext.Value, *Blob, error) {
	paramsRaw, err := marshalParams(params)
	if err != nil {
		return nil, nil, err
	}
	progress, _ := ctx.Value(progressKey{}).(func(Progress))

	for {
		conn, err := c.waitConnected(ctx)
		if err != nil {
			return nil, nil, err
		}
		id := c.newID()
		p := &pendingCall{ch: make(chan callResult, 1), progress: progress}
		c.mu.Lock()
		if c.conn != conn {
			// The connection dropped between waiting and registering.
			c.mu.Unlock()
			continue
		}
		c.pending[id] = p
		c.mu.Unlock()

		if err := c.send(conn, outFrame{Type: "request", ID: id, Method: method, Params: paramsRaw}); err != nil {
			// The read loop will notice the broken connection and fail
			// every pending call; take this one back first if it can.
			if c.takePending(id) {
				return nil, nil, ErrConnectionLost
			}
		}

		select {
		case r := <-p.ch:
			return r.raw, r.blob, r.err
		case <-ctx.Done():
			if c.takePending(id) {
				_ = c.send(conn, outFrame{Type: "cancel", ID: id})
				return nil, nil, ctx.Err()
			}
			// The result raced the cancellation and is already on its way.
			r := <-p.ch
			return r.raw, r.blob, r.err
		}
	}
}

// takePending removes a pending call and reports whether it was still there.
func (c *Client) takePending(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.pending[id]; !ok {
		return false
	}
	delete(c.pending, id)
	return true
}
