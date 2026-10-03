package client_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
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

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/client"
)

type Item struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type ItemAdded struct {
	Name string `json:"name"`
}

type ItemPatch struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type Handlers struct {
	mu        sync.Mutex
	items     []Item
	listRuns  atomic.Int64
	canceled  chan string
	slowStart chan struct{}
	server    *aprot.Server
}

func (h *Handlers) List(ctx context.Context) ([]Item, error) {
	aprot.RegisterRefreshTrigger(ctx, "items")
	h.listRuns.Add(1)
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Item(nil), h.items...), nil
}

func (h *Handlers) Add(ctx context.Context, name string) error {
	h.mu.Lock()
	h.items = append(h.items, Item{ID: len(h.items) + 1, Name: name})
	h.mu.Unlock()
	aprot.TriggerRefresh(ctx, "items")
	return nil
}

func (h *Handlers) Rename(ctx context.Context, id int, name string) error {
	h.mu.Lock()
	for i := range h.items {
		if h.items[i].ID == id {
			h.items[i].Name = name
		}
	}
	h.mu.Unlock()
	return aprot.PatchSubscription(ctx, ItemPatch{ID: id, Name: name}, "items")
}

func (h *Handlers) Get(ctx context.Context, id int) (*Item, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, it := range h.items {
		if it.ID == id {
			return &it, nil
		}
	}
	return nil, nil
}

func (h *Handlers) Forbidden(ctx context.Context) error {
	return aprot.ErrForbidden("no")
}

func (h *Handlers) Slow(ctx context.Context) (string, error) {
	h.slowStart <- struct{}{}
	<-ctx.Done()
	h.canceled <- "slow"
	return "", ctx.Err()
}

func (h *Handlers) Echo(ctx context.Context, d time.Duration, s []string) (map[string]any, error) {
	return map[string]any{"d": d, "s": s}, nil
}

func (h *Handlers) WithProgress(ctx context.Context) (string, error) {
	aprot.Progress(ctx).Update(1, 2, "half")
	return "done", nil
}

func (h *Handlers) Count(ctx context.Context, n int) (iter.Seq[int], error) {
	return func(yield func(int) bool) {
		for i := range n {
			if !yield(i) {
				h.canceled <- "count"
				return
			}
		}
	}, nil
}

func (h *Handlers) Endless(ctx context.Context) (iter.Seq[int], error) {
	return func(yield func(int) bool) {
		for i := 0; ; i++ {
			if !yield(i) || ctx.Err() != nil {
				h.canceled <- "endless"
				return
			}
			time.Sleep(time.Millisecond)
		}
	}, nil
}

func (h *Handlers) Pairs(ctx context.Context) (iter.Seq2[string, int], error) {
	return func(yield func(string, int) bool) {
		_ = yield("a", 1) && yield("b", 2)
	}, nil
}

func (h *Handlers) Image(ctx context.Context) (aprot.Blob, error) {
	return aprot.Blob{ContentType: "image/png", Data: []byte{0, 1, 2, 255}}, nil
}

func (h *Handlers) Broadcast(ctx context.Context, name string) error {
	h.server.Broadcast(ItemAdded{Name: name})
	return nil
}

type fixture struct {
	server   *aprot.Server
	handlers *Handlers
	http     *httptest.Server
	url      string
}

func newFixture(t *testing.T, setup ...func(*aprot.Server)) *fixture {
	t.Helper()
	h := &Handlers{canceled: make(chan string, 16), slowStart: make(chan struct{}, 16)}
	reg := aprot.NewRegistry()
	reg.Register(h)
	reg.RegisterPushEventFor(h, ItemAdded{})
	srv := aprot.NewServer(reg)
	h.server = srv
	for _, f := range setup {
		f(srv)
	}
	hs := httptest.NewServer(srv)
	t.Cleanup(func() {
		hs.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Stop(ctx)
	})
	return &fixture{
		server:   srv,
		handlers: h,
		http:     hs,
		url:      "ws" + strings.TrimPrefix(hs.URL, "http"),
	}
}

func (f *fixture) dial(t *testing.T, opts client.Options) *client.Client {
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

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// recv waits for the next value on ch.
func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatal("channel closed")
		}
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a value")
	}
	panic("unreachable")
}

// recvUntil reads from ch until cond holds for a value.
func recvUntil[T any](t *testing.T, ch <-chan T, cond func(T) bool) T {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				t.Fatal("channel closed")
			}
			if cond(v) {
				return v
			}
		case <-deadline:
			t.Fatal("timed out waiting for a matching value")
		}
	}
}

func waitClosed[T any](t *testing.T, ch <-chan T) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for the channel to close")
		}
	}
}

func TestCall(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, client.Options{})
	ctx := testCtx(t)

	if _, err := client.Call[struct{}](ctx, c, "Handlers.Add", []any{"apple"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	items, err := client.Call[[]Item](ctx, c, "Handlers.List", nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Name != "apple" {
		t.Fatalf("List = %+v", items)
	}

	got, err := client.Call[*Item](ctx, c, "Handlers.Get", []any{1})
	if err != nil || got == nil || got.Name != "apple" {
		t.Fatalf("Get(1) = %+v, %v", got, err)
	}
	missing, err := client.Call[*Item](ctx, c, "Handlers.Get", []any{99})
	if err != nil || missing != nil {
		t.Fatalf("Get(99) = %+v, %v; want nil, nil", missing, err)
	}

	echo, err := client.Call[map[string]jsontext.Value](ctx, c, "Handlers.Echo", []any{1500 * time.Millisecond, []string{"x"}})
	if err != nil {
		t.Fatalf("Echo: %v", err)
	}
	if string(echo["d"]) != "1500000000" {
		t.Fatalf("duration did not travel as nanoseconds: %s", echo["d"])
	}
}

func TestCallErrors(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, client.Options{})
	ctx := testCtx(t)

	_, err := client.Call[struct{}](ctx, c, "Handlers.Forbidden", nil)
	if !client.HasCode(err, client.CodeForbidden) {
		t.Fatalf("Forbidden: got %v, want CodeForbidden", err)
	}
	_, err = client.Call[struct{}](ctx, c, "Handlers.Nope", nil)
	if !client.HasCode(err, client.CodeMethodNotFound) {
		t.Fatalf("Nope: got %v, want CodeMethodNotFound", err)
	}
	_, err = client.Call[struct{}](ctx, c, "Handlers.Add", []any{func() {}})
	if err == nil {
		t.Fatal("unencodable params: want error")
	}
	_, err = client.Call[int](ctx, c, "Handlers.List", nil)
	if err == nil {
		t.Fatal("decoding a list into int: want error")
	}
}

func TestCallCancelReachesServer(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, client.Options{})
	ctx, cancel := context.WithCancel(testCtx(t))

	errc := make(chan error, 1)
	go func() {
		_, err := client.Call[string](ctx, c, "Handlers.Slow", nil)
		errc <- err
	}()
	recv(t, f.handlers.slowStart)
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if got := recv(t, f.handlers.canceled); got != "slow" {
		t.Fatalf("server saw %q", got)
	}
}

func TestProgress(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, client.Options{})
	var got []client.Progress
	var mu sync.Mutex
	ctx := client.WithProgress(testCtx(t), func(p client.Progress) {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	})
	res, err := client.Call[string](ctx, c, "Handlers.WithProgress", nil)
	if err != nil || res != "done" {
		t.Fatalf("got %q, %v", res, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || *got[0].Current != 1 || *got[0].Total != 2 || got[0].Message != "half" {
		t.Fatalf("progress = %+v", got)
	}
}

func TestSubscribeRefresh(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, client.Options{})
	ctx := testCtx(t)

	sub := client.Subscribe[[]Item](ctx, c, "Handlers.List", nil)
	defer sub.Close()
	if first := recv(t, sub.C); len(first) != 0 {
		t.Fatalf("first = %+v", first)
	}
	if _, err := client.Call[struct{}](ctx, c, "Handlers.Add", []any{"apple"}); err != nil {
		t.Fatal(err)
	}
	if next := recv(t, sub.C); len(next) != 1 || next[0].Name != "apple" {
		t.Fatalf("after Add = %+v", next)
	}
}

func TestSubscribeCloseUnsubscribes(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, client.Options{})
	ctx := testCtx(t)

	sub := client.Subscribe[[]Item](ctx, c, "Handlers.List", nil)
	recv(t, sub.C)
	sub.Close()
	sub.Close() // safe twice
	waitClosed(t, sub.C)
	if err := sub.Err(); err != nil {
		t.Fatalf("Err after Close = %v, want nil", err)
	}

	// Give the unsubscribe frame time to land, then mutate: the server must
	// no longer re-run the handler for this connection.
	time.Sleep(100 * time.Millisecond)
	before := f.handlers.listRuns.Load()
	if _, err := client.Call[struct{}](ctx, c, "Handlers.Add", []any{"x"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if after := f.handlers.listRuns.Load(); after != before {
		t.Fatalf("List re-ran %d times after Close", after-before)
	}
}

func TestSubscribeContextEnds(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, client.Options{})
	ctx, cancel := context.WithCancel(testCtx(t))

	sub := client.Subscribe[[]Item](ctx, c, "Handlers.List", nil)
	recv(t, sub.C)
	cancel()
	waitClosed(t, sub.C)
	if !errors.Is(sub.Err(), context.Canceled) {
		t.Fatalf("Err = %v, want context.Canceled", sub.Err())
	}
}

func TestSubscribeError(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, client.Options{})

	sub := client.Subscribe[struct{}](testCtx(t), c, "Handlers.Forbidden", nil)
	waitClosed(t, sub.C)
	if !client.HasCode(sub.Err(), client.CodeForbidden) {
		t.Fatalf("Err = %v, want CodeForbidden", sub.Err())
	}

	sub2 := client.Subscribe[int](testCtx(t), c, "Handlers.Count", []any{3})
	waitClosed(t, sub2.C)
	if !client.HasCode(sub2.Err(), client.CodeInvalidRequest) {
		t.Fatalf("subscribing to a stream: Err = %v, want CodeInvalidRequest", sub2.Err())
	}
}

// A reader that never reads must not hold up the connection: results for
// it are replaced, and other traffic keeps flowing.
func TestSubscribeSlowReaderDoesNotBlock(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, client.Options{})
	ctx := testCtx(t)

	stuck := client.Subscribe[[]Item](ctx, c, "Handlers.List", nil)
	defer stuck.Close()
	for i := range 20 {
		if _, err := client.Call[struct{}](ctx, c, "Handlers.Add", []any{"n" + string(rune('a'+i))}); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}
	// The latest value wins: eventually the reader sees all 20 items.
	recvUntil(t, stuck.C, func(v []Item) bool { return len(v) == 20 })
}

func TestSubscribePatch(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, client.Options{})
	ctx := testCtx(t)
	if _, err := client.Call[struct{}](ctx, c, "Handlers.Add", []any{"apple"}); err != nil {
		t.Fatal(err)
	}
	runs := f.handlers.listRuns.Load()

	sub := client.Subscribe[[]Item](ctx, c, "Handlers.List", nil,
		client.WithPatch(func(cur []Item, raw jsontext.Value) ([]Item, error) {
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
		}))
	defer sub.Close()
	recv(t, sub.C)

	if _, err := client.Call[struct{}](ctx, c, "Handlers.Rename", []any{1, "pear"}); err != nil {
		t.Fatal(err)
	}
	got := recv(t, sub.C)
	if len(got) != 1 || got[0].Name != "pear" {
		t.Fatalf("after patch = %+v", got)
	}
	if n := f.handlers.listRuns.Load() - runs; n != 1 {
		t.Fatalf("List ran %d times; the patch should not re-run it", n)
	}
}

// Without WithPatch the client does not declare patch support, so the
// server falls back to a full refresh.
func TestSubscribeWithoutPatchGetsRefresh(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, client.Options{})
	ctx := testCtx(t)
	if _, err := client.Call[struct{}](ctx, c, "Handlers.Add", []any{"apple"}); err != nil {
		t.Fatal(err)
	}
	sub := client.Subscribe[[]Item](ctx, c, "Handlers.List", nil)
	defer sub.Close()
	recv(t, sub.C)
	if _, err := client.Call[struct{}](ctx, c, "Handlers.Rename", []any{1, "pear"}); err != nil {
		t.Fatal(err)
	}
	if got := recv(t, sub.C); got[0].Name != "pear" {
		t.Fatalf("after rename = %+v", got)
	}
}

func TestReconnectResubscribes(t *testing.T) {
	f := newFixture(t, func(s *aprot.Server) {
		s.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
			conn.SetUserID("u1")
			return nil
		})
	})
	var states []client.State
	var mu sync.Mutex
	c := f.dial(t, client.Options{
		ReconnectInterval: 10 * time.Millisecond,
		OnStateChange: func(s client.State) {
			mu.Lock()
			states = append(states, s)
			mu.Unlock()
		},
	})
	ctx := testCtx(t)

	sub := client.Subscribe[[]Item](ctx, c, "Handlers.List", nil)
	defer sub.Close()
	recv(t, sub.C)

	if n := f.server.DisconnectUser("u1"); n != 1 {
		t.Fatalf("DisconnectUser closed %d connections", n)
	}
	// The resubscribe after reconnect delivers a fresh result on the same C.
	recv(t, sub.C)

	// Calls work on the new connection, and refreshes reach the
	// resubscribed subscription.
	if _, err := client.Call[struct{}](ctx, c, "Handlers.Add", []any{"apple"}); err != nil {
		t.Fatalf("Add after reconnect: %v", err)
	}
	recvUntil(t, sub.C, func(v []Item) bool { return len(v) == 1 })

	mu.Lock()
	defer mu.Unlock()
	want := []client.State{client.StateConnected, client.StateConnecting, client.StateConnected}
	if len(states) < 3 || states[0] != want[0] || states[1] != want[1] || states[2] != want[2] {
		t.Fatalf("states = %v", states)
	}
}

func TestCallInFlightFailsOnDrop(t *testing.T) {
	f := newFixture(t, func(s *aprot.Server) {
		s.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
			conn.SetUserID("u1")
			return nil
		})
	})
	c := f.dial(t, client.Options{ReconnectInterval: 10 * time.Millisecond})
	ctx := testCtx(t)

	errc := make(chan error, 1)
	go func() {
		_, err := client.Call[string](ctx, c, "Handlers.Slow", nil)
		errc <- err
	}()
	recv(t, f.handlers.slowStart)
	f.server.DisconnectUser("u1")
	if err := <-errc; !errors.Is(err, client.ErrConnectionLost) {
		t.Fatalf("got %v, want ErrConnectionLost", err)
	}
}

func TestAuth(t *testing.T) {
	setup := func(s *aprot.Server) {
		s.OnAuth(func(ctx context.Context, conn *aprot.Conn, token string) error {
			if token != "good" {
				return aprot.ErrAuthFailed("bad token")
			}
			return nil
		})
	}
	f := newFixture(t, setup)

	c := f.dial(t, client.Options{AuthToken: func(context.Context) (string, error) { return "good", nil }})
	if _, err := client.Call[[]Item](testCtx(t), c, "Handlers.List", nil); err != nil {
		t.Fatalf("List after auth: %v", err)
	}
	if err := c.RefreshAuth(testCtx(t), "bad"); !client.HasCode(err, client.CodeAuthFailed) {
		t.Fatalf("RefreshAuth(bad) = %v, want CodeAuthFailed", err)
	}
	// A failed refresh keeps the session.
	if _, err := client.Call[[]Item](testCtx(t), c, "Handlers.List", nil); err != nil {
		t.Fatalf("List after failed refresh: %v", err)
	}
	if err := c.RefreshAuth(testCtx(t), "good"); err != nil {
		t.Fatalf("RefreshAuth(good) = %v", err)
	}

	_, err := client.Dial(testCtx(t), f.url, client.Options{AuthToken: func(context.Context) (string, error) { return "bad", nil }})
	if !client.HasCode(err, client.CodeAuthFailed) {
		t.Fatalf("Dial with bad token = %v, want CodeAuthFailed", err)
	}
}

func TestConnectRejected(t *testing.T) {
	f := newFixture(t, func(s *aprot.Server) {
		s.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
			return aprot.ErrConnectionRejected("go away")
		})
	})
	_, err := client.Dial(testCtx(t), f.url, client.Options{})
	if !client.HasCode(err, client.CodeConnectionRejected) {
		t.Fatalf("Dial = %v, want CodeConnectionRejected", err)
	}
}

func TestStream(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, client.Options{})

	s := client.Stream[int](testCtx(t), c, "Handlers.Count", []any{5})
	var got []int
	for v := range s.All() {
		got = append(got, v)
	}
	if err := s.Err(); err != nil {
		t.Fatalf("Err = %v", err)
	}
	if len(got) != 5 || got[4] != 4 {
		t.Fatalf("got %v", got)
	}

	p := client.Stream2[string, int](testCtx(t), c, "Handlers.Pairs", nil)
	pairs := map[string]int{}
	for k, v := range p.All() {
		pairs[k] = v
	}
	if p.Err() != nil || pairs["a"] != 1 || pairs["b"] != 2 {
		t.Fatalf("pairs = %v, err = %v", pairs, p.Err())
	}
}

func TestStreamEarlyBreakCancels(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, client.Options{})

	s := client.Stream[int](testCtx(t), c, "Handlers.Endless", nil)
	n := 0
	for range s.All() {
		n++
		if n == 3 {
			break
		}
	}
	if s.Err() != nil {
		t.Fatalf("Err after break = %v", s.Err())
	}
	if got := recv(t, f.handlers.canceled); got != "endless" {
		t.Fatalf("server saw %q", got)
	}
}

func TestStreamContextCancel(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, client.Options{})
	ctx, cancel := context.WithCancel(testCtx(t))
	defer cancel()

	s := client.Stream[int](ctx, c, "Handlers.Endless", nil)
	for range s.All() {
		cancel()
	}
	if !errors.Is(s.Err(), context.Canceled) {
		t.Fatalf("Err = %v, want context.Canceled", s.Err())
	}
}

func TestBlobOverWebSocket(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, client.Options{})
	b, err := client.Call[client.Blob](testCtx(t), c, "Handlers.Image", nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.ContentType != "image/png" || string(b.Data) != string([]byte{0, 1, 2, 255}) {
		t.Fatalf("blob = %+v", b)
	}
}

func TestPush(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, client.Options{})

	got := make(chan ItemAdded, 4)
	remove := client.OnPush(c, "ItemAdded", func(e ItemAdded) {
		// A push handler may call back into the client.
		if _, err := client.Call[[]Item](context.Background(), c, "Handlers.List", nil); err != nil {
			t.Errorf("call from push handler: %v", err)
		}
		got <- e
	})
	if _, err := client.Call[struct{}](testCtx(t), c, "Handlers.Broadcast", []any{"hello"}); err != nil {
		t.Fatal(err)
	}
	if e := recv(t, got); e.Name != "hello" {
		t.Fatalf("push = %+v", e)
	}
	remove()
	remove()
}

func TestCloseEndsEverything(t *testing.T) {
	f := newFixture(t)
	ctx := testCtx(t)
	c, err := client.Dial(ctx, f.url, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sub := client.Subscribe[[]Item](ctx, c, "Handlers.List", nil)
	recv(t, sub.C)

	errc := make(chan error, 1)
	go func() {
		_, err := client.Call[string](ctx, c, "Handlers.Slow", nil)
		errc <- err
	}()
	recv(t, f.handlers.slowStart)

	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, sub.C)
	if !errors.Is(sub.Err(), client.ErrClosed) {
		t.Fatalf("sub.Err = %v, want ErrClosed", sub.Err())
	}
	if err := <-errc; !errors.Is(err, client.ErrClosed) {
		t.Fatalf("call = %v, want ErrClosed", err)
	}
	if c.State() != client.StateClosed || !errors.Is(c.Err(), client.ErrClosed) {
		t.Fatalf("state = %v, err = %v", c.State(), c.Err())
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("Done not closed")
	}

	// Everything after Close fails at once.
	if _, err := client.Call[[]Item](ctx, c, "Handlers.List", nil); !errors.Is(err, client.ErrClosed) {
		t.Fatalf("Call after Close = %v", err)
	}
	late := client.Subscribe[[]Item](ctx, c, "Handlers.List", nil)
	waitClosed(t, late.C)
	if !errors.Is(late.Err(), client.ErrClosed) {
		t.Fatalf("Subscribe after Close: Err = %v", late.Err())
	}
}

// Close racing deliveries must not panic with a send on a closed channel.
// Run with -race.
func TestSubscriptionCloseRacesDelivery(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, client.Options{})
	ctx := testCtx(t)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			f.server.TriggerRefresh("items")
			// Enough refreshes to keep deliveries racing Close, without
			// starving a slow CI runner.
			time.Sleep(100 * time.Microsecond)
		}
	}()
	for range 30 {
		sub := client.Subscribe[[]Item](ctx, c, "Handlers.List", nil)
		recv(t, sub.C)
		sub.Close()
	}
	close(stop)
	wg.Wait()
}

func TestDialStream(t *testing.T) {
	f := newFixture(t)
	ctx := testCtx(t)
	dials := 0
	c, err := client.DialStream(ctx, func(ctx context.Context) (io.ReadWriteCloser, error) {
		dials++
		a, b := net.Pipe()
		go func() { _ = f.server.ServeStream(context.Background(), b, aprot.ConnInfo{}) }()
		return a, nil
	}, client.Options{ReconnectInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	sub := client.Subscribe[[]Item](ctx, c, "Handlers.List", nil)
	defer sub.Close()
	recv(t, sub.C)
	if _, err := client.Call[struct{}](ctx, c, "Handlers.Add", []any{"apple"}); err != nil {
		t.Fatal(err)
	}
	recvUntil(t, sub.C, func(v []Item) bool { return len(v) == 1 })

	// Blob results arrive as the JSON envelope on a byte stream.
	b, err := client.Call[client.Blob](ctx, c, "Handlers.Image", nil)
	if err != nil || b.ContentType != "image/png" || len(b.Data) != 4 {
		t.Fatalf("blob = %+v, %v", b, err)
	}

	s := client.Stream[int](ctx, c, "Handlers.Count", []any{3})
	n := 0
	for range s.All() {
		n++
	}
	if n != 3 || s.Err() != nil {
		t.Fatalf("stream: %d items, err %v", n, s.Err())
	}
}

func TestNoReconnect(t *testing.T) {
	f := newFixture(t, func(s *aprot.Server) {
		s.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
			conn.SetUserID("u1")
			return nil
		})
	})
	c := f.dial(t, client.Options{NoReconnect: true})
	sub := client.Subscribe[[]Item](testCtx(t), c, "Handlers.List", nil)
	recv(t, sub.C)
	f.server.DisconnectUser("u1")
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("client did not stop")
	}
	if !errors.Is(c.Err(), client.ErrConnectionLost) {
		t.Fatalf("Err = %v, want ErrConnectionLost", c.Err())
	}
	waitClosed(t, sub.C)
}
