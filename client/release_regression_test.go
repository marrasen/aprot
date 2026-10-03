package client_test

import (
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/client"
)

func countStacks(substr string) int {
	buf := make([]byte, 1<<22)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), substr)
}

func userIDSetup(s *aprot.Server) {
	s.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
		conn.SetUserID("u1")
		return nil
	})
}

// Every dropped WebSocket connection leaves its pingLoop goroutine (and its
// socket) behind: run/markDisconnected never call conn.close(), and only
// close() stops the ping loop. They survive even Client.Close.
func TestReleasePingLoopLeaksPerDrop(t *testing.T) {
	f := newReviewFixture(t, aprot.ServerOptions{ReconnectInterval: 5, ReconnectMaxInterval: 5}, userIDSetup)
	var drops atomic.Int32
	c := f.dial(t, client.Options{PingInterval: 20 * time.Millisecond, OnStateChange: dropCounter(&drops)})
	const n = 5
	for i := 1; i <= n; i++ {
		f.srv.DisconnectUser("u1")
		if !waitFor(3*time.Second, func() bool { return drops.Load() == int32(i) && c.State() == client.StateConnected }) {
			t.Fatalf("drop %d: no reconnect", i)
		}
	}
	_ = c.Close()
	<-c.Done()
	time.Sleep(200 * time.Millisecond)
	if got := countStacks("(*wsConn).pingLoop"); got != 0 {
		t.Fatalf("%d pingLoop goroutines still running after %d drops and Client.Close", got, n)
	}
}

// The server's pending-auth timeout sends an id-less auth_error. When
// AuthToken is slower than the server's AuthTimeout on a reconnect, the
// client takes it as "the server requires auth and you sent none" and shuts
// down for good, even with ReconnectOnRejected set. Before 9177df6 it just
// retried, and the TS client ignores an auth_error that arrives before its
// auth frame went out.
func TestReleaseSlowAuthTokenKillsClient(t *testing.T) {
	f := newReviewFixture(t, aprot.ServerOptions{AuthTimeout: 200 * time.Millisecond, ReconnectInterval: 5, ReconnectMaxInterval: 5}, strictAuth("ok"))
	var calls atomic.Int32
	c := f.dial(t, client.Options{
		ConnectTimeout:      5 * time.Second,
		ReconnectOnRejected: &client.RejectedRetry{Delay: 10 * time.Millisecond},
		AuthToken: func(ctx context.Context) (string, error) {
			if calls.Add(1) == 2 {
				time.Sleep(400 * time.Millisecond) // slow IdP on one reconnect
			}
			return "ok", nil
		},
	})
	f.srv.DisconnectUser("u1")
	time.Sleep(1500 * time.Millisecond)
	if err := c.Err(); err != nil {
		t.Fatalf("one slow AuthToken stopped the client for good: %v", err)
	}
	if c.State() != client.StateConnected {
		t.Fatalf("state = %v, want connected", c.State())
	}
}

// Outbound boundary: every frame the client lets through must be accepted
// by the server, and the first refused one must be exactly limit+1.
func TestReleaseOutboundBoundary(t *testing.T) {
	for _, transport := range []string{"websocket", "stream"} {
		t.Run(transport, func(t *testing.T) {
			const limit = 1024
			f := newReviewFixture(t, aprot.ServerOptions{MaxMessageSize: limit})
			var drops atomic.Int32
			opts := client.Options{OnStateChange: dropCounter(&drops), NoReconnect: true}
			var c *client.Client
			if transport == "websocket" {
				c = f.dial(t, opts)
			} else {
				var err error
				c, err = client.DialStream(testCtx(t), streamDial(f.srv), opts)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
			}
			okMax := 0
			for k := limit - 120; k <= limit; k++ {
				_, err := client.Call[string](testCtx(t), c, "ReviewHandlers.Echo", []any{strings.Repeat("a", k)})
				if errors.Is(err, client.ErrMessageTooLarge) {
					if !strings.Contains(err.Error(), "is 1025 bytes") {
						t.Fatalf("first refusal = %v, want size 1025", err)
					}
					break
				}
				if err != nil {
					t.Fatalf("k=%d: %v", k, err)
				}
				okMax = k
			}
			if okMax == 0 || drops.Load() != 0 || c.Err() != nil {
				t.Fatalf("okMax=%d drops=%d err=%v", okMax, drops.Load(), c.Err())
			}
		})
	}
}

// Oversized subscriptions must give back their slots: after 200 refused
// ones, 70 normal subscriptions all get their first result.
func TestReleaseOversizedSubscribeSlots(t *testing.T) {
	f := newReviewFixture(t, aprot.ServerOptions{MaxMessageSize: 1024})
	c := f.dial(t, client.Options{})
	big := strings.Repeat("y", 2000)
	var bad []*client.Subscription[string]
	for range 200 {
		bad = append(bad, client.Subscribe[string](testCtx(t), c, "ReviewHandlers.Echo", []any{big}))
	}
	for _, s := range bad {
		waitClosed(t, s.C)
		if !errors.Is(s.Err(), client.ErrMessageTooLarge) {
			t.Fatalf("err = %v", s.Err())
		}
	}
	for i := range 70 {
		s := client.Subscribe[string](testCtx(t), c, "ReviewHandlers.Echo", []any{"ok"})
		defer s.Close()
		select {
		case <-s.C:
		case <-time.After(3 * time.Second):
			t.Fatalf("subscription %d never answered: slot leak", i)
		}
	}
}

// DialStream's MaxMessageSize: a line of exactly MaxMessageSize bytes is
// refused (the scanner needs room for the newline), unlike the server,
// which this PR fixed to accept exactly the limit.
func TestReleaseDialStreamInboundExactLimit(t *testing.T) {
	const limit = 1000
	a, b := net.Pipe()
	go func() {
		_, _ = io.WriteString(b, `{"type":"config"}`+"\n")
		// A push frame of exactly limit bytes.
		head := `{"type":"push","event":"e","data":"`
		tail := `"}`
		line := head + strings.Repeat("z", limit-len(head)-len(tail)) + tail
		if len(line) != limit {
			panic("bad size")
		}
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(b, line+"\n")
		buf := make([]byte, 4096)
		for {
			if _, err := b.Read(buf); err != nil {
				return
			}
		}
	}()
	c, err := client.DialStream(testCtx(t), func(context.Context) (io.ReadWriteCloser, error) { return a, nil },
		client.Options{MaxMessageSize: limit, NoReconnect: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got := make(chan string, 1)
	client.OnPush(c, "e", func(s string) { got <- s })
	select {
	case <-got:
	case <-c.Done():
		t.Fatalf("a %d-byte message with MaxMessageSize %d broke the connection: %v", limit, limit, c.Err())
	case <-time.After(2 * time.Second):
		t.Fatal("no push")
	}
}

// A refresh that timed out still has a verdict on the way. The next
// refresh takes it as its own: a good token is reported as rejected.
// (Pre-existing; the new authSent counter could tell the two apart.)
func TestReleaseRefreshGetsPreviousVerdict(t *testing.T) {
	f := newReviewFixture(t, aprot.ServerOptions{}, strictAuth("ok"))
	c := f.dial(t, client.Options{AuthToken: func(context.Context) (string, error) { return "ok", nil }})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.RefreshAuth(ctx, "bad"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RefreshAuth = %v", err)
	}
	if err := c.RefreshAuth(testCtx(t), "ok"); err != nil {
		t.Fatalf("RefreshAuth with a good token = %v", err)
	}
}

// LastRejection keeps the rejection after a Close during the retry delay, so
// it still explains why the session ended (as the TypeScript client's
// getLastRejection does).
func TestReleaseLastRejectionAfterClose(t *testing.T) {
	var fc failingConnect
	f := newReviewFixture(t, aprot.ServerOptions{ReconnectInterval: 5, ReconnectMaxInterval: 5}, fc.setup)
	c := f.dial(t, client.Options{ReconnectOnRejected: &client.RejectedRetry{Delay: time.Hour}})
	fc.fails.Store(-1)
	f.srv.DisconnectUser("u1")
	if !waitFor(3*time.Second, func() bool { return c.LastRejection() != nil }) {
		t.Fatal("no rejection")
	}
	_ = c.Close()
	<-c.Done()
	if r := c.LastRejection(); r == nil {
		t.Fatal("LastRejection after Close = nil, want the rejection")
	}
}
