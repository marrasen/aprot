package aprot

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/gorilla/websocket"
)

// sseEvent represents a parsed SSE event.
type sseEvent struct {
	Event string
	Data  string
}

// sseReader reads SSE events from an HTTP response body.
type sseReader struct {
	scanner *bufio.Scanner
}

func newSSEReader(resp *http.Response) *sseReader {
	return &sseReader{scanner: bufio.NewScanner(resp.Body)}
}

// readEvent reads the next SSE event, skipping comments and blank lines.
func (r *sseReader) readEvent() (*sseEvent, error) {
	var event sseEvent
	for r.scanner.Scan() {
		line := r.scanner.Text()
		if strings.HasPrefix(line, ":") {
			// SSE comment, skip
			continue
		}
		if strings.HasPrefix(line, "event: ") {
			event.Event = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			event.Data = strings.TrimPrefix(line, "data: ")
		} else if line == "" && event.Data != "" {
			return &event, nil
		}
	}
	if err := r.scanner.Err(); err != nil {
		return nil, err
	}
	if event.Data != "" {
		return &event, nil
	}
	return nil, fmt.Errorf("SSE stream ended")
}

func setupSSETestServer(t *testing.T) (*httptest.Server, *Server, *sseHandler) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	if err := registry.Register(handlers); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	server := NewServer(registry)
	handlers.server = server

	sseH := newSSEHandler(server)

	mux := http.NewServeMux()
	mux.Handle("/ws", server)
	mux.Handle("/sse", http.StripPrefix("/sse", sseH))
	mux.Handle("/sse/", http.StripPrefix("/sse", sseH))

	ts := httptest.NewServer(mux)
	return ts, server, sseH
}

// connectSSE establishes an SSE connection, reads the connected event, and returns
// the response, reader, and connection ID.
func connectSSE(t *testing.T, ts *httptest.Server) (*http.Response, *sseReader, string) {
	t.Helper()
	resp, err := http.Get(ts.URL + "/sse")
	if err != nil {
		t.Fatalf("Failed to connect SSE: %v", err)
	}

	reader := newSSEReader(resp)

	// Read connected event
	ev, err := reader.readEvent()
	if err != nil {
		t.Fatalf("Failed to read connected event: %v", err)
	}
	if ev.Event != "connected" {
		t.Fatalf("Expected 'connected' event, got '%s'", ev.Event)
	}

	var connMsg ConnectedMessage
	if err := json.Unmarshal([]byte(ev.Data), &connMsg); err != nil {
		t.Fatalf("Failed to parse connected message: %v", err)
	}

	// Read config event
	configEv, err := reader.readEvent()
	if err != nil {
		t.Fatalf("Failed to read config event: %v", err)
	}
	if configEv.Event != "config" {
		t.Fatalf("Expected 'config' event, got '%s'", configEv.Event)
	}

	return resp, reader, connMsg.ConnectionID
}

func postRPC(t *testing.T, ts *httptest.Server, connectionID, id, method string, params string) *http.Response {
	t.Helper()
	body := fmt.Sprintf(`{"connectionId":%q,"id":%q,"method":%q,"params":%s}`, connectionID, id, method, params)
	resp, err := http.Post(ts.URL+"/sse/rpc", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /rpc failed: %v", err)
	}
	return resp
}

func postCancel(t *testing.T, ts *httptest.Server, connectionID, id string) *http.Response {
	t.Helper()
	body := fmt.Sprintf(`{"connectionId":%q,"id":%q}`, connectionID, id)
	resp, err := http.Post(ts.URL+"/sse/cancel", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /cancel failed: %v", err)
	}
	return resp
}

func TestSSEEcho(t *testing.T) {
	ts, _, _ := setupSSETestServer(t)
	defer ts.Close()

	resp, reader, connID := connectSSE(t, ts)
	defer resp.Body.Close()

	// Wait for registration
	time.Sleep(50 * time.Millisecond)

	rpcResp := postRPC(t, ts, connID, "1", "Echo", `{"message":"hello"}`)
	if rpcResp.StatusCode != http.StatusAccepted {
		t.Fatalf("Expected 202, got %d", rpcResp.StatusCode)
	}
	rpcResp.Body.Close()

	// Read response from SSE stream
	ev, err := reader.readEvent()
	if err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}

	if ev.Event != "response" {
		t.Errorf("Expected 'response' event, got '%s'", ev.Event)
	}

	var respMsg ResponseMessage
	json.Unmarshal([]byte(ev.Data), &respMsg)
	if respMsg.ID != "1" {
		t.Errorf("Expected ID 1, got %s", respMsg.ID)
	}

	result, _ := json.Marshal(respMsg.Result)
	if !strings.Contains(string(result), "hello") {
		t.Errorf("Expected hello in result, got %s", string(result))
	}
}

func TestSSEMethodNotFound(t *testing.T) {
	ts, _, _ := setupSSETestServer(t)
	defer ts.Close()

	resp, reader, connID := connectSSE(t, ts)
	defer resp.Body.Close()

	time.Sleep(50 * time.Millisecond)

	rpcResp := postRPC(t, ts, connID, "1", "NonExistent", `{}`)
	rpcResp.Body.Close()

	ev, err := reader.readEvent()
	if err != nil {
		t.Fatalf("Failed to read error: %v", err)
	}

	if ev.Event != "error" {
		t.Errorf("Expected 'error' event, got '%s'", ev.Event)
	}

	var errMsg ErrorMessage
	json.Unmarshal([]byte(ev.Data), &errMsg)
	if errMsg.Code != CodeMethodNotFound {
		t.Errorf("Expected method not found code, got %d", errMsg.Code)
	}
}

func TestSSEProgress(t *testing.T) {
	ts, _, _ := setupSSETestServer(t)
	defer ts.Close()

	resp, reader, connID := connectSSE(t, ts)
	defer resp.Body.Close()

	time.Sleep(50 * time.Millisecond)

	rpcResp := postRPC(t, ts, connID, "1", "Slow", `{"steps":3}`)
	rpcResp.Body.Close()

	progressCount := 0
	for {
		ev, err := reader.readEvent()
		if err != nil {
			t.Fatalf("Failed to read event: %v", err)
		}

		if ev.Event == "progress" {
			progressCount++
		} else if ev.Event == "response" {
			break
		} else if ev.Event == "error" {
			t.Fatalf("Unexpected error: %s", ev.Data)
		}
	}

	if progressCount != 3 {
		t.Errorf("Expected 3 progress messages, got %d", progressCount)
	}
}

func TestSSECancel(t *testing.T) {
	ts, _, _ := setupSSETestServer(t)
	defer ts.Close()

	resp, reader, connID := connectSSE(t, ts)
	defer resp.Body.Close()

	time.Sleep(50 * time.Millisecond)

	rpcResp := postRPC(t, ts, connID, "1", "Slow", `{"steps":100}`)
	rpcResp.Body.Close()

	time.Sleep(50 * time.Millisecond)

	cancelResp := postCancel(t, ts, connID, "1")
	if cancelResp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200, got %d", cancelResp.StatusCode)
	}
	cancelResp.Body.Close()

	for {
		ev, err := reader.readEvent()
		if err != nil {
			t.Fatalf("Failed to read event: %v", err)
		}

		if ev.Event == "error" {
			var errMsg ErrorMessage
			json.Unmarshal([]byte(ev.Data), &errMsg)
			if errMsg.Code != CodeCanceled {
				t.Errorf("Expected canceled code, got %d", errMsg.Code)
			}
			return
		} else if ev.Event == "response" {
			// Request completed before cancel was processed
			return
		}
	}
}

func TestSSEPush(t *testing.T) {
	ts, _, _ := setupSSETestServer(t)
	defer ts.Close()

	resp, reader, connID := connectSSE(t, ts)
	defer resp.Body.Close()

	time.Sleep(50 * time.Millisecond)

	rpcResp := postRPC(t, ts, connID, "1", "TriggerPush", `{"message":"pushed"}`)
	rpcResp.Body.Close()

	gotPush := false
	gotResponse := false

	for !gotPush || !gotResponse {
		ev, err := reader.readEvent()
		if err != nil {
			t.Fatalf("Failed to read event: %v", err)
		}

		if ev.Event == "push" {
			gotPush = true
			var pushMsg PushMessage
			json.Unmarshal([]byte(ev.Data), &pushMsg)
			if pushMsg.Event != "Notification" {
				t.Errorf("Expected Notification event, got %s", pushMsg.Event)
			}
		} else if ev.Event == "response" {
			gotResponse = true
		}
	}

	if !gotPush {
		t.Error("Did not receive push message")
	}
}

func TestSSEBroadcast(t *testing.T) {
	ts, server, _ := setupSSETestServer(t)
	defer ts.Close()

	resp1, reader1, _ := connectSSE(t, ts)
	defer resp1.Body.Close()

	resp2, reader2, _ := connectSSE(t, ts)
	defer resp2.Body.Close()

	time.Sleep(50 * time.Millisecond)

	server.Broadcast("Notification", &NotificationEvent{Message: "broadcast"})

	var wg sync.WaitGroup
	wg.Add(2)

	checkPush := func(reader *sseReader) {
		defer wg.Done()
		ev, err := reader.readEvent()
		if err != nil {
			t.Errorf("Failed to read event: %v", err)
			return
		}
		if ev.Event != "push" {
			t.Errorf("Expected 'push' event, got '%s'", ev.Event)
		}
	}

	go checkPush(reader1)
	go checkPush(reader2)
	wg.Wait()
}

func TestSSEConnectHook(t *testing.T) {
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

	sseH := newSSEHandler(server)
	mux := http.NewServeMux()
	mux.Handle("/sse", http.StripPrefix("/sse", sseH))
	mux.Handle("/sse/", http.StripPrefix("/sse", sseH))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, _, _ := connectSSE(t, ts)
	defer resp.Body.Close()

	time.Sleep(50 * time.Millisecond)

	if atomic.LoadInt32(&called) != 1 {
		t.Errorf("Expected hook called once, got %d", atomic.LoadInt32(&called))
	}
}

func TestSSEConnectHookRejects(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)

	server := NewServer(registry)
	handlers.server = server

	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		return ErrConnectionRejected("max connections reached")
	})

	sseH := newSSEHandler(server)
	ts := httptest.NewServer(http.StripPrefix("/sse", sseH))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/sse")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	reader := newSSEReader(resp)
	ev, err := reader.readEvent()
	if err != nil {
		t.Fatalf("Failed to read event: %v", err)
	}

	if ev.Event != "error" {
		t.Errorf("Expected 'error' event, got '%s'", ev.Event)
	}

	var errMsg ErrorMessage
	json.Unmarshal([]byte(ev.Data), &errMsg)
	if errMsg.Code != CodeConnectionRejected {
		t.Errorf("Expected connection rejected code, got %d", errMsg.Code)
	}
	if errMsg.Message != "max connections reached" {
		t.Errorf("Expected 'max connections reached', got %s", errMsg.Message)
	}

	time.Sleep(50 * time.Millisecond)
	if server.ConnectionCount() != 0 {
		t.Errorf("Expected 0 connections, got %d", server.ConnectionCount())
	}
}

func TestSSEDisconnectHook(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)

	server := NewServer(registry)
	handlers.server = server

	var called int32
	server.OnDisconnect(func(ctx context.Context, conn *Conn) {
		atomic.AddInt32(&called, 1)
	})

	sseH := newSSEHandler(server)
	mux := http.NewServeMux()
	mux.Handle("/sse", http.StripPrefix("/sse", sseH))
	mux.Handle("/sse/", http.StripPrefix("/sse", sseH))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, _, _ := connectSSE(t, ts)

	time.Sleep(50 * time.Millisecond)

	resp.Body.Close()

	time.Sleep(100 * time.Millisecond)

	if atomic.LoadInt32(&called) != 1 {
		t.Errorf("Expected disconnect hook called once, got %d", atomic.LoadInt32(&called))
	}
}

func TestSSEConfigOnConnect(t *testing.T) {
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

	sseH := newSSEHandler(server)
	ts := httptest.NewServer(http.StripPrefix("/sse", sseH))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/sse")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	reader := newSSEReader(resp)

	// Read connected event
	ev, err := reader.readEvent()
	if err != nil {
		t.Fatalf("Failed to read connected event: %v", err)
	}
	if ev.Event != "connected" {
		t.Fatalf("Expected 'connected' event, got '%s'", ev.Event)
	}

	// Read config event
	configEv, err := reader.readEvent()
	if err != nil {
		t.Fatalf("Failed to read config event: %v", err)
	}
	if configEv.Event != "config" {
		t.Fatalf("Expected 'config' event, got '%s'", configEv.Event)
	}

	var msg ConfigMessage
	json.Unmarshal([]byte(configEv.Data), &msg)

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

func TestSSEInvalidConnectionID(t *testing.T) {
	ts, _, _ := setupSSETestServer(t)
	defer ts.Close()

	// POST /rpc with invalid connection ID
	body := `{"connectionId":"invalid","id":"1","method":"Echo","params":{"message":"hello"}}`
	resp, err := http.Post(ts.URL+"/sse/rpc", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected 400, got %d", resp.StatusCode)
	}

	// POST /cancel with invalid connection ID
	cancelBody := `{"connectionId":"invalid","id":"1"}`
	cancelResp, err := http.Post(ts.URL+"/sse/cancel", "application/json", strings.NewReader(cancelBody))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer cancelResp.Body.Close()

	if cancelResp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected 400, got %d", cancelResp.StatusCode)
	}
}

// connectWSPath connects a WebSocket client to a specific path on the test server.
func connectWSPath(t *testing.T, ts *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + path
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Failed to connect WS: %v", err)
	}
	// Read and discard config message
	_, _, err = ws.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read config: %v", err)
	}
	return ws
}

// Cross-transport tests

func TestMixedBroadcast(t *testing.T) {
	ts, server, _ := setupSSETestServer(t)
	defer ts.Close()

	// Connect WS client
	ws := connectWSPath(t, ts, "/ws")
	defer ws.Close()

	// Connect SSE client
	resp, reader, _ := connectSSE(t, ts)
	defer resp.Body.Close()

	time.Sleep(50 * time.Millisecond)

	// Broadcast
	server.Broadcast("Notification", &NotificationEvent{Message: "both"})

	// Check WS receives
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Errorf("WS read failed: %v", err)
			return
		}
		var msg PushMessage
		json.Unmarshal(data, &msg)
		if msg.Event != "Notification" {
			t.Errorf("WS: Expected Notification, got %s", msg.Event)
		}
	}()

	go func() {
		defer wg.Done()
		ev, err := reader.readEvent()
		if err != nil {
			t.Errorf("SSE read failed: %v", err)
			return
		}
		if ev.Event != "push" {
			t.Errorf("SSE: Expected 'push' event, got '%s'", ev.Event)
		}
	}()

	wg.Wait()
}

func TestMixedConnectionCount(t *testing.T) {
	ts, server, _ := setupSSETestServer(t)
	defer ts.Close()

	ws := connectWSPath(t, ts, "/ws")
	defer ws.Close()

	resp, _, _ := connectSSE(t, ts)
	defer resp.Body.Close()

	time.Sleep(50 * time.Millisecond)

	if server.ConnectionCount() != 2 {
		t.Errorf("Expected 2 connections, got %d", server.ConnectionCount())
	}
}

func TestMixedPushToUser(t *testing.T) {
	registry := NewRegistry()
	handlers := &IntegrationHandlers{}
	registry.Register(handlers)

	server := NewServer(registry)
	handlers.server = server

	server.OnConnect(func(ctx context.Context, conn *Conn) error {
		conn.SetUserID("user1")
		return nil
	})

	sseH := newSSEHandler(server)
	mux := http.NewServeMux()
	mux.Handle("/ws", server)
	mux.Handle("/sse", http.StripPrefix("/sse", sseH))
	mux.Handle("/sse/", http.StripPrefix("/sse", sseH))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Connect WS
	ws := connectWSPath(t, ts, "/ws")
	defer ws.Close()

	// Connect SSE
	resp, reader, _ := connectSSE(t, ts)
	defer resp.Body.Close()

	time.Sleep(50 * time.Millisecond)

	// Push to user
	server.PushToUser("user1", "DirectMessage", &NotificationEvent{Message: "dm"})

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Errorf("WS read failed: %v", err)
			return
		}
		var msg PushMessage
		json.Unmarshal(data, &msg)
		if msg.Event != "DirectMessage" {
			t.Errorf("WS: Expected DirectMessage, got %s", msg.Event)
		}
	}()

	go func() {
		defer wg.Done()
		ev, err := reader.readEvent()
		if err != nil {
			t.Errorf("SSE read failed: %v", err)
			return
		}
		if ev.Event != "push" {
			t.Errorf("SSE: Expected 'push' event, got '%s'", ev.Event)
		}
	}()

	wg.Wait()
}

