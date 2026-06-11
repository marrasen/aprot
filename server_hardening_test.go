package aprot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/gorilla/websocket"
)

// Handlers used by the hardening tests.
type PanicHandlers struct {
	calls atomic.Int32
}

func (h *PanicHandlers) Boom(ctx context.Context, req *EchoRequest) (*EchoResponse, error) {
	panic("boom: " + req.Message)
}

func (h *PanicHandlers) SubscribeBoom(ctx context.Context) (*EchoResponse, error) {
	panic("subscribe boom")
}

func (h *PanicHandlers) SubscribeFlaky(ctx context.Context) (*EchoResponse, error) {
	RegisterRefreshTrigger(ctx, "flaky")
	if h.calls.Add(1) > 1 {
		panic("refresh boom")
	}
	return &EchoResponse{Message: "ok"}, nil
}

func (h *PanicHandlers) Safe(ctx context.Context, req *EchoRequest) (*EchoResponse, error) {
	return &EchoResponse{Message: req.Message}, nil
}

func setupPanicServer(t *testing.T) (*httptest.Server, *Server, *PanicHandlers) {
	t.Helper()
	registry := NewRegistry()
	handlers := &PanicHandlers{}
	registry.Register(handlers)
	server := NewServer(registry)
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts, server, handlers
}

// readMessageOfType reads messages until one of the wanted type arrives,
// skipping others (e.g. push events). Fails the test on timeout.
func readMessageOfType(t *testing.T, ws *websocket.Conn, want MessageType, timeout time.Duration) ErrorMessage {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(timeout))
	defer ws.SetReadDeadline(time.Time{})
	for {
		var msg ErrorMessage // superset of ResponseMessage fields we need (type, id, code, message)
		if err := ws.ReadJSON(&msg); err != nil {
			t.Fatalf("reading message of type %q: %v", want, err)
		}
		if msg.Type == want {
			return msg
		}
	}
}

// A panic in a unary handler must be recovered, surfaced as an internal
// error response, and must not kill the connection or the process.
func TestHandlerPanicRecovered(t *testing.T) {
	ts, _, _ := setupPanicServer(t)

	ws := connectWS(t, ts)
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "PanicHandlers.Boom", Params: jsontext.Value(`[{"message":"hi"}]`)}); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	errMsg := readMessageOfType(t, ws, TypeError, 3*time.Second)
	if errMsg.ID != "1" {
		t.Errorf("expected error for request 1, got %q", errMsg.ID)
	}
	if errMsg.Code != CodeInternalError {
		t.Errorf("expected CodeInternalError, got %d", errMsg.Code)
	}
	if !strings.Contains(errMsg.Message, "panic") {
		t.Errorf("expected panic mention in message, got %q", errMsg.Message)
	}

	// The same connection must still work.
	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "2", Method: "PanicHandlers.Safe", Params: jsontext.Value(`[{"message":"still alive"}]`)}); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	resp := readMessageOfType(t, ws, TypeResponse, 3*time.Second)
	if resp.ID != "2" {
		t.Errorf("expected response for request 2, got %q", resp.ID)
	}
}

// A panic in a subscription handler must be recovered the same way.
func TestSubscribeHandlerPanicRecovered(t *testing.T) {
	ts, _, _ := setupPanicServer(t)

	ws := connectWS(t, ts)
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeSubscribe, ID: "s1", Method: "PanicHandlers.SubscribeBoom"}); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	errMsg := readMessageOfType(t, ws, TypeError, 3*time.Second)
	if errMsg.Code != CodeInternalError {
		t.Errorf("expected CodeInternalError, got %d", errMsg.Code)
	}

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "2", Method: "PanicHandlers.Safe", Params: jsontext.Value(`[{"message":"alive"}]`)}); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	resp := readMessageOfType(t, ws, TypeResponse, 3*time.Second)
	if resp.ID != "2" {
		t.Errorf("expected response for request 2, got %q", resp.ID)
	}
}

// A panic during a server-driven subscription refresh must be recovered.
func TestRefreshPanicRecovered(t *testing.T) {
	ts, server, _ := setupPanicServer(t)

	ws := connectWS(t, ts)
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeSubscribe, ID: "s1", Method: "PanicHandlers.SubscribeFlaky"}); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	resp := readMessageOfType(t, ws, TypeResponse, 3*time.Second)
	if resp.ID != "s1" {
		t.Fatalf("expected subscribe response, got id %q", resp.ID)
	}

	server.TriggerRefresh("flaky")

	errMsg := readMessageOfType(t, ws, TypeError, 3*time.Second)
	if errMsg.ID != "s1" || errMsg.Code != CodeInternalError {
		t.Errorf("expected internal error for s1, got id=%q code=%d", errMsg.ID, errMsg.Code)
	}

	// Server must still be functional.
	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "2", Method: "PanicHandlers.Safe", Params: jsontext.Value(`[{"message":"alive"}]`)}); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	resp = readMessageOfType(t, ws, TypeResponse, 3*time.Second)
	if resp.ID != "2" {
		t.Errorf("expected response for request 2, got %q", resp.ID)
	}
}

func setupHardeningServer(t *testing.T, opts ServerOptions) (*httptest.Server, *Server) {
	t.Helper()
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)
	registry.RegisterPushEventFor(handlers, NotificationEvent{})
	server := NewServer(registry, opts)
	handlers.server = server
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts, server
}

func waitForConnCount(t *testing.T, server *Server, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if server.ConnectionCount() == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("connection count never reached %d (still %d)", want, server.ConnectionCount())
}

// A frame larger than MaxMessageSize must close the connection instead of
// being buffered in full.
func TestMaxMessageSizeClosesConnection(t *testing.T) {
	ts, server := setupHardeningServer(t, ServerOptions{MaxMessageSize: 1024})

	ws := connectWS(t, ts)
	defer ws.Close()
	waitForConnCount(t, server, 1, 2*time.Second)

	big := `{"type":"request","id":"1","method":"IntegrationHandlers.Echo","params":[{"message":"` + strings.Repeat("x", 8192) + `"}]}`
	// The server may tear the connection down while we are still writing,
	// so a write error here is the same expected outcome as a read error.
	if err := ws.WriteMessage(websocket.TextMessage, []byte(big)); err == nil {
		_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				break // connection closed by server, as expected
			}
		}
	}
	waitForConnCount(t, server, 0, 3*time.Second)
}

// The server must send WebSocket pings at the configured interval.
func TestServerSendsPings(t *testing.T) {
	ts, _ := setupHardeningServer(t, ServerOptions{PingInterval: 50 * time.Millisecond})

	ws := connectWS(t, ts)
	defer ws.Close()

	pinged := make(chan struct{}, 1)
	ws.SetPingHandler(func(string) error {
		select {
		case pinged <- struct{}{}:
		default:
		}
		return nil
	})

	go func() {
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}()

	select {
	case <-pinged:
	case <-time.After(3 * time.Second):
		t.Fatal("server never sent a ping")
	}
}

// A peer that never reads (and therefore never answers pings) must be
// dropped after PongTimeout instead of lingering forever.
func TestUnresponsivePeerDropped(t *testing.T) {
	ts, server := setupHardeningServer(t, ServerOptions{
		PingInterval: 25 * time.Millisecond,
		PongTimeout:  150 * time.Millisecond,
	})

	ws := connectWS(t, ts) // reads config, then never reads again
	defer ws.Close()
	waitForConnCount(t, server, 1, 2*time.Second)

	// Don't read: no pong responses are generated.
	waitForConnCount(t, server, 0, 5*time.Second)
}

// A client that stops reading must not be able to wedge the whole server:
// Broadcast must not block other connections, and the stalled client must
// be dropped via the write timeout.
func TestStalledClientDoesNotBlockServer(t *testing.T) {
	ts, server := setupHardeningServer(t, ServerOptions{
		WriteTimeout: 200 * time.Millisecond,
	})

	// Client A connects and then stops reading entirely.
	wsA := connectWS(t, ts)
	defer wsA.Close()

	// Client B connects and drains everything in the background.
	wsB := connectWS(t, ts)
	defer wsB.Close()
	go func() {
		for {
			if _, _, err := wsB.ReadMessage(); err != nil {
				return
			}
		}
	}()
	waitForConnCount(t, server, 2, 2*time.Second)

	// Flood broadcasts until A's send queue and socket buffers are full.
	big := strings.Repeat("x", 64*1024)
	floodDone := make(chan struct{})
	go func() {
		defer close(floodDone)
		for i := 0; i < 5000 && server.ConnectionCount() > 1; i++ {
			server.Broadcast(&NotificationEvent{Message: big})
		}
	}()

	// The stalled client must get dropped (write timeout), which also
	// proves the server's run loop and locks were never wedged.
	waitForConnCount(t, server, 1, 15*time.Second)

	select {
	case <-floodDone:
	case <-time.After(15 * time.Second):
		t.Fatal("broadcast flood never finished — Broadcast is blocked on the stalled connection")
	}

	// A fresh connection must work normally.
	wsC := connectWS(t, ts)
	defer wsC.Close()
	if err := wsC.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "IntegrationHandlers.Echo", Params: jsontext.Value(`[{"message":"hello"}]`)}); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	resp := readMessageOfType(t, wsC, TypeResponse, 5*time.Second)
	if resp.ID != "1" {
		t.Errorf("expected echo response, got id %q", resp.ID)
	}
}

// SSE RPC bodies above MaxMessageSize must be rejected with 413.
func TestSSEBodyLimit(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&PanicHandlers{})
	server := NewServer(registry, ServerOptions{MaxMessageSize: 1024})

	ts := httptest.NewServer(server.HTTPTransport())
	t.Cleanup(ts.Close)

	body := `{"connectionId":"x","id":"1","method":"PanicHandlers.Safe","params":[{"message":"` + strings.Repeat("a", 8192) + `"}]}`
	resp, err := http.Post(ts.URL+"/rpc", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d", resp.StatusCode)
	}
}

// Defaults and merge semantics for the hardening options.
func TestHardeningOptionDefaults(t *testing.T) {
	opts := defaultServerOptions()
	if opts.MaxMessageSize != 4<<20 {
		t.Errorf("default MaxMessageSize = %d, want %d", opts.MaxMessageSize, 4<<20)
	}
	if opts.WriteTimeout != 30*time.Second {
		t.Errorf("default WriteTimeout = %v, want 30s", opts.WriteTimeout)
	}
	if opts.PingInterval != 30*time.Second {
		t.Errorf("default PingInterval = %v, want 30s", opts.PingInterval)
	}
	if opts.PongTimeout != 60*time.Second {
		t.Errorf("default PongTimeout = %v, want 60s", opts.PongTimeout)
	}

	// Negative values disable a limit and must survive the merge.
	registry := NewRegistry()
	server := NewServer(registry, ServerOptions{MaxMessageSize: -1, WriteTimeout: -1, PingInterval: -1, PongTimeout: -1})
	defer server.Stop(context.Background())
	if server.options.MaxMessageSize != -1 || server.options.WriteTimeout != -1 || server.options.PingInterval != -1 || server.options.PongTimeout != -1 {
		t.Errorf("negative (disabled) options were not preserved: %+v", server.options)
	}
}
