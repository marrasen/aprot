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

// A failed auth hook puts back the address and the principal provider it set.
//
// OnAuth is where consumers register the principal provider and set the
// address. A hook that sets either and then fails used to leave both applied
// while the client was told the token was rejected: every later execution
// resolved the new principal, and PushToUser delivered to the new address
// (#384). Both failure modes are covered — the client is told the same thing
// for an error and for a panic.
func TestAuth_FailedRefreshRollsBackIdentity(t *testing.T) {
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

				// Every call sets the address and the provider first, then
				// decides. That ordering is the point: both are applied by
				// the time the failure happens.
				who, _ := cutAuthToken(token)
				c.SetUserID(who)
				c.SetPrincipalProvider(func(context.Context) (any, error) { return who, nil })

				if token == "good:"+who {
					return nil
				}
				tc.fail()
				return ErrAuthFailed("invalid token")
			}

			h := &principalHandlers{}
			registry := NewRegistry()
			registry.Register(h)
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

			// The authorization input is back to what it was.
			if who := whoFromSocket(t, ws, "1"); who != "alice" {
				t.Errorf("principal after failed refresh = %q, want %q — the rejected identity is still resolving", who, "alice")
			}
			// So is the push-routing address, in the connection and in the
			// server's fan-out index, which SetUserID maintains separately.
			if got := c.UserID(); got != "alice" {
				t.Errorf("UserID after failed refresh = %q, want %q", got, "alice")
			}
			assertUserIndex(t, server, c, "alice")

			// Failure path only: a good refresh still moves both.
			sendAuth(t, ws, "good:carol")
			if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthOK) {
				t.Fatalf("expected auth_ok on the good refresh, got %q", f.Type)
			}
			if who := whoFromSocket(t, ws, "2"); who != "carol" {
				t.Errorf("principal after a good refresh = %q, want %q", who, "carol")
			}
			if got := c.UserID(); got != "carol" {
				t.Errorf("UserID after a good refresh = %q, want %q", got, "carol")
			}
			assertUserIndex(t, server, c, "carol")
		})
	}
}

// A hook that fails on the *first* auth puts both back too, leaving the
// anonymous state it started from. The connection is closed either way, so this
// covers the window before the close lands: disassociateUser reads UserID to
// clear the fan-out index, and must not find an address the hook never earned.
func TestAuth_FailedFirstAuthRollsBackToAnonymous(t *testing.T) {
	var mu sync.Mutex
	var conn *Conn

	registry := NewRegistry()
	registry.Register(&principalHandlers{})
	server := NewServer(registry, ServerOptions{})
	server.OnAuth(func(ctx context.Context, c *Conn, token string) error {
		mu.Lock()
		conn = c
		mu.Unlock()
		c.SetUserID("bob")
		c.SetPrincipalProvider(func(context.Context) (any, error) { return "bob", nil })
		return ErrAuthFailed("invalid token")
	})
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	ws := connectWSPath(t, ts, "")
	defer ws.Close()

	sendAuth(t, ws, "nope")
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

// Only the two fields aprot owns are put back. A hook that stashed a connection
// value before failing keeps it — aprot does not know what it meant. Documented
// in doc.go and README, and asserted here so the boundary does not drift.
func TestAuth_RollbackLeavesConnValuesAlone(t *testing.T) {
	type stash struct{ note string }
	var mu sync.Mutex
	var conn *Conn

	registry := NewRegistry()
	registry.Register(&principalHandlers{})
	server := NewServer(registry, ServerOptions{})
	server.OnAuth(func(ctx context.Context, c *Conn, token string) error {
		mu.Lock()
		conn = c
		mu.Unlock()
		c.Set("note", stash{note: "written before failing"})
		c.SetUserID("bob")
		return ErrAuthFailed("invalid token")
	})
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	ws := connectWSPath(t, ts, "")
	defer ws.Close()

	sendAuth(t, ws, "nope")
	if f := readFrame(t, ws, 3*time.Second); f.Type != string(TypeAuthError) {
		t.Fatalf("expected auth_error, got %q", f.Type)
	}

	mu.Lock()
	c := conn
	mu.Unlock()
	if got := c.UserID(); got != "" {
		t.Errorf("UserID = %q, want it put back to %q", got, "")
	}
	v, ok := c.Load("note")
	if got, isStash := v.(stash); !ok || !isStash || got.note != "written before failing" {
		t.Errorf("Load(\"note\") = (%+v, %v), want the hook's write left in place", v, ok)
	}
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
