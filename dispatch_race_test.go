package aprot

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type dispatchRaceHandlers struct {
	started chan struct{}
	release chan struct{}
}

func (h *dispatchRaceHandlers) List(ctx context.Context) ([]string, error) {
	RegisterRefreshTrigger(ctx, "list")
	h.started <- struct{}{}
	<-h.release
	return []string{"a"}, nil
}

func (h *dispatchRaceHandlers) Slow(ctx context.Context) (string, error) {
	h.started <- struct{}{}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-h.release:
		return "late", nil
	}
}

// An unsubscribe right behind its subscribe must not be lost. The read loop
// handles unsubscribe at once, while the subscribe runs on its own goroutine;
// the request has to be registered before that goroutine starts, or the
// unsubscribe finds nothing and the subscription is registered anyway.
func TestUnsubscribeRightAfterSubscribe(t *testing.T) {
	h := &dispatchRaceHandlers{started: make(chan struct{}, 1), release: make(chan struct{})}
	reg := NewRegistry()
	reg.Register(h)
	s := NewServer(reg)
	rt := &recordingTransport{}
	c := newConn(rt, s, atomic.AddUint64(&s.nextConnID, 1), ConnInfo{}, context.Background())
	c.authenticated.Store(true)

	c.handleIncomingMessage([]byte(`{"type":"subscribe","id":"s1","method":"dispatchRaceHandlers.List"}`))
	c.handleIncomingMessage([]byte(`{"type":"unsubscribe","id":"s1"}`))
	<-h.started
	close(h.release)
	waitIdle(t, s)

	if n := s.Stats().Subscriptions; n != 0 {
		t.Fatalf("server holds %d subscription(s) after subscribe+unsubscribe", n)
	}
}

// The same window applied to a cancel right behind its request: the cancel
// was lost and the handler ran to completion.
func TestCancelRightAfterRequest(t *testing.T) {
	h := &dispatchRaceHandlers{started: make(chan struct{}, 1), release: make(chan struct{})}
	reg := NewRegistry()
	reg.Register(h)
	s := NewServer(reg)
	rt := &recordingTransport{}
	c := newConn(rt, s, atomic.AddUint64(&s.nextConnID, 1), ConnInfo{}, context.Background())
	c.authenticated.Store(true)

	c.handleIncomingMessage([]byte(`{"type":"request","id":"r1","method":"dispatchRaceHandlers.Slow"}`))
	c.handleIncomingMessage([]byte(`{"type":"cancel","id":"r1"}`))
	<-h.started
	waitIdle(t, s)
	close(h.release)

	msgs := rt.Messages()
	if len(msgs) != 1 {
		t.Fatalf("got %d frames, want 1", len(msgs))
	}
	var frame struct {
		Type string `json:"type"`
		Code int    `json:"code"`
	}
	if err := unmarshalJSON(msgs[0], &frame); err != nil || frame.Type != "error" || frame.Code != CodeCanceled {
		t.Fatalf("frame = %s, want a canceled error", msgs[0])
	}
}

func waitIdle(t *testing.T, s *Server) {
	t.Helper()
	done := make(chan struct{})
	go func() { s.requestsWg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("requests did not finish")
	}
}
