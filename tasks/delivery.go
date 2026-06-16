package tasks

import (
	"sync"
	"sync/atomic"

	"github.com/marrasen/aprot"
)

// taskDelivery abstracts how task state updates are delivered to clients.
// requestDelivery sends to a single client; sharedDelivery broadcasts to all.
type taskDelivery interface {
	sendSnapshot(root *taskNode)
	sendOutput(taskID, msg string)
	sendProgress(taskID string, current, total int)
	onComplete(node *taskNode)
	onFail(node *taskNode)
	allocID() string
	isShared() bool
}

// requestDelivery sends task updates to a single requesting client.
type requestDelivery struct {
	conn      *aprot.Conn
	requestID string
	nextID    atomic.Int64
	rootMu    sync.Mutex // guards root; handlers may create tasks from worker goroutines
	root      *taskNode  // lazily created implicit root
}

func newRequestDelivery(conn *aprot.Conn, requestID string) *requestDelivery {
	return &requestDelivery{conn: conn, requestID: requestID}
}

func (d *requestDelivery) sendSnapshot(_ *taskNode) {
	d.rootMu.Lock()
	root := d.root
	d.rootMu.Unlock()
	if root == nil {
		return
	}
	nodes := root.snapshotChildren()
	if nodes != nil {
		_ = d.conn.Push(RequestTaskTreeEvent{RequestID: d.requestID, Tasks: nodes})
	}
}

func (d *requestDelivery) sendOutput(taskID, msg string) {
	_ = d.conn.Push(RequestTaskOutputEvent{RequestID: d.requestID, TaskID: taskID, Output: msg})
}

func (d *requestDelivery) sendProgress(taskID string, current, total int) {
	_ = d.conn.Push(RequestTaskProgressEvent{RequestID: d.requestID, TaskID: taskID, Current: current, Total: total})
}

func (d *requestDelivery) onComplete(node *taskNode) {
	node.setStatus(TaskNodeStatusCompleted)
}

func (d *requestDelivery) onFail(node *taskNode) {
	// status and error already set by caller
}

func (d *requestDelivery) allocID() string {
	n := d.nextID.Add(1)
	return "t" + itoa(n)
}

func (d *requestDelivery) isShared() bool { return false }

// sharedDelivery broadcasts task updates to all connected clients.
type sharedDelivery struct {
	manager *taskManager
}

func newSharedDelivery(manager *taskManager) *sharedDelivery {
	return &sharedDelivery{manager: manager}
}

func (d *sharedDelivery) sendSnapshot(_ *taskNode) {
	d.manager.broadcastNow()
}

func (d *sharedDelivery) sendOutput(taskID, msg string) {
	d.manager.sendUpdate(taskID, &msg, nil, nil)
}

func (d *sharedDelivery) sendProgress(taskID string, current, total int) {
	d.manager.sendUpdate(taskID, nil, &current, &total)
}

func (d *sharedDelivery) onComplete(node *taskNode) {
	node.setStatus(TaskNodeStatusCompleted)
}

func (d *sharedDelivery) onFail(node *taskNode) {
	// status and error already set by caller
}

func (d *sharedDelivery) allocID() string {
	return d.manager.allocID()
}

func (d *sharedDelivery) isShared() bool { return true }

// noopDelivery is used for detached tasks created when there is no client to
// deliver to (e.g. the REST path, which has no connection or task manager).
// Every method is a safe no-op so handler code written for WebSocket — which
// calls Progress/Output/SetMeta/Close — does not panic over REST; the task
// state simply isn't delivered anywhere.
type noopDelivery struct {
	nextID atomic.Int64
}

func (d *noopDelivery) sendSnapshot(_ *taskNode)        {}
func (d *noopDelivery) sendOutput(_, _ string)          {}
func (d *noopDelivery) sendProgress(_ string, _, _ int) {}
func (d *noopDelivery) onComplete(node *taskNode)       { node.setStatus(TaskNodeStatusCompleted) }
func (d *noopDelivery) onFail(_ *taskNode)              {}
func (d *noopDelivery) allocID() string                 { return "t" + itoa(d.nextID.Add(1)) }
func (d *noopDelivery) isShared() bool                  { return false }
