package client_test

// Regression tests for bugs found in review. Each asserts the behaviour the
// client must have.

import (
	"context"
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
	"encoding/json/v2"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/client"
)

type RegressionHandlers struct {
	mu    sync.Mutex
	items []Item
	fail  atomic.Bool
}

func (h *RegressionHandlers) List(ctx context.Context) ([]Item, error) {
	aprot.RegisterRefreshTrigger(ctx, "items")
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Item(nil), h.items...), nil
}

func (h *RegressionHandlers) Flaky(ctx context.Context) (int, error) {
	aprot.RegisterRefreshTrigger(ctx, "flaky")
	if h.fail.Load() {
		return 0, errors.New("transient")
	}
	return 1, nil
}

func (h *RegressionHandlers) Count(ctx context.Context, n int) (iter.Seq[int], error) {
	return func(yield func(int) bool) {
		for i := range n {
			if !yield(i) {
				return
			}
		}
	}, nil
}

type regressionFixture struct {
	srv *aprot.Server
	h   *RegressionHandlers
	url string
}

func newRegressionFixture(t *testing.T, opts aprot.ServerOptions, setup ...func(*aprot.Server)) *regressionFixture {
	t.Helper()
	h := &RegressionHandlers{items: []Item{{ID: 1, Name: "a"}}}
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
	return &regressionFixture{srv: srv, h: h, url: "ws" + strings.TrimPrefix(hs.URL, "http")}
}

func setUser(s *aprot.Server) {
	s.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
		conn.SetUserID("u1")
		return nil
	})
}

// returnsWithin runs fn and reports whether it returned within d.
func returnsWithin(d time.Duration, fn func()) bool {
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// silentPipe returns one end of a pipe whose peer never writes and never
// closes: a server that accepted the connection but has not sent the
// config frame yet.
func silentPipe() io.ReadWriteCloser {
	a, b := net.Pipe()
	go func() { _, _ = io.Copy(io.Discard, b) }()
	return a
}

// Dial must honour ctx while waiting for the server's first frame.
func TestDialHonorsCtxDuringHandshake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	ok := returnsWithin(3*time.Second, func() {
		_, _ = client.DialStream(ctx, func(context.Context) (io.ReadWriteCloser, error) {
			return silentPipe(), nil
		}, client.Options{})
	})
	if !ok {
		t.Fatal("DialStream did not return 3s after its 200ms ctx expired: the handshake read ignores ctx")
	}
}

// Close must not hang when the run goroutine is inside a reconnect whose
// server has not sent its config frame yet.
func TestCloseDuringReconnectHandshake(t *testing.T) {
	reg := aprot.NewRegistry()
	reg.Register(&RegressionHandlers{})
	srv := aprot.NewServer(reg, aprot.ServerOptions{ReconnectInterval: 10, ReconnectMaxInterval: 10})

	var dials atomic.Int32
	secondDial := make(chan struct{})
	var first net.Conn
	c, err := client.DialStream(context.Background(), func(context.Context) (io.ReadWriteCloser, error) {
		if dials.Add(1) == 1 {
			a, b := net.Pipe()
			first = b
			go func() { _ = srv.ServeStream(context.Background(), b, aprot.ConnInfo{}) }()
			return a, nil
		}
		select {
		case <-secondDial:
		default:
			close(secondDial)
		}
		return silentPipe(), nil
	}, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close() // drop the connection; the client reconnects
	select {
	case <-secondDial:
	case <-time.After(3 * time.Second):
		t.Fatal("client did not reconnect")
	}
	time.Sleep(50 * time.Millisecond) // now blocked reading the handshake
	if !returnsWithin(3*time.Second, func() { _ = c.Close() }) {
		t.Fatal("Close hung: run goroutine is stuck in connect() reading the handshake, which ignores runCtx")
	}
}

// Closing a subscription while markConnected is between publishing the
// connection and re-sending subscribe frames sends unsubscribe before
// subscribe. The server then holds a subscription the client has dropped.
func TestUnsubscribeNotOvertakenByResubscribe(t *testing.T) {
	f := newRegressionFixture(t, aprot.ServerOptions{ReconnectInterval: 10, ReconnectMaxInterval: 10}, setUser)
	var armed atomic.Bool
	var subp atomic.Pointer[client.Subscription[[]Item]]
	ctx := testCtx(t)
	c, err := client.Dial(ctx, f.url, client.Options{
		OnStateChange: func(s client.State) {
			// Stands in for any goroutine calling sub.Close() right after
			// the reconnect publishes the connection.
			if s == client.StateConnected && armed.Load() {
				subp.Load().Close()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	sub := client.Subscribe[[]Item](ctx, c, "RegressionHandlers.List", nil)
	subp.Store(sub)
	recv(t, sub.C)
	if n := f.srv.Stats().Subscriptions; n != 1 {
		t.Fatalf("server subscriptions = %d, want 1", n)
	}
	armed.Store(true)
	f.srv.DisconnectUser("u1")
	waitClosed(t, sub.C)
	if _, err := client.Call[[]Item](ctx, c, "RegressionHandlers.List", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := f.srv.Stats().Subscriptions; n != 0 {
		t.Fatalf("server still holds %d subscription(s) for a subscription the client closed: unsubscribe was sent before the resubscribe", n)
	}
}

// The server keeps a subscription registered after a refresh error (see
// refreshSubscription), and the TypeScript client keeps it too. The Go
// client closes it for good.
func TestRefreshErrorKeepsSubscription(t *testing.T) {
	f := newRegressionFixture(t, aprot.ServerOptions{})
	ctx := testCtx(t)
	c, err := client.Dial(ctx, f.url, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sub := client.Subscribe[int](ctx, c, "RegressionHandlers.Flaky", nil)
	defer sub.Close()
	recv(t, sub.C)

	f.h.fail.Store(true)
	f.srv.TriggerRefresh("flaky")
	time.Sleep(200 * time.Millisecond)
	f.h.fail.Store(false)
	f.srv.TriggerRefresh("flaky")

	select {
	case v, ok := <-sub.C:
		if !ok {
			t.Fatalf("subscription closed by a transient refresh error (Err = %v); server kept it registered", sub.Err())
		}
		_ = v
	case <-time.After(2 * time.Second):
		t.Fatal("no value after recovery")
	}
}

// Close from OnStateChange (which runs on the run goroutine) waits for the
// run goroutine to exit: a self-deadlock.
func TestCloseFromOnStateChange(t *testing.T) {
	f := newRegressionFixture(t, aprot.ServerOptions{}, setUser)
	var cp atomic.Pointer[client.Client]
	closeReturned := make(chan struct{})
	var once sync.Once
	c, err := client.Dial(testCtx(t), f.url, client.Options{
		OnStateChange: func(s client.State) {
			if s == client.StateConnecting {
				once.Do(func() {
					_ = cp.Load().Close()
					close(closeReturned)
				})
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cp.Store(c)
	f.srv.DisconnectUser("u1")
	select {
	case <-closeReturned:
	case <-time.After(3 * time.Second):
		t.Fatal("Client.Close called from OnStateChange never returned (waits on runDone from the run goroutine)")
	}
}

type patchObserver struct {
	onRegistered func(id string)
}

func (o *patchObserver) ConnectionOpened(*aprot.Conn)                         {}
func (o *patchObserver) ConnectionClosed(*aprot.Conn)                         {}
func (o *patchObserver) RequestCompleted(aprot.RequestEvent)                  {}
func (o *patchObserver) SubscriptionUnregistered(*aprot.Conn, string, string) {}
func (o *patchObserver) RefreshFanout(string, int)                            {}
func (o *patchObserver) PatchFanout(string, int, int)                         {}
func (o *patchObserver) SendBufferFull(*aprot.Conn)                           {}
func (o *patchObserver) WriteTimedOut(*aprot.Conn)                            {}
func (o *patchObserver) SubscriptionRegistered(_ *aprot.Conn, _, id string) {
	if o.onRegistered != nil {
		o.onRegistered(id)
	}
}

// After a reconnect, the subscription keeps the previous connection's value
// as the patch base. A patch that reaches the client before the resubscribe
// result is applied to that stale base.
func TestPatchAfterReconnectNotAppliedToStaleValue(t *testing.T) {
	var armed atomic.Bool
	var srvp atomic.Pointer[aprot.Server]
	obs := &patchObserver{onRegistered: func(string) {
		if armed.Load() {
			_ = srvp.Load().PatchSubscription(ItemPatch{ID: 1, Name: "patched"}, "items")
			time.Sleep(100 * time.Millisecond) // let the patch frame go out first
		}
	}}
	f := newRegressionFixture(t, aprot.ServerOptions{ReconnectInterval: 10, ReconnectMaxInterval: 10, Observer: obs}, setUser)
	srvp.Store(f.srv)

	var mu sync.Mutex
	var bases [][]Item
	apply := func(cur []Item, raw jsontext.Value) ([]Item, error) {
		mu.Lock()
		bases = append(bases, append([]Item(nil), cur...))
		mu.Unlock()
		var p ItemPatch
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		out := append([]Item(nil), cur...)
		for i := range out {
			if out[i].ID == p.ID {
				out[i].Name = p.Name
			}
		}
		return out, nil
	}
	ctx := testCtx(t)
	c, err := client.Dial(ctx, f.url, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sub := client.Subscribe[[]Item](ctx, c, "RegressionHandlers.List", nil, client.WithPatch(apply))
	defer sub.Close()
	recv(t, sub.C)

	// While disconnected, the data changes: a second item appears.
	armed.Store(true)
	f.srv.DisconnectUser("u1")
	f.h.mu.Lock()
	f.h.items = append(f.h.items, Item{ID: 2, Name: "b"})
	f.h.mu.Unlock()

	recvUntil(t, sub.C, func(v []Item) bool { return len(v) == 2 })
	mu.Lock()
	defer mu.Unlock()
	for _, b := range bases {
		if len(b) == 1 {
			t.Fatalf("patch from the new connection was applied to the pre-disconnect value %v; a reader could see it on C", b)
		}
	}
}

// Call on a streaming method never completes: stream_end is not routed to
// pending calls.
func TestCallOnStreamMethodFails(t *testing.T) {
	f := newRegressionFixture(t, aprot.ServerOptions{})
	ctx := testCtx(t)
	c, err := client.Dial(ctx, f.url, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, err = client.Call[int](cctx, c, "RegressionHandlers.Count", []any{2})
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Call on a streaming method hung until ctx expired")
	}
	if !client.HasCode(err, client.CodeInvalidRequest) {
		t.Fatalf("Call on a streaming method = %v, want CodeInvalidRequest", err)
	}
}

// OnError receives refresh errors, and the subscription keeps delivering.
func TestOnErrorReceivesRefreshErrors(t *testing.T) {
	f := newRegressionFixture(t, aprot.ServerOptions{})
	ctx := testCtx(t)
	c, err := client.Dial(ctx, f.url, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	errs := make(chan error, 4)
	sub := client.Subscribe(ctx, c, "RegressionHandlers.Flaky", nil,
		client.OnError[int](func(err error) { errs <- err }))
	defer sub.Close()
	recv(t, sub.C)

	f.h.fail.Store(true)
	f.srv.TriggerRefresh("flaky")
	if err := recv(t, errs); !client.HasCode(err, client.CodeInternalError) {
		t.Fatalf("OnError got %v, want CodeInternalError", err)
	}
	f.h.fail.Store(false)
	f.srv.TriggerRefresh("flaky")
	if v := recv(t, sub.C); v != 1 {
		t.Fatalf("after recovery got %d", v)
	}
}

// A reconnect to a server that accepts but never answers must time out and
// retry, not stall until Close.
func TestReconnectHandshakeTimesOut(t *testing.T) {
	reg := aprot.NewRegistry()
	reg.Register(&RegressionHandlers{})
	srv := aprot.NewServer(reg, aprot.ServerOptions{ReconnectInterval: 10, ReconnectMaxInterval: 10})

	var dials atomic.Int32
	var first net.Conn
	c, err := client.DialStream(context.Background(), func(context.Context) (io.ReadWriteCloser, error) {
		switch dials.Add(1) {
		case 1, 3:
			a, b := net.Pipe()
			if first == nil {
				first = b
			}
			go func() { _ = srv.ServeStream(context.Background(), b, aprot.ConnInfo{}) }()
			return a, nil
		default:
			return silentPipe(), nil
		}
	}, client.Options{ConnectTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = first.Close()
	// Dial 2 is silent and must time out; dial 3 reconnects.
	deadline := time.Now().Add(5 * time.Second)
	for dials.Load() < 3 || c.State() != client.StateConnected {
		if time.Now().After(deadline) {
			t.Fatalf("no reconnect after a silent server: dials = %d, state = %v", dials.Load(), c.State())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type burstHandlers struct {
	running atomic.Int64
	peak    atomic.Int64
}

func (h *burstHandlers) Get(ctx context.Context, n int) (int, error) {
	aprot.RegisterRefreshTrigger(ctx, "burst")
	cur := h.running.Add(1)
	defer h.running.Add(-1)
	for {
		p := h.peak.Load()
		if cur <= p || h.peak.CompareAndSwap(p, cur) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	return n, nil
}

// More subscriptions than the server's MaxConcurrentRequests (256), with a
// handler slow enough that their first runs overlap. Without the client's
// subscribe cap, the burst at start and the resubscribe burst after a
// reconnect had some subscriptions refused with CodeTooManyRequests.
func TestSubscribeBurstStaysUnderServerLimit(t *testing.T) {
	h := &burstHandlers{}
	reg := aprot.NewRegistry()
	reg.Register(h)
	srv := aprot.NewServer(reg, aprot.ServerOptions{ReconnectInterval: 10, ReconnectMaxInterval: 10})
	setUser(srv)
	hs := httptest.NewServer(srv)
	defer func() {
		hs.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Stop(ctx)
	}()

	ctx := testCtx(t)
	c, err := client.Dial(ctx, "ws"+strings.TrimPrefix(hs.URL, "http"), client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	const n = 300
	subs := make([]*client.Subscription[int], n)
	for i := range subs {
		subs[i] = client.Subscribe[int](ctx, c, "burstHandlers.Get", []any{i})
		defer subs[i].Close()
	}
	for i, s := range subs {
		if v := recv(t, s.C); v != i {
			t.Fatalf("sub %d got %d", i, v)
		}
	}

	srv.DisconnectUser("u1")
	for i, s := range subs {
		if v := recv(t, s.C); v != i {
			t.Fatalf("after reconnect, sub %d got %d (Err = %v)", i, v, s.Err())
		}
	}
	if p := h.peak.Load(); p > 64 {
		t.Fatalf("server ran %d first runs at once; the client cap is 64", p)
	}
}
