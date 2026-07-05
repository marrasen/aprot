package aprot

import (
	"context"
	"sync"
	"time"

	"github.com/go-json-experiment/json/jsontext"
)

// streamChunker batches marshaled stream items into StreamChunkMessage frames
// per the server's [StreamChunking] thresholds. It is created per streaming
// request by streamIterator when chunking is enabled.
//
// add is called from the iteration goroutine; the MaxDelay flush fires on a
// timer goroutine, so the buffer is mutex-guarded. Frame ordering is
// preserved because every flush enqueues on the transport while holding the
// mutex.
type streamChunker struct {
	conn  *Conn
	ctx   context.Context
	reqID string
	cfg   StreamChunking

	mu    sync.Mutex
	items []jsontext.Value
	bytes int
	timer *time.Timer
	// err is the first marshal/send failure. It is sticky: once set, add
	// returns it so the yield callback stops the handler's iteration.
	err error
}

func newStreamChunker(ctx context.Context, conn *Conn, reqID string, cfg StreamChunking) *streamChunker {
	return &streamChunker{conn: conn, ctx: ctx, reqID: reqID, cfg: cfg}
}

// add marshals item into the buffer and flushes when the item-count or byte
// threshold is reached. The first item buffered after a flush arms the
// MaxDelay timer so a stalled producer cannot hold delivered items back.
func (sc *streamChunker) add(item any) error {
	data, err := marshalJSON(item)
	if err != nil {
		sc.mu.Lock()
		if sc.err == nil {
			sc.err = err
		}
		sc.mu.Unlock()
		return err
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.err != nil {
		return sc.err
	}
	sc.items = append(sc.items, data)
	sc.bytes += len(data)
	if len(sc.items) >= sc.cfg.MaxItems || sc.bytes >= sc.cfg.MaxBytes {
		return sc.flushLocked()
	}
	if len(sc.items) == 1 {
		sc.timer = time.AfterFunc(sc.cfg.MaxDelay, sc.delayFlush)
	}
	return nil
}

// delayFlush is the MaxDelay timer callback. A send failure here is recorded
// in sc.err and surfaces on the next add (or close).
func (sc *streamChunker) delayFlush() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.err == nil {
		_ = sc.flushLocked()
	}
}

// flushLocked sends the buffered items as one StreamChunkMessage. Caller
// must hold sc.mu. A no-op on an empty buffer.
func (sc *streamChunker) flushLocked() error {
	if sc.timer != nil {
		sc.timer.Stop()
		sc.timer = nil
	}
	if len(sc.items) == 0 {
		return sc.err
	}
	msg := StreamChunkMessage{Type: TypeStreamChunk, ID: sc.reqID, Items: sc.items}
	sc.items = nil
	sc.bytes = 0
	if err := sc.conn.sendJSONCtx(sc.ctx, msg); err != nil {
		if sc.err == nil {
			sc.err = err
		}
		return err
	}
	return nil
}

// close flushes any partial chunk and returns the first error the chunker
// encountered (marshal, send, or timer-flush failure). Called once after the
// handler's iteration finishes, before the terminal stream_end frame.
func (sc *streamChunker) close() error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.flushLocked()
}
