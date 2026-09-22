package aprot

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A failed auth hook keeps the address and the principal provider it set.
//
// aprot does not undo SetUserID or SetPrincipalProvider when the hook fails.
// The hook called them, and undoing that would be aprot deciding what the call
// meant. The contract is on the hook instead: run the checks first, set the
// address and the provider last, once success is certain (#384).
//
// Both failure modes are covered, because the rule does not bend for a panic:
// a recovered panic is still a hook that ran SetUserID.
func TestAuth_FailedHookKeepsWhatItSet(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func()
	}{
		{"error", func() {}},
		{"panic", func() { panic("refresh boom") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var conn *Conn // the server-side conn, captured from the hook

			hook := func(ctx context.Context, c *Conn, token string) error {
				mu.Lock()
				conn = c
				mu.Unlock()

				// The shape this test pins down: set first, then fail. A
				// well-written hook does the opposite, which is why the
				// doc comments say so.
				who, _ := cutAuthToken(token)
				c.SetUserID(who)
				c.SetPrincipalProvider(func(context.Context) (any, error) { return who, nil })

				if token == "good:"+who {
					return nil
				}
				tc.fail()
				return ErrAuthFailed("invalid token")
			}

			registry := NewRegistry()
			registry.Register(&principalHandlers{})
			server := NewServer(registry, ServerOptions{
				Logger: slog.New(slog.NewTextHandler(&syncBuffer{}, nil)),
			})
			server.OnAuth(hook)
			ts := httptest.NewServer(server)
			t.Cleanup(ts.Close)

			ws := connectWSPath(t, ts, "")
			defer ws.Close()

			sendAuth(t, ws, "good:alice")
			if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthOK) {
				t.Fatalf("expected auth_ok, got %q", f.Type)
			}

			// A refresh that sets bob and then fails.
			sendAuth(t, ws, "bad:bob")
			if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthError) {
				t.Fatalf("expected auth_error, got %q", f.Type)
			}

			mu.Lock()
			c := conn
			mu.Unlock()

			// Both stay where the hook put them.
			if who := whoFromSocket(t, ws, "1"); who != "bob" {
				t.Errorf("principal after a failed refresh = %q, want %q — aprot does not undo the hook's SetPrincipalProvider", who, "bob")
			}
			if got := c.UserID(); got != "bob" {
				t.Errorf("UserID after a failed refresh = %q, want %q", got, "bob")
			}
			assertUserIndex(t, server, c, "bob")

			// The session itself is still live and still authenticated: a
			// failed refresh never downgrades a connection (#341).
			sendAuth(t, ws, "good:carol")
			if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthOK) {
				t.Fatalf("expected auth_ok on the good refresh, got %q", f.Type)
			}
			if who := whoFromSocket(t, ws, "2"); who != "carol" {
				t.Errorf("principal after a good refresh = %q, want %q", who, "carol")
			}
			assertUserIndex(t, server, c, "carol")
		})
	}
}

// The documented shape: checks first, set last. A hook written this way has
// nothing half-applied to reason about, on either failure mode.
func TestAuth_HookThatSetsLastLeavesNothingBehind(t *testing.T) {
	var mu sync.Mutex
	var conn *Conn

	registry := NewRegistry()
	registry.Register(&principalHandlers{})
	server := NewServer(registry, ServerOptions{})
	server.OnAuth(func(ctx context.Context, c *Conn, token string) error {
		mu.Lock()
		conn = c
		mu.Unlock()
		who, ok := cutAuthToken(token)
		if !ok {
			return ErrAuthFailed("invalid token")
		}
		c.SetUserID(who)
		c.SetPrincipalProvider(func(context.Context) (any, error) { return who, nil })
		return nil
	})
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	ws := connectWSPath(t, ts, "")
	defer ws.Close()

	sendAuth(t, ws, "nope") // no ":", so the hook fails before it sets anything
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthError) {
		t.Fatalf("expected auth_error, got %q", f.Type)
	}

	mu.Lock()
	c := conn
	mu.Unlock()
	if got := c.UserID(); got != "" {
		t.Errorf("UserID after a failed first auth = %q, want %q", got, "")
	}
	assertUserIndex(t, server, c, "")
}

// cutAuthToken splits "<prefix>:<user>" into the user and whether it parsed.
func cutAuthToken(token string) (string, bool) {
	for i := 0; i < len(token); i++ {
		if token[i] == ':' {
			return token[i+1:], true
		}
	}
	return "", false
}

// whoFromSocket runs principalHandlers.Who over the socket and returns the
// principal the handler saw.
func whoFromSocket(t *testing.T, ws *websocket.Conn, id string) string {
	t.Helper()
	if err := ws.WriteJSON(IncomingMessage{Type: TypeRequest, ID: id, Method: "principalHandlers.Who"}); err != nil {
		t.Fatalf("write request: %v", err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer ws.SetReadDeadline(time.Time{})
	var f principalFrame
	if err := ws.ReadJSON(&f); err != nil {
		t.Fatalf("read response: %v", err)
	}
	if f.Result == nil {
		t.Fatalf("expected a result frame, got type=%q code=%d msg=%q", f.Type, f.Code, f.Message)
	}
	return f.Result.Who
}

// assertUserIndex checks the server's push fan-out index holds conn under
// exactly want (and under nothing else). "" means it must not be indexed at all.
func assertUserIndex(t *testing.T, s *Server, conn *Conn, want string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for user, conns := range s.userConns {
		if _, ok := conns[conn]; !ok {
			continue
		}
		if user != want {
			t.Errorf("connection is indexed under user %q, want %q", user, want)
		}
		return
	}
	if want != "" {
		t.Errorf("connection is not in the user index, want it under %q", want)
	}
}
