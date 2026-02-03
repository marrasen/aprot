package aprot

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
		Params: json.RawMessage(`{"message":"hello"}`),
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
		Params: json.RawMessage(`{"steps":3}`),
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
		Params: json.RawMessage(`{"steps":100}`),
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
		Params: json.RawMessage(`{"message":"pushed"}`),
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
