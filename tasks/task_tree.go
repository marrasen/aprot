package tasks

import (
	"context"
	"errors"
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
	parentID string // empty for root nodes
	title    string
	status   TaskNodeStatus
	error    string
	current  int
	total    int
	meta     any
	children []*taskNode
	mu       sync.Mutex

	// Lifecycle hook state. hooks is shared across all nodes in a task tree
	// (set at root creation, inherited by children). hookCtx, guarded by mu,
	// is the context returned by the start hook — passed to the end hook so
	// decorations done at start (e.g. attaching a logger) reach completion.
	hooks   *enableOptions
	hookCtx context.Context

	// Shared-task-only fields:
	cancel      context.CancelFunc // non-nil for top-level shared tasks
	ctx         context.Context    // task-scoped context for shared tasks
	manager     *taskManager       // back-reference to manager (shared only)
	ownerConnID uint64             // connection that created this task
	topLevel    bool               // true if created by StartTask with Shared()

}

// fireStart calls the start hook (if installed) and stashes the resulting
// context for later use by fireEnd. Returns the context to use downstream
// (decorated by the hook, or the input ctx if no hook is installed).
//
// The input ctx is stashed even when no start hook is installed, so that an
// end-hook-only configuration still sees the original request ctx (instead
// of context.Background()) on completion. Each call site in api.go invokes
// fireStart exactly once per node, so no idempotency guard is needed here.
func (n *taskNode) fireStart(ctx context.Context) context.Context {
	if n.hooks == nil {
		return ctx
	}
	out := ctx
	if n.hooks.onStart != nil {
		out = n.hooks.onStart(ctx, n.id, n.title, n.parentID)
	}
	n.mu.Lock()
	n.hookCtx = out
	n.mu.Unlock()
	return out
}

// fireEnd calls the end hook (if installed) with the context returned by
// fireStart (or context.Background() if none). Idempotency lives in the
// callers (setStatus, setFailed, completeTop, failTop) which short-circuit
// on terminal status, so this method does not re-check.
func (n *taskNode) fireEnd(err error) {
	if n.hooks == nil || n.hooks.onEnd == nil {
		return
	}
	n.mu.Lock()
	ctx := n.hookCtx
	n.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	n.hooks.onEnd(ctx, n.id, n.title, n.parentID, err)
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
	if n.status == TaskNodeStatusCompleted || n.status == TaskNodeStatusFailed {
		n.mu.Unlock()
		return
	}
	n.status = s
	n.mu.Unlock()
	if s == TaskNodeStatusCompleted {
		n.fireEnd(nil)
	}
}

func (n *taskNode) setFailed(msg string) {
	n.mu.Lock()
	if n.status == TaskNodeStatusCompleted || n.status == TaskNodeStatusFailed {
		n.mu.Unlock()
		return
	}
	n.status = TaskNodeStatusFailed
	n.error = msg
	n.mu.Unlock()
	n.fireEnd(errors.New(msg))
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
		parentID: n.id,
		title:    title,
		status:   TaskNodeStatusCreated,
		hooks:    n.hooks,
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
	n.fireEnd(nil)
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
	n.failChildren(msg)
	n.delivery.sendSnapshot(nil)
	n.fireEnd(errors.New(msg))
	if n.manager != nil {
		go func() {
			time.Sleep(200 * time.Millisecond)
			n.manager.remove(n.id)
		}()
	}
}

// failChildren recursively marks all running/created children as failed.
func (n *taskNode) failChildren(msg string) {
	n.mu.Lock()
	children := make([]*taskNode, len(n.children))
	copy(children, n.children)
	n.mu.Unlock()

	for _, child := range children {
		child.mu.Lock()
		fired := false
		if child.status == TaskNodeStatusRunning || child.status == TaskNodeStatusCreated {
			child.status = TaskNodeStatusFailed
			child.error = msg
			fired = true
		}
		child.mu.Unlock()
		if fired {
			child.fireEnd(errors.New(msg))
		}
		child.failChildren(msg)
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
		hooks:    d.hooks,
	}
	return d.root
}
