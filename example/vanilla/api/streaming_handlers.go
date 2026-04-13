package api

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"time"
)

// StreamingHandlers demonstrates server-streaming handlers returning
// iter.Seq / iter.Seq2. Each yielded value is delivered to the client as
// a separate websocket message, so UIs can populate lists incrementally
// instead of waiting for the full response.
type StreamingHandlers struct{}

// NewStreamingHandlers creates a new StreamingHandlers instance.
func NewStreamingHandlers() *StreamingHandlers {
	return &StreamingHandlers{}
}

// StreamNumberItem is a single element of the StreamNumbers sequence.
type StreamNumberItem struct {
	Index int    `json:"index"`
	Label string `json:"label"`
}

// Numbers yields `count` sequential items with a small delay between
// each, simulating a server-side generator that produces results over time.
func (h *StreamingHandlers) Numbers(ctx context.Context, count int) (iter.Seq[*StreamNumberItem], error) {
	if count < 0 {
		return nil, errors.New("count must be non-negative")
	}
	return func(yield func(*StreamNumberItem) bool) {
		for i := 1; i <= count; i++ {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if !yield(&StreamNumberItem{Index: i, Label: fmt.Sprintf("item-%d", i)}) {
				return
			}
			// Tiny sleep so progressive rendering is observable in demos,
			// but fast enough that tests stay well under a second.
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}, nil
}

// Failing returns a preflight error. The client should observe a
// rejection on the initial request, not a stream_end error payload.
func (h *StreamingHandlers) Failing(_ context.Context) (iter.Seq[int], error) {
	return nil, errors.New("nope")
}

// Pairs demonstrates iter.Seq2[K, V]. Items arrive as [key, value]
// tuples on the TypeScript side.
func (h *StreamingHandlers) Pairs(_ context.Context) (iter.Seq2[string, int], error) {
	return func(yield func(string, int) bool) {
		pairs := []struct {
			k string
			v int
		}{{"alpha", 1}, {"beta", 2}, {"gamma", 3}}
		for _, p := range pairs {
			if !yield(p.k, p.v) {
				return
			}
		}
	}, nil
}

// Panics intentionally panics mid-stream. The server recovers the panic
// and sends a stream_end with an internal-error code. This lets the test
// suite verify that a crashing handler does not take down the process.
func (h *StreamingHandlers) Panics(_ context.Context) (iter.Seq[int], error) {
	return func(yield func(int) bool) {
		yield(1)
		yield(2)
		panic("stream panic for test")
	}, nil
}
