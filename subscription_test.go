package aprot

import (
	"context"
	"sync"
	"testing"
)

func TestSubscriptionManager_RegisterAndRefresh(t *testing.T) {
	sm := newSubscriptionManager()

	conn := &Conn{id: 1, transport: &mockTransport{}}

	sub := &subscription{
		conn:   conn,
		id:     "sub-1",
		method: "Handlers.GetUserList",
		keys:   map[string]struct{}{"users": {}},
	}
	sm.register(sub)

	// Verify indexes
	sm.mu.RLock()
	if _, ok := sm.byKey["users"]; !ok {
		t.Fatal("subscription not indexed by key")
	}
	if _, ok := sm.byConn[1]["sub-1"]; !ok {
		t.Fatal("subscription not indexed by conn+id")
	}
	sm.mu.RUnlock()

	// Refresh should find the subscription
	refreshed := sm.getSubscriptionsForKey("users")
	if len(refreshed) != 1 {
		t.Fatalf("expected 1 subscription for key 'users', got %d", len(refreshed))
	}
}

func TestSubscriptionManager_Unregister(t *testing.T) {
	sm := newSubscriptionManager()
	conn := &Conn{id: 1}

	sub := &subscription{
		conn:   conn,
		id:     "sub-1",
		method: "Handlers.GetUserList",
		keys:   map[string]struct{}{"users": {}},
	}
	sm.register(sub)
	sm.unregister(1, "sub-1")

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if subs, ok := sm.byKey["users"]; ok && len(subs) > 0 {
		t.Fatal("subscription not removed from key index")
	}
	if subs, ok := sm.byConn[1]; ok && len(subs) > 0 {
		t.Fatal("subscription not removed from conn index")
	}
}

func TestSubscriptionManager_UnregisterConn(t *testing.T) {
	sm := newSubscriptionManager()
	conn := &Conn{id: 1}

	sm.register(&subscription{conn: conn, id: "sub-1", method: "A", keys: map[string]struct{}{"k1": {}}})
	sm.register(&subscription{conn: conn, id: "sub-2", method: "B", keys: map[string]struct{}{"k2": {}}})

	sm.unregisterConn(1)

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if _, ok := sm.byConn[1]; ok {
		t.Fatal("conn entry not removed")
	}
	if subs, ok := sm.byKey["k1"]; ok && len(subs) > 0 {
		t.Fatal("k1 subscriptions not cleaned up")
	}
	if subs, ok := sm.byKey["k2"]; ok && len(subs) > 0 {
		t.Fatal("k2 subscriptions not cleaned up")
	}
}

func TestSubscriptionManager_UpdateKeys(t *testing.T) {
	sm := newSubscriptionManager()
	conn := &Conn{id: 1}

	sub := &subscription{
		conn:   conn,
		id:     "sub-1",
		method: "Handlers.GetUserList",
		keys:   map[string]struct{}{"old-key": {}},
	}
	sm.register(sub)

	newKeys := map[string]struct{}{"new-key": {}}
	sm.updateKeys(1, "sub-1", newKeys)

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if subs, ok := sm.byKey["old-key"]; ok && len(subs) > 0 {
		t.Fatal("old key not removed")
	}
	if _, ok := sm.byKey["new-key"]; !ok {
		t.Fatal("new key not indexed")
	}
}

func TestSubscriptionManager_MultipleSubsSameKey(t *testing.T) {
	sm := newSubscriptionManager()
	conn1 := &Conn{id: 1}
	conn2 := &Conn{id: 2}

	sm.register(&subscription{conn: conn1, id: "s1", method: "A", keys: map[string]struct{}{"shared": {}}})
	sm.register(&subscription{conn: conn2, id: "s2", method: "A", keys: map[string]struct{}{"shared": {}}})

	refreshed := sm.getSubscriptionsForKey("shared")
	if len(refreshed) != 2 {
		t.Fatalf("expected 2 subscriptions, got %d", len(refreshed))
	}

	// Unregister one, should still have the other
	sm.unregister(1, "s1")
	refreshed = sm.getSubscriptionsForKey("shared")
	if len(refreshed) != 1 {
		t.Fatalf("expected 1 subscription after unregister, got %d", len(refreshed))
	}
}

func TestSubscriptionManager_ConcurrentAccess(t *testing.T) {
	sm := newSubscriptionManager()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conn := &Conn{id: uint64(i)}
			sm.register(&subscription{
				conn:   conn,
				id:     "s1",
				method: "A",
				keys:   map[string]struct{}{"key": {}},
			})
			sm.getSubscriptionsForKey("key")
			sm.unregister(uint64(i), "s1")
		}(i)
	}
	wg.Wait()
}

func TestCompositeKey(t *testing.T) {
	key := compositeKey("user", "123")
	if key != "user\x00123" {
		t.Fatalf("unexpected composite key: %q", key)
	}

	key = compositeKey("users")
	if key != "users" {
		t.Fatalf("unexpected single key: %q", key)
	}
}

func TestTriggerCollector(t *testing.T) {
	tc := &triggerCollector{keys: make(map[string]struct{})}
	ctx := context.WithValue(context.Background(), triggerCollectorKey, tc)

	// RegisterRefreshTrigger should add keys
	RegisterRefreshTrigger(ctx, "users")
	RegisterRefreshTrigger(ctx, "user", "123")

	if _, ok := tc.keys["users"]; !ok {
		t.Fatal("'users' key not registered")
	}
	if _, ok := tc.keys["user\x00123"]; !ok {
		t.Fatal("composite key not registered")
	}
}

func TestRegisterRefreshTrigger_NoCollector(t *testing.T) {
	// Should be a no-op when no collector in context
	ctx := context.Background()
	RegisterRefreshTrigger(ctx, "users") // should not panic
}

func TestTriggerRefresh_QueuesKeys(t *testing.T) {
	rq := &refreshQueue{}
	ctx := withRefreshQueue(context.Background(), rq)

	TriggerRefresh(ctx, "users")
	TriggerRefresh(ctx, "user", "123")

	rq.mu.Lock()
	defer rq.mu.Unlock()

	if len(rq.keys) != 2 {
		t.Fatalf("expected 2 queued keys, got %d", len(rq.keys))
	}
	if rq.keys[0] != "users" {
		t.Fatalf("expected first key 'users', got %q", rq.keys[0])
	}
	if rq.keys[1] != "user\x00123" {
		t.Fatalf("expected second key 'user\\x00123', got %q", rq.keys[1])
	}
}

func TestTriggerRefresh_NoQueue(t *testing.T) {
	// Should be a no-op when no refresh queue in context
	ctx := context.Background()
	TriggerRefresh(ctx, "users") // should not panic
}

func TestProcessRefreshQueue_Deduplication(t *testing.T) {
	sm := newSubscriptionManager()

	var sent int
	conn := &Conn{
		id:        1,
		transport: &mockTransport{onSend: func(data []byte) { sent++ }},
		requests:  make(map[string]context.CancelCauseFunc),
	}

	// One subscription watching two keys
	sm.register(&subscription{
		conn:   conn,
		id:     "sub-1",
		method: "Handlers.GetUserList",
		keys:   map[string]struct{}{"users": {}, "user\x00123": {}},
	})

	// Queue both keys — should resolve to one unique subscription
	rq := &refreshQueue{keys: []string{"users", "user\x00123"}}

	seen := make(map[*subscription]struct{})
	var toRefresh []*subscription
	rq.mu.Lock()
	keys := rq.keys
	rq.keys = nil
	rq.mu.Unlock()

	for _, key := range keys {
		for _, sub := range sm.getSubscriptionsForKey(key) {
			if _, ok := seen[sub]; !ok {
				seen[sub] = struct{}{}
				toRefresh = append(toRefresh, sub)
			}
		}
	}

	if len(toRefresh) != 1 {
		t.Fatalf("expected 1 unique subscription after dedup, got %d", len(toRefresh))
	}
}

// mockTransport for testing
type mockTransport struct {
	onSend func(data []byte)
}

func (m *mockTransport) Send(data []byte) error {
	if m.onSend != nil {
		m.onSend(data)
	}
	return nil
}
func (m *mockTransport) Close() error           { return nil }
func (m *mockTransport) CloseGracefully() error { return nil }
