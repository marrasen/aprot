package aprot

import (
	"context"
	"iter"
	"sync/atomic"
	"testing"
	"time"
)

// Regression tests for request registration: duplicate in-flight IDs (#225),
// frames rejected before they run, and cancels that land before the
// principal provider returns.

// registrationHandlers block until released or canceled, so a test can hold a
// request in flight and watch what happens to it.
type registrationHandlers struct {
	started  chan string
	finished chan string
	gate     chan struct{}
}

func (h *registrationHandlers) Slow(ctx context.Context) (string, error) {
	h.started <- "slow"
	defer func() { h.finished <- "slow" }()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-h.gate:
		return "late", nil
	}
}

func (h *registrationHandlers) List(ctx context.Context) ([]string, error) {
	RegisterRefreshTrigger(ctx, "k")
	h.started <- "list"
	defer func() { h.finished <- "list" }()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-h.gate:
		return []string{"a"}, nil
	}
}

func (h *registrationHandlers) Feed(ctx context.Context) (iter.Seq[int], error) {
	return func(yield func(int) bool) {}, nil
}

func newRegistrationConn(t *testing.T, opts ...ServerOptions) (*Server, *Conn, *registrationHandlers, *recordingTransport) {
	t.Helper()
	h := &registrationHandlers{
		started:  make(chan string, 8),
		finished: make(chan string, 8),
		gate:     make(chan struct{}),
	}
	reg := NewRegistry()
	reg.Register(h)
	s := NewServer(reg, opts...)
	rt := &recordingTransport{}
	c := newConn(rt, s, atomic.AddUint64(&s.nextConnID, 1), ConnInfo{}, context.Background())
	c.authenticated.Store(true)
	return s, c, h, rt
}

func waitSignal(t *testing.T, ch <-chan string, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for handler to %s", what)
	}
}

// waitIdleOrRelease waits for every request to finish. If they have not
// finished within d, it releases the blocked handlers so the test can end, and
// fails.
func waitIdleOrRelease(t *testing.T, s *Server, h *registrationHandlers, d time.Duration, failMsg string) {
	t.Helper()
	done := make(chan struct{})
	go func() { s.requestsWg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		close(h.gate)
		<-done
		t.Fatal(failMsg)
	}
}

type errorFrame struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Code int    `json:"code"`
}

func errorFrames(t *testing.T, rt *recordingTransport) []errorFrame {
	t.Helper()
	var out []errorFrame
	for _, m := range rt.Messages() {
		var f errorFrame
		if err := unmarshalJSON(m, &f); err != nil {
			t.Fatalf("decode frame %s: %v", m, err)
		}
		if f.Type == string(TypeError) {
			out = append(out, f)
		}
	}
	return out
}

// startDuplicateID runs two requests with the same ID and waits for the
// shadowed first one to unwind, so its deferred unregister has run.
func startDuplicateID(t *testing.T, c *Conn, h *registrationHandlers) {
	t.Helper()
	c.handleIncomingMessage([]byte(`{"type":"request","id":"r","method":"registrationHandlers.Slow"}`))
	waitSignal(t, h.started, "start the first request")
	c.handleIncomingMessage([]byte(`{"type":"request","id":"r","method":"registrationHandlers.Slow"}`))
	waitSignal(t, h.started, "start the replacement request")
	waitShadowedUnwound(t, c, h, 1)
}

// waitShadowedUnwound waits until a shadowed request has fully unwound,
// leaving `running` requests on c. The handler's finished signal fires before
// dispatch's deferred unregister, so it also waits for the request slot,
// which is released only after the unregister has run.
func waitShadowedUnwound(t *testing.T, c *Conn, h *registrationHandlers, running int) {
	t.Helper()
	waitSignal(t, h.finished, "unwind the shadowed request")
	eventually(t, 3*time.Second, func() bool { return len(c.reqSem) == running })
}

// The shadowed request's unwind must leave the replacement registered, so it
// is still counted and a cancel frame still reaches it. (#225: the guard
// compared cancel funcs by code pointer, which every CancelCauseFunc shares.)
func TestDuplicateRequestID_ReplacementStaysCancelable(t *testing.T) {
	s, c, h, _ := newRegistrationConn(t)
	startDuplicateID(t, c, h)

	if n := c.InFlightRequests(); n != 1 {
		t.Errorf("InFlightRequests() = %d after the shadowed request unwound, want 1 (the replacement)", n)
	}

	c.handleIncomingMessage([]byte(`{"type":"cancel","id":"r"}`))
	waitIdleOrRelease(t, s, h, 2*time.Second, "cancel frame did not reach the replacement request")
}

// Closing the connection — what Server.Stop does to every connection before
// it waits for in-flight requests — must cancel the replacement of a
// duplicate ID. Otherwise Stop stalls until its context deadline.
func TestDuplicateRequestID_CloseCancelsReplacement(t *testing.T) {
	s, c, h, _ := newRegistrationConn(t)
	startDuplicateID(t, c, h)

	c.closeGracefully()
	waitIdleOrRelease(t, s, h, 2*time.Second, "closing the connection did not cancel the replacement request")
}

// A refresh F of subscription s is shadowed by a client re-subscribe R of s.
// F's unwind must not delete R's entry; otherwise an unsubscribe cannot cancel
// R, and R registers the subscription the client just dropped.
func TestRefreshUnwindDoesNotLeakResubscribe(t *testing.T) {
	s, c, h, _ := newRegistrationConn(t)

	// The first subscribe completes and registers subscription "s".
	c.handleIncomingMessage([]byte(`{"type":"subscribe","id":"s","method":"registrationHandlers.List"}`))
	waitSignal(t, h.started, "start the subscribe")
	h.gate <- struct{}{}
	waitSignal(t, h.finished, "finish the subscribe")
	waitIdle(t, s)
	if n := s.Stats().Subscriptions; n != 1 {
		t.Fatalf("subscriptions = %d, want 1", n)
	}

	// A server-driven refresh F starts and blocks.
	s.TriggerRefresh("k")
	waitSignal(t, h.started, "start the refresh")

	// The client re-subscribes with the same ID: R registers and cancels F.
	c.handleIncomingMessage([]byte(`{"type":"subscribe","id":"s","method":"registrationHandlers.List"}`))
	waitSignal(t, h.started, "start the re-subscribe")
	waitSignal(t, h.finished, "unwind the shadowed refresh")
	// The handler's finished signal fires just before the refresh's deferred
	// unregister. The refresh holds no request slot to wait on, and with the
	// fix its unregister changes nothing observable, so give it a moment.
	time.Sleep(100 * time.Millisecond)

	// The client unsubscribes; that must cancel R.
	c.handleIncomingMessage([]byte(`{"type":"unsubscribe","id":"s"}`))
	waitIdleOrRelease(t, s, h, 2*time.Second, "unsubscribe did not cancel the re-subscribe")

	if n := s.Stats().Subscriptions; n != 0 {
		t.Fatalf("server holds %d subscription(s) after unsubscribe", n)
	}
}

// A frame that cannot run must not register: registering cancels the
// in-flight request with the same ID. It still gets its error frame and its
// observer event.
func TestRejectedFrameDoesNotCancelInflightRequest(t *testing.T) {
	cases := []struct {
		name      string
		frame     string
		subscribe bool
		code      int
	}{
		{"unknown request method", `{"type":"request","id":"r","method":"Nope.Nope"}`, false, CodeMethodNotFound},
		{"unknown subscribe method", `{"type":"subscribe","id":"r","method":"Nope.Nope"}`, true, CodeMethodNotFound},
		{"subscribe to streaming handler", `{"type":"subscribe","id":"r","method":"registrationHandlers.Feed"}`, true, CodeInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := newRecordingObserver()
			s, c, h, rt := newRegistrationConn(t, ServerOptions{Observer: obs})
			c.handleIncomingMessage([]byte(`{"type":"request","id":"r","method":"registrationHandlers.Slow"}`))
			waitSignal(t, h.started, "start the in-flight request")

			c.handleIncomingMessage([]byte(tc.frame))

			if n := c.InFlightRequests(); n != 1 {
				t.Errorf("InFlightRequests() = %d after the rejected frame, want 1", n)
			}
			close(h.gate)
			waitIdle(t, s)

			frames := errorFrames(t, rt)
			if len(frames) != 1 || frames[0].ID != "r" || frames[0].Code != tc.code {
				t.Fatalf("error frames = %+v, want exactly one with code %d (the in-flight request must complete normally)", frames, tc.code)
			}
			var rejected []RequestEvent
			for _, e := range obs.snapshotRequests() {
				if e.Code == tc.code {
					rejected = append(rejected, e)
				}
			}
			if len(rejected) != 1 || rejected[0].Subscribe != tc.subscribe {
				t.Fatalf("observer events = %+v, want one with code %d and Subscribe=%v", obs.snapshotRequests(), tc.code, tc.subscribe)
			}
		})
	}
}

// The handleSubscribe test wrapper releases the caller's requestsWg slot when
// it rejects the frame, the same as when the subscribe runs.
func TestHandleSubscribeRejectReleasesWaitGroup(t *testing.T) {
	s, c, _, rt := newRegistrationConn(t)
	s.requestsWg.Add(1)
	c.handleSubscribe(IncomingMessage{Type: TypeSubscribe, ID: "x", Method: "Nope.Nope"})
	waitIdle(t, s)
	if frames := errorFrames(t, rt); len(frames) != 1 || frames[0].Code != CodeMethodNotFound {
		t.Fatalf("error frames = %+v, want one CodeMethodNotFound", frames)
	}
}

// A cancel that lands while the principal provider runs is a cancel, even when
// the provider returns ctx.Err(): the client gets CodeCanceled, and so does the
// observer, rather than an internal error.
func TestPrincipalProviderErrorAfterCancelReportsCanceled(t *testing.T) {
	cases := []struct {
		name      string
		frame     string
		subscribe bool
	}{
		{"request", `{"type":"request","id":"r","method":"registrationHandlers.Slow"}`, false},
		{"subscribe", `{"type":"subscribe","id":"r","method":"registrationHandlers.List"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := newRecordingObserver()
			s, c, _, rt := newRegistrationConn(t, ServerOptions{Observer: obs})
			gate := make(chan struct{})
			c.SetPrincipalProvider(func(ctx context.Context) (any, error) {
				<-gate
				return nil, ctx.Err()
			})
			c.handleIncomingMessage([]byte(tc.frame))
			c.handleIncomingMessage([]byte(`{"type":"cancel","id":"r"}`))
			close(gate)
			waitIdle(t, s)

			frames := errorFrames(t, rt)
			if len(frames) != 1 || frames[0].Code != CodeCanceled {
				t.Fatalf("error frames = %+v, want one CodeCanceled", frames)
			}
			events := obs.snapshotRequests()
			if len(events) != 1 || events[0].Code != CodeCanceled || events[0].Subscribe != tc.subscribe {
				t.Fatalf("observer events = %+v, want one CodeCanceled", events)
			}
		})
	}
}
