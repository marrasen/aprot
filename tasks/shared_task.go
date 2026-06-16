package tasks

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marrasen/aprot"
)

// progressThrottleInterval is the minimum spacing between broadcast progress
// updates for a single task node.
const progressThrottleInterval = 50 * time.Millisecond

// progressUpdate holds the most recent suppressed progress values for a task.
type progressUpdate struct {
	current int
	total   int
}

// progressThrottle tracks per-task progress throttling state. A suppressed
// update is remembered in pending and replayed by a trailing timer so the
// final value is never dropped.
type progressThrottle struct {
	last    time.Time
	pending *progressUpdate
	timer   *time.Timer
}

// taskManager is the server-wide registry of active shared tasks.
type taskManager struct {
	server *aprot.Server
	// broadcast sends a push event to all clients. It defaults to
	// server.Broadcast and is a field so tests can observe emitted events.
	broadcast func(any)
	tasks     map[string]*taskNode
	mu        sync.Mutex
	nextID    atomic.Int64

	throttles  map[string]*progressThrottle
	progressMu sync.Mutex
}

func newTaskManager(server *aprot.Server) *taskManager {
	return &taskManager{
		server:    server,
		broadcast: server.Broadcast,
		tasks:     make(map[string]*taskNode),
		throttles: make(map[string]*progressThrottle),
	}
}

func (tm *taskManager) allocID() string {
	n := tm.nextID.Add(1)
	return "st" + itoa(n)
}

func (tm *taskManager) create(title string, connID uint64, topLevel bool, ctx context.Context) *taskNode {
	taskCtx, cancel := context.WithCancel(ctx)

	id := tm.allocID()
	delivery := newSharedDelivery(tm)
	node := &taskNode{
		delivery:    delivery,
		id:          id,
		title:       title,
		status:      TaskNodeStatusCreated,
		cancel:      cancel,
		ctx:         taskCtx,
		manager:     tm,
		ownerConnID: connID,
		topLevel:    topLevel,
	}

	tm.mu.Lock()
	tm.tasks[id] = node
	tm.mu.Unlock()

	return node
}

func (tm *taskManager) remove(id string) {
	tm.mu.Lock()
	delete(tm.tasks, id)
	tm.mu.Unlock()

	// Drop the task's throttle bookkeeping so it can't grow without bound on a
	// long-running server, and stop any pending trailing flush.
	tm.progressMu.Lock()
	if pt := tm.throttles[id]; pt != nil && pt.timer != nil {
		pt.timer.Stop()
	}
	delete(tm.throttles, id)
	tm.progressMu.Unlock()

	tm.broadcastNow()
}

// sendUpdate broadcasts a per-node update event.
// Output updates are never throttled. Progress updates are throttled to at most
// once per progressThrottleInterval per task node, but the most recent
// suppressed value is always flushed once the window elapses so clients never
// get stuck displaying a stale value.
func (tm *taskManager) sendUpdate(taskID string, output *string, current, total *int) {
	if output != nil {
		tm.broadcast(TaskUpdateEvent{TaskID: taskID, Output: output})
		return
	}
	if current == nil || total == nil {
		return
	}

	tm.progressMu.Lock()
	pt := tm.throttles[taskID]
	if pt == nil {
		pt = &progressThrottle{}
		tm.throttles[taskID] = pt
	}
	now := time.Now()
	if now.Sub(pt.last) >= progressThrottleInterval {
		pt.last = now
		pt.pending = nil
		tm.progressMu.Unlock()
		tm.broadcast(TaskUpdateEvent{TaskID: taskID, Current: current, Total: total})
		return
	}
	// Within the throttle window: remember the latest values and ensure a
	// trailing flush is scheduled so the final value is not dropped.
	pt.pending = &progressUpdate{current: *current, total: *total}
	if pt.timer == nil {
		delay := progressThrottleInterval - now.Sub(pt.last)
		pt.timer = time.AfterFunc(delay, func() { tm.flushProgress(taskID) })
	}
	tm.progressMu.Unlock()
}

// flushProgress broadcasts the most recent suppressed progress value for a
// task, if any. It runs from the trailing-flush timer scheduled by sendUpdate.
func (tm *taskManager) flushProgress(taskID string) {
	tm.progressMu.Lock()
	pt := tm.throttles[taskID]
	if pt == nil {
		tm.progressMu.Unlock()
		return
	}
	pt.timer = nil
	if pt.pending == nil {
		tm.progressMu.Unlock()
		return
	}
	upd := *pt.pending
	pt.pending = nil
	pt.last = time.Now()
	tm.progressMu.Unlock()

	cur, tot := upd.current, upd.total
	tm.broadcast(TaskUpdateEvent{TaskID: taskID, Current: &cur, Total: &tot})
}

func (tm *taskManager) cancelTask(id string) bool {
	tm.mu.Lock()
	node, ok := tm.tasks[id]
	tm.mu.Unlock()
	if !ok {
		return false
	}
	node.failTop("canceled")
	return true
}

// cancelTaskOwnedBy cancels a task only if it is owned by connID. It reports
// whether the task was found and canceled. ownerConnID is set once at creation
// and never mutated, so it is safe to read without the task lock.
func (tm *taskManager) cancelTaskOwnedBy(id string, connID uint64) bool {
	tm.mu.Lock()
	node, ok := tm.tasks[id]
	tm.mu.Unlock()
	if !ok || node.ownerConnID != connID {
		return false
	}
	node.failTop("canceled")
	return true
}

func (tm *taskManager) snapshotAll() []SharedTaskState {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	states := make([]SharedTaskState, 0, len(tm.tasks))
	for _, node := range tm.tasks {
		states = append(states, node.sharedSnapshot())
	}
	return states
}

func (tm *taskManager) snapshotAllForConn(connID uint64) []SharedTaskState {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	states := make([]SharedTaskState, 0, len(tm.tasks))
	for _, node := range tm.tasks {
		states = append(states, node.sharedSnapshotForConn(connID))
	}
	return states
}

// broadcastNow sends the full task state to all clients immediately.
func (tm *taskManager) broadcastNow() {
	tm.server.ForEachConn(func(conn *aprot.Conn) {
		states := tm.snapshotAllForConn(conn.ID())
		_ = conn.Push(TaskStateEvent{Tasks: states})
	})
}

// stop cancels any pending trailing-flush timers so they cannot fire after the
// server has shut down. Called from the OnStop hook.
func (tm *taskManager) stop() {
	tm.progressMu.Lock()
	for _, pt := range tm.throttles {
		if pt.timer != nil {
			pt.timer.Stop()
		}
	}
	tm.throttles = make(map[string]*progressThrottle)
	tm.progressMu.Unlock()
}
