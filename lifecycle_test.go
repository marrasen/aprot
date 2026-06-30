package aprot

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/gorilla/websocket"
)

func TestNewServer_ClampsPongTimeout(t *testing.T) {
	tests := []struct {
		name            string
		ping, pong      time.Duration
		wantPongAtLeast time.Duration // 0 means "don't assert a lower bound"
		wantPongExact   time.Duration // 0 means "don't assert exact"
	}{
		{
			name:            "pong below ping is clamped above ping",
			ping:            30 * time.Second,
			pong:            10 * time.Second,
			wantPongAtLeast: 30*time.Second + time.Nanosecond,
		},
		{
			name:            "pong equal to ping is clamped above ping",
			ping:            30 * time.Second,
			pong:            30 * time.Second,
			wantPongAtLeast: 30*time.Second + time.Nanosecond,
		},
		{
			name:          "valid pong is left untouched",
			ping:          30 * time.Second,
			pong:          90 * time.Second,
			wantPongExact: 90 * time.Second,
		},
		{
			name:          "disabled keepalive is not clamped",
			ping:          -1,
			pong:          5 * time.Second,
			wantPongExact: 5 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewServer(NewRegistry(), ServerOptions{PingInterval: tt.ping, PongTimeout: tt.pong})
			defer s.Stop(context.Background())

			got := s.options.PongTimeout
			if tt.wantPongExact != 0 && got != tt.wantPongExact {
				t.Errorf("PongTimeout = %v, want exactly %v", got, tt.wantPongExact)
			}
			if tt.wantPongAtLeast != 0 {
				if got < tt.wantPongAtLeast {
					t.Errorf("PongTimeout = %v, want >= %v (must exceed PingInterval %v)", got, tt.wantPongAtLeast, s.options.PingInterval)
				}
				if got <= s.options.PingInterval {
					t.Errorf("PongTimeout %v must exceed PingInterval %v", got, s.options.PingInterval)
				}
			}
		})
	}
}

// userConnCount returns the number of connections tracked for a user.
func userConnCount(s *Server, userID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.userConns[userID])
}

// waitForUserConnCount polls until the user's tracked connection count reaches
// want, failing the test on timeout. Association happens on the server's
// connection goroutine, so a poll avoids racing it.
func waitForUserConnCount(t *testing.T, s *Server, userID string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if userConnCount(s, userID) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("userConns[%q] never reached %d (still %d)", userID, want, userConnCount(s, userID))
}

func wsURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}

// A request that registers after the connection has been closed must be
// canceled immediately. Otherwise a handler blocking on ctx.Done() runs
// forever, holds requestsWg, and stalls Server.Stop. (#207 P2)
func TestRegisterRequestAfterCloseCancelsImmediately(t *testing.T) {
	tc := NewTestPushConn(1)
	c := tc.Conn

	c.close()

	var gotCause error
	called := false
	cancel := func(cause error) {
		called = true
		gotCause = cause
	}
	c.registerRequest("late", cancel)

	if !called {
		t.Fatal("late request registered after close was never canceled (leaks requestsWg, stalls Stop)")
	}
	if gotCause != ErrConnectionClosed {
		t.Errorf("cancel cause = %v, want ErrConnectionClosed", gotCause)
	}

	c.mu.Lock()
	_, exists := c.requests["late"]
	c.mu.Unlock()
	if exists {
		t.Error("a canceled late request must not be retained in c.requests")
	}
}

// A connect hook that rejects the connection after an earlier hook called
// SetUserID must not leave the connection registered in userConns. Otherwise
// a later PushToUser fills the dead connection's send buffer and can wedge
// the server. (#207 P2)
func TestConnectHookRejectionAfterSetUserIDDisassociates(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	server := NewServer(registry)
	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		conn.SetUserID("u1")
		return nil
	})
	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		return errors.New("rejected by policy")
	})
	ts := httptest.NewServer(server)
	defer ts.Close()
	defer server.Stop(context.Background())

	ws, _, err := websocket.DefaultDialer.Dial(wsURL(ts.URL), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	// The connection associates "u1" during the first hook, then the second
	// hook rejects it; the rejection path must disassociate it back to 0.
	waitForUserConnCount(t, server, "u1", 0, 2*time.Second)
}

// Same guarantee for the SSE transport.
func TestSSEConnectHookRejectionAfterSetUserIDDisassociates(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	server := NewServer(registry)
	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		conn.SetUserID("u1")
		return nil
	})
	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		return errors.New("rejected by policy")
	})
	ts := httptest.NewServer(server.HTTPTransport())
	defer ts.Close()
	defer server.Stop(context.Background())

	resp, err := ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET SSE: %v", err)
	}
	defer resp.Body.Close()

	waitForUserConnCount(t, server, "u1", 0, 2*time.Second)
}

// --- Re-subscribe params freshness (#207 P2) ---

type paramReq struct {
	Tag string `json:"tag"`
}

type paramResp struct {
	Tag string `json:"tag"`
}

type paramSubHandlers struct{}

// Watch is a subscribable handler: it echoes the request tag and registers a
// fixed refresh trigger so a server-driven refresh re-executes it using the
// subscription's stored params.
func (h *paramSubHandlers) Watch(ctx context.Context, req *paramReq) (*paramResp, error) {
	RegisterRefreshTrigger(ctx, "watch")
	return &paramResp{Tag: req.Tag}, nil
}

// readResponseTag reads messages until a response for wantID arrives and
// returns its result tag.
func readResponseTag(t *testing.T, ws *websocket.Conn, wantID string, timeout time.Duration) string {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(timeout))
	defer ws.SetReadDeadline(time.Time{})
	for {
		var msg struct {
			Type   MessageType `json:"type"`
			ID     string      `json:"id"`
			Result paramResp   `json:"result"`
		}
		if err := ws.ReadJSON(&msg); err != nil {
			t.Fatalf("waiting for response %q: %v", wantID, err)
		}
		if msg.Type == TypeResponse && msg.ID == wantID {
			return msg.Result.Tag
		}
	}
}

// Re-subscribing with the same ID but different params must update the stored
// params, so a later server-driven refresh re-executes with the new params,
// not the stale ones from the first subscribe. (#207 P2)
func TestReSubscribeUpdatesStoredParams(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&paramSubHandlers{})
	server := NewServer(registry)
	ts := httptest.NewServer(server)
	defer ts.Close()
	defer server.Stop(context.Background())

	ws := connectWS(t, ts)
	defer ws.Close()

	sub := func(tag string) {
		if err := ws.WriteJSON(IncomingMessage{
			Type:   TypeSubscribe,
			ID:     "s1",
			Method: "paramSubHandlers.Watch",
			Params: jsontext.Value(`[{"tag":"` + tag + `"}]`),
		}); err != nil {
			t.Fatalf("subscribe write: %v", err)
		}
		if got := readResponseTag(t, ws, "s1", 3*time.Second); got != tag {
			t.Fatalf("subscribe response tag = %q, want %q", got, tag)
		}
	}

	sub("first")
	sub("second") // re-subscribe with same ID, new params

	server.TriggerRefresh("watch")

	if got := readResponseTag(t, ws, "s1", 3*time.Second); got != "second" {
		t.Errorf("server-driven refresh used stale params: tag = %q, want %q", got, "second")
	}
}

// A subscription must not be registered for a connection that has already
// been closed. The handler's ctx.Err() guard runs before register(), so a
// close racing in between could otherwise leave a subscription that
// re-executes forever on every TriggerRefresh and writes to a dead
// transport. register() must refuse closed connections. (#207 P2)
func TestSubscribeRegisterAfterCloseDoesNotLeak(t *testing.T) {
	tc := NewTestPushConn(7)
	c := tc.Conn
	sm := c.server.subscriptions

	c.close() // sets closed, runs unregisterConn

	sub := &subscription{
		conn:   c,
		id:     "s1",
		method: "X",
		keys:   map[string]struct{}{"k": {}},
	}
	sm.register(sub)

	if sm.has(c.id, "s1") {
		t.Fatal("subscription registered for a closed connection leaks forever")
	}
}

// --- Server.Stop lifecycle (#207 P2) ---

// (a) A connection that registers after shutdown began must be closed by
// run() and must not keep the server alive, or Stop hangs forever waiting
// for the connection set to drain.
func TestStopClosesConnectionRegisteredDuringShutdown(t *testing.T) {
	server := NewServer(NewRegistry())

	rt := &recordingTransport{}
	conn := &Conn{
		transport: rt,
		server:    server,
		requests:  make(map[string]context.CancelCauseFunc),
		id:        99,
	}

	// Simulate ServeHTTP registering a connection after Stop has begun.
	server.stopping.Store(true)
	server.register <- conn

	stopDone := make(chan error, 1)
	go func() { stopDone <- server.Stop(context.Background()) }()

	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Server.Stop hung: a connection registered during shutdown was never drained")
	}
}

// (b) A connection that finishes connecting after run() has already exited
// must not block forever trying to register — ServeHTTP must give up.
func TestLateConnectAfterStopDoesNotLeak(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	server := NewServer(registry)

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		once.Do(func() { close(entered) })
		<-release
		return nil
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	connectDone := make(chan struct{})
	go func() {
		defer close(connectDone)
		ws, _, err := websocket.DefaultDialer.Dial(wsURL(ts.URL), nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}()

	<-entered // the connection is in the hook, not yet registered

	stopDone := make(chan error, 1)
	go func() { stopDone <- server.Stop(context.Background()) }()

	// Let Stop set stopping + drain (the in-hook conn isn't in its snapshot),
	// then release the hook so the connection tries to register afterwards.
	time.Sleep(50 * time.Millisecond)
	close(release)

	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Server.Stop hung")
	}
	select {
	case <-connectDone:
	case <-time.After(5 * time.Second):
		t.Fatal("connection goroutine leaked: ServeHTTP blocked forever on register after run() exited")
	}
}

// (c) Stop must honor the caller's context even on the final drain wait — a
// pathological (e.g. blocking) disconnect hook must not hang Stop past its
// deadline.
func TestStopRespectsContextOnFinalWait(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	server := NewServer(registry)
	server.OnDisconnect(func(ctx context.Context, conn *Conn) {
		select {} // hang forever
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()
	waitForConnCount(t, server, 1, 2*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	stopDone := make(chan error, 1)
	go func() { stopDone <- server.Stop(ctx) }()

	select {
	case err := <-stopDone:
		if err == nil {
			t.Fatal("Stop returned nil; expected context deadline error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Server.Stop ignored its context and hung on a blocking disconnect hook")
	}
}

// --- Small races (#207 P2) ---

// Reusing an in-flight request ID must cancel the previously-registered
// request. Otherwise the old cancel func is silently overwritten in the
// requests map, and the shadowed request's context leaks until the whole
// connection closes. (#207 P2)
func TestRegisterRequestIDCollisionCancelsPrevious(t *testing.T) {
	tc := NewTestPushConn(2)
	c := tc.Conn

	var firstCause error
	firstCanceled := false
	c.registerRequest("dup", func(cause error) {
		firstCanceled = true
		firstCause = cause
	})
	c.registerRequest("dup", func(cause error) {})

	if !firstCanceled {
		t.Fatal("reusing an in-flight request ID must cancel the shadowed request to avoid a leak")
	}
	if firstCause != ErrClientCanceled {
		t.Errorf("shadowed request cancel cause = %v, want ErrClientCanceled", firstCause)
	}
}

// When a shadowed request unwinds after its replacement (same ID) has been
// registered, its deferred unregister must not delete the replacement's cancel
// func. Otherwise the replacement can no longer be canceled via cancel,
// unsubscribe, or connection-close bookkeeping, leaking its goroutine until it
// finishes naturally and stalling graceful shutdown. (#225)
func TestUnregisterRequestKeepsReplacement(t *testing.T) {
	tc := NewTestPushConn(2)
	c := tc.Conn

	first := func(cause error) {}
	c.registerRequest("dup", first)

	replacementCanceled := false
	replacement := func(cause error) { replacementCanceled = true }
	c.registerRequest("dup", replacement)

	// The shadowed (first) handler unwinds and runs its deferred unregister
	// with its own cancel func. This must be a no-op because the map now holds
	// the replacement.
	c.unregisterRequest("dup", first)

	c.mu.Lock()
	got, exists := c.requests["dup"]
	c.mu.Unlock()
	if !exists {
		t.Fatal("replacement request was unregistered by the shadowed handler's deferred unregister")
	}

	// The retained entry must be the replacement, and canceling it must work.
	got(ErrClientCanceled)
	if !replacementCanceled {
		t.Error("retained cancel func is not the replacement's")
	}
}

// SetUserID racing a disconnect (disassociateUser) must not be a data race on
// Conn.userID. This is primarily a guard for `go test -race` in CI; it should
// always pass otherwise. (#207 P2)
func TestSetUserIDConcurrentWithDisassociate(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&IntegrationHandlers{})
	server := NewServer(registry)
	defer server.Stop(context.Background())

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		conn := &Conn{
			server:   server,
			requests: make(map[string]context.CancelCauseFunc),
			id:       uint64(i + 1),
		}
		wg.Add(2)
		go func() {
			defer wg.Done()
			conn.SetUserID("u")
		}()
		go func() {
			defer wg.Done()
			server.disassociateUser(conn)
		}()
	}
	wg.Wait()
}
