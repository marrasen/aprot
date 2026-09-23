package aprot

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A panicking OnAuth hook must not take down the process.
//
// handleAuth runs in the WebSocket read-loop goroutine, which has no recover
// above it, so before #341 a hook panic killed the whole server — this test
// would take the test binary with it rather than fail.
//
// The client sees the same redacted message as any other non-ProtocolError from
// the hook: a panic value can embed a token or a DSN, and the caller is
// unauthenticated by definition.
func TestAuth_HookPanicDoesNotCrashOverWebSocket(t *testing.T) {
	secret := "pq: password authentication failed for host internal-db:5432"
	logBuf := &syncBuffer{}
	hook := func(ctx context.Context, conn *Conn, token string) error {
		panic(secret)
	}
	ts := newAuthServer(t, ServerOptions{
		Logger: slog.New(slog.NewTextHandler(logBuf, nil)),
	}, hook)
	ws := connectWSPath(t, ts, "/ws")
	defer ws.Close()

	sendAuth(t, ws, "whatever")
	f := readFrame(t, ws, 3*time.Second)
	if f.Type != string(TypeAuthError) {
		t.Fatalf("expected auth_error after a hook panic, got type=%q", f.Type)
	}
	if f.Message != "authentication failed" {
		t.Errorf("message = %q, want %q", f.Message, "authentication failed")
	}
	if strings.Contains(f.Message, "internal-db") {
		t.Errorf("panic value leaked to the client: %q", f.Message)
	}

	// The value and stack reach the log, exactly as for every other recover.
	logged := logBuf.String()
	if !strings.Contains(logged, "internal-db") {
		t.Errorf("panic value missing from the log; got %q", logged)
	}
	if !strings.Contains(logged, "OnAuth") {
		t.Errorf("log line does not name the OnAuth hook; got %q", logged)
	}
	if !strings.Contains(logged, "stack") {
		t.Errorf("stack missing from the log; got %q", logged)
	}
}

// A hook panic while already authenticated is a failed refresh, so it keeps the
// live session — same as any other refresh failure. A panic must not downgrade
// a connection that is already authenticated.
func TestAuth_HookPanicOnRefreshKeepsSession(t *testing.T) {
	// atomic: written here, read on the server read-loop goroutine. A socket
	// write between them is not a happens-before edge, and CI runs -race.
	var panicNow atomic.Bool
	hook := func(ctx context.Context, conn *Conn, token string) error {
		if panicNow.Load() {
			panic("refresh boom")
		}
		conn.SetUserID("alice")
		return nil
	}
	ts := newAuthServer(t, ServerOptions{
		Logger: slog.New(slog.NewTextHandler(&syncBuffer{}, nil)),
	}, hook)
	ws := connectWSPath(t, ts, "/ws")
	defer ws.Close()

	sendAuth(t, ws, "good:alice")
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthOK) {
		t.Fatalf("expected auth_ok, got %q", f.Type)
	}

	// A panicking refresh reports the failure but leaves the session up.
	panicNow.Store(true)
	sendAuth(t, ws, "refresh")
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthError) {
		t.Fatalf("expected auth_error for the panicking refresh, got %q", f.Type)
	}

	// The connection is still usable — it was not closed.
	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "1", Method: "authHandlers.WhoAmI"}); err != nil {
		t.Fatalf("write after panicking refresh: %v", err)
	}
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeResponse) {
		t.Errorf("session was dropped by a panicking refresh; got frame type %q", f.Type)
	}
}

// streamCompleteHandlers provides a streaming handler whose OnStreamComplete
// hook can be made to panic.
type panicHookHandlers struct{}

func (panicHookHandlers) Count(ctx context.Context) (func(func(int) bool), error) {
	return func(yield func(int) bool) {
		for i := range 3 {
			if !yield(i) {
				return
			}
		}
	}, nil
}

// An OnStreamComplete hook that panics must have its value and stack logged.
// This was the one recover site doing a bare `_ = recover()`, so a broken hook
// left no trace at all (#341). The stream's own outcome is already decided by
// the time hooks run, so the log line is the entire signal.
func TestStreamCompleteHookPanicIsLogged(t *testing.T) {
	logBuf := &syncBuffer{}
	registry := NewRegistry()
	registry.Register(&panicHookHandlers{})
	server := NewServer(registry, ServerOptions{
		Logger: slog.New(slog.NewTextHandler(logBuf, nil)),
	})

	// Middleware registers a hook that panics once the stream finishes, plus a
	// later hook that must still run.
	secondRan := make(chan struct{}, 1)
	server.Use(func(next Handler) Handler {
		return func(ctx context.Context, req *Request) (any, error) {
			OnStreamComplete(ctx, func(err error, items int) {
				panic("hook boom")
			})
			OnStreamComplete(ctx, func(err error, items int) {
				select {
				case secondRan <- struct{}{}:
				default:
				}
			})
			return next(ctx, req)
		}
	})

	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	ws := connectWS(t, ts)
	defer ws.Close()

	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: "s1", Method: "panicHookHandlers.Count"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	readMessageOfType(t, ws, TypeStreamEnd, 3*time.Second)

	// A panic in one hook does not prevent later hooks from running.
	select {
	case <-secondRan:
	case <-time.After(3 * time.Second):
		t.Fatal("a panicking hook stopped later hooks from running")
	}

	eventually(t, 3*time.Second, func() bool {
		return strings.Contains(logBuf.String(), "hook boom")
	})
	logged := logBuf.String()
	if !strings.Contains(logged, "OnStreamComplete hook panicked") {
		t.Errorf("log line does not identify the hook; got %q", logged)
	}
	if !strings.Contains(logged, "panicHookHandlers.Count") {
		t.Errorf("log line does not name the method; got %q", logged)
	}
	if !strings.Contains(logged, "stack") {
		t.Errorf("stack missing from the log; got %q", logged)
	}
}
