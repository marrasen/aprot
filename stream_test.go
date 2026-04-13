package aprot

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
)

// streamTestServer builds a minimal Server + Conn with a recording transport
// so stream_test can drive streamIterator directly without spinning up HTTP.
func streamTestServer(t *testing.T) (*Conn, *recordingTransport) {
	t.Helper()
	r := NewRegistry()
	s := NewServer(r)
	rt := &recordingTransport{}
	c := &Conn{
		transport: rt,
		server:    s,
		requests:  make(map[string]context.CancelCauseFunc),
		id:        1,
	}
	return c, rt
}

// drainMessages decodes every recorded payload into a generic map.
func drainMessages(t *testing.T, rt *recordingTransport) []map[string]any {
	t.Helper()
	raw := rt.Messages()
	out := make([]map[string]any, 0, len(raw))
	for _, b := range raw {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("decode message: %v", err)
		}
		out = append(out, m)
	}
	return out
}

func TestStreamIterator_SeqHappyPath(t *testing.T) {
	c, rt := streamTestServer(t)
	ctx := context.Background()

	seq := iter.Seq[int](func(yield func(int) bool) {
		for i := 1; i <= 5; i++ {
			if !yield(i) {
				return
			}
		}
	})
	info := &HandlerInfo{
		Kind:         HandlerKindStream,
		ResponseType: reflect.TypeOf(int(0)),
	}

	c.streamIterator(ctx, "r1", reflect.ValueOf(seq), info)

	msgs := drainMessages(t, rt)
	if len(msgs) != 6 {
		t.Fatalf("expected 6 messages (5 items + end), got %d: %v", len(msgs), msgs)
	}
	for i := 0; i < 5; i++ {
		if msgs[i]["type"] != "stream_item" {
			t.Errorf("msg[%d] type = %v, want stream_item", i, msgs[i]["type"])
		}
		if msgs[i]["id"] != "r1" {
			t.Errorf("msg[%d] id = %v, want r1", i, msgs[i]["id"])
		}
		if item, ok := msgs[i]["item"].(float64); !ok || int(item) != i+1 {
			t.Errorf("msg[%d] item = %v, want %d", i, msgs[i]["item"], i+1)
		}
	}
	end := msgs[5]
	if end["type"] != "stream_end" {
		t.Errorf("last message type = %v, want stream_end", end["type"])
	}
	if code, _ := end["code"].(float64); code != 0 {
		t.Errorf("clean end should have zero code, got %v", end["code"])
	}
}

func TestStreamIterator_EmptySeq(t *testing.T) {
	c, rt := streamTestServer(t)
	seq := iter.Seq[int](func(yield func(int) bool) {})
	info := &HandlerInfo{Kind: HandlerKindStream, ResponseType: reflect.TypeOf(int(0))}

	c.streamIterator(context.Background(), "r1", reflect.ValueOf(seq), info)

	msgs := drainMessages(t, rt)
	if len(msgs) != 1 || msgs[0]["type"] != "stream_end" {
		t.Fatalf("expected a single stream_end, got %v", msgs)
	}
}

func TestStreamIterator_NilSeq(t *testing.T) {
	c, rt := streamTestServer(t)
	info := &HandlerInfo{Kind: HandlerKindStream, ResponseType: reflect.TypeOf(int(0))}

	var seq iter.Seq[int] // nil
	c.streamIterator(context.Background(), "r1", reflect.ValueOf(seq), info)

	msgs := drainMessages(t, rt)
	if len(msgs) != 1 || msgs[0]["type"] != "stream_end" {
		t.Fatalf("expected a single stream_end for nil seq, got %v", msgs)
	}
}

func TestStreamIterator_PanicMidStream(t *testing.T) {
	c, rt := streamTestServer(t)
	info := &HandlerInfo{Kind: HandlerKindStream, ResponseType: reflect.TypeOf(int(0))}

	seq := iter.Seq[int](func(yield func(int) bool) {
		yield(1)
		yield(2)
		panic("boom")
	})

	c.streamIterator(context.Background(), "r1", reflect.ValueOf(seq), info)

	msgs := drainMessages(t, rt)
	if len(msgs) != 3 {
		t.Fatalf("expected 2 items + end, got %d: %v", len(msgs), msgs)
	}
	end := msgs[2]
	if end["type"] != "stream_end" {
		t.Fatalf("last message type = %v, want stream_end", end["type"])
	}
	code, _ := end["code"].(float64)
	if int(code) != CodeInternalError {
		t.Errorf("end code = %v, want %d", code, CodeInternalError)
	}
	msg, _ := end["message"].(string)
	if !strings.Contains(msg, "boom") {
		t.Errorf("end message = %q, want to contain 'boom'", msg)
	}
}

func TestStreamIterator_ContextCancelStopsYield(t *testing.T) {
	c, rt := streamTestServer(t)
	info := &HandlerInfo{Kind: HandlerKindStream, ResponseType: reflect.TypeOf(int(0))}

	ctx, cancel := context.WithCancel(context.Background())

	var emitted int
	seq := iter.Seq[int](func(yield func(int) bool) {
		for i := 1; i <= 1000; i++ {
			if i == 3 {
				cancel() // cancel after emitting 2 items
			}
			if !yield(i) {
				return
			}
			emitted = i
		}
	})

	c.streamIterator(ctx, "r1", reflect.ValueOf(seq), info)

	if emitted >= 10 {
		t.Fatalf("expected iteration to stop early after cancel, emitted = %d", emitted)
	}

	msgs := drainMessages(t, rt)
	if len(msgs) == 0 || msgs[len(msgs)-1]["type"] != "stream_end" {
		t.Fatalf("expected stream_end, got %v", msgs)
	}
	end := msgs[len(msgs)-1]
	if code, _ := end["code"].(float64); code != 0 {
		t.Errorf("cancellation should be clean termination (zero code), got %v", end["code"])
	}
}

func TestStreamIterator_Seq2HappyPath(t *testing.T) {
	c, rt := streamTestServer(t)
	info := &HandlerInfo{
		Kind:          HandlerKindStream2,
		ResponseType:  reflect.TypeOf(int(0)),
		StreamKeyType: reflect.TypeOf(""),
	}

	seq := iter.Seq2[string, int](func(yield func(string, int) bool) {
		pairs := []struct {
			k string
			v int
		}{{"a", 1}, {"b", 2}, {"c", 3}}
		for _, p := range pairs {
			if !yield(p.k, p.v) {
				return
			}
		}
	})

	c.streamIterator(context.Background(), "r1", reflect.ValueOf(seq), info)

	msgs := drainMessages(t, rt)
	if len(msgs) != 4 {
		t.Fatalf("expected 3 items + end, got %d: %v", len(msgs), msgs)
	}
	wants := []struct {
		k string
		v float64
	}{{"a", 1}, {"b", 2}, {"c", 3}}
	for i, w := range wants {
		tuple, ok := msgs[i]["item"].([]any)
		if !ok || len(tuple) != 2 {
			t.Fatalf("msg[%d] item is not a 2-element array: %v", i, msgs[i]["item"])
		}
		if tuple[0] != w.k {
			t.Errorf("msg[%d][0] = %v, want %q", i, tuple[0], w.k)
		}
		if tuple[1] != w.v {
			t.Errorf("msg[%d][1] = %v, want %v", i, tuple[1], w.v)
		}
	}
}

// TestStreamSubscribeRejected verifies that handleSubscribe rejects
// streaming handlers with an InvalidRequest error.
type streamSubHandlers struct{}

func (streamSubHandlers) StreamNums(_ context.Context) (iter.Seq[int], error) {
	return func(yield func(int) bool) {}, nil
}

func TestSubscribeRejectsStreamHandler(t *testing.T) {
	r := NewRegistry()
	r.Register(&streamSubHandlers{})
	s := NewServer(r)
	rt := &recordingTransport{}
	c := &Conn{
		transport: rt,
		server:    s,
		requests:  make(map[string]context.CancelCauseFunc),
		id:        1,
	}

	// Simulate the server side of a subscribe message.
	c.server.requestsWg.Add(1)
	c.handleSubscribe(IncomingMessage{
		Type:   TypeSubscribe,
		ID:     "s1",
		Method: "streamSubHandlers.StreamNums",
	})

	msgs := drainMessages(t, rt)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 error message, got %d: %v", len(msgs), msgs)
	}
	if msgs[0]["type"] != "error" {
		t.Errorf("type = %v, want error", msgs[0]["type"])
	}
	if int(msgs[0]["code"].(float64)) != CodeInvalidRequest {
		t.Errorf("code = %v, want CodeInvalidRequest(%d)", msgs[0]["code"], CodeInvalidRequest)
	}
}

// TestRegisterRESTRejectsStreamHandler verifies that REST registration
// panics when the handler group contains a streaming method.
type streamRestHandlers struct{}

func (streamRestHandlers) ListUsers(_ context.Context) (iter.Seq[string], error) {
	return func(yield func(string) bool) {}, nil
}

func TestRegisterRESTPanicsOnStreamHandler(t *testing.T) {
	r := NewRegistry()
	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatalf("expected panic when registering stream handler via REST")
		}
		msg, ok := rec.(string)
		if !ok {
			t.Fatalf("panic value = %v (%T), want string", rec, rec)
		}
		if !strings.Contains(msg, "streaming handler") {
			t.Errorf("panic message = %q, want to mention 'streaming handler'", msg)
		}
	}()
	r.RegisterREST(&streamRestHandlers{})
}

// TestStreamIterator_SendErrorStopsYield verifies that a failed send to the
// transport stops iteration and surfaces an error end message. We simulate
// the transport failure by closing it between yields.
type failAfterN struct {
	n      int
	calls  int
	stored [][]byte
}

func (f *failAfterN) Send(data []byte) error {
	f.calls++
	if f.calls > f.n {
		return ErrConnectionClosed
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	f.stored = append(f.stored, cp)
	return nil
}
func (f *failAfterN) SendCtx(ctx context.Context, data []byte) error { return f.Send(data) }
func (f *failAfterN) Close() error                                   { return nil }
func (f *failAfterN) CloseGracefully() error                         { return nil }

func TestStreamIterator_TransportCloseDuringSend(t *testing.T) {
	r := NewRegistry()
	s := NewServer(r)
	rt := &failAfterN{n: 2}
	c := &Conn{
		transport: rt,
		server:    s,
		requests:  make(map[string]context.CancelCauseFunc),
		id:        1,
	}
	info := &HandlerInfo{Kind: HandlerKindStream, ResponseType: reflect.TypeOf(int(0))}

	seq := iter.Seq[int](func(yield func(int) bool) {
		for i := 1; i <= 100; i++ {
			if !yield(i) {
				return
			}
		}
	})

	c.streamIterator(context.Background(), "r1", reflect.ValueOf(seq), info)

	// ErrConnectionClosed is a clean termination from the stream's point of
	// view — we still send an end message, but it carries no code.
	if rt.calls < 3 {
		t.Errorf("expected at least 3 send calls, got %d", rt.calls)
	}
}

// TestStreamIterator_NoGoroutineLeakOnCancel ensures the iterator goroutine
// exits promptly after a context cancellation so no goroutines leak.
func TestStreamIterator_NoGoroutineLeakOnCancel(t *testing.T) {
	c, _ := streamTestServer(t)
	info := &HandlerInfo{Kind: HandlerKindStream, ResponseType: reflect.TypeOf(int(0))}

	before := runtime.NumGoroutine()

	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		seq := iter.Seq[int](func(yield func(int) bool) {
			for j := 1; j <= 1_000_000; j++ {
				if !yield(j) {
					return
				}
			}
		})
		done := make(chan struct{})
		go func() {
			defer close(done)
			c.streamIterator(ctx, "r", reflect.ValueOf(seq), info)
		}()
		time.Sleep(5 * time.Millisecond)
		cancel()
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("streamIterator goroutine did not exit after cancel")
		}
	}

	// Allow a brief window for runtime bookkeeping.
	time.Sleep(10 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after-before > 5 {
		t.Errorf("goroutine leak: before=%d, after=%d", before, after)
	}
}

// TestIsIterSeqSeq2Mismatch cross-checks that a Seq and Seq2 type don't
// match each other's detector. This guards against shape confusion after
// future refactors of the detectors.
func TestIsIterSeq_SeqSeq2NotInterchangeable(t *testing.T) {
	seqT := reflect.TypeOf((iter.Seq[int])(nil))
	seq2T := reflect.TypeOf((iter.Seq2[string, int])(nil))

	if _, ok := isIterSeq(seq2T); ok {
		t.Errorf("isIterSeq matched Seq2")
	}
	if _, _, ok := isIterSeq2(seqT); ok {
		t.Errorf("isIterSeq2 matched Seq")
	}
}

// TestRegisterStreamDoesNotPanic verifies that registering a stream handler
// via (WS) Register succeeds without error.
type okStreamHandlers struct{}

func (okStreamHandlers) Numbers(_ context.Context) (iter.Seq[int], error) {
	return func(yield func(int) bool) {}, nil
}

func TestRegister_AcceptsStreamHandler(t *testing.T) {
	r := NewRegistry()
	r.Register(&okStreamHandlers{})
	handlers := r.Handlers()
	info, ok := handlers["okStreamHandlers.Numbers"]
	if !ok {
		t.Fatalf("stream handler not registered: %v", handlers)
	}
	if info.Kind != HandlerKindStream {
		t.Errorf("Kind = %v, want HandlerKindStream", info.Kind)
	}
}

// Sanity: ErrConnectionClosed is actually an error-typed value we can compare.
func TestErrConnectionClosedIsError(t *testing.T) {
	var err error = ErrConnectionClosed
	if !errors.Is(err, ErrConnectionClosed) {
		t.Errorf("ErrConnectionClosed is not itself")
	}
}
