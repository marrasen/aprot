package aprot

import (
	"context"
	"sync"
)

// WithTestConnection returns a context carrying a minimal [Conn] with the
// given ID. The connection has no functioning transport and is intended
// exclusively for use in tests.
func WithTestConnection(ctx context.Context, id uint64) context.Context {
	return withConnection(ctx, &Conn{id: id})
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

func (t *recordingTransport) Close() error            { return nil }
func (t *recordingTransport) CloseGracefully() error   { return nil }

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
		requests:  make(map[string]context.CancelFunc),
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
