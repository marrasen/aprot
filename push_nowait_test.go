package aprot

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newPushTestServer returns a server with the NotificationEvent push event
// registered and the given send buffer size.
func newPushTestServer(t *testing.T, obs Observer, sendBufferSize int) *Server {
	t.Helper()
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)
	registry.RegisterPushEventFor(handlers, NotificationEvent{})
	server := NewServer(registry, ServerOptions{Observer: obs, SendBufferSize: sendBufferSize})
	handlers.server = server
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Stop(ctx)
	})
	return server
}

// newUnreadConn returns a connection on a byte stream whose write pump is not
// running, so nothing leaves its send buffer.
func newUnreadConn(t *testing.T, server *Server, id uint64) *Conn {
	t.Helper()
	serverEnd, clientEnd := net.Pipe()
	t.Cleanup(func() {
		_ = serverEnd.Close()
		_ = clientEnd.Close()
	})
	st := newStreamTransport(serverEnd, server.options)
	conn := newConn(st, server, id, ConnInfo{}, context.Background())
	st.conn = conn
	return conn
}

func slowClosedCount(obs *recordingObserver) int {
	obs.mu.Lock()
	defer obs.mu.Unlock()
	return obs.slowClosed
}

// A WebSocket client that stops reading is closed by Broadcast, and the
// caller and the other client are not held up. Before this, every Broadcast
// waited on the stalled connection until WriteTimeout (30s) closed it.
func TestBroadcast_StalledConnectionDoesNotHoldUpCaller(t *testing.T) {
	obs := newRecordingObserver()
	server := newPushTestServer(t, obs, 8)
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	stalled := connectWS(t, ts)
	defer stalled.Close()
	healthy := connectWS(t, ts)
	defer healthy.Close()
	waitForConnections(t, server, 2)

	// The healthy client reads every push and reports it.
	got := make(chan struct{}, 1)
	go func() {
		for {
			if _, _, err := healthy.ReadMessage(); err != nil {
				return
			}
			got <- struct{}{}
		}
	}()

	// Large pushes fill the stalled client's socket buffers, then its send
	// buffer. Waiting for the healthy client after each one keeps the healthy
	// connection's send buffer empty.
	event := NotificationEvent{Message: strings.Repeat("x", 64<<10)}
	const maxPushes = 4000
	pushes := 0
	for ; pushes < maxPushes && slowClosedCount(obs) == 0; pushes++ {
		start := time.Now()
		server.Broadcast(event)
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("Broadcast %d took %v, want it to return without waiting", pushes, d)
		}
		select {
		case <-got:
		case <-time.After(5 * time.Second):
			t.Fatalf("healthy client did not receive push %d", pushes)
		}
	}
	if slowClosedCount(obs) != 1 {
		t.Fatalf("SlowConsumerClosed fired %d times after %d pushes, want 1", slowClosedCount(obs), pushes)
	}

	// The stalled connection is torn down at once, not after WriteTimeout.
	waitForConnections(t, server, 1)
	obs.mu.Lock()
	writeTimeouts := obs.writeTimeout
	obs.mu.Unlock()
	if writeTimeouts != 0 {
		t.Errorf("WriteTimedOut fired %d times, want 0", writeTimeouts)
	}
}

func TestPushNoWait_FullBufferClosesConnection(t *testing.T) {
	obs := newRecordingObserver()
	server := newPushTestServer(t, obs, 4)
	conn := newUnreadConn(t, server, 1)

	for i := 0; i < 4; i++ {
		if err := conn.PushNoWait(NotificationEvent{Message: "m"}); err != nil {
			t.Fatalf("PushNoWait %d returned %v, want nil", i, err)
		}
	}
	if n := slowClosedCount(obs); n != 0 {
		t.Fatalf("SlowConsumerClosed fired %d times before the buffer was full, want 0", n)
	}

	if err := conn.PushNoWait(NotificationEvent{Message: "m"}); !errors.Is(err, ErrConnectionClosed) {
		t.Fatalf("PushNoWait on a full buffer returned %v, want ErrConnectionClosed", err)
	}
	conn.mu.Lock()
	closed := conn.closed
	conn.mu.Unlock()
	if !closed {
		t.Error("connection is open after PushNoWait found the buffer full")
	}

	// A push to the closed connection reports it and fires nothing more.
	if err := conn.PushNoWait(NotificationEvent{Message: "m"}); !errors.Is(err, ErrConnectionClosed) {
		t.Fatalf("PushNoWait on a closed connection returned %v, want ErrConnectionClosed", err)
	}
	if n := slowClosedCount(obs); n != 1 {
		t.Errorf("SlowConsumerClosed fired %d times, want 1", n)
	}
}

func TestPushToUser_FullBufferClosesConnection(t *testing.T) {
	obs := newRecordingObserver()
	server := newPushTestServer(t, obs, 2)
	conn := newUnreadConn(t, server, 1)
	conn.SetUserID("alice")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 3; i++ {
			server.PushToUser("alice", NotificationEvent{Message: "m"})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("PushToUser waited on a full send buffer")
	}
	if n := slowClosedCount(obs); n != 1 {
		t.Errorf("SlowConsumerClosed fired %d times, want 1", n)
	}
}

// Conn.Push keeps waiting for room: a handler pushing to its own connection
// is slowed down, and the connection stays open.
func TestPush_FullBufferWaits(t *testing.T) {
	obs := newRecordingObserver()
	server := newPushTestServer(t, obs, 2)
	conn := newUnreadConn(t, server, 1)
	st := conn.transport.(*streamTransport)

	for i := 0; i < 2; i++ {
		if err := conn.Push(NotificationEvent{Message: "m"}); err != nil {
			t.Fatalf("Push %d returned %v, want nil", i, err)
		}
	}

	sent := make(chan error, 1)
	go func() { sent <- conn.Push(NotificationEvent{Message: "m"}) }()

	eventually(t, 3*time.Second, func() bool {
		obs.mu.Lock()
		defer obs.mu.Unlock()
		return obs.bufferFull == 1
	})
	select {
	case err := <-sent:
		t.Fatalf("Push returned %v with the buffer full, want it to wait", err)
	default:
	}

	// Free one slot so the waiting Push can finish.
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

// A byte-stream peer that stops reading has no write deadline to close it.
// Broadcast closes it once its send buffer is full, and ServeStream returns.
func TestServeStream_StalledPeerClosedByBroadcast(t *testing.T) {
	obs := newRecordingObserver()
	server := newPushTestServer(t, obs, 4)
	clientEnd, errCh := startStreamServer(context.Background(), server, ConnInfo{})
	defer clientEnd.Close()
	client := newStreamTestClient(t, clientEnd)
	client.readFrameOfType("config")
	waitForConnections(t, server, 1)

	// The client reads nothing more. One frame is taken by the write pump,
	// four fill the buffer, and the next one closes the connection.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10 && slowClosedCount(obs) == 0; i++ {
			server.Broadcast(NotificationEvent{Message: "m"})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Broadcast waited on a stalled byte-stream peer")
	}
	if n := slowClosedCount(obs); n != 1 {
		t.Fatalf("SlowConsumerClosed fired %d times, want 1", n)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("ServeStream returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeStream did not return after the connection was closed")
	}
	waitForConnections(t, server, 0)
}

func TestSendBufferSize(t *testing.T) {
	tests := []struct {
		name string
		opts []ServerOptions
		want int
	}{
		{"default", nil, defaultSendBufferSize},
		{"zero keeps the default", []ServerOptions{{}}, defaultSendBufferSize},
		{"negative keeps the default", []ServerOptions{{SendBufferSize: -1}}, defaultSendBufferSize},
		{"set", []ServerOptions{{SendBufferSize: 8}}, 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := NewServer(NewRegistry(), tt.opts...)
			t.Cleanup(func() { _ = server.Stop(context.Background()) })

			if got := cap(newWSTransport(nil, server.options, false).send); got != tt.want {
				t.Errorf("WebSocket send buffer holds %d frames, want %d", got, tt.want)
			}
			if got := cap(newStreamTransport(nil, server.options).send); got != tt.want {
				t.Errorf("byte-stream send buffer holds %d frames, want %d", got, tt.want)
			}
		})
	}
}
