package aprot

import (
	"context"
	"errors"
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
	h.server.Broadcast("Notification", &NotificationEvent{Message: req.Message})
	return &BroadcastResponse{Sent: true}, nil
}

func (h *IntegrationHandlers) TriggerPush(ctx context.Context, req *BroadcastRequest) (*BroadcastResponse, error) {
	conn := Connection(ctx)
	if conn != nil {
		conn.Push("Notification", &NotificationEvent{Message: req.Message})
	}
	return &BroadcastResponse{Sent: true}, nil
}

func setupTestServer(t *testing.T) (*httptest.Server, *Server, *IntegrationHandlers) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	if err := registry.Register(handlers); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

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
		Method: "Echo",
		Params: jsontext.Value(`{"message":"hello"}`),
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
		Method: "Slow",
		Params: jsontext.Value(`{"steps":3}`),
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
		Method: "Slow",
		Params: jsontext.Value(`{"steps":100}`),
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
		Method: "TriggerPush",
		Params: jsontext.Value(`{"message":"pushed"}`),
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
			if msg["event"] != "Notification" {
				t.Errorf("Expected Notification event, got %v", msg["event"])
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
	server.Broadcast("Notification", &NotificationEvent{Message: "broadcast"})

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
		if msg.Event != "Notification" {
			t.Errorf("Expected Notification event, got %s", msg.Event)
		}
	}

	go checkPush(ws1)
	go checkPush(ws2)

	wg.Wait()
}

func TestOnConnectHookCalled(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	if err := registry.Register(handlers); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

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
	if err := registry.Register(handlers); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

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
	if err := registry.Register(handlers); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

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
	if err := registry.Register(handlers); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

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
	if err := registry.Register(handlers); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

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
	if err := registry.Register(handlers); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

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
	if err := registry.Register(handlers); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

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
	if err := registry.Register(&VoidTestHandlers{}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	server := NewServer(registry)
	ts := httptest.NewServer(server)
	defer ts.Close()

	ws := connectWS(t, ts)
	defer ws.Close()

	req := IncomingMessage{
		Type:   TypeRequest,
		ID:     "1",
		Method: "DeleteItem",
		Params: jsontext.Value(`{"id":"item_1"}`),
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

func TestServerOptionsDefaults(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	if err := registry.Register(handlers); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

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
