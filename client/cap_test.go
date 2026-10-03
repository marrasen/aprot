package client

// Tests for the cap on unanswered subscribe frames (maxSubscribesInFlight),
// run against a fake connection so the frames on the wire can be counted.

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

type fakeConn struct {
	in     chan []byte
	closed chan struct{}
	once   sync.Once

	mu  sync.Mutex
	out []outFrame
	sig chan struct{}
}

func newFakeConn() *fakeConn {
	fc := &fakeConn{in: make(chan []byte, 1<<16), closed: make(chan struct{}), sig: make(chan struct{}, 1)}
	fc.in <- []byte(`{"type":"config"}`)
	return fc
}

func (f *fakeConn) read() ([]byte, bool, error) {
	select {
	case d := <-f.in:
		return d, false, nil
	case <-f.closed:
		return nil, false, io.EOF
	}
}

func (f *fakeConn) write(data []byte) error {
	select {
	case <-f.closed:
		return io.ErrClosedPipe
	default:
	}
	var o outFrame
	if err := json.Unmarshal(data, &o); err != nil {
		panic(err)
	}
	f.mu.Lock()
	f.out = append(f.out, o)
	f.mu.Unlock()
	select {
	case f.sig <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakeConn) close() error { f.once.Do(func() { close(f.closed) }); return nil }

func (f *fakeConn) take() []outFrame {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := f.out
	f.out = nil
	return o
}

func (f *fakeConn) send(s string) {
	select {
	case f.in <- []byte(s):
	case <-f.closed:
	}
}

func fakeDial(t *testing.T) (*Client, chan *fakeConn) {
	conns := make(chan *fakeConn, 100)
	c, err := start(context.Background(), Options{ReconnectInterval: time.Millisecond, ReconnectMaxInterval: time.Millisecond}, func(ctx context.Context) (wireConn, error) {
		fc := newFakeConn()
		conns <- fc
		return fc, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, conns
}

// 100 subscriptions put only 64 frames on the wire. Each answer sends one
// more, and a subscription closed while queued is never sent.
func TestSubscribeCapQueuesAndSkipsClosed(t *testing.T) {
	c, conns := fakeDial(t)
	defer c.Close()
	fc := <-conns
	subs := make([]*Subscription[int], 100)
	for i := range subs {
		subs[i] = Subscribe[int](context.Background(), c, "M", nil)
	}
	got := fc.take()
	if len(got) != 64 {
		t.Fatalf("sent %d, want 64", len(got))
	}
	subs[64].Close() // queued
	fc.send(fmt.Sprintf(`{"type":"response","id":%q,"result":1}`, got[0].ID))
	<-subs[0].C
	time.Sleep(20 * time.Millisecond)
	more := fc.take()
	var subsN int
	for _, f := range more {
		if f.Type == "subscribe" {
			subsN++
			if f.ID == subs[64].subID {
				t.Fatalf("closed queued sub was sent")
			}
		}
	}
	if subsN != 1 {
		t.Fatalf("after one answer sent %d subscribes (%v)", subsN, more)
	}
	c.mu.Lock()
	n := c.subInFlight
	c.mu.Unlock()
	if n != 64 {
		t.Fatalf("subInFlight = %d", n)
	}
}

// A patch that arrives before a subscription's first result makes the
// client fetch the full result again. The re-send must wait for the first
// answer and reuse its slot: sent at once, the server would answer both
// frames, the first answer would free the slot early, and a queued
// subscribe would make 65 frames unanswered at the server.
func TestSubscribeCapPatchResendWaitsForAnswer(t *testing.T) {
	c, conns := fakeDial(t)
	defer c.Close()
	fc := <-conns
	subs := make([]*Subscription[int], 65)
	for i := range subs {
		subs[i] = Subscribe[int](context.Background(), c, "M", nil,
			WithPatch(func(cur int, _ jsontext.Value) (int, error) { return cur, nil }))
	}
	outstanding := map[string]int{}
	count := func() {
		for _, f := range fc.take() {
			if f.Type == "subscribe" {
				outstanding[f.ID]++
			}
		}
	}
	count()
	if len(outstanding) != 64 {
		t.Fatalf("outstanding %d", len(outstanding))
	}
	id := subs[0].subID
	// A refresh patch lands between the server registering sub 0 and sending
	// its first result (the documented "patch before the full result" case).
	fc.send(fmt.Sprintf(`{"type":"subscription_patch","id":%q,"patch":[]}`, id))
	fc.send(fmt.Sprintf(`{"type":"response","id":%q,"result":1}`, id))
	<-subs[0].C
	time.Sleep(20 * time.Millisecond)
	count()
	outstanding[id]-- // the server answered the first frame only
	n := 0
	for _, v := range outstanding {
		if v > 0 {
			n++
		}
	}
	if n > maxSubscribesInFlight {
		t.Fatalf("%d subscribe frames unanswered at the server; cap is %d", n, maxSubscribesInFlight)
	}
}
