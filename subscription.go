package aprot

import (
	"context"
	"strings"
	"sync"

	"github.com/go-json-experiment/json/jsontext"
)

// triggerCollector collects trigger keys during handler execution.
type triggerCollector struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

// subscription represents an active client subscription.
type subscription struct {
	conn   *Conn
	id     string              // client-assigned subscription ID
	method string              // handler method name
	keys   map[string]struct{} // trigger keys this subscription depends on
	params jsontext.Value      // raw JSON params for server-driven re-execution
}

// subscriptionManager tracks all active subscriptions across connections.
type subscriptionManager struct {
	mu     sync.RWMutex
	byKey  map[string]map[*subscription]struct{} // triggerKey → subscriptions
	byConn map[uint64]map[string]*subscription   // connID → subID → subscription
}

func newSubscriptionManager() *subscriptionManager {
	return &subscriptionManager{
		byKey:  make(map[string]map[*subscription]struct{}),
		byConn: make(map[uint64]map[string]*subscription),
	}
}

func (sm *subscriptionManager) register(sub *subscription) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	connID := sub.conn.id

	// Add to conn index
	if sm.byConn[connID] == nil {
		sm.byConn[connID] = make(map[string]*subscription)
	}
	sm.byConn[connID][sub.id] = sub

	// Add to key index
	for key := range sub.keys {
		if sm.byKey[key] == nil {
			sm.byKey[key] = make(map[*subscription]struct{})
		}
		sm.byKey[key][sub] = struct{}{}
	}
}

func (sm *subscriptionManager) unregister(connID uint64, subID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	connSubs, ok := sm.byConn[connID]
	if !ok {
		return
	}
	sub, ok := connSubs[subID]
	if !ok {
		return
	}

	// Remove from key index
	for key := range sub.keys {
		if subs, ok := sm.byKey[key]; ok {
			delete(subs, sub)
			if len(subs) == 0 {
				delete(sm.byKey, key)
			}
		}
	}

	// Remove from conn index
	delete(connSubs, subID)
	if len(connSubs) == 0 {
		delete(sm.byConn, connID)
	}
}

func (sm *subscriptionManager) unregisterConn(connID uint64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	connSubs, ok := sm.byConn[connID]
	if !ok {
		return
	}

	for _, sub := range connSubs {
		for key := range sub.keys {
			if subs, ok := sm.byKey[key]; ok {
				delete(subs, sub)
				if len(subs) == 0 {
					delete(sm.byKey, key)
				}
			}
		}
	}

	delete(sm.byConn, connID)
}

func (sm *subscriptionManager) updateKeys(connID uint64, subID string, newKeys map[string]struct{}) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	connSubs, ok := sm.byConn[connID]
	if !ok {
		return
	}
	sub, ok := connSubs[subID]
	if !ok {
		return
	}

	// Remove old keys from index
	for key := range sub.keys {
		if subs, ok := sm.byKey[key]; ok {
			delete(subs, sub)
			if len(subs) == 0 {
				delete(sm.byKey, key)
			}
		}
	}

	// Set new keys
	sub.keys = newKeys

	// Add new keys to index
	for key := range newKeys {
		if sm.byKey[key] == nil {
			sm.byKey[key] = make(map[*subscription]struct{})
		}
		sm.byKey[key][sub] = struct{}{}
	}
}

// has returns true if a subscription exists for the given conn and sub ID.
func (sm *subscriptionManager) has(connID uint64, subID string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if connSubs, ok := sm.byConn[connID]; ok {
		_, exists := connSubs[subID]
		return exists
	}
	return false
}

// getSubscriptionsForKey returns all subscriptions for a trigger key.
func (sm *subscriptionManager) getSubscriptionsForKey(key string) []*subscription {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	subs, ok := sm.byKey[key]
	if !ok {
		return nil
	}

	result := make([]*subscription, 0, len(subs))
	for sub := range subs {
		result = append(result, sub)
	}
	return result
}

// refreshQueue collects trigger keys during a request handler execution.
// After the handler returns, collected keys are resolved to subscriptions
// and each is re-executed once (deduplicated by subscription identity).
//
// When server is set, TriggerRefreshNow can flush the queue immediately
// from within the handler. When server is nil, only batched processing
// (by the caller of processRefreshQueue) is supported.
type refreshQueue struct {
	mu     sync.Mutex
	keys   []string
	server *Server
}

func withRefreshQueue(ctx context.Context, rq *refreshQueue) context.Context {
	return context.WithValue(ctx, refreshQueueKey, rq)
}

// processRefreshQueue resolves queued trigger keys to unique subscriptions
// and re-executes each subscription handler in its own goroutine.
func (s *Server) processRefreshQueue(rq *refreshQueue) {
	rq.mu.Lock()
	keys := rq.keys
	rq.keys = nil
	rq.mu.Unlock()

	if len(keys) == 0 {
		return
	}

	seen := make(map[*subscription]struct{})
	var toRefresh []*subscription
	for _, key := range keys {
		for _, sub := range s.subscriptions.getSubscriptionsForKey(key) {
			if _, ok := seen[sub]; !ok {
				seen[sub] = struct{}{}
				toRefresh = append(toRefresh, sub)
			}
		}
	}

	for _, sub := range toRefresh {
		s.requestsWg.Add(1)
		go sub.conn.refreshSubscription(sub)
	}
}

// compositeKey joins variadic key parts with a null byte separator.
func compositeKey(keys ...string) string {
	return strings.Join(keys, "\x00")
}

// withTriggerCollector returns a context with a trigger collector.
func withTriggerCollector(ctx context.Context, tc *triggerCollector) context.Context {
	return context.WithValue(ctx, triggerCollectorKey, tc)
}

// RegisterRefreshTrigger registers trigger keys that this subscription depends on.
// When called from a regular (non-subscribe) request, this is a no-op.
// Keys are variadic strings that form a composite trigger key.
func RegisterRefreshTrigger(ctx context.Context, keys ...string) {
	tc, ok := ctx.Value(triggerCollectorKey).(*triggerCollector)
	if !ok || tc == nil {
		return
	}
	key := compositeKey(keys...)
	tc.mu.Lock()
	tc.keys[key] = struct{}{}
	tc.mu.Unlock()
}

// TriggerRefresh queues a refresh for all subscriptions matching the given keys.
// Called from mutation handlers to notify subscribed clients of data changes.
// Triggers are batched per-request and deduplicated by subscription when the
// request handler completes. This is a no-op outside a request context.
func TriggerRefresh(ctx context.Context, keys ...string) {
	rq, ok := ctx.Value(refreshQueueKey).(*refreshQueue)
	if !ok || rq == nil {
		return
	}
	key := compositeKey(keys...)
	rq.mu.Lock()
	rq.keys = append(rq.keys, key)
	rq.mu.Unlock()
}

// TriggerRefreshNow is like TriggerRefresh but flushes the refresh queue
// immediately instead of deferring until the handler returns. Use this in
// long-running handlers when you want subscribers to observe intermediate
// state transitions before the handler completes.
//
// TriggerRefreshNow flushes every key queued so far (including keys passed to
// prior TriggerRefresh calls), not just the keys in this call. Subsequent
// TriggerRefresh / TriggerRefreshNow calls start with an empty queue, so a
// handler can fire multiple rounds of refreshes by calling TriggerRefreshNow
// at each state transition.
//
// This is a no-op outside a request context and during subscription
// re-execution (cascading refreshes are prevented).
//
// Concurrency: triggered subscription handlers run in their own goroutines,
// concurrently with the rest of the calling handler. If the calling handler
// mutates shared state that the subscription handler reads, the subscription
// must return a defensive copy (or otherwise be safe to read without
// coordination) to avoid data races. The same applies to any response
// marshaling — a shared slice or map returned by the subscription may still
// be marshaled after the caller continues executing.
func TriggerRefreshNow(ctx context.Context, keys ...string) {
	rq, ok := ctx.Value(refreshQueueKey).(*refreshQueue)
	if !ok || rq == nil {
		return
	}
	key := compositeKey(keys...)
	rq.mu.Lock()
	rq.keys = append(rq.keys, key)
	server := rq.server
	rq.mu.Unlock()
	if server != nil {
		server.processRefreshQueue(rq)
	}
}
