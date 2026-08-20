package aprot

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A connection must be in the server's fan-out set no later than the moment
// the client can see it. Registration used to be a channel handoff to run(),
// so the insert landed on another goroutine after the client already held the
// config frame: a client could complete request round-trips while
// connsSnapshot did not contain it, and every Broadcast in that window was
// dropped (#347). The probe below measured ~275/300 connections invisible on
// master.
//
// The check is deliberately taken from the client's side of the wire — the
// first frame the client receives is the contract — and repeated, because the
// old bug was a race whose window a single iteration could miss.
const registerVisibilityRuns = 50

// stopBounded shuts the server down with a deadline. An unbounded Stop can
// hang when a connection never drains, which turns a failing assertion into a
// hung test run instead of a report.
func stopBounded(t *testing.T, s *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Logf("server shutdown: %v", err)
	}
}

// visibleToBroadcast reports whether the server counts conn among the
// connections Broadcast and ForEachConn reach.
func visibleToBroadcast(s *Server) int {
	n := 0
	s.ForEachConn(func(*Conn) { n++ })
	return n
}

// WebSocket: the config frame is written directly before the pumps start, so
// it is the client's first evidence the connection is live.
func TestRegisterBeforeClientVisible_WebSocket(t *testing.T) {
	server := NewServer(NewRegistry())
	ts := httptest.NewServer(server)
	defer ts.Close()
	t.Cleanup(func() { stopBounded(t, server) })

	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	for i := range registerVisibilityRuns {
		ws, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatalf("run %d: dial: %v", i, err)
		}
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, _, err := ws.ReadMessage(); err != nil {
			t.Fatalf("run %d: read config frame: %v", i, err)
		}
		if n := visibleToBroadcast(server); n != 1 {
			t.Fatalf("run %d: client holds the config frame but ForEachConn sees %d connections, want 1", i, n)
		}
		ws.Close()
		// Let the unregister drain so each run starts from an empty set.
		waitForConnCount(t, server, 0, 5*time.Second)
	}
}

// SSE: the connected and config events are the client's first evidence.
func TestRegisterBeforeClientVisible_SSE(t *testing.T) {
	server := NewServer(NewRegistry())
	sseH := newSSEHandler(server)
	mux := http.NewServeMux()
	mux.Handle("/sse", sseH)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	t.Cleanup(func() { stopBounded(t, server) })

	for i := range registerVisibilityRuns {
		// The body is closed unconditionally: an SSE request stays open until
		// the client hangs up, and httptest.Server.Close blocks on outstanding
		// requests — so a bare t.Fatalf here would wedge the run instead of
		// reporting it.
		func() {
			req, _ := http.NewRequest("GET", ts.URL+"/sse", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("run %d: GET /sse: %v", i, err)
			}
			defer resp.Body.Close()

			// Read up to and including the connected event.
			br := bufio.NewReader(resp.Body)
			for {
				line, err := br.ReadString('\n')
				if err != nil {
					t.Fatalf("run %d: read SSE stream: %v", i, err)
				}
				if strings.HasPrefix(line, "event: connected") {
					break
				}
			}
			if n := visibleToBroadcast(server); n != 1 {
				t.Errorf("run %d: client holds the connected event but ForEachConn sees %d connections, want 1", i, n)
			}
		}()
		if t.Failed() {
			return
		}
		waitForConnCount(t, server, 0, 5*time.Second)
	}
}

// Byte stream: the config frame is enqueued, so the client sees it only once
// the write pump flushes — registration must precede the pump.
func TestRegisterBeforeClientVisible_Stream(t *testing.T) {
	server := NewServer(NewRegistry())
	t.Cleanup(func() { stopBounded(t, server) })

	for i := range registerVisibilityRuns {
		ctx, cancel := context.WithCancel(context.Background())
		serverEnd, clientEnd := net.Pipe()
		go func() { _ = server.ServeStream(ctx, serverEnd, ConnInfo{}) }()

		sc := bufio.NewScanner(clientEnd)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		_ = clientEnd.SetReadDeadline(time.Now().Add(5 * time.Second))
		if !sc.Scan() {
			cancel()
			t.Fatalf("run %d: read config frame: %v", i, sc.Err())
		}
		if n := visibleToBroadcast(server); n != 1 {
			cancel()
			t.Fatalf("run %d: client holds the config frame but ForEachConn sees %d connections, want 1", i, n)
		}
		_ = clientEnd.Close()
		cancel()
		waitForConnCount(t, server, 0, 5*time.Second)
	}
}

// A just-connected client must actually receive a broadcast — the
// user-visible consequence of the registration window.
func TestBroadcastReachesJustConnectedClient(t *testing.T) {
	registry := NewRegistry()
	h := &IntegrationHandlers{}
	registry.Register(h)
	registry.RegisterPushEventFor(h, NotificationEvent{})
	server := NewServer(registry)
	h.server = server
	ts := httptest.NewServer(server)
	defer ts.Close()
	t.Cleanup(func() { stopBounded(t, server) })

	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	for i := range registerVisibilityRuns {
		ws, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatalf("run %d: dial: %v", i, err)
		}
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, _, err := ws.ReadMessage(); err != nil {
			t.Fatalf("run %d: read config frame: %v", i, err)
		}

		// The client is live as far as it knows; a broadcast must reach it.
		server.Broadcast(NotificationEvent{Message: "hello"})

		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("run %d: broadcast never arrived: %v", i, err)
		}
		if !strings.Contains(string(data), "hello") {
			t.Fatalf("run %d: unexpected frame %s", i, data)
		}
		ws.Close()
		waitForConnCount(t, server, 0, 5*time.Second)
	}
}
