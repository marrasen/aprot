package aprot

import (
	"context"
	"strings"
	"sync"
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

// refresh sends a RefreshMessage to all subscriptions matching the trigger key.
func (sm *subscriptionManager) refresh(key string) {
	subs := sm.getSubscriptionsForKey(key)
	for _, sub := range subs {
		msg := RefreshMessage{
			Type: TypeRefresh,
			ID:   sub.id,
		}
		_ = sub.conn.sendJSON(msg)
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

// TriggerRefresh triggers a refresh for all subscriptions matching the given keys.
// Called from mutation handlers to notify subscribed clients to re-fetch.
func TriggerRefresh(ctx context.Context, keys ...string) {
	conn := Connection(ctx)
	if conn == nil {
		return
	}
	key := compositeKey(keys...)
	conn.server.subscriptions.refresh(key)
}
