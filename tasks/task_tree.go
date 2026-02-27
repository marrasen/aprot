package tasks

import (
	"context"
	"sync"
	"time"
)

// itoa converts an int64 to string without importing strconv.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// taskNode is the internal mutable tree node used by both request-scoped and
// shared task systems. The delivery field determines how updates are sent.
type taskNode struct {
	delivery taskDelivery
	id       string
	title    string
	status   TaskNodeStatus
	error    string
	current  int
	total    int
	meta     any
	children []*taskNode
	mu       sync.Mutex

	// Shared-task-only fields:
	cancel      context.CancelFunc // non-nil for top-level shared tasks
	ctx         context.Context    // task-scoped context for shared tasks
	manager     *taskManager       // back-reference to manager (shared only)
	ownerConnID uint64             // connection that created this task
	topLevel    bool               // true if created by StartTask with Shared()

}

func (n *taskNode) snapshot() *TaskNode {
	n.mu.Lock()
	defer n.mu.Unlock()
	node := &TaskNode{
		ID:      n.id,
		Title:   n.title,
		Status:  n.status,
		Error:   n.error,
		Current: n.current,
		Total:   n.total,
		Meta:    n.meta,
	}
	for _, child := range n.children {
		node.Children = append(node.Children, child.snapshot())
	}
	return node
}

func (n *taskNode) snapshotChildren() []*TaskNode {
	n.mu.Lock()
	defer n.mu.Unlock()
	var nodes []*TaskNode
	for _, child := range n.children {
		nodes = append(nodes, child.snapshot())
	}
	return nodes
}

func (n *taskNode) addChild(child *taskNode) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.children = append(n.children, child)
}

func (n *taskNode) setStatus(s TaskNodeStatus) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.status = s
}

func (n *taskNode) setFailed(msg string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.status = TaskNodeStatusFailed
	n.error = msg
}

func (n *taskNode) setProgress(current, total int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.current = current
	n.total = total
}

func (n *taskNode) stepProgress(step int) (current, total int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.current += step
	return n.current, n.total
}

// output sends a text output message for this node.
func (n *taskNode) output(msg string) {
	n.delivery.sendOutput(n.id, msg)
}

// progress updates progress counters and sends an update.
func (n *taskNode) progress(current, total int) {
	n.setProgress(current, total)
	n.delivery.sendProgress(n.id, current, total)
}

// createChild creates a child node under this node, broadcasts, and transitions to running.
func (n *taskNode) createChild(title string) *taskNode {
	child := &taskNode{
		delivery: n.delivery,
		id:       n.delivery.allocID(),
		title:    title,
		status:   TaskNodeStatusCreated,
	}
	n.addChild(child)
	n.delivery.sendSnapshot(nil)
	child.setStatus(TaskNodeStatusRunning)
	return child
}

// closeNode marks this child node as completed and broadcasts.
func (n *taskNode) closeNode() {
	n.delivery.onComplete(n)
	n.delivery.sendSnapshot(nil)
}

// failNode marks this child node as failed and broadcasts.
func (n *taskNode) failNode(msg string) {
	n.setFailed(msg)
	n.delivery.onFail(n)
	n.delivery.sendSnapshot(nil)
}

// completeTop marks a top-level shared task as completed.
// Idempotent: no-op if already completed or failed.
func (n *taskNode) completeTop() {
	n.mu.Lock()
	if n.status != TaskNodeStatusRunning && n.status != TaskNodeStatusCreated {
		n.mu.Unlock()
		return
	}
	n.status = TaskNodeStatusCompleted
	n.mu.Unlock()
	if n.cancel != nil {
		n.cancel()
	}
	n.delivery.sendSnapshot(nil)
	if n.manager != nil {
		go func() {
			time.Sleep(200 * time.Millisecond)
			n.manager.remove(n.id)
		}()
	}
}

// failTop marks a top-level shared task as failed.
// Idempotent: no-op if already completed or failed.
func (n *taskNode) failTop(msg string) {
	n.mu.Lock()
	if n.status != TaskNodeStatusRunning && n.status != TaskNodeStatusCreated {
		n.mu.Unlock()
		return
	}
	n.status = TaskNodeStatusFailed
	n.error = msg
	n.mu.Unlock()
	if n.cancel != nil {
		n.cancel()
	}
	n.delivery.sendSnapshot(nil)
	if n.manager != nil {
		go func() {
			time.Sleep(200 * time.Millisecond)
			n.manager.remove(n.id)
		}()
	}
}

// IsShared returns true if this node uses shared delivery.
func (n *taskNode) IsShared() bool {
	return n.delivery.isShared()
}

// sharedSnapshot returns a SharedTaskState for this node.
func (n *taskNode) sharedSnapshot() SharedTaskState {
	n.mu.Lock()
	defer n.mu.Unlock()
	state := SharedTaskState{
		ID:      n.id,
		Title:   n.title,
		Status:  n.status,
		Error:   n.error,
		Current: n.current,
		Total:   n.total,
		Meta:    n.meta,
	}
	for _, child := range n.children {
		state.Children = append(state.Children, child.snapshot())
	}
	return state
}

// sharedSnapshotForConn returns a SharedTaskState with IsOwner set.
func (n *taskNode) sharedSnapshotForConn(connID uint64) SharedTaskState {
	state := n.sharedSnapshot()
	state.IsOwner = n.topLevel && (n.ownerConnID == connID)
	return state
}

// ensureRoot lazily creates an implicit root node for a request delivery.
func ensureRoot(d *requestDelivery) *taskNode {
	if d.root != nil {
		return d.root
	}
	d.root = &taskNode{
		delivery: d,
		id:       "root",
		title:    "",
		status:   TaskNodeStatusRunning,
	}
	return d.root
}
