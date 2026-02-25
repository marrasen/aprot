package aprot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/gorilla/websocket"
)

// Integration test types
type EchoRequest struct {
	Message string `json:"message"`
}

type EchoResponse struct {
	Message string `json:"message"`
}

type SlowRequest struct {
	Steps int `json:"steps"`
}

type SlowResponse struct {
	Completed bool `json:"completed"`
}

type BroadcastRequest struct {
	Message string `json:"message"`
}

type BroadcastResponse struct {
	Sent bool `json:"sent"`
}

type NotificationEvent struct {
	Message string `json:"message"`
}

// Integration test handlers
type IntegrationHandlers struct {
	server *Server
}

func (h *IntegrationHandlers) Echo(ctx context.Context, req *EchoRequest) (*EchoResponse, error) {
	return &EchoResponse{Message: req.Message}, nil
}

func (h *IntegrationHandlers) Slow(ctx context.Context, req *SlowRequest) (*SlowResponse, error) {
	progress := Progress(ctx)
	for i := 1; i <= req.Steps; i++ {
		select {
		case <-ctx.Done():
			return nil, ErrCanceled()
		case <-time.After(10 * time.Millisecond):
			progress.Update(i, req.Steps, "Processing step")
		}
	}
	return &SlowResponse{Completed: true}, nil
}

func (h *IntegrationHandlers) TriggerBroadcast(ctx context.Context, req *BroadcastRequest) (*BroadcastResponse, error) {
	h.server.Broadcast(&NotificationEvent{Message: req.Message})
	return &BroadcastResponse{Sent: true}, nil
}

func (h *IntegrationHandlers) TriggerPush(ctx context.Context, req *BroadcastRequest) (*BroadcastResponse, error) {
	conn := Connection(ctx)
	if conn != nil {
		conn.Push(&NotificationEvent{Message: req.Message})
	}
	return &BroadcastResponse{Sent: true}, nil
}

func setupTestServer(t *testing.T) (*httptest.Server, *Server, *IntegrationHandlers) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)
	registry.RegisterPushEventFor(handlers, NotificationEvent{})

	server := NewServer(registry)
	handlers.server = server

	ts := httptest.NewServer(server)
	return ts, server, handlers
}

func connectWS(t *testing.T, ts *httptest.Server) *websocket.Conn {
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	// Read and discard the config message that is sent on connect
	_, _, err = ws.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read config message: %v", err)
	}
	return ws
}

func TestServerEcho(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	// Send request
	req := IncomingMessage{
		Type:   TypeRequest,
		ID:     "1",
		Method: "IntegrationHandlers.Echo",
		Params: jsontext.Value(`[{"message":"hello"}]`),
	}
	if err := ws.WriteJSON(req); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Read response
	var resp ResponseMessage
	if err := ws.ReadJSON(&resp); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if resp.Type != TypeResponse {
		t.Errorf("Expected response type, got %s", resp.Type)
	}
	if resp.ID != "1" {
		t.Errorf("Expected ID 1, got %s", resp.ID)
	}

	result, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(result), "hello") {
		t.Errorf("Expected hello in result, got %s", string(result))
	}
}

func TestServerMethodNotFound(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	req := IncomingMessage{
		Type:   TypeRequest,
		ID:     "1",
		Method: "NonExistent",
	}
	if err := ws.WriteJSON(req); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	var resp ErrorMessage
	if err := ws.ReadJSON(&resp); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if resp.Type != TypeError {
		t.Errorf("Expected error type, got %s", resp.Type)
	}
	if resp.Code != CodeMethodNotFound {
		t.Errorf("Expected method not found code, got %d", resp.Code)
	}
}

func TestServerProgress(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	req := IncomingMessage{
		Type:   TypeRequest,
		ID:     "1",
		Method: "IntegrationHandlers.Slow",
		Params: jsontext.Value(`[{"steps":3}]`),
	}
	if err := ws.WriteJSON(req); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	progressCount := 0
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}

		var msg map[string]any
		json.Unmarshal(data, &msg)

		if msg["type"] == string(TypeProgress) {
			progressCount++
		} else if msg["type"] == string(TypeResponse) {
			break
		} else if msg["type"] == string(TypeError) {
			t.Fatalf("Unexpected error: %v", msg)
		}
	}

	if progressCount != 3 {
		t.Errorf("Expected 3 progress messages, got %d", progressCount)
	}
}

func TestServerCancel(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	// Start slow request
	req := IncomingMessage{
		Type:   TypeRequest,
		ID:     "1",
		Method: "IntegrationHandlers.Slow",
		Params: jsontext.Value(`[{"steps":100}]`),
	}
	if err := ws.WriteJSON(req); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Wait a bit then cancel
	time.Sleep(50 * time.Millisecond)

	cancel := IncomingMessage{
		Type: TypeCancel,
		ID:   "1",
	}
	if err := ws.WriteJSON(cancel); err != nil {
		t.Fatalf("Write cancel failed: %v", err)
	}

	// Read until we get error or response
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}

		var msg map[string]any
		json.Unmarshal(data, &msg)

		if msg["type"] == string(TypeError) {
			code := int(msg["code"].(float64))
			if code != CodeCanceled {
				t.Errorf("Expected canceled code, got %d", code)
			}
			return
		} else if msg["type"] == string(TypeResponse) {
			// Request completed before cancel was processed
			return
		}
	}
}

func TestServerPush(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	// Trigger push
	req := IncomingMessage{
		Type:   TypeRequest,
		ID:     "1",
		Method: "IntegrationHandlers.TriggerPush",
		Params: jsontext.Value(`[{"message":"pushed"}]`),
	}
	if err := ws.WriteJSON(req); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	gotPush := false
	gotResponse := false

	for !gotPush || !gotResponse {
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}

		var msg map[string]any
		json.Unmarshal(data, &msg)

		if msg["type"] == string(TypePush) {
			gotPush = true
			if msg["event"] != "NotificationEvent" {
				t.Errorf("Expected NotificationEvent event, got %v", msg["event"])
			}
		} else if msg["type"] == string(TypeResponse) {
			gotResponse = true
		}
	}

	if !gotPush {
		t.Error("Did not receive push message")
	}
}

func TestServerBroadcast(t *testing.T) {
	ts, server, _ := setupTestServer(t)
	defer ts.Close()

	ws1 := connectWS(t, ts)
	defer ws1.Close()

	ws2 := connectWS(t, ts)
	defer ws2.Close()

	// Wait for connections to be registered
	time.Sleep(50 * time.Millisecond)

	if server.ConnectionCount() != 2 {
		t.Errorf("Expected 2 connections, got %d", server.ConnectionCount())
	}

	// Broadcast from server
	server.Broadcast(&NotificationEvent{Message: "broadcast"})

	// Both should receive
	var wg sync.WaitGroup
	wg.Add(2)

	checkPush := func(ws *websocket.Conn) {
		defer wg.Done()
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Errorf("Read failed: %v", err)
			return
		}

		var msg PushMessage
		json.Unmarshal(data, &msg)

		if msg.Type != TypePush {
			t.Errorf("Expected push type, got %s", msg.Type)
		}
		if msg.Event != "NotificationEvent" {
			t.Errorf("Expected NotificationEvent event, got %s", msg.Event)
		}
	}

	go checkPush(ws1)
	go checkPush(ws2)

	wg.Wait()
}

func TestOnConnectHookCalled(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)

	server := NewServer(registry)
	handlers.server = server

	var called int32
	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		atomic.AddInt32(&called, 1)
		return nil
	})

	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	time.Sleep(50 * time.Millisecond)

	if atomic.LoadInt32(&called) != 1 {
		t.Errorf("Expected hook called once, got %d", atomic.LoadInt32(&called))
	}
}

func TestOnConnectHookRejectsConnection(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)

	server := NewServer(registry)
	handlers.server = server

	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		return ErrConnectionRejected("max connections reached")
	})

	ts := httptest.NewServer(server)
	defer ts.Close()

	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer ws.Close()

	// Should receive error message
	_, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	var msg ErrorMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if msg.Type != TypeError {
		t.Errorf("Expected error type, got %s", msg.Type)
	}
	if msg.Code != CodeConnectionRejected {
		t.Errorf("Expected connection rejected code, got %d", msg.Code)
	}
	if msg.Message != "max connections reached" {
		t.Errorf("Expected 'max connections reached', got %s", msg.Message)
	}

	// Connection should be closed - server should have 0 connections
	time.Sleep(50 * time.Millisecond)
	if server.ConnectionCount() != 0 {
		t.Errorf("Expected 0 connections, got %d", server.ConnectionCount())
	}
}

func TestOnConnectMultipleHooks(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)

	server := NewServer(registry)
	handlers.server = server

	var order []int
	var mu sync.Mutex

	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		mu.Lock()
		order = append(order, 1)
		mu.Unlock()
		return nil
	})

	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		mu.Lock()
		order = append(order, 2)
		mu.Unlock()
		return errors.New("second hook fails")
	})

	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		mu.Lock()
		order = append(order, 3)
		mu.Unlock()
		return nil
	})

	ts := httptest.NewServer(server)
	defer ts.Close()

	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer ws.Close()

	// Read error message
	ws.ReadMessage()

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if len(order) != 2 {
		t.Errorf("Expected 2 hooks called, got %d", len(order))
	}
	if len(order) >= 2 && (order[0] != 1 || order[1] != 2) {
		t.Errorf("Expected order [1,2], got %v", order)
	}
}

func TestOnDisconnectHookCalled(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)

	server := NewServer(registry)
	handlers.server = server

	var called int32
	server.OnDisconnect(func(ctx context.Context, conn *Conn) {
		atomic.AddInt32(&called, 1)
	})

	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)

	// Wait for connection to be registered
	time.Sleep(50 * time.Millisecond)

	// Close the connection
	ws.Close()

	// Wait for disconnect hook to be called
	time.Sleep(50 * time.Millisecond)

	if atomic.LoadInt32(&called) != 1 {
		t.Errorf("Expected disconnect hook called once, got %d", atomic.LoadInt32(&called))
	}
}

func TestOnDisconnectHookHasUserID(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)

	server := NewServer(registry)
	handlers.server = server

	var capturedUserID string
	var mu sync.Mutex

	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		conn.SetUserID("user123")
		return nil
	})

	server.OnDisconnect(func(ctx context.Context, conn *Conn) {
		mu.Lock()
		capturedUserID = conn.UserID()
		mu.Unlock()
	})

	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)

	// Wait for connection to be registered
	time.Sleep(50 * time.Millisecond)

	// Close the connection
	ws.Close()

	// Wait for disconnect hook to be called
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if capturedUserID != "user123" {
		t.Errorf("Expected UserID 'user123', got '%s'", capturedUserID)
	}
}

func TestHookContextContainsConnection(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)

	server := NewServer(registry)
	handlers.server = server

	var capturedConnID uint64

	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		capturedConnID = conn.ID()
		// Verify we have access to the connection context
		if conn.Context() == nil {
			t.Error("Expected non-nil context")
		}
		return nil
	})

	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	time.Sleep(50 * time.Millisecond)

	if capturedConnID == 0 {
		t.Error("Expected non-zero connection ID")
	}
}

func TestConfigSentOnConnect(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)

	server := NewServer(registry, ServerOptions{
		ReconnectInterval:    2000,
		ReconnectMaxInterval: 60000,
		HeartbeatInterval:    15000,
		HeartbeatTimeout:     3000,
	})
	handlers.server = server

	ts := httptest.NewServer(server)
	defer ts.Close()

	// Connect without using connectWS to inspect the raw config message
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer ws.Close()

	// Read config message
	_, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	var msg ConfigMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if msg.Type != TypeConfig {
		t.Errorf("Expected config type, got %s", msg.Type)
	}
	if msg.ReconnectInterval != 2000 {
		t.Errorf("Expected ReconnectInterval 2000, got %d", msg.ReconnectInterval)
	}
	if msg.ReconnectMaxInterval != 60000 {
		t.Errorf("Expected ReconnectMaxInterval 60000, got %d", msg.ReconnectMaxInterval)
	}
	if msg.HeartbeatInterval != 15000 {
		t.Errorf("Expected HeartbeatInterval 15000, got %d", msg.HeartbeatInterval)
	}
	if msg.HeartbeatTimeout != 3000 {
		t.Errorf("Expected HeartbeatTimeout 3000, got %d", msg.HeartbeatTimeout)
	}
}

// Void handler for integration testing
type VoidRequest struct {
	ID string `json:"id"`
}

type VoidTestHandlers struct{}

func (h *VoidTestHandlers) DeleteItem(ctx context.Context, req *VoidRequest) error {
	return nil
}

func TestServerVoidResponse(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&VoidTestHandlers{})

	server := NewServer(registry)
	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	req := IncomingMessage{
		Type:   TypeRequest,
		ID:     "1",
		Method: "VoidTestHandlers.DeleteItem",
		Params: jsontext.Value(`[{"id":"item_1"}]`),
	}
	if err := ws.WriteJSON(req); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	var resp ResponseMessage
	if err := ws.ReadJSON(&resp); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if resp.Type != TypeResponse {
		t.Errorf("Expected response type, got %s", resp.Type)
	}
	if resp.ID != "1" {
		t.Errorf("Expected ID 1, got %s", resp.ID)
	}
	// Result should be nil for void response
	if resp.Result != nil {
		t.Errorf("Expected nil result for void response, got %v", resp.Result)
	}
}

// No-request handler for integration testing

type NoRequestTestHandlers struct{}

type StatusResponse struct {
	Status string `json:"status"`
}

func (h *NoRequestTestHandlers) GetStatus(ctx context.Context) (*StatusResponse, error) {
	return &StatusResponse{Status: "ok"}, nil
}

func (h *NoRequestTestHandlers) Ping(ctx context.Context) error {
	return nil
}

func TestServerNoRequestHandler(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&NoRequestTestHandlers{})

	server := NewServer(registry)
	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	// Test no-request handler with response
	req := IncomingMessage{
		Type:   TypeRequest,
		ID:     "1",
		Method: "NoRequestTestHandlers.GetStatus",
	}
	if err := ws.WriteJSON(req); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	var resp ResponseMessage
	if err := ws.ReadJSON(&resp); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if resp.Type != TypeResponse {
		t.Errorf("Expected response type, got %s", resp.Type)
	}
	if resp.ID != "1" {
		t.Errorf("Expected ID 1, got %s", resp.ID)
	}

	result, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(result), "ok") {
		t.Errorf("Expected 'ok' in result, got %s", string(result))
	}

	// Test no-request void handler
	req2 := IncomingMessage{
		Type:   TypeRequest,
		ID:     "2",
		Method: "NoRequestTestHandlers.Ping",
	}
	if err := ws.WriteJSON(req2); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	var resp2 ResponseMessage
	if err := ws.ReadJSON(&resp2); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if resp2.Type != TypeResponse {
		t.Errorf("Expected response type, got %s", resp2.Type)
	}
	if resp2.Result != nil {
		t.Errorf("Expected nil result for void response, got %v", resp2.Result)
	}
}

func TestServerNoRequestHandlerWithParams(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&NoRequestTestHandlers{})

	server := NewServer(registry)
	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	// Sending params to a no-params handler should still work (params are ignored)
	req := IncomingMessage{
		Type:   TypeRequest,
		ID:     "1",
		Method: "NoRequestTestHandlers.GetStatus",
		Params: jsontext.Value(`["bar"]`),
	}
	if err := ws.WriteJSON(req); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	var resp ResponseMessage
	if err := ws.ReadJSON(&resp); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if resp.Type != TypeResponse {
		t.Errorf("Expected response type, got %s", resp.Type)
	}
}

// BlockingRequest is used for shutdown tests where we need precise control
// over when a request completes.
type BlockingRequest struct {
	Token string `json:"token"`
}

type BlockingResponse struct {
	Done bool `json:"done"`
}

type BlockingHandlers struct {
	ch chan struct{} // closed to unblock the handler
}

func (h *BlockingHandlers) Block(ctx context.Context, req *BlockingRequest) (*BlockingResponse, error) {
	select {
	case <-h.ch:
		return &BlockingResponse{Done: true}, nil
	case <-ctx.Done():
		return nil, ErrCanceled()
	}
}

// StubbornHandlers has a handler that ignores context cancellation,
// used to test Stop() timeout behavior.
type StubbornHandlers struct {
	ch chan struct{}
}

func (h *StubbornHandlers) Stubborn(ctx context.Context, req *BlockingRequest) (*BlockingResponse, error) {
	<-h.ch // blocks until channel is closed, ignores ctx
	return &BlockingResponse{Done: true}, nil
}

func TestStopGraceful(t *testing.T) {
	registry := NewRegistry()
	blockCh := make(chan struct{})
	handlers := &BlockingHandlers{ch: blockCh}
	registry.Register(handlers)

	server := NewServer(registry)

	var disconnected int32
	server.OnDisconnect(func(ctx context.Context, conn *Conn) {
		atomic.AddInt32(&disconnected, 1)
	})

	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	// Wait for connection registration
	time.Sleep(50 * time.Millisecond)

	// Start a long-running request
	req := IncomingMessage{
		Type:   TypeRequest,
		ID:     "1",
		Method: "BlockingHandlers.Block",
		Params: jsontext.Value(`[{"token":"test"}]`),
	}
	if err := ws.WriteJSON(req); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Give time for request to be dispatched
	time.Sleep(50 * time.Millisecond)

	// Start Stop in a goroutine
	stopDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopDone <- server.Stop(ctx)
	}()

	// Unblock the handler after a short delay
	time.Sleep(50 * time.Millisecond)
	close(blockCh)

	// Stop should complete without error
	err := <-stopDone
	if err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}

	// Disconnect hook should have been called
	if atomic.LoadInt32(&disconnected) != 1 {
		t.Errorf("Expected 1 disconnect hook call, got %d", atomic.LoadInt32(&disconnected))
	}

	// Server should have 0 connections
	if server.ConnectionCount() != 0 {
		t.Errorf("Expected 0 connections, got %d", server.ConnectionCount())
	}
}

func TestStopTimeout(t *testing.T) {
	registry := NewRegistry()
	blockCh := make(chan struct{})
	handlers := &StubbornHandlers{ch: blockCh}
	registry.Register(handlers)
	defer close(blockCh) // unblock after test so goroutines don't leak

	server := NewServer(registry)

	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	time.Sleep(50 * time.Millisecond)

	// Start a request that will block and ignore cancellation
	req := IncomingMessage{
		Type:   TypeRequest,
		ID:     "1",
		Method: "StubbornHandlers.Stubborn",
		Params: jsontext.Value(`[{"token":"test"}]`),
	}
	if err := ws.WriteJSON(req); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Stop with a very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := server.Stop(ctx)
	if err == nil {
		t.Fatal("Expected timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Expected context.DeadlineExceeded, got %v", err)
	}
}

func TestStopRejectsNewConnections(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)

	server := NewServer(registry)
	handlers.server = server

	ts := httptest.NewServer(server)
	defer ts.Close()

	// Stop the server (no connections, should return immediately)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Stop(ctx); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	// Try to connect via WebSocket — should get 503
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	_, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil {
		t.Fatal("Expected connection to fail")
	}
	if resp != nil && resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("Expected 503, got %d", resp.StatusCode)
	}
}

func TestStopNoConnections(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)

	server := NewServer(registry)
	handlers.server = server

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := server.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
}

func TestStopIdempotent(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)

	server := NewServer(registry)
	handlers.server = server

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First stop
	err := server.Stop(ctx)
	if err != nil {
		t.Fatalf("First Stop returned error: %v", err)
	}

	// Second stop should not panic
	// Use a separate context since done is already closed
	ctx2, cancel2 := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel2()
	err = server.Stop(ctx2)
	if err != nil {
		t.Fatalf("Second Stop returned error: %v", err)
	}
}

func TestConnSetGet(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)

	server := NewServer(registry)
	handlers.server = server

	type principalKey struct{}
	type principal struct{ Name string }

	var capturedName string
	var mu sync.Mutex

	// Set a value in ConnectHook, read it in middleware
	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		conn.Set(principalKey{}, &principal{Name: "alice"})
		return nil
	})

	server.Use(func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			conn := Connection(ctx)
			p, _ := conn.Get(principalKey{}).(*principal)
			mu.Lock()
			capturedName = p.Name
			mu.Unlock()
			return next(ctx, req)
		}
	})

	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	// Send a request to trigger middleware
	req := IncomingMessage{
		Type:   TypeRequest,
		ID:     "1",
		Method: "IntegrationHandlers.Echo",
		Params: jsontext.Value(`[{"message":"hi"}]`),
	}
	if err := ws.WriteJSON(req); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	var resp ResponseMessage
	if err := ws.ReadJSON(&resp); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if capturedName != "alice" {
		t.Errorf("Expected 'alice', got %q", capturedName)
	}
}

func TestConnGetBeforeSet(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)

	server := NewServer(registry)
	handlers.server = server

	type unknownKey struct{}

	var capturedValue any
	var mu sync.Mutex

	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		mu.Lock()
		capturedValue = conn.Get(unknownKey{})
		mu.Unlock()
		return nil
	})

	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if capturedValue != nil {
		t.Errorf("Expected nil, got %v", capturedValue)
	}
}

func TestConnSetOverwrite(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)

	server := NewServer(registry)
	handlers.server = server

	type roleKey struct{}

	var capturedRole string
	var mu sync.Mutex

	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		conn.Set(roleKey{}, "viewer")
		conn.Set(roleKey{}, "admin") // overwrite
		mu.Lock()
		capturedRole, _ = conn.Get(roleKey{}).(string)
		mu.Unlock()
		return nil
	})

	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if capturedRole != "admin" {
		t.Errorf("Expected 'admin', got %q", capturedRole)
	}
}

func TestConnLoadDistinguishesUnsetFromNil(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)

	server := NewServer(registry)
	handlers.server = server

	type myKey struct{}

	var (
		getUnset       any
		loadUnsetVal   any
		loadUnsetOk    bool
		getAfterSetNil any
		loadSetNilVal  any
		loadSetNilOk   bool
		mu             sync.Mutex
	)

	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		mu.Lock()
		defer mu.Unlock()

		// Before Set: Get returns nil, Load returns (nil, false)
		getUnset = conn.Get(myKey{})
		loadUnsetVal, loadUnsetOk = conn.Load(myKey{})

		// Set key to nil explicitly
		conn.Set(myKey{}, nil)

		// After Set(nil): Get still returns nil, but Load returns (nil, true)
		getAfterSetNil = conn.Get(myKey{})
		loadSetNilVal, loadSetNilOk = conn.Load(myKey{})
		return nil
	})

	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	// Unset key
	if getUnset != nil {
		t.Errorf("Get on unset key: expected nil, got %v", getUnset)
	}
	if loadUnsetVal != nil {
		t.Errorf("Load on unset key: expected nil value, got %v", loadUnsetVal)
	}
	if loadUnsetOk {
		t.Error("Load on unset key: expected ok=false, got true")
	}

	// Key set to nil
	if getAfterSetNil != nil {
		t.Errorf("Get after Set(nil): expected nil, got %v", getAfterSetNil)
	}
	if loadSetNilVal != nil {
		t.Errorf("Load after Set(nil): expected nil value, got %v", loadSetNilVal)
	}
	if !loadSetNilOk {
		t.Error("Load after Set(nil): expected ok=true, got false")
	}
}

func TestConnSetGetConcurrent(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)

	server := NewServer(registry)
	handlers.server = server

	var testConn *Conn
	var connMu sync.Mutex

	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		connMu.Lock()
		testConn = conn
		connMu.Unlock()
		return nil
	})

	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	time.Sleep(50 * time.Millisecond)

	connMu.Lock()
	conn := testConn
	connMu.Unlock()

	if conn == nil {
		t.Fatal("Connection not captured")
	}

	// Concurrent Set/Get/Load should not race (run with -race)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(3)
		i := i
		go func() {
			defer wg.Done()
			conn.Set(i, i*10)
		}()
		go func() {
			defer wg.Done()
			conn.Get(i)
		}()
		go func() {
			defer wg.Done()
			conn.Load(i)
		}()
	}
	wg.Wait()

	// Verify final values via both Get and Load
	for i := 0; i < 100; i++ {
		v := conn.Get(i)
		if v != i*10 {
			t.Errorf("Get key %d: expected %d, got %v", i, i*10, v)
		}
		lv, ok := conn.Load(i)
		if !ok {
			t.Errorf("Load key %d: expected ok=true", i)
		}
		if lv != i*10 {
			t.Errorf("Load key %d: expected %d, got %v", i, i*10, lv)
		}
	}
}

// --- Arbitrary return type e2e tests ---

type UserItem struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type SliceReturnHandlers struct{}

func (h *SliceReturnHandlers) ListUsers(ctx context.Context) ([]UserItem, error) {
	return []UserItem{{ID: 1, Name: "Alice"}, {ID: 2, Name: "Bob"}}, nil
}

func (h *SliceReturnHandlers) GetName(ctx context.Context) (string, error) {
	return "hello", nil
}

func TestServerSliceReturnType(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&SliceReturnHandlers{})

	server := NewServer(registry)
	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	req := IncomingMessage{
		Type:   TypeRequest,
		ID:     "1",
		Method: "SliceReturnHandlers.ListUsers",
	}
	if err := ws.WriteJSON(req); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	var resp ResponseMessage
	if err := ws.ReadJSON(&resp); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if resp.Type != TypeResponse {
		t.Errorf("Expected response type, got %s", resp.Type)
	}

	result, _ := json.Marshal(resp.Result)
	resultStr := string(result)
	if !strings.Contains(resultStr, "Alice") || !strings.Contains(resultStr, "Bob") {
		t.Errorf("Expected Alice and Bob in result, got %s", resultStr)
	}
}

func TestServerPrimitiveReturnType(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&SliceReturnHandlers{})

	server := NewServer(registry)
	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	req := IncomingMessage{
		Type:   TypeRequest,
		ID:     "1",
		Method: "SliceReturnHandlers.GetName",
	}
	if err := ws.WriteJSON(req); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	var resp ResponseMessage
	if err := ws.ReadJSON(&resp); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if resp.Type != TypeResponse {
		t.Errorf("Expected response type, got %s", resp.Type)
	}

	result, _ := json.Marshal(resp.Result)
	if string(result) != `"hello"` {
		t.Errorf("Expected \"hello\", got %s", string(result))
	}
}

func TestServerOptionsDefaults(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)

	server := NewServer(registry)
	handlers.server = server

	ts := httptest.NewServer(server)
	defer ts.Close()

	// Connect without using connectWS to inspect the raw config message
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer ws.Close()

	// Read config message
	_, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	var msg ConfigMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Check defaults
	if msg.ReconnectInterval != 1000 {
		t.Errorf("Expected default ReconnectInterval 1000, got %d", msg.ReconnectInterval)
	}
	if msg.ReconnectMaxInterval != 30000 {
		t.Errorf("Expected default ReconnectMaxInterval 30000, got %d", msg.ReconnectMaxInterval)
	}
	if msg.HeartbeatInterval != 30000 {
		t.Errorf("Expected default HeartbeatInterval 30000, got %d", msg.HeartbeatInterval)
	}
	if msg.HeartbeatTimeout != 5000 {
		t.Errorf("Expected default HeartbeatTimeout 5000, got %d", msg.HeartbeatTimeout)
	}
}
