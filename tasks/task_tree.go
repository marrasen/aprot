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

	// Lifecycle middleware state. hooks is shared across all nodes in a
	// task tree (set at root creation, inherited by createChild). For
	// manual-lifecycle tasks (StartTask, OutputWriter, WriterProgress,
	// Task.SubTask, TaskSub.SubTask), runManaged spawns a goroutine that
	// holds the middleware open; middlewareCtx captures the ctx the
	// middleware passed to next(); middlewareDone is signaled by the
	// lifecycle terminators so next() returns. middlewareEnded records that
	// a terminal signal arrived (with its error) so a terminal state reached
	// before next() begins waiting is not lost — see signalMiddlewareEnd and
	// runManaged. All are mu-guarded.
	hooks            *enableOptions
	middlewareCtx    context.Context
	middlewareDone   chan error
	middlewareEnded  bool
	middlewareEndErr error

	// Shared-task-only fields:
	cancel      context.CancelFunc // non-nil for top-level shared tasks
	ctx         context.Context    // task-scoped context for shared tasks
	manager     *taskManager       // back-reference to manager (shared only)
	ownerConnID uint64             // connection that created this task
	topLevel    bool               // true if created by StartTask with Shared()

}

// runScoped wraps fn with the registered task middleware (if any) and runs
// it synchronously. fn receives a (possibly decorated) context. Used by the
// scope-based task entry points (SubTask, SharedSubTask) where there is a
// natural function boundary to wrap.
func (n *taskNode) runScoped(ctx context.Context, fn func(context.Context) error) error {
	if n.hooks == nil || n.hooks.middleware == nil {
		return fn(ctx)
	}
	info := TaskInfo{ID: n.id, Title: n.title, ParentID: n.parentID}
	return n.hooks.middleware(ctx, info, fn)
}

// runManaged spawns the registered task middleware (if any) in a goroutine
// and blocks until the middleware calls next(). Returns the ctx the
// middleware passed to next(), or the input ctx if no middleware is
// installed (or if the middleware returned without calling next()). The
// terminal lifecycle methods (setStatus, setFailed, completeTop, failTop,
// failChildren) signal the middleware's next() return value via
// middlewareDone. Used by manual-lifecycle entry points (StartTask,
// OutputWriter, WriterProgress, Task.SubTask, TaskSub.SubTask) where the
// task body has no synchronous function boundary.
func (n *taskNode) runManaged(ctx context.Context) context.Context {
	if n.hooks == nil || n.hooks.middleware == nil {
		return ctx
	}
	info := TaskInfo{ID: n.id, Title: n.title, ParentID: n.parentID}
	done := make(chan error, 1)
	ready := make(chan struct{})

	n.mu.Lock()
	n.middlewareDone = done
	n.mu.Unlock()

	var readyOnce sync.Once
	closeReady := func() { readyOnce.Do(func() { close(ready) }) }

	// startupPanic carries a panic raised by the middleware *before* it calls
	// next() back to the caller's goroutine, so it surfaces at the task entry
	// point — matching how a scope-based middleware panic propagates — rather
	// than crashing this detached goroutine with no request context. A panic
	// *after* next() has no caller left to receive it (the entry point has
	// already returned), so it is re-raised here to preserve crash-on-bug
	// semantics.
	var startupPanic any

	go func() {
		defer func() {
			if r := recover(); r != nil {
				n.mu.Lock()
				calledNext := n.middlewareCtx != nil
				n.mu.Unlock()
				if calledNext {
					panic(r)
				}
				startupPanic = r
				closeReady()
			}
		}()
		_ = n.hooks.middleware(ctx, info, func(midCtx context.Context) error {
			n.mu.Lock()
			n.middlewareCtx = midCtx
			ended := n.middlewareEnded
			endErr := n.middlewareEndErr
			n.mu.Unlock()
			closeReady()
			if ended {
				// The task reached a terminal state before next() began
				// waiting — e.g. a parent cascade-failed this child in the
				// window between createChild and runManaged. Return the
				// recorded error immediately so the middleware observes the
				// end and the goroutine does not block on done forever.
				return endErr
			}
			return <-done
		})
		// If middleware returned without ever calling next(), unblock the
		// runManaged caller. A later signalMiddlewareEnd send is buffered
		// (cap 1) and never read — no goroutine leak.
		closeReady()
	}()

	<-ready
	if startupPanic != nil {
		panic(startupPanic)
	}
	n.mu.Lock()
	out := n.middlewareCtx
	n.mu.Unlock()
	if out == nil {
		return ctx
	}
	return out
}

// middlewareInheritedCtx returns the ctx most recently captured by this
// node's middleware (via runManaged), or context.Background if the node
// has no middleware ctx stashed yet. Used by child task creation paths
// that take no caller ctx (Task.SubTask, TaskSub.SubTask), so the
// child's middleware sees whatever decorations the parent applied.
func (n *taskNode) middlewareInheritedCtx() context.Context {
	n.mu.Lock()
	ctx := n.middlewareCtx
	n.mu.Unlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// signalMiddlewareEnd reports the task's terminal result to its middleware
// next() callback (if one was installed via runManaged). It records the end
// on the node so a terminal state reached before next() begins waiting is not
// lost (runManaged's next() checks middlewareEnded), and delivers it over the
// buffered (cap 1) done channel for the common case where next() is already
// blocked. The middlewareEnded guard makes this idempotent and ensures the
// single buffered send never blocks. Safe to call when no middleware is
// installed.
func (n *taskNode) signalMiddlewareEnd(err error) {
	n.mu.Lock()
	if n.middlewareEnded {
		n.mu.Unlock()
		return
	}
	n.middlewareEnded = true
	n.middlewareEndErr = err
	done := n.middlewareDone
	n.mu.Unlock()
	if done != nil {
		done <- err
	}
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
		n.signalMiddlewareEnd(nil)
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
	n.signalMiddlewareEnd(errors.New(msg))
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
	n.signalMiddlewareEnd(nil)
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
	n.signalMiddlewareEnd(errors.New(msg))
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
		signaled := false
		if child.status == TaskNodeStatusRunning || child.status == TaskNodeStatusCreated {
			child.status = TaskNodeStatusFailed
			child.error = msg
			signaled = true
		}
		child.mu.Unlock()
		if signaled {
			child.signalMiddlewareEnd(errors.New(msg))
		}
		child.failChildren(msg)
	}
}

// IsShared returns true if this node uses shared delivery.
func (n *taskNode) IsShared() bool {
	return n.delivery.isShared()
}

// collectIDs appends this node's descendant ids (depth-first) to acc and
// returns the result. The node's own id is not appended — callers seed acc with
// it. Node ids are assigned once at creation and never mutated, so they are
// safe to read without the node lock; only the children slice is guarded.
func (n *taskNode) collectIDs(acc []string) []string {
	n.mu.Lock()
	children := make([]*taskNode, len(n.children))
	copy(children, n.children)
	n.mu.Unlock()

	for _, child := range children {
		acc = append(acc, child.id)
		acc = child.collectIDs(acc)
	}
	return acc
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
// Handlers may create tasks from multiple worker goroutines, so creation is
// guarded: without the lock two callers could each install a root and one's
// subtree would be silently lost.
func ensureRoot(d *requestDelivery) *taskNode {
	d.rootMu.Lock()
	defer d.rootMu.Unlock()
	if d.root == nil {
		d.root = &taskNode{
			delivery: d,
			id:       "root",
			title:    "",
			status:   TaskNodeStatusRunning,
			hooks:    d.hooks,
		}
	}
	return d.root
}
