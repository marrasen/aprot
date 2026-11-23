package aprot

import (
	"context"
	"errors"
	"iter"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// newPushTestServer returns a server with the NotificationEvent push event
// registered and the given PushTimeout.
func newPushTestServer(t *testing.T, obs Observer, pushTimeout time.Duration) *Server {
	t.Helper()
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)
	registry.RegisterPushEventFor(handlers, NotificationEvent{})
	server := NewServer(registry, ServerOptions{Observer: obs, PushTimeout: pushTimeout})
	handlers.server = server
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Stop(ctx)
	})
	return server
}

// newUndrainedWSConn returns a connection on a WebSocket transport with a
// two-slot send buffer and no write pump, so a frame leaves the buffer only
// when the test takes it. The socket is real, so a push that times out closes
// it as it would in a server.
func newUndrainedWSConn(t *testing.T, server *Server) (*Conn, *wsTransport) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		accepted <- ws
	}))
	t.Cleanup(hs.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(hs.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ws := <-accepted
	t.Cleanup(func() { _ = ws.Close() })

	tr := newWSTransport(ws, server.options, false)
	tr.send = make(chan outboundFrame, 2)
	conn := newConn(tr, server, 1, ConnInfo{}, context.Background())
	tr.conn = conn
	return conn, tr
}

// takeFrame removes one frame from the send buffer the way the write pump
// does.
func takeFrame(tr *wsTransport) {
	<-tr.send
	tr.tookFrame()
}

func slowClosedCount(obs *recordingObserver) int {
	obs.mu.Lock()
	defer obs.mu.Unlock()
	return obs.slowClosed
}

func bufferFullCount(obs *recordingObserver) int {
	obs.mu.Lock()
	defer obs.mu.Unlock()
	return obs.bufferFull
}

func connClosed(c *Conn) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// A WebSocket client that stops reading is closed after PushTimeout, and the
// caller and the other client carry on. Before this, every push waited on the
// stalled connection until WriteTimeout (30s) closed it.
func TestPush_StalledWebSocketIsClosedAfterPushTimeout(t *testing.T) {
	tests := []struct {
		name string
		push func(server *Server, stalled, healthy *Conn, event NotificationEvent)
	}{
		{"Broadcast", func(server *Server, _, _ *Conn, event NotificationEvent) {
			server.Broadcast(event)
		}},
		{"PushToUser", func(server *Server, _, _ *Conn, event NotificationEvent) {
			server.PushToUser("stalled", event)
			server.PushToUser("healthy", event)
		}},
		{"Conn.Push", func(_ *Server, stalled, healthy *Conn, event NotificationEvent) {
			_ = stalled.Push(event)
			_ = healthy.Push(event)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := newRecordingObserver()
			server := newPushTestServer(t, obs, 100*time.Millisecond)

			// The first client to connect is the one that stops reading.
			var mu sync.Mutex
			var conns []*Conn
			server.OnConnect(func(_ context.Context, c *Conn) error {
				mu.Lock()
				defer mu.Unlock()
				if len(conns) == 0 {
					c.SetUserID("stalled")
				} else {
					c.SetUserID("healthy")
				}
				conns = append(conns, c)
				return nil
			})
			ts := httptest.NewServer(server)
			t.Cleanup(ts.Close)

			stalledWS := connectWS(t, ts)
			defer stalledWS.Close()
			waitForConnections(t, server, 1)
			healthyWS := connectWS(t, ts)
			defer healthyWS.Close()
			waitForConnections(t, server, 2)
			mu.Lock()
			stalled, healthy := conns[0], conns[1]
			mu.Unlock()

			// The healthy client reads every push and reports it.
			got := make(chan struct{}, 1)
			go func() {
				for {
					if _, _, err := healthyWS.ReadMessage(); err != nil {
						return
					}
					got <- struct{}{}
				}
			}()

			// Large pushes fill the stalled client's socket buffers, then its
			// send buffer. The push after that waits PushTimeout and closes it.
			event := NotificationEvent{Message: strings.Repeat("x", 64<<10)}
			const maxPushes = 4000
			pushes := 0
			for ; pushes < maxPushes && slowClosedCount(obs) == 0; pushes++ {
				start := time.Now()
				tt.push(server, stalled, healthy, event)
				if d := time.Since(start); d > 5*time.Second {
					t.Fatalf("push %d took %v, want at most PushTimeout (100ms)", pushes, d)
				}
				select {
				case <-got:
				case <-time.After(5 * time.Second):
					t.Fatalf("healthy client did not receive push %d", pushes)
				}
			}
			if n := slowClosedCount(obs); n != 1 {
				t.Fatalf("SlowConsumerClosed fired %d times after %d pushes, want 1", n, pushes)
			}
			if !connClosed(stalled) {
				t.Error("stalled connection is still open")
			}
			if connClosed(healthy) {
				t.Error("healthy connection was closed")
			}

			// The stalled connection is torn down at once, not after WriteTimeout.
			waitForConnections(t, server, 1)
			obs.mu.Lock()
			writeTimeouts := obs.writeTimeout
			obs.mu.Unlock()
			if writeTimeouts != 0 {
				t.Errorf("WriteTimedOut fired %d times, want 0", writeTimeouts)
			}
		})
	}
}

// A full send buffer that frees a slot within PushTimeout is ordinary
// backpressure: the push waits and the connection stays open.
func TestPush_FullBufferThatDrainsKeepsConnection(t *testing.T) {
	obs := newRecordingObserver()
	server := newPushTestServer(t, obs, 30*time.Second)
	conn, tr := newUndrainedWSConn(t, server)

	for i := 0; i < 2; i++ {
		if err := conn.Push(NotificationEvent{Message: "m"}); err != nil {
			t.Fatalf("Push %d returned %v, want nil", i, err)
		}
	}

	sent := make(chan error, 1)
	go func() { sent <- conn.Push(NotificationEvent{Message: "m"}) }()
	eventually(t, 3*time.Second, func() bool { return bufferFullCount(obs) == 1 })

	// Free one slot, as the write pump of a client that is reading would.
	takeFrame(tr)
	select {
	case err := <-sent:
		if err != nil {
			t.Fatalf("Push returned %v after a slot freed, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Push did not finish after a slot freed")
	}
	if connClosed(conn) {
		t.Error("connection was closed although its buffer drained")
	}
	if n := slowClosedCount(obs); n != 0 {
		t.Errorf("SlowConsumerClosed fired %d times, want 0", n)
	}
}

// A client on a slow link still takes frames. Pushes queued behind each other
// wait longer than PushTimeout in total, and the connection stays open because
// it never goes PushTimeout without taking a frame.
func TestPush_SlowConnectionThatKeepsReadingStaysOpen(t *testing.T) {
	const pushTimeout = 500 * time.Millisecond
	obs := newRecordingObserver()
	server := newPushTestServer(t, obs, pushTimeout)
	conn, tr := newUndrainedWSConn(t, server)

	// The buffer holds 2 frames and one leaves every 50ms, so the last of 30
	// pushes waits about 1.4s, well past PushTimeout.
	const pushers = 30
	stop := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				select {
				case <-tr.send:
					tr.tookFrame()
				default:
				}
			}
		}
	}()

	type result struct {
		err  error
		wait time.Duration
	}
	results := make(chan result, pushers)
	for i := 0; i < pushers; i++ {
		go func() {
			start := time.Now()
			err := conn.Push(NotificationEvent{Message: "m"})
			results <- result{err: err, wait: time.Since(start)}
		}()
	}

	var longest time.Duration
	for i := 0; i < pushers; i++ {
		select {
		case r := <-results:
			if r.err != nil {
				t.Errorf("Push returned %v, want nil", r.err)
			}
			longest = max(longest, r.wait)
		case <-time.After(30 * time.Second):
			t.Fatal("a push did not finish")
		}
	}
	close(stop)
	<-drained

	if longest <= pushTimeout {
		t.Errorf("the longest push waited %v, want more than PushTimeout (%v) for the test to mean anything", longest, pushTimeout)
	}
	if connClosed(conn) {
		t.Error("connection was closed although it kept taking frames")
	}
	if n := slowClosedCount(obs); n != 0 {
		t.Errorf("SlowConsumerClosed fired %d times, want 0", n)
	}
}

// A push keeps its place among the senders waiting for room. A sender that
// refills the buffer every time a slot frees, as a stream does, cannot keep
// the push waiting.
func TestPush_KeepsItsPlaceBehindOtherSenders(t *testing.T) {
	obs := newRecordingObserver()
	// Shorter than the time between two frames taken, so the push's timer
	// runs many times while it waits.
	server := newPushTestServer(t, obs, 60*time.Millisecond)
	conn, tr := newUndrainedWSConn(t, server)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	defer func() {
		close(stop)
		wg.Wait()
	}()

	// A sender that always wants the next slot.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			ctx, cancel := context.WithCancel(context.Background())
			sent := make(chan struct{})
			go func() {
				defer close(sent)
				_ = tr.SendCtx(ctx, []byte("stream item"))
			}()
			select {
			case <-sent:
				cancel()
			case <-stop:
				cancel()
				<-sent
				return
			}
		}
	}()
	// The sender fills the buffer and is then waiting for room.
	eventually(t, 3*time.Second, func() bool { return len(tr.send) == cap(tr.send) && bufferFullCount(obs) > 0 })

	// One frame leaves every 50ms.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				select {
				case <-tr.send:
					tr.tookFrame()
				default:
				}
			}
		}
	}()

	sent := make(chan error, 1)
	go func() { sent <- conn.Push(NotificationEvent{Message: "m"}) }()
	select {
	case err := <-sent:
		if err != nil {
			t.Fatalf("Push returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Push is still waiting after 100 frames left the buffer")
	}
	if connClosed(conn) {
		t.Error("connection was closed although it kept taking frames")
	}
}

// A connection that takes no frame for PushTimeout is closed, and
// SlowConsumerClosed fires before the transport is closed.
func TestPush_ConnectionThatTakesNoFrameIsClosed(t *testing.T) {
	const pushTimeout = 100 * time.Millisecond
	obs := newRecordingObserver()
	server := newPushTestServer(t, obs, pushTimeout)
	conn, tr := newUndrainedWSConn(t, server)

	for i := 0; i < 2; i++ {
		if err := conn.Push(NotificationEvent{Message: "m"}); err != nil {
			t.Fatalf("Push %d returned %v, want nil", i, err)
		}
	}

	start := time.Now()
	err := conn.Push(NotificationEvent{Message: "m"})
	if !errors.Is(err, ErrConnectionClosed) {
		t.Fatalf("Push to a connection that takes no frame returned %v, want ErrConnectionClosed", err)
	}
	if d := time.Since(start); d < pushTimeout {
		t.Errorf("Push gave up after %v, want it to wait PushTimeout (%v)", d, pushTimeout)
	}
	if !connClosed(conn) {
		t.Error("connection is still open")
	}
	select {
	case <-tr.done:
	default:
		t.Error("transport is still open")
	}
	if n := slowClosedCount(obs); n != 1 {
		t.Errorf("SlowConsumerClosed fired %d times, want 1", n)
	}
}

// The write pump records each frame it takes, which is what tells
// SendTimeout that the peer is reading.
func TestWritePump_RecordsFramesTaken(t *testing.T) {
	server := newPushTestServer(t, nil, time.Second)
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	connCh := make(chan *Conn, 1)
	server.OnConnect(func(_ context.Context, c *Conn) error {
		connCh <- c
		return nil
	})
	ws := connectWS(t, ts)
	defer ws.Close()
	conn := <-connCh
	tr := conn.transport.(*wsTransport)

	before := time.Now()
	server.Broadcast(NotificationEvent{Message: "m"})
	if _, _, err := ws.ReadMessage(); err != nil {
		t.Fatalf("read push: %v", err)
	}
	if taken := time.Unix(0, tr.lastTake.Load()); taken.Before(before) {
		t.Errorf("write pump recorded its last frame at %v, want a time after the push at %v", taken, before)
	}
}

// With PushTimeout disabled a push waits for as long as the buffer is full.
func TestPush_PushTimeoutDisabledWaits(t *testing.T) {
	obs := newRecordingObserver()
	server := newPushTestServer(t, obs, -1)
	conn, tr := newUndrainedWSConn(t, server)

	for i := 0; i < 2; i++ {
		if err := conn.Push(NotificationEvent{Message: "m"}); err != nil {
			t.Fatalf("Push %d returned %v, want nil", i, err)
		}
	}

	sent := make(chan error, 1)
	go func() { sent <- conn.Push(NotificationEvent{Message: "m"}) }()
	select {
	case err := <-sent:
		t.Fatalf("Push returned %v with the buffer full, want it to wait", err)
	case <-time.After(300 * time.Millisecond):
	}
	if connClosed(conn) {
		t.Fatal("connection was closed with PushTimeout disabled")
	}

	<-tr.send
	select {
	case err := <-sent:
		if err != nil {
			t.Fatalf("Push returned %v after a slot freed, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Push did not finish after a slot freed")
	}
}

// A byte-stream peer cannot reconnect, so PushTimeout does not apply to it:
// the push waits and the connection stays open.
func TestPush_ByteStreamIgnoresPushTimeout(t *testing.T) {
	obs := newRecordingObserver()
	server := newPushTestServer(t, obs, 50*time.Millisecond)
	serverEnd, clientEnd := net.Pipe()
	t.Cleanup(func() {
		_ = serverEnd.Close()
		_ = clientEnd.Close()
	})
	st := newStreamTransport(serverEnd, server.options)
	st.send = make(chan []byte, 2)
	conn := newConn(st, server, 1, ConnInfo{}, context.Background())
	st.conn = conn

	for i := 0; i < 2; i++ {
		if err := conn.Push(NotificationEvent{Message: "m"}); err != nil {
			t.Fatalf("Push %d returned %v, want nil", i, err)
		}
	}

	sent := make(chan error, 1)
	go func() { sent <- conn.Push(NotificationEvent{Message: "m"}) }()
	select {
	case err := <-sent:
		t.Fatalf("Push returned %v with the buffer full, want it to wait", err)
	case <-time.After(300 * time.Millisecond): // six times PushTimeout
	}
	if connClosed(conn) {
		t.Fatal("byte-stream connection was closed by PushTimeout")
	}

	<-st.send
	select {
	case err := <-sent:
		if err != nil {
			t.Fatalf("Push returned %v after a slot freed, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Push did not finish after a slot freed")
	}
	if n := slowClosedCount(obs); n != 0 {
		t.Errorf("SlowConsumerClosed fired %d times, want 0", n)
	}
}

// A push to a WebSocket connection whose buffer stays full returns
// ErrConnectionClosed after PushTimeout, and later pushes return at once.
func TestPush_FullBufferReturnsErrConnectionClosed(t *testing.T) {
	obs := newRecordingObserver()
	server := newPushTestServer(t, obs, 500*time.Millisecond)
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	connCh := make(chan *Conn, 1)
	server.OnConnect(func(_ context.Context, c *Conn) error {
		connCh <- c
		return nil
	})
	ws := connectWS(t, ts) // reads the config frame, then nothing
	defer ws.Close()
	conn := <-connCh
	waitForConnections(t, server, 1)

	event := NotificationEvent{Message: strings.Repeat("x", 64<<10)}
	var err error
	for i := 0; i < 4000 && err == nil; i++ {
		err = conn.Push(event)
	}
	if !errors.Is(err, ErrConnectionClosed) {
		t.Fatalf("Push to a stalled connection returned %v, want ErrConnectionClosed", err)
	}

	start := time.Now()
	if err := conn.Push(event); !errors.Is(err, ErrConnectionClosed) {
		t.Fatalf("Push to the closed connection returned %v, want ErrConnectionClosed", err)
	}
	if d := time.Since(start); d > 250*time.Millisecond {
		t.Errorf("Push to the closed connection took %v, want it to return without waiting PushTimeout (500ms)", d)
	}
	if n := slowClosedCount(obs); n != 1 {
		t.Errorf("SlowConsumerClosed fired %d times, want 1", n)
	}
}

type pushStreamHandlers struct{}

type pushStreamRow struct {
	N    int    `json:"n"`
	Text string `json:"text"`
}

// Rows yields rows as fast as the transport takes them, which keeps the send
// buffer full.
func (pushStreamHandlers) Rows(_ context.Context, count int) (iter.Seq[pushStreamRow], error) {
	return func(yield func(pushStreamRow) bool) {
		for i := 0; i < count; i++ {
			if !yield(pushStreamRow{N: i, Text: "some row payload some row payload"}) {
				return
			}
		}
	}, nil
}

// A client that is receiving a stream has a full send buffer and is reading.
// Broadcasts sent meanwhile wait their turn and the connection stays open.
// The client starts reading only once the buffer is full, so the test does not
// depend on the server outrunning the socket.
func TestBroadcast_DuringStreamKeepsConnection(t *testing.T) {
	obs := newRecordingObserver()
	registry := NewRegistry()
	handlers := &pushStreamHandlers{}
	registry.Register(handlers)
	registry.RegisterPushEventFor(handlers, NotificationEvent{})
	// A long PushTimeout keeps a slow test machine from closing the connection
	// in the moment between the buffer filling and the client starting to read.
	server := NewServer(registry, ServerOptions{Observer: obs, PushTimeout: 30 * time.Second})
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	ws := connectWS(t, ts)
	defer ws.Close()
	waitForConnections(t, server, 1)

	request := `{"type":"request","id":"1","method":"pushStreamHandlers.Rows","params":[50000]}`
	if err := ws.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
		t.Fatalf("write: %v", err)
	}

	stop := make(chan struct{})
	broadcasting := make(chan struct{})
	go func() {
		defer close(broadcasting)
		for {
			select {
			case <-stop:
				return
			default:
				server.Broadcast(NotificationEvent{Message: "tick"})
			}
		}
	}()

	// Read nothing until the stream and the broadcasts have filled the socket
	// buffers and then the send buffer.
	eventually(t, 30*time.Second, func() bool { return bufferFullCount(obs) > 0 })

	// The client then reads until the test closes the socket. It must keep
	// reading after the stream ends, or the broadcast in flight would wait on
	// a client that stopped.
	streamEnded := make(chan struct{})
	readErr := make(chan error, 1)
	go func() {
		ended := false
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			if !ended && strings.Contains(string(data), `"type":"stream_end"`) {
				ended = true
				close(streamEnded)
			}
		}
	}()

	select {
	case <-streamEnded:
	case err := <-readErr:
		close(stop)
		<-broadcasting
		t.Fatalf("socket failed before the stream ended: %v (SlowConsumerClosed fired %d times)", err, slowClosedCount(obs))
	case <-time.After(60 * time.Second):
		close(stop)
		t.Fatal("the stream did not end")
	}
	close(stop)
	<-broadcasting

	if n := slowClosedCount(obs); n != 0 {
		t.Errorf("SlowConsumerClosed fired %d times, want 0", n)
	}
}

func TestPushTimeoutOption(t *testing.T) {
	tests := []struct {
		name string
		opts []ServerOptions
		want time.Duration
	}{
		{"default", nil, 5 * time.Second},
		{"zero keeps the default", []ServerOptions{{}}, 5 * time.Second},
		{"set", []ServerOptions{{PushTimeout: 250 * time.Millisecond}}, 250 * time.Millisecond},
		{"disabled", []ServerOptions{{PushTimeout: -1}}, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := NewServer(NewRegistry(), tt.opts...)
			t.Cleanup(func() { _ = server.Stop(context.Background()) })
			if got := server.options.PushTimeout; got != tt.want {
				t.Errorf("PushTimeout = %v, want %v", got, tt.want)
			}
		})
	}
}
