package api

import (
	"context"
	"fmt"
	"iter"
	"time"
)

// StreamingHandlers demonstrates server-streaming handlers returning
// iter.Seq[T]. Each yielded value is delivered to the client as a separate
// websocket message, so the React UI can populate lists incrementally
// instead of waiting for the full response.
type StreamingHandlers struct{}

// NewStreamingHandlers creates a new StreamingHandlers instance.
func NewStreamingHandlers() *StreamingHandlers {
	return &StreamingHandlers{}
}

// StreamRow is one element of the Numbers stream.
type StreamRow struct {
	Index int    `json:"index"`
	Label string `json:"label"`
	Delay int    `json:"delayMs"`
}

// Numbers yields `count` rows with a configurable delay between each,
// simulating a slow list query (e.g. hitting multiple upstream APIs).
// The generated React hook useNumbers() accumulates items into state
// as they arrive, so the table grows row-by-row.
func (h *StreamingHandlers) Numbers(ctx context.Context, count int, delayMs int) (iter.Seq[*StreamRow], error) {
	if count < 0 {
		count = 0
	}
	if count > 500 {
		count = 500
	}
	if delayMs < 0 {
		delayMs = 0
	}
	if delayMs > 5000 {
		delayMs = 5000
	}
	return func(yield func(*StreamRow) bool) {
		for i := 1; i <= count; i++ {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(delayMs) * time.Millisecond):
			}
			if !yield(&StreamRow{
				Index: i,
				Label: fmt.Sprintf("row %d", i),
				Delay: delayMs,
			}) {
				return
			}
		}
	}, nil
}
