package aprot

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

// WithTestConnection returns a context carrying a minimal [Conn] with the
// given ID. The connection has no functioning transport and is intended
// exclusively for use in tests.
func WithTestConnection(ctx context.Context, id uint64) context.Context {
	return withConnection(ctx, &Conn{id: id})
}

// WithTestConnectionUser returns a context carrying a minimal [Conn] with the
// given connection ID and authenticated user ID. Unlike calling SetUserID, it
// needs no server, so it is usable for tests that exercise user-based
// ownership. Test-only.
func WithTestConnectionUser(ctx context.Context, id uint64, userID string) context.Context {
	return withConnection(ctx, &Conn{id: id, userID: userID})
}

// recordingTransport captures all data sent through the connection.
type recordingTransport struct {
	mu   sync.Mutex
	data [][]byte
}

func (t *recordingTransport) Send(data []byte) error {
	t.mu.Lock()
	cp := make([]byte, len(data))
	copy(cp, data)
	t.data = append(t.data, cp)
	t.mu.Unlock()
	return nil
}

func (t *recordingTransport) SendCtx(ctx context.Context, data []byte) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return t.Send(data)
}

func (t *recordingTransport) SupportsBinary() bool { return true }

func (t *recordingTransport) SendBinary(data []byte) error {
	return t.Send(data)
}

func (t *recordingTransport) SendBinaryCtx(ctx context.Context, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return t.SendBinary(data)
}

func (t *recordingTransport) Close() error           { return nil }
func (t *recordingTransport) CloseGracefully() error { return nil }

func (t *recordingTransport) Messages() [][]byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([][]byte, len(t.data))
	copy(out, t.data)
	return out
}

// TestPushConn is a test-only [Conn] that records all messages sent via Push.
// Use [NewTestPushConn] to create one.
type TestPushConn struct {
	Conn      *Conn
	transport *recordingTransport
}

// Messages returns all raw JSON messages sent through the connection.
func (tc *TestPushConn) Messages() [][]byte {
	return tc.transport.Messages()
}

// NewTestPushConn creates a [Conn] backed by a recording transport and a
// [Server] whose registry has the given push events registered. This allows
// [Conn.Push] to work in tests. The returned [TestPushConn] provides access
// to captured messages.
func NewTestPushConn(id uint64, pushEvents ...any) *TestPushConn {
	registry := NewRegistry()
	// We need a handler to register push events against.
	handler := &testPushHandler{}
	registry.Register(handler)
	for _, ev := range pushEvents {
		registry.RegisterPushEventFor(handler, ev)
	}
	server := NewServer(registry)
	rt := &recordingTransport{}
	conn := &Conn{
		transport: rt,
		server:    server,
		requests:  make(map[string]context.CancelCauseFunc),
		id:        id,
	}
	return &TestPushConn{Conn: conn, transport: rt}
}

// WithTestPushConn returns a context carrying the conn from tc.
func (tc *TestPushConn) WithContext(ctx context.Context) context.Context {
	return withConnection(ctx, tc.Conn)
}

// testPushHandler is a minimal handler for registering push events in tests.
type testPushHandler struct{}

func (h *testPushHandler) Ping(_ context.Context) error { return nil }

// TestSubscriber is a test-only subscriber: a transport-less connection that
// has run a real subscribe flow against a [Server], recording every frame
// the server sends it. Use it to assert that mutations on other transports
// (REST, MCP, [Server.Invoke]) refresh subscribed clients. Test-only.
type TestSubscriber struct {
	server *Server
	rt     *recordingTransport
	base   int // frames recorded up to and including the subscribe response
}

// NewTestSubscriber subscribes to method (wire name, e.g. "Todos.List") on a
// fresh recording connection and returns the subscriber. It fails the test
// if the subscribe itself is rejected.
func NewTestSubscriber(t testing.TB, s *Server, method string) *TestSubscriber {
	t.Helper()
	rt := &recordingTransport{}
	c := newConn(rt, s, atomic.AddUint64(&s.nextConnID, 1), ConnInfo{}, context.Background())
	c.authenticated.Store(true)
	s.requestsWg.Add(1)
	c.handleSubscribe(IncomingMessage{Type: TypeSubscribe, ID: "test-sub", Method: method})

	msgs := rt.Messages()
	if len(msgs) == 0 {
		t.Fatalf("subscribe to %s produced no response", method)
	}
	var frame struct {
		Type  string `json:"type"`
		Error any    `json:"error"`
	}
	if err := unmarshalJSON(msgs[len(msgs)-1], &frame); err != nil || frame.Type != "response" {
		t.Fatalf("subscribe to %s failed: %s", method, msgs[len(msgs)-1])
	}
	return &TestSubscriber{server: s, rt: rt, base: len(msgs)}
}

// WaitFrames waits for in-flight refreshes to settle and returns the frames
// received after the initial subscribe response, decoded into generic maps.
func (ts *TestSubscriber) WaitFrames(t testing.TB) []map[string]any {
	t.Helper()
	ts.server.requestsWg.Wait()
	raw := ts.rt.Messages()[ts.base:]
	out := make([]map[string]any, 0, len(raw))
	for _, b := range raw {
		var m map[string]any
		if err := unmarshalJSON(b, &m); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		out = append(out, m)
	}
	return out
}
