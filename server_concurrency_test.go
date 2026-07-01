package aprot

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/gorilla/websocket"
)

// gateHandlers exposes a blocking unary handler and a subscribe-able handler so
// concurrency-cap tests can pin a controllable number of requests in flight.
type gateHandlers struct {
	entered chan struct{} // signaled when Block starts executing
	release chan struct{} // closed by the test to let Block return
}

func (h *gateHandlers) Block(ctx context.Context) (*EchoResponse, error) {
	// entered is buffered wider than any test's fan-out, so this send never
	// blocks and every invocation is observable.
	h.entered <- struct{}{}
	select {
	case <-h.release:
	case <-ctx.Done():
	}
	return &EchoResponse{Message: "ok"}, nil
}

// Sub registers a trigger key so the subscribe path actually creates a
// subscription (subscriptions with no keys are never registered).
func (h *gateHandlers) Sub(ctx context.Context) (*EchoResponse, error) {
	RegisterRefreshTrigger(ctx, "k")
	return &EchoResponse{Message: "ok"}, nil
}

func setupGateServer(t *testing.T, opts ServerOptions) (*httptest.Server, *gateHandlers) {
	t.Helper()
	registry := NewRegistry()
	h := &gateHandlers{entered: make(chan struct{}, 128), release: make(chan struct{})}
	registry.Register(h)
	server := NewServer(registry, opts)
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts, h
}

// readUntilID reads frames until one carrying the given ID arrives, returning
// it (as the ErrorMessage superset). Fails on timeout.
func readUntilID(t *testing.T, ws *websocket.Conn, id string, timeout time.Duration) ErrorMessage {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(timeout))
	defer ws.SetReadDeadline(time.Time{})
	for {
		var msg ErrorMessage
		if err := ws.ReadJSON(&msg); err != nil {
			t.Fatalf("reading frame for ID %q: %v", id, err)
		}
		if msg.ID == id {
			return msg
		}
	}
}

// A connection that already has MaxConcurrentRequests in flight must have
// further requests rejected with CodeTooManyRequests, while the in-flight ones
// keep running and a slot becomes reusable once one completes.
func TestMaxConcurrentRequestsRejectsOverCap(t *testing.T) {
	ts, h := setupGateServer(t, ServerOptions{MaxConcurrentRequests: 2})
	ws := connectWS(t, ts)
	defer ws.Close()

	// Fill both slots with blocking requests and wait until both execute.
	for i := 0; i < 2; i++ {
		if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: fmt.Sprintf("block-%d", i), Method: "gateHandlers.Block"}); err != nil {
			t.Fatalf("write failed: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		<-h.entered
	}

	// A third concurrent request exceeds the cap and is rejected outright.
	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "over", Method: "gateHandlers.Block"}); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	over := readUntilID(t, ws, "over", 3*time.Second)
	if over.Type != TypeError || over.Code != CodeTooManyRequests {
		t.Fatalf("expected CodeTooManyRequests error for over-cap request, got type=%q code=%d", over.Type, over.Code)
	}

	// Releasing the blocked handlers frees their slots, after which a new
	// request succeeds. The slot is returned in the dispatch goroutine just
	// after the handler sends its response, so poll for reuse rather than
	// racing that window (a request sent before the slot frees is legitimately
	// rejected).
	close(h.release)
	deadline := time.Now().Add(3 * time.Second)
	for attempt := 0; ; attempt++ {
		id := fmt.Sprintf("after-%d", attempt)
		if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: id, Method: "gateHandlers.Block"}); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		reply := readUntilID(t, ws, id, 3*time.Second)
		if reply.Type == TypeResponse {
			break // a slot freed and was reused
		}
		if reply.Code != CodeTooManyRequests {
			t.Fatalf("unexpected reply for %s: type=%q code=%d", id, reply.Type, reply.Code)
		}
		if time.Now().After(deadline) {
			t.Fatal("no slot freed within timeout after releasing blocked handlers")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The server-wide cap bounds total in-flight requests across all connections,
// independent of the per-connection cap.
func TestMaxServerConcurrentRequestsRejectsOverCap(t *testing.T) {
	// Disable the per-connection cap so the server-wide cap is the only limit.
	ts, h := setupGateServer(t, ServerOptions{MaxConcurrentRequests: -1, MaxServerConcurrentRequests: 2})

	ws1 := connectWS(t, ts)
	defer ws1.Close()
	ws2 := connectWS(t, ts)
	defer ws2.Close()

	// One blocking request on each connection fills the server-wide budget.
	if err := ws1.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "a", Method: "gateHandlers.Block"}); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if err := ws2.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "b", Method: "gateHandlers.Block"}); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	<-h.entered
	<-h.entered

	// A third request on either connection is rejected server-wide.
	if err := ws1.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "over", Method: "gateHandlers.Block"}); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	over := readUntilID(t, ws1, "over", 3*time.Second)
	if over.Type != TypeError || over.Code != CodeTooManyRequests {
		t.Fatalf("expected CodeTooManyRequests for server-wide over-cap request, got type=%q code=%d", over.Type, over.Code)
	}
	close(h.release)
}

// A connection may hold at most MaxSubscriptions active subscriptions; the next
// subscribe is rejected with CodeTooManyRequests.
func TestMaxSubscriptionsRejectsOverCap(t *testing.T) {
	ts, _ := setupGateServer(t, ServerOptions{MaxSubscriptions: 2})
	ws := connectWS(t, ts)
	defer ws.Close()

	// Two subscriptions register successfully (read each response before the
	// next so registration order is deterministic).
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("sub-%d", i)
		if err := ws.WriteJSON(IncomingMessage{Type: TypeSubscribe, ID: id, Method: "gateHandlers.Sub"}); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		resp := readUntilID(t, ws, id, 3*time.Second)
		if resp.Type != TypeResponse {
			t.Fatalf("expected subscription %s to succeed, got type=%q code=%d", id, resp.Type, resp.Code)
		}
	}

	// The third subscription exceeds the cap.
	if err := ws.WriteJSON(IncomingMessage{Type: TypeSubscribe, ID: "sub-over", Method: "gateHandlers.Sub"}); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	over := readUntilID(t, ws, "sub-over", 3*time.Second)
	if over.Type != TypeError || over.Code != CodeTooManyRequests {
		t.Fatalf("expected CodeTooManyRequests for over-cap subscription, got type=%q code=%d", over.Type, over.Code)
	}
}

// With the caps disabled (-1), many concurrent requests all run without any
// being rejected.
func TestConcurrencyCapsDisabled(t *testing.T) {
	ts, h := setupGateServer(t, ServerOptions{MaxConcurrentRequests: -1, MaxServerConcurrentRequests: -1})
	ws := connectWS(t, ts)
	defer ws.Close()

	const n = 50
	for i := 0; i < n; i++ {
		if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: fmt.Sprintf("r-%d", i), Method: "gateHandlers.Block", Params: jsontext.Value(`[]`)}); err != nil {
			t.Fatalf("write failed: %v", err)
		}
	}
	// All n must reach the handler; none rejected.
	for i := 0; i < n; i++ {
		select {
		case <-h.entered:
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d/%d requests reached the handler; some were rejected with caps disabled", i, n)
		}
	}
	close(h.release)
}
