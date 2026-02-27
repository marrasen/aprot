package tasks

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marrasen/aprot"
)

// taskManager is the server-wide registry of active shared tasks.
type taskManager struct {
	server         *aprot.Server
	tasks          map[string]*taskNode
	mu             sync.Mutex
	nextID         atomic.Int64
	lastProgress   map[string]time.Time
	lastProgressMu sync.Mutex
}

func newTaskManager(server *aprot.Server) *taskManager {
	return &taskManager{
		server:       server,
		tasks:        make(map[string]*taskNode),
		lastProgress: make(map[string]time.Time),
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
	tm.broadcastNow()
}

// sendUpdate broadcasts a per-node update event.
// Output updates are never throttled. Progress updates are throttled
// to max once per 50ms per task node.
func (tm *taskManager) sendUpdate(taskID string, output *string, current, total *int) {
	if output != nil {
		tm.server.Broadcast(TaskUpdateEvent{TaskID: taskID, Output: output})
		return
	}
	// Progress is throttled to 50ms per node.
	tm.lastProgressMu.Lock()
	last := tm.lastProgress[taskID]
	now := time.Now()
	if now.Sub(last) < 50*time.Millisecond {
		tm.lastProgressMu.Unlock()
		return
	}
	tm.lastProgress[taskID] = now
	tm.lastProgressMu.Unlock()
	tm.server.Broadcast(TaskUpdateEvent{TaskID: taskID, Current: current, Total: total})
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
		conn.Push(TaskStateEvent{Tasks: states})
	})
}

// stop is a no-op now that flushLoop has been removed. Retained for
// interface compatibility with the OnStop hook.
func (tm *taskManager) stop() {}
