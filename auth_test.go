package aprot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	mux := http.NewServeMux()
	mux.Handle("/ws", server)
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

// A raw (non-ProtocolError) error from the auth hook is redacted before being
// sent to the client, so internal detail can't leak at the auth boundary.
func TestAuth_RawHookErrorRedacted(t *testing.T) {
	secret := "pq: password authentication failed for host internal-db:5432"
	hook := func(ctx context.Context, conn *Conn, token string) error {
		return errors.New(secret)
	}
	ts := newAuthServer(t, ServerOptions{}, hook)
	ws := connectWSPath(t, ts, "/ws")
	defer ws.Close()

	sendAuth(t, ws, "whatever")
	f := readFrame(t, ws, 3*time.Second)
	if f.Type != string(TypeAuthError) {
		t.Fatalf("expected auth_error, got type=%q", f.Type)
	}
	if strings.Contains(f.Message, "internal-db") || f.Message != "authentication failed" {
		t.Errorf("raw hook error leaked to client: %q", f.Message)
	}
}

// PushToUser must not deliver a user's push to a connection that has since
// re-authenticated as a different user (the SetUserID / userConns transition is
// not atomic, so the snapshot can be momentarily stale). Regression for the
// identity race flagged in review.
func TestPushToUser_SkipsReauthenticatedConnection(t *testing.T) {
	registry := NewRegistry()
	h := &IntegrationHandlers{}
	registry.Register(h)
	registry.RegisterPushEventFor(h, NotificationEvent{})
	server := NewServer(registry)
	t.Cleanup(func() { _ = server.Stop(context.Background()) })

	var sends int32
	conn := &Conn{
		id:        1,
		server:    server,
		transport: &mockTransport{onSend: func([]byte) { atomic.AddInt32(&sends, 1) }},
	}
	conn.userID = "bob" // already re-authenticated as bob

	// Momentarily-stale index: conn is still listed under alice.
	server.userConns["alice"] = map[*Conn]struct{}{conn: {}}

	server.PushToUser("alice", NotificationEvent{Message: "for-alice"})
	if n := atomic.LoadInt32(&sends); n != 0 {
		t.Fatalf("alice's push was delivered to a connection now identified as bob (%d sends)", n)
	}

	// Sanity: a matching identity still receives the push.
	conn.userID = "alice"
	server.PushToUser("alice", NotificationEvent{Message: "for-alice"})
	if n := atomic.LoadInt32(&sends); n != 1 {
		t.Fatalf("expected exactly one send to alice's connection, got %d", n)
	}
}

// DisconnectUser must not close a connection that has since re-authenticated
// as a different user — the same momentarily-stale-snapshot identity race
// that PushToUser guards against.
func TestDisconnectUser_SkipsReauthenticatedConnection(t *testing.T) {
	server := NewServer(NewRegistry())
	t.Cleanup(func() { _ = server.Stop(context.Background()) })

	conn := &Conn{
		id:        1,
		server:    server,
		transport: &mockTransport{},
		requests:  make(map[string]inflight),
	}
	conn.userID = "bob" // already re-authenticated as bob

	// Momentarily-stale index: conn is still listed under alice.
	server.userConns["alice"] = map[*Conn]struct{}{conn: {}}

	if n := server.DisconnectUser("alice"); n != 0 {
		t.Fatalf("DisconnectUser(alice) = %d, want 0 (conn now belongs to bob)", n)
	}
	if conn.isClosed() {
		t.Fatal("alice's disconnect closed a connection now identified as bob")
	}

	// Sanity: a matching identity is closed and counted exactly once.
	conn.userID = "alice"
	if n := server.DisconnectUser("alice"); n != 1 {
		t.Fatalf("DisconnectUser(alice) = %d, want 1", n)
	}
	if !conn.isClosed() {
		t.Fatal("matching connection was not closed")
	}
	if n := server.DisconnectUser("alice"); n != 0 {
		t.Fatalf("repeat DisconnectUser(alice) = %d, want 0 (already closed)", n)
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

// --- WebSocket: AllowAnonymous ---

// anonymousOptions enables anonymous connections with an auth timeout short
// enough that any accidental arming shows up as a closed connection.
func anonymousOptions() ServerOptions {
	return ServerOptions{AllowAnonymous: true, AuthTimeout: 150 * time.Millisecond}
}

// With AllowAnonymous, a connection that never authenticates may issue requests
// and is seen by handlers as an anonymous caller (empty user ID).
func TestAuthAnonymous_RequestSucceedsWithEmptyUserID(t *testing.T) {
	ts := newAuthServer(t, anonymousOptions(), tokenHook)
	ws := connectWSPath(t, ts, "/ws")
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "authHandlers.WhoAmI"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	f := readFrame(t, ws, 3*time.Second)
	if f.Type != string(TypeResponse) {
		t.Fatalf("expected an anonymous request to succeed, got type=%q message=%q", f.Type, f.Message)
	}
	if f.Result == nil || f.Result.Message != "" {
		t.Fatalf("expected empty user ID for an anonymous caller, got %+v", f.Result)
	}
}

// No auth timeout is armed for anonymous connections: the connection outlives
// AuthTimeout and keeps serving requests.
func TestAuthAnonymous_SurvivesPastTimeout(t *testing.T) {
	ts := newAuthServer(t, anonymousOptions(), tokenHook)
	ws := connectWSPath(t, ts, "/ws")
	defer ws.Close()

	time.Sleep(450 * time.Millisecond) // 3x AuthTimeout

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "authHandlers.Ping"}); err != nil {
		t.Fatalf("write after the auth timeout window: %v", err)
	}
	// Any armed timeout would have sent auth_error and closed the connection
	// before this response.
	f := readFrame(t, ws, 3*time.Second)
	if f.Type != string(TypeResponse) || f.Result == nil || f.Result.Message != "pong" {
		t.Fatalf("expected the anonymous connection to outlive AuthTimeout, got type=%q message=%q", f.Type, f.Message)
	}
}

// An anonymous connection can authenticate later, upgrading the live session's
// identity in place.
func TestAuthAnonymous_LateAuthUpgradesIdentity(t *testing.T) {
	ts := newAuthServer(t, anonymousOptions(), tokenHook)
	ws := connectWSPath(t, ts, "/ws")
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "authHandlers.WhoAmI"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if f := readFrame(t, ws, 3*time.Second); f.Result == nil || f.Result.Message != "" {
		t.Fatalf("expected anonymous identity before auth, got %+v", f.Result)
	}

	sendAuth(t, ws, "good:alice")
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthOK) {
		t.Fatalf("expected auth_ok on late auth, got type=%q message=%q", f.Type, f.Message)
	}

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "2", Method: "authHandlers.WhoAmI"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if f := readFrame(t, ws, 3*time.Second); f.Result == nil || f.Result.Message != "alice" {
		t.Fatalf("expected identity upgraded to alice, got %+v", f.Result)
	}
}

// A token that is offered and rejected still closes an unauthenticated
// connection under AllowAnonymous: a bad credential must not silently degrade
// into a working anonymous session.
func TestAuthAnonymous_FailedAuthClosesConnection(t *testing.T) {
	ts := newAuthServer(t, anonymousOptions(), tokenHook)
	ws := connectWSPath(t, ts, "/ws")
	defer ws.Close()

	sendAuth(t, ws, "nope")
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthError) {
		t.Fatalf("expected auth_error for a bad token, got type=%q", f.Type)
	}
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	var f wsFrame
	if err := ws.ReadJSON(&f); err == nil {
		t.Fatalf("expected the connection to close after a failed auth, but read succeeded: %+v", f)
	}
}

// The keep-the-session rule for a failed refresh keys on the authenticated
// state, not on the mode: once authenticated, a bad refresh is not a downgrade.
func TestAuthAnonymous_FailedRefreshKeepsAuthenticatedSession(t *testing.T) {
	ts := newAuthServer(t, anonymousOptions(), tokenHook)
	ws := connectWSPath(t, ts, "/ws")
	defer ws.Close()

	sendAuth(t, ws, "good:alice")
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthOK) {
		t.Fatalf("expected auth_ok, got type=%q", f.Type)
	}

	sendAuth(t, ws, "nope")
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthError) {
		t.Fatalf("expected auth_error on a bad refresh, got type=%q", f.Type)
	}

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "authHandlers.WhoAmI"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	f := readFrame(t, ws, 3*time.Second)
	if f.Type != string(TypeResponse) || f.Result == nil || f.Result.Message != "alice" {
		t.Fatalf("expected the session kept as alice after a failed refresh, got type=%q result=%+v", f.Type, f.Result)
	}
}

// AllowAnonymous without an auth hook is a no-op: connections already work, and
// an auth frame still gets the no-op auth_ok.
func TestAuthAnonymous_NoHook(t *testing.T) {
	ts := newAuthServer(t, anonymousOptions(), nil)
	ws := connectWSPath(t, ts, "/ws")
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "authHandlers.Ping"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeResponse) {
		t.Fatalf("expected a response with no hook registered, got type=%q", f.Type)
	}

	sendAuth(t, ws, "anything")
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthOK) {
		t.Fatalf("expected the no-op auth_ok with no hook registered, got type=%q", f.Type)
	}
}
