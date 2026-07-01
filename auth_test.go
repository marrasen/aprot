package aprot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/gorilla/websocket"
)

// authHandlers is a minimal handler set for auth tests.
type authHandlers struct{}

func (authHandlers) Ping(ctx context.Context) (*EchoResponse, error) {
	return &EchoResponse{Message: "pong"}, nil
}

// WhoAmI returns the authenticated connection's user ID.
func (authHandlers) WhoAmI(ctx context.Context) (*EchoResponse, error) {
	return &EchoResponse{Message: Connection(ctx).UserID()}, nil
}

// tokenHook accepts tokens of the form "good:<user>" (setting that user id) and
// rejects everything else.
func tokenHook(ctx context.Context, conn *Conn, token string) error {
	if u, ok := strings.CutPrefix(token, "good:"); ok {
		conn.SetUserID(u)
		return nil
	}
	return ErrAuthFailed("invalid token")
}

func newAuthServer(t *testing.T, opts ServerOptions, hook AuthHook) *httptest.Server {
	t.Helper()
	registry := NewRegistry()
	registry.Register(&authHandlers{})
	server := NewServer(registry, opts)
	if hook != nil {
		server.OnAuth(hook)
	}
	sseH := newSSEHandler(server)
	mux := http.NewServeMux()
	mux.Handle("/ws", server)
	mux.Handle("/sse", http.StripPrefix("/sse", sseH))
	mux.Handle("/sse/", http.StripPrefix("/sse", sseH))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// wsFrame is a permissive view of any server->client WebSocket frame.
type wsFrame struct {
	Type    string        `json:"type"`
	ID      string        `json:"id"`
	Code    int           `json:"code"`
	Message string        `json:"message"`
	Result  *EchoResponse `json:"result"`
}

func readFrame(t *testing.T, ws *websocket.Conn, timeout time.Duration) wsFrame {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(timeout))
	defer ws.SetReadDeadline(time.Time{})
	var f wsFrame
	if err := ws.ReadJSON(&f); err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return f
}

func sendAuth(t *testing.T, ws *websocket.Conn, token string) {
	t.Helper()
	if err := ws.WriteJSON(IncomingMessage{Type: TypeAuth, Token: token}); err != nil {
		t.Fatalf("send auth: %v", err)
	}
}

// --- WebSocket ---

// With no auth hook registered, connections work immediately (no pending state).
func TestAuth_NoHook_BackwardCompatible(t *testing.T) {
	ts := newAuthServer(t, ServerOptions{}, nil)
	ws := connectWSPath(t, ts, "/ws")
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "authHandlers.Ping"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	f := readFrame(t, ws, 3*time.Second)
	if f.Type != string(TypeResponse) {
		t.Fatalf("expected response without auth when no hook is set, got type=%q code=%d", f.Type, f.Code)
	}
}

// A request sent before authenticating is rejected with auth_error and no
// handler runs.
func TestAuth_PreAuthRequestRejected(t *testing.T) {
	ts := newAuthServer(t, ServerOptions{}, tokenHook)
	ws := connectWSPath(t, ts, "/ws")
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "authHandlers.Ping"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	f := readFrame(t, ws, 3*time.Second)
	if f.Type != string(TypeAuthError) {
		t.Fatalf("expected auth_error for a pre-auth request, got type=%q", f.Type)
	}
}

// A valid token yields auth_ok and unlocks normal requests.
func TestAuth_ValidTokenUnlocksRequests(t *testing.T) {
	ts := newAuthServer(t, ServerOptions{}, tokenHook)
	ws := connectWSPath(t, ts, "/ws")
	defer ws.Close()

	sendAuth(t, ws, "good:alice")
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthOK) {
		t.Fatalf("expected auth_ok, got type=%q message=%q", f.Type, f.Message)
	}

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "authHandlers.WhoAmI"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	f := readFrame(t, ws, 3*time.Second)
	if f.Type != string(TypeResponse) || f.Result == nil || f.Result.Message != "alice" {
		t.Fatalf("expected WhoAmI=alice after auth, got type=%q result=%+v", f.Type, f.Result)
	}
}

// An invalid token yields auth_error and closes the connection.
func TestAuth_InvalidTokenClosesConnection(t *testing.T) {
	ts := newAuthServer(t, ServerOptions{}, tokenHook)
	ws := connectWSPath(t, ts, "/ws")
	defer ws.Close()

	sendAuth(t, ws, "nope")
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthError) {
		t.Fatalf("expected auth_error, got type=%q", f.Type)
	}
	// The connection is closed: the next read fails.
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	var f wsFrame
	if err := ws.ReadJSON(&f); err == nil {
		t.Fatalf("expected connection to be closed after invalid token, but read succeeded: %+v", f)
	}
}

// A connection that never authenticates is closed after AuthTimeout.
func TestAuth_TimeoutClosesConnection(t *testing.T) {
	ts := newAuthServer(t, ServerOptions{AuthTimeout: 150 * time.Millisecond}, tokenHook)
	ws := connectWSPath(t, ts, "/ws")
	defer ws.Close()

	f := readFrame(t, ws, 3*time.Second)
	if f.Type != string(TypeAuthError) {
		t.Fatalf("expected auth_error on timeout, got type=%q", f.Type)
	}
	if !strings.Contains(f.Message, "timeout") {
		t.Errorf("expected a timeout message, got %q", f.Message)
	}
}

// A mid-session auth frame refreshes the connection's identity.
func TestAuth_RefreshUpdatesIdentity(t *testing.T) {
	ts := newAuthServer(t, ServerOptions{}, tokenHook)
	ws := connectWSPath(t, ts, "/ws")
	defer ws.Close()

	sendAuth(t, ws, "good:alice")
	readFrame(t, ws, 3*time.Second) // auth_ok

	sendAuth(t, ws, "good:bob")
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthOK) {
		t.Fatalf("expected auth_ok on refresh, got type=%q", f.Type)
	}

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "authHandlers.WhoAmI"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if f := readFrame(t, ws, 3*time.Second); f.Result == nil || f.Result.Message != "bob" {
		t.Fatalf("expected identity refreshed to bob, got %+v", f.Result)
	}
}

// A failed refresh on a live connection reports auth_error but keeps the session.
func TestAuth_FailedRefreshKeepsSession(t *testing.T) {
	ts := newAuthServer(t, ServerOptions{}, tokenHook)
	ws := connectWSPath(t, ts, "/ws")
	defer ws.Close()

	sendAuth(t, ws, "good:alice")
	readFrame(t, ws, 3*time.Second) // auth_ok

	sendAuth(t, ws, "bad")
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthError) {
		t.Fatalf("expected auth_error on bad refresh, got type=%q", f.Type)
	}

	// The session survives: a request still works, still as alice.
	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "authHandlers.WhoAmI"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	f := readFrame(t, ws, 3*time.Second)
	if f.Type != string(TypeResponse) || f.Result == nil || f.Result.Message != "alice" {
		t.Fatalf("expected session kept as alice after failed refresh, got type=%q result=%+v", f.Type, f.Result)
	}
}

// --- SSE ---

func postAuthSSE(t *testing.T, ts *httptest.Server, connectionID, token string) *http.Response {
	t.Helper()
	body := fmt.Sprintf(`{"type":"auth","connectionId":%q,"token":%q}`, connectionID, token)
	resp, err := http.Post(ts.URL+"/sse/rpc", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /rpc auth failed: %v", err)
	}
	return resp
}

// SSE: a valid token (in the first POST /rpc body) yields an auth_ok stream
// event and unlocks requests; a pre-auth request is rejected with auth_error.
func TestAuthSSE_Flow(t *testing.T) {
	ts := newAuthServer(t, ServerOptions{}, tokenHook)
	resp, reader, connID := connectSSE(t, ts)
	defer resp.Body.Close()

	// Pre-auth request is rejected over the stream.
	postRPC(t, ts, connID, "1", "authHandlers.Ping", "[]")
	ev, err := reader.readEvent()
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	if ev.Event != string(TypeAuthError) {
		t.Fatalf("expected auth_error for pre-auth SSE request, got %q", ev.Event)
	}

	// Authenticate.
	postAuthSSE(t, ts, connID, "good:alice")
	ev, err = reader.readEvent()
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	if ev.Event != string(TypeAuthOK) {
		t.Fatalf("expected auth_ok event, got %q data=%q", ev.Event, ev.Data)
	}

	// Now a request works and sees the authenticated identity.
	postRPC(t, ts, connID, "2", "authHandlers.WhoAmI", "[]")
	ev, err = reader.readEvent()
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	if ev.Event != string(TypeResponse) {
		t.Fatalf("expected response event, got %q", ev.Event)
	}
	var msg ResponseMessage
	if err := json.Unmarshal([]byte(ev.Data), &msg); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if m, _ := msg.Result.(map[string]any); m == nil || m["message"] != "alice" {
		t.Fatalf("expected WhoAmI=alice, got %v", msg.Result)
	}
}
