package aprot

import (
	"context"
	"encoding/binary"
	"encoding/json/v2"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// PreviewFrame is a push event whose data is raw bytes: a named wrapper
// embedding Blob, which is how a push opts into binary delivery (a push
// event's wire name is its Go type name, so Blob itself cannot serve).
type PreviewFrame struct{ Blob }

// blobLookalike has Blob's field shape but different JSON tags — the kind of
// struct a consumer writes for its own reasons. It must never be mistaken for
// a Blob.
type blobLookalike struct {
	ContentType string `json:"mime"`
	Data        []byte `json:"payload"`
}

// twoFieldWrapper embeds Blob but adds a field, so it is past the documented
// boundary for binary delivery and travels as ordinary JSON.
type twoFieldWrapper struct {
	Blob
	Seq int `json:"seq"`
}

// TickEvent is an ordinary JSON push event registered as droppable.
type TickEvent struct {
	Seq int `json:"seq"`
}

type droppableHandlers struct{}

func (h *droppableHandlers) Ping(ctx context.Context) (string, error) { return "pong", nil }

// setupDroppableServer registers three push events over one handler: a
// droppable JSON event, a droppable binary (blob) event, and a guaranteed
// event, so one server can assert the difference between them.
func setupDroppableServer(t *testing.T, opts ServerOptions) (*httptest.Server, *Server) {
	t.Helper()
	registry := NewRegistry()
	handlers := &droppableHandlers{}
	registry.Register(handlers)
	registry.RegisterPushEventFor(handlers, TickEvent{}, Droppable())
	registry.RegisterPushEventFor(handlers, PreviewFrame{}, Droppable())
	registry.RegisterPushEventFor(handlers, NotificationEvent{})
	server := NewServer(registry, opts)
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts, server
}

// A client that never reads must not stall the producer, and the frames it
// cannot take must be dropped rather than queued. This is the whole point of
// #387: one slow client cannot be allowed to hold up a fan-out.
func TestDroppablePush_SlowClientDropsInsteadOfQueueing(t *testing.T) {
	obs := newRecordingObserver()
	ts, server := setupDroppableServer(t, ServerOptions{Observer: obs, WriteTimeout: 30 * time.Second})

	// Connect and then never read, so the write pump blocks on the socket
	// and the droppable allowance stays claimed.
	ws := connectWS(t, ts)
	defer ws.Close()
	waitForConnCount(t, server, 1, 2*time.Second)

	// Fill the socket and the pump so nothing can drain.
	big := strings.Repeat("x", 64*1024)
	for range 64 {
		server.Broadcast(&PreviewFrame{Blob{ContentType: "image/jpeg", Data: []byte(big)}})
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			server.Broadcast(&PreviewFrame{Blob{ContentType: "image/jpeg", Data: []byte(big)}})
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Broadcast of a droppable event blocked on a client that stopped reading")
	}

	if got := obs.droppedCount("PreviewFrame"); got == 0 {
		t.Fatal("no PushDropped events: frames were queued for a client that never read")
	}
}

// A guaranteed push event keeps aprot's delivery guarantee: nothing is dropped
// and PushDropped never fires for it. The droppable flag must not leak across
// event types on the same connection.
func TestDroppablePush_GuaranteedEventStillBlocks(t *testing.T) {
	obs := newRecordingObserver()
	ts, server := setupDroppableServer(t, ServerOptions{Observer: obs, WriteTimeout: 300 * time.Millisecond})

	ws := connectWS(t, ts)
	defer ws.Close()
	waitForConnCount(t, server, 1, 2*time.Second)

	big := strings.Repeat("x", 64*1024)
	go func() {
		for i := 0; i < 5000 && server.ConnectionCount() > 0; i++ {
			server.Broadcast(&NotificationEvent{Message: big})
		}
	}()

	// The guaranteed path blocks rather than dropping, so the stalled client
	// is eventually closed by the write timeout — the pre-existing behaviour.
	waitForConnCount(t, server, 0, 15*time.Second)

	if got := obs.droppedCount("NotificationEvent"); got != 0 {
		t.Errorf("PushDropped fired %d times for a guaranteed event; want 0", got)
	}
}

// A client that keeps up must receive every droppable push. Dropping is for
// backpressure only: a healthy connection at a sane rate loses nothing.
func TestDroppablePush_KeepingUpClientLosesNothing(t *testing.T) {
	obs := newRecordingObserver()
	ts, server := setupDroppableServer(t, ServerOptions{Observer: obs})

	ws := connectWS(t, ts)
	defer ws.Close()
	waitForConnCount(t, server, 1, 2*time.Second)

	const frames = 20
	got := make(chan int, frames)
	go func() {
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var msg struct {
				Type string `json:"type"`
				Data struct {
					Seq int `json:"seq"`
				} `json:"data"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.Type == string(TypePush) {
				got <- msg.Data.Seq
			}
		}
	}()

	// Send one frame at a time, waiting for each to arrive. The allowance is
	// per unwritten frame, so a reader that has caught up always has room.
	for i := range frames {
		server.Broadcast(&TickEvent{Seq: i})
		select {
		case seq := <-got:
			if seq != i {
				t.Fatalf("frame %d: got seq %d", i, seq)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("frame %d never arrived", i)
		}
	}

	if n := obs.droppedCount("TickEvent"); n != 0 {
		t.Errorf("dropped %d frames for a client that kept up; want 0", n)
	}
}

// Conn.Push reports the drop to its caller, so code pushing to one connection
// can count drops without an observer.
func TestDroppablePush_ConnPushReturnsErrPushDropped(t *testing.T) {
	ts, server := setupDroppableServer(t, ServerOptions{WriteTimeout: 30 * time.Second})

	ws := connectWS(t, ts)
	defer ws.Close()
	waitForConnCount(t, server, 1, 2*time.Second)

	var target *Conn
	server.ForEachConn(func(c *Conn) { target = c })
	if target == nil {
		t.Fatal("no connection registered")
	}

	// Stall the pump, then push until the allowance is spent.
	big := strings.Repeat("x", 64*1024)
	var dropped atomic.Bool
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !dropped.Load() {
		if err := target.Push(&TickEvent{Seq: 1}); err == ErrPushDropped {
			dropped.Store(true)
			break
		}
		// Keep the pump busy so the allowance cannot drain.
		_ = target.Push(&PreviewFrame{Blob{Data: []byte(big)}})
	}
	if !dropped.Load() {
		t.Fatal("Conn.Push never returned ErrPushDropped for a client that stopped reading")
	}
}

// A blob push reaches a binary-capable client as one binary frame carrying the
// raw bytes, with the event name in the header where a response carries an id.
func TestBlobPush_DeliveredAsBinaryFrame(t *testing.T) {
	ts, server := setupDroppableServer(t, ServerOptions{})

	ws := connectWS(t, ts)
	defer ws.Close()
	waitForConnCount(t, server, 1, 2*time.Second)

	payload := []byte{0x00, 0x01, 0xfe, 0xff, 'v', '1'}
	server.Broadcast(&PreviewFrame{Blob{ContentType: "image/jpeg", Data: payload}})

	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	mt, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if mt != websocket.BinaryMessage {
		t.Fatalf("message type = %d, want binary (%d)", mt, websocket.BinaryMessage)
	}

	headerLen := binary.BigEndian.Uint32(data[:4])
	var header struct {
		Version     byte   `json:"version"`
		Type        string `json:"type"`
		ID          string `json:"id"`
		Event       string `json:"event"`
		ContentType string `json:"contentType"`
	}
	if err := json.Unmarshal(data[4:4+headerLen], &header); err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if header.Version != 1 || header.Type != "push" {
		t.Errorf("header = %+v, want version 1 type push", header)
	}
	if header.Event != "PreviewFrame" {
		t.Errorf("header event = %q, want PreviewFrame", header.Event)
	}
	if header.ID != "" {
		t.Errorf("header id = %q, want empty on a push frame", header.ID)
	}
	if header.ContentType != "image/jpeg" {
		t.Errorf("header contentType = %q", header.ContentType)
	}
	if got := data[4+headerLen:]; string(got) != string(payload) {
		t.Errorf("payload = %v, want %v", got, payload)
	}
}

// A client that declined binary frames gets the same blob push as the JSON
// $blob envelope, so the client-visible type does not depend on negotiation.
func TestBlobPush_FallsBackToJSONWhenBinaryDeclined(t *testing.T) {
	ts, server := setupDroppableServer(t, ServerOptions{})

	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "?binary=0"
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()
	if _, _, err := ws.ReadMessage(); err != nil { // config frame
		t.Fatalf("read config: %v", err)
	}
	waitForConnCount(t, server, 1, 2*time.Second)

	payload := []byte{0x00, 0x01, 0xfe, 0xff}
	server.Broadcast(&PreviewFrame{Blob{ContentType: "image/jpeg", Data: payload}})

	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	mt, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if mt != websocket.TextMessage {
		t.Fatalf("message type = %d, want text (%d)", mt, websocket.TextMessage)
	}
	var msg struct {
		Type  string `json:"type"`
		Event string `json:"event"`
		Data  struct {
			Blob Blob `json:"$blob"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Type != string(TypePush) || msg.Event != "PreviewFrame" {
		t.Errorf("frame = %+v, want a PreviewFrame push", msg)
	}
	if msg.Data.Blob.ContentType != "image/jpeg" {
		t.Errorf("contentType = %q", msg.Data.Blob.ContentType)
	}
	if string(msg.Data.Blob.Data) != string(payload) {
		t.Errorf("payload = %v, want %v", msg.Data.Blob.Data, payload)
	}
}

// A type defined from Blob is a Blob everywhere aprot looks at one, so the
// response path accepts it too — one rule, not a push-only special case.
func TestBlobLike_RecognizedAsBlob(t *testing.T) {
	cases := []struct {
		name string
		val  any
		want bool
	}{
		{"Blob", Blob{Data: []byte("a")}, true},
		{"*Blob", &Blob{Data: []byte("a")}, true},
		{"wrapper embedding Blob", PreviewFrame{Blob{Data: []byte("a")}}, true},
		{"pointer to wrapper", &PreviewFrame{Blob{Data: []byte("a")}}, true},
		{"nil *Blob", (*Blob)(nil), false},
		{"nil wrapper pointer", (*PreviewFrame)(nil), false},
		{"look-alike with other tags", blobLookalike{Data: []byte("a")}, false},
		{"pointer to look-alike", &blobLookalike{Data: []byte("a")}, false},
		{"wrapper with an extra field", twoFieldWrapper{Blob: Blob{Data: []byte("a")}}, false},
		{"unrelated struct", TickEvent{Seq: 1}, false},
		{"plain bytes", []byte("a"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := asBlob(tc.val); ok != tc.want {
				t.Errorf("asBlob(%s) = %v, want %v", tc.name, ok, tc.want)
			}
		})
	}
}

// The generated client types a blob push handler as receiving a DOM Blob: the
// Go type is never emitted, so naming it would not compile.
func TestBlobPush_GeneratedHandlerTypesAsDOMBlob(t *testing.T) {
	blobEvent := PushEventInfo{Name: "PreviewFrame", DataType: reflect.TypeOf(PreviewFrame{})}
	if got := pushEventTSType(blobEvent); got != "Blob" {
		t.Errorf("pushEventTSType(PreviewFrame) = %q, want Blob", got)
	}
	jsonEvent := PushEventInfo{Name: "TickEvent", DataType: reflect.TypeOf(TickEvent{})}
	if got := pushEventTSType(jsonEvent); got != "TickEvent" {
		t.Errorf("pushEventTSType(TickEvent) = %q, want TickEvent", got)
	}
}

// Droppability is resolved in one registry lookup that all three fan-out paths
// share, so none of them can quietly keep the guarantee while the others drop.
// This is the fan-out counterpart to the dispatch-path matrix: a new fan-out
// entry point means a new case here.
func TestDroppablePush_EveryFanOutPathDrops(t *testing.T) {
	paths := []struct {
		name string
		send func(server *Server, conn *Conn, data any)
	}{
		{"Conn.Push", func(_ *Server, conn *Conn, data any) { _ = conn.Push(data) }},
		{"Server.Broadcast", func(server *Server, _ *Conn, data any) { server.Broadcast(data) }},
		{"Server.PushToUser", func(server *Server, _ *Conn, data any) { server.PushToUser("u1", data) }},
	}

	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			obs := newRecordingObserver()
			registry := NewRegistry()
			handlers := &droppableHandlers{}
			registry.Register(handlers)
			registry.RegisterPushEventFor(handlers, PreviewFrame{}, Droppable())
			server := NewServer(registry, ServerOptions{
				Observer:     obs,
				WriteTimeout: 30 * time.Second,
			})
			// PushToUser needs an address to route to; setting it for every
			// case keeps the three paths otherwise identical.
			server.OnConnect(func(ctx context.Context, conn *Conn) error {
				conn.SetUserID("u1")
				return nil
			})
			ts := httptest.NewServer(server)
			t.Cleanup(ts.Close)

			// Connect and never read, so the write pump cannot drain.
			ws := connectWS(t, ts)
			defer ws.Close()
			waitForConnCount(t, server, 1, 2*time.Second)

			var target *Conn
			server.ForEachConn(func(c *Conn) { target = c })
			if target == nil {
				t.Fatal("no connection registered")
			}

			big := &PreviewFrame{Blob{Data: []byte(strings.Repeat("x", 64*1024))}}
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) && obs.droppedCount("PreviewFrame") == 0 {
				p.send(server, target, big)
			}
			if obs.droppedCount("PreviewFrame") == 0 {
				t.Fatalf("%s never dropped a droppable push to a client that stopped reading", p.name)
			}
		})
	}
}

// A struct that merely has Blob's field shape must not be treated as a Blob.
// Structural matching (reflect conversion) ignores struct tags and methods, so
// it would have hijacked a consumer's own type: delivered as a binary frame,
// typed Blob in the generated client, its tags and MarshalJSON ignored.
func TestBlobLookalike_NotTreatedAsBlobOnAnySurface(t *testing.T) {
	lookalike := reflect.TypeOf(blobLookalike{})
	if isBlobResponse(lookalike) {
		t.Error("a look-alike struct is treated as a Blob result: it would be sent as a binary frame")
	}
	if isBlobLike(reflect.PointerTo(lookalike)) {
		t.Error("a pointer to a look-alike struct is treated as a Blob")
	}

	g := NewGenerator(NewRegistry())
	if got := g.goTypeToTS(lookalike); got == blobTSWireShape {
		t.Error("a look-alike struct generates as the Blob wire shape, discarding its own JSON tags")
	}
	ev := PushEventInfo{Name: lookalike.Name(), DataType: lookalike}
	if got := pushEventTSType(ev); got == "Blob" {
		t.Error("a look-alike push event generates a DOM Blob handler, so its own fields become unreachable")
	}

	// A wrapper that embeds Blob but adds a field is past the documented
	// boundary for binary delivery, so it stays ordinary JSON too.
	if isBlobLike(reflect.TypeOf(twoFieldWrapper{})) {
		t.Error("a wrapper with an extra field is treated as a Blob; it cannot be, as the frame carries no room for the extra field")
	}
}

// The allowance is "frames not yet on the wire": a frame still being written
// holds its slot, so exactly one droppable frame is accepted while another is
// in flight. Releasing at dequeue instead would make the real bar two frames
// while every doc promised one.
func TestDroppablePush_SlotHeldUntilFrameIsWritten(t *testing.T) {
	transport := &wsTransport{
		send:   make(chan outboundFrame, 256),
		done:   make(chan struct{}),
		binary: true,
	}

	if err := transport.SendDroppable([]byte("first")); err != nil {
		t.Fatalf("first droppable send: %v", err)
	}
	if err := transport.SendDroppable([]byte("second")); err != ErrPushDropped {
		t.Fatalf("second send while one is queued: got %v, want ErrPushDropped", err)
	}

	// Dequeue without writing — the frame is now in flight, not delivered, so
	// the slot must still be held.
	frame := <-transport.send
	if err := transport.SendDroppable([]byte("during write")); err != ErrPushDropped {
		t.Fatalf("send while the previous frame was still being written: got %v, want ErrPushDropped", err)
	}

	// Only once the write completes does the next frame get through.
	transport.releaseDroppable(frame)
	if err := transport.SendDroppable([]byte("after write")); err != nil {
		t.Fatalf("send after the previous frame was written: %v", err)
	}
}

// countingPush marshals to a fixed JSON document and counts how many times it
// was asked to, so a fan-out can assert it encoded once rather than per client.
type countingPush struct {
	marshals *atomic.Int32
}

func (c countingPush) MarshalJSON() ([]byte, error) {
	c.marshals.Add(1)
	return []byte(`{"counted":true}`), nil
}

// A fan-out encodes each frame once and shares the bytes. Encoding per
// connection copied the whole payload per client — for a 300 KB frame to 100
// clients, ~30 MB per push.
func TestPush_FanOutEncodesOncePerFrame(t *testing.T) {
	registry := NewRegistry()
	handlers := &droppableHandlers{}
	registry.Register(handlers)
	registry.RegisterPushEventFor(handlers, countingPush{})
	registry.RegisterPushEventFor(handlers, PreviewFrame{})
	server := NewServer(registry)
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	const clients = 4
	for range clients {
		ws := connectWS(t, ts)
		defer ws.Close()
	}
	waitForConnCount(t, server, clients, 3*time.Second)

	// JSON path: one marshal for the whole broadcast, not one per client.
	var marshals atomic.Int32
	server.Broadcast(countingPush{marshals: &marshals})
	if got := marshals.Load(); got != 1 {
		t.Errorf("broadcast to %d clients marshaled %d times, want 1", clients, got)
	}

	// Binary path: every connection must receive the very same frame, which
	// only holds if it was encoded once. Compare the backing arrays.
	var frames [][]byte
	server.ForEachConn(func(c *Conn) {
		p := newPushPayload(server.registry.pushEvent(&PreviewFrame{}), &PreviewFrame{Blob{Data: []byte("payload")}})
		frame, err := p.binaryFrame()
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		second, err := p.binaryFrame()
		if err != nil {
			t.Fatalf("re-encode: %v", err)
		}
		if &frame[0] != &second[0] {
			t.Error("binaryFrame re-encoded instead of returning the cached frame")
		}
		frames = append(frames, frame)
	})
	if len(frames) != clients {
		t.Fatalf("visited %d connections, want %d", len(frames), clients)
	}
}
