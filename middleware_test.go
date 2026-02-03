package aprot

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/gorilla/websocket"
)

// Test handler for middleware tests
type MiddlewareTestHandler struct{}

type MWEchoRequest struct {
	Message string `json:"message"`
	UserID  string `json:"user_id,omitempty"`
}

type MWEchoResponse struct {
	Message string `json:"message"`
	UserID  string `json:"user_id,omitempty"`
}

func (h *MiddlewareTestHandler) Echo(ctx context.Context, req *MWEchoRequest) (*MWEchoResponse, error) {
	// Check if user was added to context by middleware
	userID := ""
	if u := ctx.Value(testUserKey); u != nil {
		userID = u.(string)
	}
	return &MWEchoResponse{Message: req.Message, UserID: userID}, nil
}

type MWSecretRequest struct{}
type MWSecretResponse struct {
	Secret string `json:"secret"`
}

type contextKeyType string

const testUserKey contextKeyType = "test_user"

func TestMiddlewareChainExecutionOrder(t *testing.T) {
	var order []string
	var mu sync.Mutex

	mw1 := func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			mu.Lock()
			order = append(order, "mw1-before")
			mu.Unlock()
			result, err := next(ctx, req)
			mu.Lock()
			order = append(order, "mw1-after")
			mu.Unlock()
			return result, err
		}
	}

	mw2 := func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			mu.Lock()
			order = append(order, "mw2-before")
			mu.Unlock()
			result, err := next(ctx, req)
			mu.Lock()
			order = append(order, "mw2-after")
			mu.Unlock()
			return result, err
		}
	}

	registry := NewRegistry()
	registry.Register(&MiddlewareTestHandler{})

	server := NewServer(registry)
	server.Use(mw1, mw2)

	ts := httptest.NewServer(server)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer ws.Close()

	// Send request
	reqMsg := map[string]any{
		"type":   "request",
		"id":     "1",
		"method": "Echo",
		"params": map[string]string{"message": "hello"},
	}
	if err := ws.WriteJSON(reqMsg); err != nil {
		t.Fatalf("write error: %v", err)
	}

	// Read response
	var resp map[string]any
	if err := ws.ReadJSON(&resp); err != nil {
		t.Fatalf("read error: %v", err)
	}

	if resp["type"] != "response" {
		t.Errorf("expected response type, got %v", resp["type"])
	}

	// Verify middleware execution order
	mu.Lock()
	defer mu.Unlock()

	expected := []string{"mw1-before", "mw2-before", "mw2-after", "mw1-after"}
	if len(order) != len(expected) {
		t.Fatalf("expected %d middleware calls, got %d: %v", len(expected), len(order), order)
	}
	for i, exp := range expected {
		if order[i] != exp {
			t.Errorf("expected order[%d]=%s, got %s", i, exp, order[i])
		}
	}
}

func TestMiddlewareContextModification(t *testing.T) {
	// Middleware that adds user to context
	authMiddleware := func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			ctx = context.WithValue(ctx, testUserKey, "user123")
			return next(ctx, req)
		}
	}

	registry := NewRegistry()
	registry.Register(&MiddlewareTestHandler{})

	server := NewServer(registry)
	server.Use(authMiddleware)

	ts := httptest.NewServer(server)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer ws.Close()

	// Send request
	reqMsg := map[string]any{
		"type":   "request",
		"id":     "1",
		"method": "Echo",
		"params": map[string]string{"message": "hello"},
	}
	if err := ws.WriteJSON(reqMsg); err != nil {
		t.Fatalf("write error: %v", err)
	}

	// Read response
	var resp map[string]any
	if err := ws.ReadJSON(&resp); err != nil {
		t.Fatalf("read error: %v", err)
	}

	result := resp["result"].(map[string]any)
	if result["user_id"] != "user123" {
		t.Errorf("expected user_id=user123, got %v", result["user_id"])
	}
}

func TestMiddlewareRequestRejection(t *testing.T) {
	// Middleware that rejects all requests
	rejectMiddleware := func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			return nil, ErrUnauthorized("access denied")
		}
	}

	registry := NewRegistry()
	registry.Register(&MiddlewareTestHandler{})

	server := NewServer(registry)
	server.Use(rejectMiddleware)

	ts := httptest.NewServer(server)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer ws.Close()

	// Send request
	reqMsg := map[string]any{
		"type":   "request",
		"id":     "1",
		"method": "Echo",
		"params": map[string]string{"message": "hello"},
	}
	if err := ws.WriteJSON(reqMsg); err != nil {
		t.Fatalf("write error: %v", err)
	}

	// Read response
	var resp map[string]any
	if err := ws.ReadJSON(&resp); err != nil {
		t.Fatalf("read error: %v", err)
	}

	if resp["type"] != "error" {
		t.Errorf("expected error type, got %v", resp["type"])
	}
	if resp["code"].(float64) != CodeUnauthorized {
		t.Errorf("expected code %d, got %v", CodeUnauthorized, resp["code"])
	}
	if resp["message"] != "access denied" {
		t.Errorf("expected message 'access denied', got %v", resp["message"])
	}
}

// PublicTestHandler is a handler with no middleware
type PublicTestHandler struct{}

func (h *PublicTestHandler) Echo(ctx context.Context, req *MWEchoRequest) (*MWEchoResponse, error) {
	userID := ""
	if u := ctx.Value(testUserKey); u != nil {
		userID = u.(string)
	}
	return &MWEchoResponse{Message: req.Message, UserID: userID}, nil
}

// ProtectedTestHandler is a handler that requires auth middleware
type ProtectedTestHandler struct{}

func (h *ProtectedTestHandler) GetSecret(ctx context.Context, req *MWSecretRequest) (*MWSecretResponse, error) {
	return &MWSecretResponse{Secret: "classified"}, nil
}

func TestPerHandlerMiddleware(t *testing.T) {
	// Auth middleware that always rejects (for testing)
	authMiddleware := func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			return nil, ErrUnauthorized("authentication required")
		}
	}

	registry := NewRegistry()
	registry.Register(&PublicTestHandler{})                   // No middleware
	registry.Register(&ProtectedTestHandler{}, authMiddleware) // With auth

	server := NewServer(registry)

	ts := httptest.NewServer(server)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer ws.Close()

	// Test Echo (public handler, no auth required) - should succeed
	echoReq := map[string]any{
		"type":   "request",
		"id":     "1",
		"method": "Echo",
		"params": map[string]string{"message": "hello"},
	}
	if err := ws.WriteJSON(echoReq); err != nil {
		t.Fatalf("write error: %v", err)
	}

	var echoResp map[string]any
	if err := ws.ReadJSON(&echoResp); err != nil {
		t.Fatalf("read error: %v", err)
	}

	if echoResp["type"] != "response" {
		t.Errorf("Echo: expected response type, got %v", echoResp["type"])
	}

	// Test GetSecret (protected handler, auth required) - should fail
	secretReq := map[string]any{
		"type":   "request",
		"id":     "2",
		"method": "GetSecret",
		"params": map[string]any{},
	}
	if err := ws.WriteJSON(secretReq); err != nil {
		t.Fatalf("write error: %v", err)
	}

	var secretResp map[string]any
	if err := ws.ReadJSON(&secretResp); err != nil {
		t.Fatalf("read error: %v", err)
	}

	if secretResp["type"] != "error" {
		t.Errorf("GetSecret: expected error type, got %v", secretResp["type"])
	}
	if secretResp["code"].(float64) != CodeUnauthorized {
		t.Errorf("GetSecret: expected code %d, got %v", CodeUnauthorized, secretResp["code"])
	}
}

func TestGetMiddleware(t *testing.T) {
	testMiddleware := func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			return next(ctx, req)
		}
	}

	registry := NewRegistry()
	registry.Register(&PublicTestHandler{})                   // No middleware
	registry.Register(&ProtectedTestHandler{}, testMiddleware) // With middleware

	// Check middleware retrieval
	publicMW := registry.GetMiddleware("Echo")
	if len(publicMW) != 0 {
		t.Errorf("expected no middleware for Echo, got %d", len(publicMW))
	}

	protectedMW := registry.GetMiddleware("GetSecret")
	if len(protectedMW) != 1 {
		t.Errorf("expected 1 middleware for GetSecret, got %d", len(protectedMW))
	}
}

func TestServerAndHandlerMiddlewareCombined(t *testing.T) {
	var order []string
	var mu sync.Mutex

	serverMiddleware := func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			mu.Lock()
			order = append(order, "server-before")
			mu.Unlock()
			result, err := next(ctx, req)
			mu.Lock()
			order = append(order, "server-after")
			mu.Unlock()
			return result, err
		}
	}

	handlerMiddleware := func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			mu.Lock()
			order = append(order, "handler-before")
			mu.Unlock()
			result, err := next(ctx, req)
			mu.Lock()
			order = append(order, "handler-after")
			mu.Unlock()
			return result, err
		}
	}

	registry := NewRegistry()
	registry.Register(&ProtectedTestHandler{}, handlerMiddleware)

	server := NewServer(registry)
	server.Use(serverMiddleware)

	ts := httptest.NewServer(server)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer ws.Close()

	reqMsg := map[string]any{
		"type":   "request",
		"id":     "1",
		"method": "GetSecret",
		"params": map[string]any{},
	}
	if err := ws.WriteJSON(reqMsg); err != nil {
		t.Fatalf("write error: %v", err)
	}

	var resp map[string]any
	if err := ws.ReadJSON(&resp); err != nil {
		t.Fatalf("read error: %v", err)
	}

	// Verify execution order: server middleware is outer, handler middleware is inner
	mu.Lock()
	defer mu.Unlock()

	expected := []string{"server-before", "handler-before", "handler-after", "server-after"}
	if len(order) != len(expected) {
		t.Fatalf("expected %d middleware calls, got %d: %v", len(expected), len(order), order)
	}
	for i, exp := range expected {
		if order[i] != exp {
			t.Errorf("expected order[%d]=%s, got %s", i, exp, order[i])
		}
	}
}

func TestUserTargetedPush(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&MiddlewareTestHandler{})

	server := NewServer(registry)

	// Create test server
	ts := httptest.NewServer(server)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Connect two clients for user1
	ws1a, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer ws1a.Close()

	ws1b, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer ws1b.Close()

	// Connect one client for user2
	ws2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer ws2.Close()

	// Wait for connections to be registered
	time.Sleep(50 * time.Millisecond)

	// Middleware that sets user ID
	server.Use(func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			// Parse user ID from params
			var params struct {
				UserID string `json:"user_id"`
			}
			json.Unmarshal(req.Params, &params)
			if params.UserID != "" {
				conn := Connection(ctx)
				if conn != nil {
					conn.SetUserID(params.UserID)
				}
			}
			return next(ctx, req)
		}
	})

	// Identify each connection
	identify := func(ws *websocket.Conn, userID, reqID string) {
		msg := map[string]any{
			"type":   "request",
			"id":     reqID,
			"method": "Echo",
			"params": map[string]string{"message": "identify", "user_id": userID},
		}
		ws.WriteJSON(msg)
		var resp map[string]any
		ws.ReadJSON(&resp)
	}

	identify(ws1a, "user1", "id-1a")
	identify(ws1b, "user1", "id-1b")
	identify(ws2, "user2", "id-2")

	// Set read deadline for all connections
	ws1a.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	ws1b.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	ws2.SetReadDeadline(time.Now().Add(500 * time.Millisecond))

	// Send push to user1
	type NotificationData struct {
		Message string `json:"message"`
	}
	server.PushToUser("user1", "Notification", &NotificationData{Message: "hello user1"})

	// user1's connections should receive the push
	var push1a, push1b map[string]any
	if err := ws1a.ReadJSON(&push1a); err != nil {
		t.Errorf("user1a should receive push: %v", err)
	} else if push1a["type"] != "push" || push1a["event"] != "Notification" {
		t.Errorf("unexpected push to user1a: %v", push1a)
	}

	if err := ws1b.ReadJSON(&push1b); err != nil {
		t.Errorf("user1b should receive push: %v", err)
	} else if push1b["type"] != "push" || push1b["event"] != "Notification" {
		t.Errorf("unexpected push to user1b: %v", push1b)
	}

	// user2 should NOT receive the push (timeout expected)
	var push2 map[string]any
	if err := ws2.ReadJSON(&push2); err == nil {
		t.Errorf("user2 should NOT receive push, but got: %v", push2)
	}
}

func TestRequestFromContext(t *testing.T) {
	var capturedReq *Request

	middleware := func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			capturedReq = RequestFromContext(ctx)
			return next(ctx, req)
		}
	}

	registry := NewRegistry()
	registry.Register(&MiddlewareTestHandler{})

	server := NewServer(registry)
	server.Use(middleware)

	ts := httptest.NewServer(server)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer ws.Close()

	reqMsg := map[string]any{
		"type":   "request",
		"id":     "test-123",
		"method": "Echo",
		"params": map[string]string{"message": "hello"},
	}
	if err := ws.WriteJSON(reqMsg); err != nil {
		t.Fatalf("write error: %v", err)
	}

	var resp map[string]any
	if err := ws.ReadJSON(&resp); err != nil {
		t.Fatalf("read error: %v", err)
	}

	if capturedReq == nil {
		t.Fatal("Request not found in context")
	}
	if capturedReq.ID != "test-123" {
		t.Errorf("expected ID=test-123, got %s", capturedReq.ID)
	}
	if capturedReq.Method != "Echo" {
		t.Errorf("expected Method=Echo, got %s", capturedReq.Method)
	}
}

func TestSetUserIDDisassociatesOldUser(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&MiddlewareTestHandler{})
	server := NewServer(registry)

	ts := httptest.NewServer(server)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer ws.Close()

	time.Sleep(50 * time.Millisecond)

	// Middleware that sets user ID from params
	server.Use(func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			var params struct {
				UserID string `json:"user_id"`
			}
			json.Unmarshal(req.Params, &params)
			if params.UserID != "" {
				conn := Connection(ctx)
				if conn != nil {
					conn.SetUserID(params.UserID)
				}
			}
			return next(ctx, req)
		}
	})

	// Set user to user1
	msg1 := map[string]any{
		"type":   "request",
		"id":     "1",
		"method": "Echo",
		"params": map[string]string{"message": "hi", "user_id": "user1"},
	}
	ws.WriteJSON(msg1)
	var resp1 map[string]any
	ws.ReadJSON(&resp1)

	// Verify user1 has the connection
	server.mu.RLock()
	user1Conns := len(server.userConns["user1"])
	server.mu.RUnlock()
	if user1Conns != 1 {
		t.Errorf("expected 1 connection for user1, got %d", user1Conns)
	}

	// Change to user2
	msg2 := map[string]any{
		"type":   "request",
		"id":     "2",
		"method": "Echo",
		"params": map[string]string{"message": "hi", "user_id": "user2"},
	}
	ws.WriteJSON(msg2)
	var resp2 map[string]any
	ws.ReadJSON(&resp2)

	// Verify user1 no longer has the connection and user2 does
	server.mu.RLock()
	user1Conns = len(server.userConns["user1"])
	user2Conns := len(server.userConns["user2"])
	server.mu.RUnlock()

	if user1Conns != 0 {
		t.Errorf("expected 0 connections for user1, got %d", user1Conns)
	}
	if user2Conns != 1 {
		t.Errorf("expected 1 connection for user2, got %d", user2Conns)
	}
}

func TestErrorCodes(t *testing.T) {
	tests := []struct {
		name     string
		err      *ProtocolError
		code     int
		message  string
	}{
		{"Unauthorized", ErrUnauthorized("not logged in"), CodeUnauthorized, "not logged in"},
		{"Forbidden", ErrForbidden("access denied"), CodeForbidden, "access denied"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Code != tt.code {
				t.Errorf("expected code %d, got %d", tt.code, tt.err.Code)
			}
			if tt.err.Message != tt.message {
				t.Errorf("expected message %q, got %q", tt.message, tt.err.Message)
			}
		})
	}
}

// Sentinel errors for testing
var (
	ErrTestNotFound = errors.New("not found")
	ErrTestTimeout  = errors.New("timeout")
)

func TestRegisterError(t *testing.T) {
	registry := NewRegistry()

	// Register custom errors
	registry.RegisterError(ErrTestNotFound, "NotFound")
	registry.RegisterError(ErrTestTimeout, "Timeout")

	// Verify error codes were registered
	codes := registry.ErrorCodes()
	if len(codes) != 2 {
		t.Fatalf("expected 2 error codes, got %d", len(codes))
	}

	if codes[0].Name != "NotFound" || codes[0].Code != 1000 {
		t.Errorf("expected NotFound:1000, got %s:%d", codes[0].Name, codes[0].Code)
	}
	if codes[1].Name != "Timeout" || codes[1].Code != 1001 {
		t.Errorf("expected Timeout:1001, got %s:%d", codes[1].Name, codes[1].Code)
	}

	// Verify lookup works
	code, found := registry.LookupError(ErrTestNotFound)
	if !found || code != 1000 {
		t.Errorf("expected to find ErrTestNotFound with code 1000, got %d, found=%v", code, found)
	}

	code, found = registry.LookupError(ErrTestTimeout)
	if !found || code != 1001 {
		t.Errorf("expected to find ErrTestTimeout with code 1001, got %d, found=%v", code, found)
	}

	// Verify unknown error returns not found
	code, found = registry.LookupError(errors.New("unknown"))
	if found {
		t.Errorf("expected unknown error to not be found, got code %d", code)
	}
}

func TestRegisterErrorCode(t *testing.T) {
	registry := NewRegistry()

	// Register error code without mapping
	code1 := registry.RegisterErrorCode("InsufficientBalance")
	code2 := registry.RegisterErrorCode("OutOfStock")

	if code1 != 1000 {
		t.Errorf("expected code 1000, got %d", code1)
	}
	if code2 != 1001 {
		t.Errorf("expected code 1001, got %d", code2)
	}

	codes := registry.ErrorCodes()
	if len(codes) != 2 {
		t.Fatalf("expected 2 error codes, got %d", len(codes))
	}
}

func TestRegisterErrorWrapped(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterError(ErrTestNotFound, "NotFound")

	// Test that wrapped errors are also matched
	wrappedErr := fmt.Errorf("failed to get user: %w", ErrTestNotFound)

	code, found := registry.LookupError(wrappedErr)
	if !found || code != 1000 {
		t.Errorf("expected wrapped error to match, got code %d, found=%v", code, found)
	}
}

// Handler that returns registered errors
type ErrorTestHandler struct{}

type ErrorTestRequest struct{}
type ErrorTestResponse struct {
	OK bool `json:"ok"`
}

func (h *ErrorTestHandler) TriggerNotFound(ctx context.Context, req *ErrorTestRequest) (*ErrorTestResponse, error) {
	return nil, ErrTestNotFound
}

func (h *ErrorTestHandler) TriggerWrapped(ctx context.Context, req *ErrorTestRequest) (*ErrorTestResponse, error) {
	return nil, fmt.Errorf("wrapped: %w", ErrTestNotFound)
}

func TestRegisteredErrorSentToClient(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterError(ErrTestNotFound, "NotFound")
	registry.Register(&ErrorTestHandler{})

	server := NewServer(registry)

	ts := httptest.NewServer(server)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer ws.Close()

	// Test direct error
	reqMsg := map[string]any{
		"type":   "request",
		"id":     "1",
		"method": "TriggerNotFound",
		"params": map[string]any{},
	}
	if err := ws.WriteJSON(reqMsg); err != nil {
		t.Fatalf("write error: %v", err)
	}

	var resp map[string]any
	if err := ws.ReadJSON(&resp); err != nil {
		t.Fatalf("read error: %v", err)
	}

	if resp["type"] != "error" {
		t.Errorf("expected error type, got %v", resp["type"])
	}
	if resp["code"].(float64) != 1000 {
		t.Errorf("expected code 1000, got %v", resp["code"])
	}

	// Test wrapped error
	reqMsg2 := map[string]any{
		"type":   "request",
		"id":     "2",
		"method": "TriggerWrapped",
		"params": map[string]any{},
	}
	if err := ws.WriteJSON(reqMsg2); err != nil {
		t.Fatalf("write error: %v", err)
	}

	var resp2 map[string]any
	if err := ws.ReadJSON(&resp2); err != nil {
		t.Fatalf("read error: %v", err)
	}

	if resp2["type"] != "error" {
		t.Errorf("expected error type, got %v", resp2["type"])
	}
	if resp2["code"].(float64) != 1000 {
		t.Errorf("expected code 1000 for wrapped error, got %v", resp2["code"])
	}
}

func TestMiddlewareWithNoMiddleware(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&MiddlewareTestHandler{})

	server := NewServer(registry)
	// No middleware added

	ts := httptest.NewServer(server)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer ws.Close()

	reqMsg := map[string]any{
		"type":   "request",
		"id":     "1",
		"method": "Echo",
		"params": map[string]string{"message": "hello"},
	}
	if err := ws.WriteJSON(reqMsg); err != nil {
		t.Fatalf("write error: %v", err)
	}

	var resp map[string]any
	if err := ws.ReadJSON(&resp); err != nil {
		t.Fatalf("read error: %v", err)
	}

	if resp["type"] != "response" {
		t.Errorf("expected response type, got %v", resp["type"])
	}

	result := resp["result"].(map[string]any)
	if result["message"] != "hello" {
		t.Errorf("expected message=hello, got %v", result["message"])
	}
}
