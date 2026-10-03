package client_test

// Regression tests for defects found in the pre-release review of the Go
// client. Each runs against a real aprot.Server.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"iter"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"encoding/json/jsontext"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/client"
)

type ReviewHandlers struct {
	counter atomic.Int64
}

func (h *ReviewHandlers) Echo(ctx context.Context, s string) (string, error) {
	aprot.RegisterRefreshTrigger(ctx, "echo")
	return s, nil
}

func (h *ReviewHandlers) Big(ctx context.Context, n int) (string, error) {
	return strings.Repeat("x", n), nil
}

func (h *ReviewHandlers) Count(ctx context.Context) (int64, error) {
	aprot.RegisterRefreshTrigger(ctx, "count")
	return h.counter.Load(), nil
}

func (h *ReviewHandlers) Items(ctx context.Context, s string) (iter.Seq[string], error) {
	return func(yield func(string) bool) { yield(s) }, nil
}

type reviewFixture struct {
	srv *aprot.Server
	h   *ReviewHandlers
	url string
	ln  string // host:port of the HTTP listener
}

func newReviewFixture(t *testing.T, opts aprot.ServerOptions, setup ...func(*aprot.Server)) *reviewFixture {
	t.Helper()
	h := &ReviewHandlers{}
	reg := aprot.NewRegistry()
	reg.Register(h)
	srv := aprot.NewServer(reg, opts)
	for _, f := range setup {
		f(srv)
	}
	hs := httptest.NewServer(srv)
	t.Cleanup(func() {
		hs.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Stop(ctx)
	})
	return &reviewFixture{srv: srv, h: h, url: "ws" + strings.TrimPrefix(hs.URL, "http"), ln: hs.Listener.Addr().String()}
}

func (f *reviewFixture) dial(t *testing.T, opts client.Options) *client.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, f.url, opts)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// streamDial returns a DialStream dial func that serves each connection with
// srv.ServeStream over an in-memory pipe.
func streamDial(srv *aprot.Server) func(context.Context) (io.ReadWriteCloser, error) {
	return func(context.Context) (io.ReadWriteCloser, error) {
		a, b := net.Pipe()
		go srv.ServeStream(context.Background(), b, aprot.ConnInfo{})
		return a, nil
	}
}

// dropCounter counts transitions to StateConnecting: connection drops.
func dropCounter(n *atomic.Int32) func(client.State) {
	return func(s client.State) {
		if s == client.StateConnecting {
			n.Add(1)
		}
	}
}

// waitFor polls cond until it holds or d passes.
func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// A frame larger than the server's MaxMessageSize makes the server close the
// connection. The client used to re-send an oversized subscription on every
// reconnect, so one subscription kept the client reconnecting forever and
// broke every other call. It must refuse such frames locally instead.
func TestOversizedSubscribeLoop(t *testing.T) {
	big := strings.Repeat("y", 2000)
	for _, transport := range []string{"websocket", "stream"} {
		t.Run(transport, func(t *testing.T) {
			f := newReviewFixture(t, aprot.ServerOptions{MaxMessageSize: 1024, ReconnectInterval: 10, ReconnectMaxInterval: 10})
			var drops atomic.Int32
			opts := client.Options{OnStateChange: dropCounter(&drops)}
			var c *client.Client
			if transport == "websocket" {
				c = f.dial(t, opts)
			} else {
				var err error
				c, err = client.DialStream(testCtx(t), streamDial(f.srv), opts)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = c.Close() })
			}
			ctx := testCtx(t)

			small := client.Subscribe[string](ctx, c, "ReviewHandlers.Echo", []any{"small"})
			defer small.Close()
			if got := recv(t, small.C); got != "small" {
				t.Fatalf("small subscription got %q", got)
			}

			sub := client.Subscribe[string](ctx, c, "ReviewHandlers.Echo", []any{big})
			defer sub.Close()
			waitClosed(t, sub.C)
			if err := sub.Err(); !errors.Is(err, client.ErrMessageTooLarge) || !strings.Contains(err.Error(), "1024") {
				t.Fatalf("oversized subscription Err = %v, want ErrMessageTooLarge naming the 1024-byte limit", err)
			}

			if _, err := client.Call[string](ctx, c, "ReviewHandlers.Echo", []any{big}); !errors.Is(err, client.ErrMessageTooLarge) {
				t.Fatalf("oversized Call = %v, want ErrMessageTooLarge", err)
			}
			st := client.Stream[string](ctx, c, "ReviewHandlers.Items", []any{big})
			for range st.All() {
				t.Fatal("oversized stream yielded an item")
			}
			if err := st.Err(); !errors.Is(err, client.ErrMessageTooLarge) {
				t.Fatalf("oversized Stream Err = %v, want ErrMessageTooLarge", err)
			}

			// The connection stayed up: unrelated calls work, and the small
			// subscription still gets refreshes.
			for i := 0; i < 5; i++ {
				if got, err := client.Call[string](ctx, c, "ReviewHandlers.Echo", []any{"ok"}); err != nil || got != "ok" {
					t.Fatalf("small call %d = %q, %v", i, got, err)
				}
			}
			f.srv.TriggerRefresh("echo")
			recv(t, small.C)
			time.Sleep(50 * time.Millisecond)
			if n := drops.Load(); n != 0 {
				t.Fatalf("connection dropped %d times; an oversized frame must not reach the server", n)
			}
		})
	}
}

func strictAuth(token string) func(*aprot.Server) {
	return func(s *aprot.Server) {
		s.OnAuth(func(ctx context.Context, conn *aprot.Conn, tok string) error {
			if tok != token {
				time.Sleep(100 * time.Millisecond) // a verdict that arrives late
				return aprot.ErrAuthFailed("bad token")
			}
			conn.SetUserID("u1")
			return nil
		})
	}
}

// A server that requires auth answers every frame from a client without a
// token with an auth_error that carries no request ID. The client used to
// drop it, so calls hung until the server's AuthTimeout closed the
// connection, and then it reconnected into the same state forever.
func TestStrictAuthNoToken(t *testing.T) {
	f := newReviewFixture(t, aprot.ServerOptions{AuthTimeout: 30 * time.Second}, strictAuth("ok"))
	var drops atomic.Int32
	c := f.dial(t, client.Options{OnStateChange: dropCounter(&drops)})
	sub := client.Subscribe[int64](context.Background(), c, "ReviewHandlers.Count", nil)
	defer sub.Close()

	var err error
	if !returnsWithin(3*time.Second, func() {
		_, err = client.Call[string](context.Background(), c, "ReviewHandlers.Echo", []any{"x"})
	}) {
		t.Fatal("a call without a token hung on a server that requires auth")
	}
	if !client.HasCode(err, client.CodeAuthFailed) {
		t.Fatalf("call err = %v, want CodeAuthFailed", err)
	}
	select {
	case <-c.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("client did not stop")
	}
	if err := c.Err(); !client.HasCode(err, client.CodeAuthFailed) || !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("client Err = %v, want CodeAuthFailed \"authentication required\"", err)
	}
	waitClosed(t, sub.C)
	if !client.HasCode(sub.Err(), client.CodeAuthFailed) {
		t.Fatalf("subscription Err = %v, want CodeAuthFailed", sub.Err())
	}
	if n := drops.Load(); n != 0 {
		t.Fatalf("client reconnected %d times; it must stop", n)
	}
}

// A refresh whose caller gave up gets its auth_error after the caller left.
// That verdict was asked for, so it must not stop the client the way an
// unsolicited auth_error does.
func TestLateRefreshVerdictKeepsClient(t *testing.T) {
	f := newReviewFixture(t, aprot.ServerOptions{}, strictAuth("ok"))
	c := f.dial(t, client.Options{AuthToken: func(context.Context) (string, error) { return "ok", nil }})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.RefreshAuth(ctx, "bad"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RefreshAuth = %v, want the ctx deadline", err)
	}
	time.Sleep(300 * time.Millisecond) // the late auth_error arrives
	if err := c.Err(); err != nil {
		t.Fatalf("client stopped on a late refresh verdict: %v", err)
	}
	if got, err := client.Call[string](testCtx(t), c, "ReviewHandlers.Echo", []any{"x"}); err != nil || got != "x" {
		t.Fatalf("call after late verdict = %q, %v", got, err)
	}
}

// failingConnect rejects connections while fails > 0, or always when
// fails < 0, and counts the attempts it sees.
type failingConnect struct {
	fails    atomic.Int32
	attempts atomic.Int32
}

func (fc *failingConnect) setup(s *aprot.Server) {
	s.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
		conn.SetUserID("u1")
		fc.attempts.Add(1)
		switch n := fc.fails.Load(); {
		case n < 0:
			return aprot.ErrUnauthorized("session store unavailable")
		case n > 0:
			fc.fails.Add(-1)
			return aprot.ErrUnauthorized("session store unavailable")
		}
		return nil
	})
}

// One rejected reconnect used to close a long-running client for good.
// With ReconnectOnRejected, the client retries at a fixed delay.
func TestReconnectOnRejected(t *testing.T) {
	t.Run("retries", func(t *testing.T) {
		var fc failingConnect
		f := newReviewFixture(t, aprot.ServerOptions{ReconnectInterval: 10, ReconnectMaxInterval: 10}, fc.setup)
		c := f.dial(t, client.Options{ReconnectOnRejected: &client.RejectedRetry{Delay: 30 * time.Millisecond}})
		sub := client.Subscribe[int64](testCtx(t), c, "ReviewHandlers.Count", nil)
		defer sub.Close()
		recv(t, sub.C)

		fc.fails.Store(3)
		f.h.counter.Store(7)
		f.srv.DisconnectUser("u1")
		if !waitFor(3*time.Second, func() bool { return c.LastRejection() != nil }) {
			t.Fatal("LastRejection stayed nil while the server rejected reconnects")
		}
		if rej := c.LastRejection(); rej.Code != client.CodeUnauthorized {
			t.Fatalf("LastRejection = %v, want CodeUnauthorized", rej)
		}
		if got := recvUntil(t, sub.C, func(v int64) bool { return v == 7 }); got != 7 {
			t.Fatal("unreachable")
		}
		if err := c.Err(); err != nil {
			t.Fatalf("client stopped: %v", err)
		}
		if rej := c.LastRejection(); rej != nil {
			t.Fatalf("LastRejection = %v after a successful reconnect, want nil", rej)
		}
		if n := fc.attempts.Load(); n != 5 { // initial + 3 rejected + 1 accepted
			t.Fatalf("connect attempts = %d, want 5", n)
		}
	})

	t.Run("max attempts", func(t *testing.T) {
		var fc failingConnect
		f := newReviewFixture(t, aprot.ServerOptions{ReconnectInterval: 10, ReconnectMaxInterval: 10}, fc.setup)
		c := f.dial(t, client.Options{ReconnectOnRejected: &client.RejectedRetry{Delay: 10 * time.Millisecond, MaxAttempts: 2}})
		fc.fails.Store(-1)
		f.srv.DisconnectUser("u1")
		select {
		case <-c.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("client did not stop after MaxAttempts rejections")
		}
		if !client.HasCode(c.Err(), client.CodeUnauthorized) {
			t.Fatalf("Err = %v, want the rejection", c.Err())
		}
		// initial + the rejected reconnect + 2 retries
		if n := fc.attempts.Load(); n != 4 {
			t.Fatalf("connect attempts = %d, want 4", n)
		}
	})

	t.Run("nil stops", func(t *testing.T) {
		var fc failingConnect
		f := newReviewFixture(t, aprot.ServerOptions{ReconnectInterval: 10, ReconnectMaxInterval: 10}, fc.setup)
		c := f.dial(t, client.Options{})
		fc.fails.Store(-1)
		f.srv.DisconnectUser("u1")
		select {
		case <-c.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("client without ReconnectOnRejected kept retrying a rejection")
		}
		if !client.HasCode(c.Err(), client.CodeUnauthorized) {
			t.Fatalf("Err = %v, want the rejection", c.Err())
		}
	})

	t.Run("initial dial still fails", func(t *testing.T) {
		var fc failingConnect
		fc.fails.Store(-1)
		f := newReviewFixture(t, aprot.ServerOptions{}, fc.setup)
		_, err := client.Dial(testCtx(t), f.url, client.Options{ReconnectOnRejected: &client.RejectedRetry{}})
		if !client.HasCode(err, client.CodeUnauthorized) {
			t.Fatalf("Dial = %v, want the rejection", err)
		}
	})
}

// OnError takes no type argument: the subscription's T comes from Subscribe.
// A WithPatch whose T does not match closes the subscription.
func TestSubscribeOptionsUntyped(t *testing.T) {
	f := newReviewFixture(t, aprot.ServerOptions{})
	c := f.dial(t, client.Options{})
	ctx := testCtx(t)

	ok := client.Subscribe[string](ctx, c, "ReviewHandlers.Echo", []any{"a"}, client.OnError(func(error) {}))
	defer ok.Close()
	if got := recv(t, ok.C); got != "a" {
		t.Fatalf("got %q", got)
	}

	bad := client.Subscribe[string](ctx, c, "ReviewHandlers.Echo", []any{"a"},
		client.WithPatch(func(cur int, _ jsontext.Value) (int, error) { return cur, nil }))
	waitClosed(t, bad.C)
	if err := bad.Err(); err == nil || !strings.Contains(err.Error(), "func(int, jsontext.Value) (int, error)") || !strings.Contains(err.Error(), "func(string, jsontext.Value) (string, error)") {
		t.Fatalf("mismatched WithPatch Err = %v, want both func types named", err)
	}
}

// Options.ReconnectInterval used to be overwritten by the server's config
// frame (1s by default), so setting it did nothing.
func TestReconnectOptionsWinOverServer(t *testing.T) {
	f := newReviewFixture(t, aprot.ServerOptions{}, setUser) // server: 1s, 10s
	var drops atomic.Int32
	c := f.dial(t, client.Options{
		ReconnectInterval:    10 * time.Millisecond,
		ReconnectMaxInterval: 10 * time.Millisecond,
		OnStateChange:        dropCounter(&drops),
	})
	f.srv.DisconnectUser("u1")
	if !waitFor(2*time.Second, func() bool { return drops.Load() == 1 }) {
		t.Fatal("connection did not drop")
	}
	start := time.Now()
	if !waitFor(2*time.Second, func() bool { return c.State() == client.StateConnected }) {
		t.Fatal("client did not reconnect")
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("reconnect took %v with ReconnectInterval 10ms; the server's 1s won", d)
	}
}

// TLSConfig and NetDialContext replace the gorilla Dialer option.
func TestDialTLSConfigAndNetDialContext(t *testing.T) {
	reg := aprot.NewRegistry()
	reg.Register(&ReviewHandlers{})
	srv := aprot.NewServer(reg)
	ts := httptest.NewTLSServer(srv)
	defer ts.Close()
	pool := x509.NewCertPool()
	pool.AddCert(ts.Certificate())
	var dials atomic.Int32
	c, err := client.Dial(testCtx(t), "wss"+strings.TrimPrefix(ts.URL, "https"), client.Options{
		TLSConfig: &tls.Config{RootCAs: pool},
		NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	})
	if err != nil {
		t.Fatalf("Dial over TLS: %v", err)
	}
	defer c.Close()
	if got, err := client.Call[string](testCtx(t), c, "ReviewHandlers.Echo", []any{"tls"}); err != nil || got != "tls" {
		t.Fatalf("call = %q, %v", got, err)
	}
	if dials.Load() != 1 {
		t.Fatalf("NetDialContext called %d times, want 1", dials.Load())
	}
}

// freezeProxy forwards TCP to target until freeze, after which the
// connections open at that moment stop forwarding without closing: a
// half-open path. Connections accepted later forward normally.
type freezeProxy struct {
	ln     net.Listener
	mu     sync.Mutex
	frozen []*atomic.Bool
}

func newFreezeProxy(t *testing.T, target string) *freezeProxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &freezeProxy{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			cc, err := ln.Accept()
			if err != nil {
				return
			}
			sc, err := net.Dial("tcp", target)
			if err != nil {
				_ = cc.Close()
				continue
			}
			frozen := new(atomic.Bool)
			p.mu.Lock()
			p.frozen = append(p.frozen, frozen)
			p.mu.Unlock()
			pipe := func(dst, src net.Conn) {
				buf := make([]byte, 32<<10)
				for {
					n, err := src.Read(buf)
					if err != nil {
						if !frozen.Load() {
							_ = dst.Close()
						}
						return
					}
					if !frozen.Load() {
						_, _ = dst.Write(buf[:n])
					}
				}
			}
			go pipe(sc, cc)
			go pipe(cc, sc)
		}
	}()
	return p
}

func (p *freezeProxy) freeze() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, f := range p.frozen {
		f.Store(true)
	}
}

// A connection whose network path dies without a close was never detected:
// the client stayed "connected" and every call timed out. Client pings
// must notice the silence and reconnect.
func TestHalfOpenDetected(t *testing.T) {
	f := newReviewFixture(t, aprot.ServerOptions{ReconnectInterval: 10, ReconnectMaxInterval: 10})
	p := newFreezeProxy(t, f.ln)
	var drops atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, "ws://"+p.ln.Addr().String()+"/", client.Options{
		PingInterval:  50 * time.Millisecond,
		OnStateChange: dropCounter(&drops),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sub := client.Subscribe[int64](testCtx(t), c, "ReviewHandlers.Count", nil)
	defer sub.Close()
	recv(t, sub.C)
	// Pings keep a healthy idle connection alive.
	time.Sleep(300 * time.Millisecond)
	if drops.Load() != 0 {
		t.Fatal("a healthy idle connection was dropped")
	}

	p.freeze()
	f.h.counter.Store(3)
	if !waitFor(2*time.Second, func() bool { return drops.Load() == 1 }) {
		t.Fatal("client did not notice the half-open connection")
	}
	if got := recvUntil(t, sub.C, func(v int64) bool { return v == 3 }); got != 3 {
		t.Fatal("unreachable")
	}
}

// DialStream ignored a MaxMessageSize below 64 KiB: the scanner's initial
// buffer set the real limit.
func TestDialStreamSmallMaxMessageSize(t *testing.T) {
	f := newReviewFixture(t, aprot.ServerOptions{})
	c, err := client.DialStream(testCtx(t), streamDial(f.srv), client.Options{MaxMessageSize: 1000, NoReconnect: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := client.Call[string](testCtx(t), c, "ReviewHandlers.Big", []any{100}); err != nil {
		t.Fatalf("small result: %v", err)
	}
	_, err = client.Call[string](testCtx(t), c, "ReviewHandlers.Big", []any{2000})
	if !errors.Is(err, client.ErrConnectionLost) || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("2000-byte result with MaxMessageSize 1000 = %v, want the read to fail", err)
	}
}

// Stream on a method that is not a streaming handler used to wait until its
// ctx ended. It must fail at once, as Call on a streaming method does.
func TestStreamOnNonStreamingMethod(t *testing.T) {
	f := newReviewFixture(t, aprot.ServerOptions{})
	c := f.dial(t, client.Options{})
	ctx := testCtx(t)
	var s1 *client.StreamResult[string]
	var s2 *client.Stream2Result[string, int]
	if !returnsWithin(2*time.Second, func() {
		s1 = client.Stream[string](ctx, c, "ReviewHandlers.Echo", []any{"hi"})
		for range s1.All() {
			t.Error("Stream yielded the plain result")
		}
		s2 = client.Stream2[string, int](ctx, c, "ReviewHandlers.Echo", []any{"hi"})
		for range s2.All() {
			t.Error("Stream2 yielded the plain result")
		}
	}) {
		t.Fatal("Stream on a non-streaming method hung")
	}
	for _, err := range []error{s1.Err(), s2.Err()} {
		if !client.HasCode(err, client.CodeInvalidRequest) || !strings.Contains(err.Error(), "use Call") {
			t.Fatalf("Err = %v, want CodeInvalidRequest pointing to Call", err)
		}
	}
	// The connection is fine afterwards.
	if got, err := client.Call[string](ctx, c, "ReviewHandlers.Echo", []any{"x"}); err != nil || got != "x" {
		t.Fatalf("call = %q, %v", got, err)
	}
}
