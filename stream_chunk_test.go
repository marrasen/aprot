package aprot

import (
	"context"
	"iter"
	"reflect"
	"testing"
	"time"
)

// streamChunkTestServer builds a Server with chunking enabled + a Conn with a
// recording transport, mirroring streamTestServer.
func streamChunkTestServer(t *testing.T, cfg StreamChunking) (*Conn, *recordingTransport) {
	t.Helper()
	r := NewRegistry()
	s := NewServer(r, ServerOptions{StreamChunking: &cfg})
	rt := &recordingTransport{}
	c := &Conn{
		transport: rt,
		server:    s,
		requests:  make(map[string]context.CancelCauseFunc),
		id:        1,
	}
	return c, rt
}

// chunkItems extracts the items array from a decoded stream_chunk message.
func chunkItems(t *testing.T, msg map[string]any) []any {
	t.Helper()
	items, ok := msg["items"].([]any)
	if !ok {
		t.Fatalf("stream_chunk message has no items array: %v", msg)
	}
	return items
}

func TestStreamChunking_MaxItems(t *testing.T) {
	c, rt := streamChunkTestServer(t, StreamChunking{MaxItems: 2, MaxBytes: 1 << 20, MaxDelay: time.Minute})

	seq := iter.Seq[int](func(yield func(int) bool) {
		for i := 1; i <= 5; i++ {
			if !yield(i) {
				return
			}
		}
	})
	info := &HandlerInfo{Kind: HandlerKindStream, ResponseType: reflect.TypeOf(int(0))}

	c.streamIterator(context.Background(), "r1", reflect.ValueOf(seq), info, nil)

	msgs := drainMessages(t, rt)
	// 5 items at MaxItems=2 → chunks of [1,2], [3,4], [5], then stream_end.
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages (3 chunks + end), got %d: %v", len(msgs), msgs)
	}
	wantChunks := [][]int{{1, 2}, {3, 4}, {5}}
	for i, want := range wantChunks {
		if msgs[i]["type"] != "stream_chunk" {
			t.Errorf("msg[%d] type = %v, want stream_chunk", i, msgs[i]["type"])
		}
		if msgs[i]["id"] != "r1" {
			t.Errorf("msg[%d] id = %v, want r1", i, msgs[i]["id"])
		}
		items := chunkItems(t, msgs[i])
		if len(items) != len(want) {
			t.Fatalf("chunk[%d] has %d items, want %d: %v", i, len(items), len(want), items)
		}
		for j, w := range want {
			if v, ok := items[j].(float64); !ok || int(v) != w {
				t.Errorf("chunk[%d] item[%d] = %v, want %d", i, j, items[j], w)
			}
		}
	}
	if msgs[3]["type"] != "stream_end" {
		t.Errorf("last message type = %v, want stream_end", msgs[3]["type"])
	}
	if code, _ := msgs[3]["code"].(float64); code != 0 {
		t.Errorf("clean end should have zero code, got %v", msgs[3]["code"])
	}
}

func TestStreamChunking_MaxBytes(t *testing.T) {
	// Each item is a 10-byte JSON string ("aaaaaaaa"). MaxBytes 15 → the
	// second item pushes the buffer to 20 ≥ 15, flushing a 2-item chunk.
	c, rt := streamChunkTestServer(t, StreamChunking{MaxItems: 100, MaxBytes: 15, MaxDelay: time.Minute})

	seq := iter.Seq[string](func(yield func(string) bool) {
		for i := 0; i < 4; i++ {
			if !yield("aaaaaaaa") {
				return
			}
		}
	})
	info := &HandlerInfo{Kind: HandlerKindStream, ResponseType: reflect.TypeOf("")}

	c.streamIterator(context.Background(), "r1", reflect.ValueOf(seq), info, nil)

	msgs := drainMessages(t, rt)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (2 chunks + end), got %d: %v", len(msgs), msgs)
	}
	for i := 0; i < 2; i++ {
		if msgs[i]["type"] != "stream_chunk" {
			t.Errorf("msg[%d] type = %v, want stream_chunk", i, msgs[i]["type"])
		}
		items := chunkItems(t, msgs[i])
		if len(items) != 2 {
			t.Errorf("chunk[%d] has %d items, want 2", i, len(items))
		}
	}
	if msgs[2]["type"] != "stream_end" {
		t.Errorf("last message type = %v, want stream_end", msgs[2]["type"])
	}
}

func TestStreamChunking_MaxDelayFlushesPartialChunk(t *testing.T) {
	// A producer that stalls after two items must not hold them hostage:
	// the MaxDelay timer flushes the partial chunk while the stream is open.
	c, rt := streamChunkTestServer(t, StreamChunking{MaxItems: 100, MaxBytes: 1 << 20, MaxDelay: 20 * time.Millisecond})

	release := make(chan struct{})
	flushed := make(chan struct{})
	seq := iter.Seq[int](func(yield func(int) bool) {
		yield(1)
		yield(2)
		// Signal readiness, then stall until the test has observed the
		// timer-driven flush.
		close(flushed)
		<-release
		yield(3)
	})
	info := &HandlerInfo{Kind: HandlerKindStream, ResponseType: reflect.TypeOf(int(0))}

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.streamIterator(context.Background(), "r1", reflect.ValueOf(seq), info, nil)
	}()

	<-flushed
	// Wait for the MaxDelay timer to fire and flush the partial chunk.
	deadline := time.After(2 * time.Second)
	for {
		if msgs := drainMessages(t, rt); len(msgs) > 0 {
			items := chunkItems(t, msgs[0])
			if msgs[0]["type"] != "stream_chunk" || len(items) != 2 {
				t.Fatalf("expected first frame to be a 2-item stream_chunk, got %v", msgs[0])
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for MaxDelay flush of the partial chunk")
		case <-time.After(5 * time.Millisecond):
		}
	}

	close(release)
	<-done

	msgs := drainMessages(t, rt)
	last := msgs[len(msgs)-1]
	if last["type"] != "stream_end" {
		t.Fatalf("last message type = %v, want stream_end", last["type"])
	}
	// All three items must have arrived across the chunk frames, in order.
	var got []int
	for _, m := range msgs[:len(msgs)-1] {
		if m["type"] != "stream_chunk" {
			t.Fatalf("unexpected frame type %v: %v", m["type"], m)
		}
		for _, it := range chunkItems(t, m) {
			got = append(got, int(it.(float64)))
		}
	}
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("items across chunks = %v, want [1 2 3]", got)
	}
}

func TestStreamChunking_Seq2Pairs(t *testing.T) {
	c, rt := streamChunkTestServer(t, StreamChunking{MaxItems: 10, MaxBytes: 1 << 20, MaxDelay: time.Minute})

	seq := iter.Seq2[string, int](func(yield func(string, int) bool) {
		yield("a", 1)
		yield("b", 2)
	})
	info := &HandlerInfo{
		Kind:          HandlerKindStream2,
		ResponseType:  reflect.TypeOf(int(0)),
		StreamKeyType: reflect.TypeOf(""),
	}

	c.streamIterator(context.Background(), "r1", reflect.ValueOf(seq), info, nil)

	msgs := drainMessages(t, rt)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (1 chunk + end), got %d: %v", len(msgs), msgs)
	}
	items := chunkItems(t, msgs[0])
	if len(items) != 2 {
		t.Fatalf("chunk has %d items, want 2", len(items))
	}
	want := []struct {
		k string
		v int
	}{{"a", 1}, {"b", 2}}
	for i, w := range want {
		pair, ok := items[i].([]any)
		if !ok || len(pair) != 2 {
			t.Fatalf("item[%d] = %v, want a [k, v] pair", i, items[i])
		}
		if pair[0] != w.k {
			t.Errorf("item[%d][0] = %v, want %q", i, pair[0], w.k)
		}
		if v, ok := pair[1].(float64); !ok || int(v) != w.v {
			t.Errorf("item[%d][1] = %v, want %d", i, pair[1], w.v)
		}
	}
}

func TestStreamChunking_PanicFlushesBufferedItemsBeforeErrorEnd(t *testing.T) {
	c, rt := streamChunkTestServer(t, StreamChunking{MaxItems: 10, MaxBytes: 1 << 20, MaxDelay: time.Minute})

	seq := iter.Seq[int](func(yield func(int) bool) {
		yield(1)
		yield(2)
		panic("boom")
	})
	info := &HandlerInfo{Kind: HandlerKindStream, ResponseType: reflect.TypeOf(int(0))}

	c.streamIterator(context.Background(), "r1", reflect.ValueOf(seq), info, nil)

	msgs := drainMessages(t, rt)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (flushed chunk + error end), got %d: %v", len(msgs), msgs)
	}
	items := chunkItems(t, msgs[0])
	if len(items) != 2 {
		t.Errorf("flushed chunk has %d items, want 2 (yielded before the panic)", len(items))
	}
	end := msgs[1]
	if end["type"] != "stream_end" {
		t.Fatalf("last message type = %v, want stream_end", end["type"])
	}
	if code, _ := end["code"].(float64); int(code) != CodeInternalError {
		t.Errorf("end code = %v, want %d", end["code"], CodeInternalError)
	}
}

func TestStreamChunking_EmptySeq(t *testing.T) {
	c, rt := streamChunkTestServer(t, StreamChunking{MaxItems: 10, MaxBytes: 1 << 20, MaxDelay: time.Minute})

	seq := iter.Seq[int](func(yield func(int) bool) {})
	info := &HandlerInfo{Kind: HandlerKindStream, ResponseType: reflect.TypeOf(int(0))}

	c.streamIterator(context.Background(), "r1", reflect.ValueOf(seq), info, nil)

	msgs := drainMessages(t, rt)
	if len(msgs) != 1 || msgs[0]["type"] != "stream_end" {
		t.Fatalf("expected a single stream_end (no empty chunk frame), got %v", msgs)
	}
}

func TestStreamChunking_UnmarshalableItemErrorsStream(t *testing.T) {
	c, rt := streamChunkTestServer(t, StreamChunking{MaxItems: 10, MaxBytes: 1 << 20, MaxDelay: time.Minute})

	// Channels have no JSON encoding; marshaling must fail and terminate the
	// stream with an error end, not silently drop the item.
	seq := iter.Seq[chan int](func(yield func(chan int) bool) {
		yield(make(chan int))
	})
	info := &HandlerInfo{Kind: HandlerKindStream, ResponseType: reflect.TypeOf(make(chan int))}

	err := c.streamIterator(context.Background(), "r1", reflect.ValueOf(seq), info, nil)
	if err == nil {
		t.Error("expected streamIterator to return the marshal error")
	}

	msgs := drainMessages(t, rt)
	last := msgs[len(msgs)-1]
	if last["type"] != "stream_end" {
		t.Fatalf("last message type = %v, want stream_end", last["type"])
	}
	if code, _ := last["code"].(float64); code == 0 {
		t.Error("stream_end after marshal failure should carry an error code")
	}
}

func TestStreamChunking_HookCountsItemsNotChunks(t *testing.T) {
	c, _ := streamChunkTestServer(t, StreamChunking{MaxItems: 2, MaxBytes: 1 << 20, MaxDelay: time.Minute})

	seq := iter.Seq[int](func(yield func(int) bool) {
		for i := 1; i <= 5; i++ {
			if !yield(i) {
				return
			}
		}
	})
	info := &HandlerInfo{Kind: HandlerKindStream, ResponseType: reflect.TypeOf(int(0))}

	ctx, hooks := withStreamCompleteHooks(context.Background())
	var gotItems int
	var gotErr error
	OnStreamComplete(ctx, func(err error, items int) {
		gotErr = err
		gotItems = items
	})

	c.streamIterator(ctx, "r1", reflect.ValueOf(seq), info, hooks)

	if gotErr != nil {
		t.Errorf("hook err = %v, want nil", gotErr)
	}
	if gotItems != 5 {
		t.Errorf("hook items = %d, want 5 (individual items, not chunk frames)", gotItems)
	}
}

func TestStreamChunking_OptionDefaults(t *testing.T) {
	// Enabling chunking with an empty config applies the documented defaults.
	s := NewServer(NewRegistry(), ServerOptions{StreamChunking: &StreamChunking{}})
	cfg := s.options.StreamChunking
	if cfg == nil {
		t.Fatal("StreamChunking option was not retained")
	}
	if cfg.MaxItems != 128 {
		t.Errorf("MaxItems default = %d, want 128", cfg.MaxItems)
	}
	if cfg.MaxBytes != 64<<10 {
		t.Errorf("MaxBytes default = %d, want %d", cfg.MaxBytes, 64<<10)
	}
	if cfg.MaxDelay != 20*time.Millisecond {
		t.Errorf("MaxDelay default = %v, want 20ms", cfg.MaxDelay)
	}

	// Not setting the option leaves chunking disabled.
	s2 := NewServer(NewRegistry())
	if s2.options.StreamChunking != nil {
		t.Error("chunking should be disabled by default")
	}
}
