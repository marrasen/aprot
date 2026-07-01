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

// register adds a subscription to the indexes. The ok result reports whether
// the subscription was added; limited reports that it was refused because the
// connection is already at its MaxSubscriptions cap (so the caller can reply
// with CodeTooManyRequests rather than treating it as a closed connection).
//
// It refuses connections that have already been closed, so a subscribe handler
// that races a disconnect cannot leave a subscription that outlives its
// connection (re-executing forever and writing to a dead transport). The closed
// check holds sm.mu while reading the conn's lock; this never nests against
// close(), which releases the conn lock before calling unregisterConn.
//
// The per-connection cap is enforced here, under sm.mu, so concurrent subscribe
// handlers on the same connection cannot race past the limit.
func (sm *subscriptionManager) register(sub *subscription) (ok bool, limited bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sub.conn.isClosed() {
		return false, false
	}

	connID := sub.conn.id

	// Enforce the per-connection subscription cap. A test conn may have no
	// server; that path is uncapped. Only a brand-new subscription ID counts —
	// re-registering an existing ID (the caller's update path) is never
	// rejected.
	if sub.conn.server != nil {
		if max := sub.conn.server.options.MaxSubscriptions; max > 0 {
			existing := sm.byConn[connID]
			if _, isReReg := existing[sub.id]; !isReReg && len(existing) >= max {
				return false, true
			}
		}
	}

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
	return true, false
}

func (sm *subscriptionManager) unregister(connID uint64, subID string) {
	sm.mu.Lock()

	connSubs, ok := sm.byConn[connID]
	if !ok {
		sm.mu.Unlock()
		return
	}
	sub, ok := connSubs[subID]
	if !ok {
		sm.mu.Unlock()
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
	sm.mu.Unlock()

	// Notify outside the lock so a slow observer can't stall subscription
	// bookkeeping for other connections.
	notifySubscriptionUnregistered(sub)
}

// notifySubscriptionUnregistered fires the observer's SubscriptionUnregistered
// event for sub, if an observer is registered. Safe to call with a subscription
// whose connection has no server (unit-test conns).
func notifySubscriptionUnregistered(sub *subscription) {
	if sub.conn == nil || sub.conn.server == nil {
		return
	}
	if o := sub.conn.server.observer; o != nil {
		o.SubscriptionUnregistered(sub.conn, sub.method, sub.id)
	}
}

func (sm *subscriptionManager) unregisterConn(connID uint64) {
	sm.mu.Lock()

	connSubs, ok := sm.byConn[connID]
	if !ok {
		sm.mu.Unlock()
		return
	}

	removed := make([]*subscription, 0, len(connSubs))
	for _, sub := range connSubs {
		removed = append(removed, sub)
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
	sm.mu.Unlock()

	// Notify outside the lock, once per subscription that was still active.
	for _, sub := range removed {
		notifySubscriptionUnregistered(sub)
	}
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

// updateParams replaces the stored raw params for a subscription. Called when
// a client re-subscribes with the same ID but different params, so that
// server-driven re-execution uses the latest params rather than stale ones.
func (sm *subscriptionManager) updateParams(connID uint64, subID string, params jsontext.Value) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if connSubs, ok := sm.byConn[connID]; ok {
		if sub, ok := connSubs[subID]; ok {
			sub.params = params
		}
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

// count returns the total number of active subscriptions across all
// connections. Used by Server.Stats for gauge-style metrics.
func (sm *subscriptionManager) count() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	n := 0
	for _, subs := range sm.byConn {
		n += len(subs)
	}
	return n
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
		subs := s.subscriptions.getSubscriptionsForKey(key)
		if s.observer != nil {
			s.observer.RefreshFanout(key, len(subs))
		}
		for _, sub := range subs {
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
// request handler completes. This is a no-op outside a request context — for
// background goroutines, cron jobs, or other out-of-request callers, use
// [Server.TriggerRefresh] instead.
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

// TriggerRefresh fires a refresh for all subscriptions matching the given keys,
// across every connection. Unlike the package-level [TriggerRefresh] — which
// batches within a request handler and flushes after the handler returns —
// this method flushes immediately and is safe to call from background
// goroutines, cron jobs, webhook fan-in, or any other out-of-request code path.
//
// Matching subscriptions are re-executed once, each in its own goroutine, the
// same way request-scoped triggers are dispatched. Cascading refreshes remain
// prevented because subscription re-execution runs without a refresh queue
// in its context (TriggerRefresh calls from inside a re-executed subscription
// handler are no-ops).
//
// Keys are variadic strings that form a single composite key, matching the
// convention used by [RegisterRefreshTrigger] and [TriggerRefresh]. To fire
// multiple distinct keys, call this method multiple times.
func (s *Server) TriggerRefresh(keys ...string) {
	rq := &refreshQueue{keys: []string{compositeKey(keys...)}}
	s.processRefreshQueue(rq)
}
