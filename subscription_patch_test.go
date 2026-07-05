package aprot

import (
	"context"
	"math"
	"testing"
)

// patchTestHandlers provides a subscribable query for fallback-refresh tests.
type patchTestHandlers struct{}

type patchTestList struct {
	Items []string `json:"items"`
}

func (patchTestHandlers) GetList(ctx context.Context) (*patchTestList, error) {
	RegisterRefreshTrigger(ctx, "items")
	return &patchTestList{Items: []string{"a", "b"}}, nil
}

// newPatchTestConn builds a server-backed conn on a recording transport.
func newPatchTestConn(t *testing.T) (*Server, *Conn, *recordingTransport) {
	t.Helper()
	r := NewRegistry()
	r.Register(&patchTestHandlers{})
	s := NewServer(r)
	rt := &recordingTransport{}
	c := &Conn{
		transport: rt,
		server:    s,
		requests:  make(map[string]context.CancelCauseFunc),
		id:        1,
	}
	return s, c, rt
}

// subscribeForPatch runs a real subscribe flow, asserts the initial response,
// and returns the number of frames recorded so far (so callers can slice off
// only the frames produced after the subscribe).
func subscribeForPatch(t *testing.T, c *Conn, rt *recordingTransport, id string, wantsPatch bool) int {
	t.Helper()
	before := len(drainMessages(t, rt))
	c.server.requestsWg.Add(1)
	c.handleSubscribe(IncomingMessage{
		Type:   TypeSubscribe,
		ID:     id,
		Method: "patchTestHandlers.GetList",
		Patch:  wantsPatch,
	})
	msgs := drainMessages(t, rt)
	if len(msgs) != before+1 || msgs[len(msgs)-1]["type"] != "response" {
		t.Fatalf("subscribe: expected 1 new response, got %v", msgs[before:])
	}
	return len(msgs)
}

// messagesAfter returns the decoded frames recorded after the first n.
func messagesAfter(t *testing.T, rt *recordingTransport, n int) []map[string]any {
	t.Helper()
	return drainMessages(t, rt)[n:]
}

func TestPatchSubscription_SendsPatchFrameToOptedInSubscriber(t *testing.T) {
	s, c, rt := newPatchTestConn(t)
	n := subscribeForPatch(t, c, rt, "sub-1", true)

	if err := s.PatchSubscription(map[string]any{"id": "a", "rating": 3}, "items"); err != nil {
		t.Fatalf("PatchSubscription: %v", err)
	}
	s.requestsWg.Wait()

	msgs := messagesAfter(t, rt, n)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 patch frame, got %d: %v", len(msgs), msgs)
	}
	if msgs[0]["type"] != "subscription_patch" {
		t.Fatalf("type = %v, want subscription_patch", msgs[0]["type"])
	}
	if msgs[0]["id"] != "sub-1" {
		t.Fatalf("id = %v, want sub-1", msgs[0]["id"])
	}
	patch, ok := msgs[0]["patch"].(map[string]any)
	if !ok {
		t.Fatalf("patch missing or wrong shape: %v", msgs[0]["patch"])
	}
	if patch["id"] != "a" || patch["rating"] != float64(3) {
		t.Fatalf("unexpected patch payload: %v", patch)
	}
}

func TestPatchSubscription_FallsBackToRefreshWithoutPatchSupport(t *testing.T) {
	s, c, rt := newPatchTestConn(t)
	n := subscribeForPatch(t, c, rt, "sub-1", false)

	if err := s.PatchSubscription(map[string]any{"id": "a"}, "items"); err != nil {
		t.Fatalf("PatchSubscription: %v", err)
	}
	s.requestsWg.Wait()

	msgs := messagesAfter(t, rt, n)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 refresh response, got %d: %v", len(msgs), msgs)
	}
	if msgs[0]["type"] != "response" {
		t.Fatalf("type = %v, want response (full refresh fallback)", msgs[0]["type"])
	}
	if msgs[0]["id"] != "sub-1" {
		t.Fatalf("id = %v, want sub-1", msgs[0]["id"])
	}
}

func TestPatchSubscription_ResubscribeUpdatesPatchSupport(t *testing.T) {
	s, c, rt := newPatchTestConn(t)
	subscribeForPatch(t, c, rt, "sub-1", false)
	// Re-subscribe with the same ID, now declaring patch support.
	n := subscribeForPatch(t, c, rt, "sub-1", true)

	if err := s.PatchSubscription(map[string]any{"id": "a"}, "items"); err != nil {
		t.Fatalf("PatchSubscription: %v", err)
	}
	s.requestsWg.Wait()

	msgs := messagesAfter(t, rt, n)
	if len(msgs) != 1 || msgs[0]["type"] != "subscription_patch" {
		t.Fatalf("expected subscription_patch after re-subscribe upgrade, got %v", msgs)
	}
}

func TestPatchSubscription_MarshalErrorReturned(t *testing.T) {
	s, c, rt := newPatchTestConn(t)
	n := subscribeForPatch(t, c, rt, "sub-1", true)

	if err := s.PatchSubscription(math.NaN(), "items"); err == nil {
		t.Fatal("expected marshal error for NaN patch, got nil")
	}
	s.requestsWg.Wait()
	if msgs := messagesAfter(t, rt, n); len(msgs) != 0 {
		t.Fatalf("expected no frames after marshal failure, got %v", msgs)
	}
}

func TestPatchSubscription_NoSubscribers(t *testing.T) {
	s, _, _ := newPatchTestConn(t)
	if err := s.PatchSubscription(map[string]any{"id": "a"}, "items"); err != nil {
		t.Fatalf("PatchSubscription with no subscribers: %v", err)
	}
}

func TestPatchSubscriptionCtx_DeliversViaRequestContext(t *testing.T) {
	s, c, rt := newPatchTestConn(t)
	n := subscribeForPatch(t, c, rt, "sub-1", true)

	ctx := withRefreshQueue(context.Background(), &refreshQueue{server: s})
	if err := PatchSubscription(ctx, map[string]any{"id": "a"}, "items"); err != nil {
		t.Fatalf("PatchSubscription(ctx): %v", err)
	}
	s.requestsWg.Wait()

	msgs := messagesAfter(t, rt, n)
	if len(msgs) != 1 || msgs[0]["type"] != "subscription_patch" {
		t.Fatalf("expected subscription_patch, got %v", msgs)
	}
}

func TestPatchSubscriptionCtx_NoOpOutsideRequest(t *testing.T) {
	// No refresh queue in context (e.g. subscription re-execution, or a
	// background goroutine): must be a silent no-op, mirroring TriggerRefresh.
	if err := PatchSubscription(context.Background(), map[string]any{"id": "a"}, "items"); err != nil {
		t.Fatalf("expected nil error outside request context, got %v", err)
	}
}
