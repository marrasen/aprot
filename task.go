package aprot

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marrasen/aprot/tasks"
)

// taskTree is the mutable state for a request's task hierarchy.
// It is created per-request and stored in the context.
type taskTree struct {
	reporter *progressReporter
	root     *taskNode
	mu       sync.Mutex
	nextID   atomic.Int64
}

func newTaskTree(reporter *progressReporter) *taskTree {
	return &taskTree{
		reporter: reporter,
	}
}

// snapshot returns the current task tree as a slice of TaskNodes.
func (t *taskTree) snapshot() []*tasks.TaskNode {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.root == nil {
		return nil
	}
	return t.root.snapshotChildren()
}

// send sends the current task tree snapshot to the client.
// Used for structural changes (add/remove subtask, status change).
func (t *taskTree) send() {
	nodes := t.snapshot()
	if nodes != nil {
		t.reporter.updateTasks(nodes)
	}
}

// allocID returns a unique task node ID within this tree.
func (t *taskTree) allocID() string {
	n := t.nextID.Add(1)
	// Use a simple numeric ID prefixed with "t"
	return "t" + itoa(n)
}

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

// taskNode is the internal mutable tree node.
type taskNode struct {
	tree     *taskTree
	id       string
	title    string
	status   tasks.TaskNodeStatus
	error    string
	current  int
	total    int
	meta     any
	children []*taskNode
	mu       sync.Mutex
}

func (n *taskNode) snapshot() *tasks.TaskNode {
	n.mu.Lock()
	defer n.mu.Unlock()
	node := &tasks.TaskNode{
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

func (n *taskNode) snapshotChildren() []*tasks.TaskNode {
	n.mu.Lock()
	defer n.mu.Unlock()
	var nodes []*tasks.TaskNode
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

func (n *taskNode) setStatus(s tasks.TaskNodeStatus) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.status = s
}

func (n *taskNode) setFailed(msg string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.status = tasks.TaskNodeStatusFailed
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

// ensureRoot lazily creates the implicit root node.
func (t *taskTree) ensureRoot() *taskNode {
	if t.root == nil {
		t.root = &taskNode{
			tree:   t,
			id:     "root",
			title:  "",
			status: tasks.TaskNodeStatusRunning,
		}
	}
	return t.root
}

// SubTask creates a child task under the current task node, runs fn, and marks
// the sub-task as completed (or failed if fn returns an error).
// The child task node is stored in the context passed to fn, so nested SubTask
// calls create a proper hierarchy.
//
// When a sharedContext is present on ctx, SubTask routes through the shared
// task system only (broadcast to all clients). Otherwise it uses the
// request-scoped task tree (visible only to the requesting client).
func SubTask(ctx context.Context, title string, fn func(ctx context.Context) error) error {
	sc := sharedCtxFromContext(ctx)
	if sc != nil {
		return sharedSubTaskInner(ctx, sc, title, fn)
	}

	tree := taskTreeFromContext(ctx)
	if tree == nil {
		// No task tree and no shared context — just run the function directly.
		return fn(ctx)
	}

	// --- request-scoped path only ---
	parent := taskNodeFromContext(ctx)

	tree.mu.Lock()
	root := tree.ensureRoot()
	tree.mu.Unlock()

	if parent == nil {
		parent = root
	}

	child := &taskNode{
		tree:   tree,
		id:     tree.allocID(),
		title:  title,
		status: tasks.TaskNodeStatusCreated,
	}
	parent.addChild(child)
	tree.send()

	child.setStatus(tasks.TaskNodeStatusRunning)
	childCtx := withTaskNode(ctx, child)
	err := fn(childCtx)

	if err != nil {
		child.setFailed(err.Error())
	} else {
		child.setStatus(tasks.TaskNodeStatusCompleted)
	}
	tree.send()

	return err
}

// sharedSubTaskInner handles SubTask when a shared context is present.
// It only creates nodes in the shared task system (no request-scoped tree).
func sharedSubTaskInner(ctx context.Context, sc *sharedContext, title string, fn func(ctx context.Context) error) error {
	var sharedNode *sharedTaskNode
	if sc.node != nil {
		sharedNode = sc.node.subTask(sc.core, title)
	} else {
		sharedNode = sc.core.subTask(title)
	}

	childCtx := withSharedContext(ctx, &sharedContext{core: sc.core, node: sharedNode})
	err := fn(childCtx)

	sharedNode.mu.Lock()
	if err != nil {
		sharedNode.status = tasks.TaskNodeStatusFailed
		sharedNode.error = err.Error()
	} else {
		sharedNode.status = tasks.TaskNodeStatusCompleted
	}
	sharedNode.mu.Unlock()
	sc.core.manager.markDirty(sc.core.id)

	return err
}

// Output sends a text output message attached to the nearest task node.
// When a sharedContext is present, the output is broadcast to all clients
// via the shared task system. Otherwise it is sent to the requesting client
// via the progress channel.
func Output(ctx context.Context, msg string) {
	if sc := sharedCtxFromContext(ctx); sc != nil {
		nodeID := sc.core.id
		if sc.node != nil {
			nodeID = sc.node.id
		}
		sc.core.manager.sendUpdate(nodeID, &msg, nil, nil)
		return
	}
	tree := taskTreeFromContext(ctx)
	if tree != nil {
		taskID := ""
		if node := taskNodeFromContext(ctx); node != nil {
			taskID = node.id
		}
		tree.reporter.sendNodeUpdate(taskID, &msg, nil, nil)
	}
}

// TaskProgress sets the progress (current/total) on the current task node.
// When a sharedContext is present, sends a targeted update via the shared
// task system. Otherwise sends via the request-scoped progress channel.
// No-op if called outside a SubTask context.
func TaskProgress(ctx context.Context, current, total int) {
	if sc := sharedCtxFromContext(ctx); sc != nil && sc.node != nil {
		sc.node.mu.Lock()
		sc.node.current = current
		sc.node.total = total
		sc.node.mu.Unlock()
		sc.core.manager.sendUpdate(sc.node.id, nil, &current, &total)
		return
	}
	node := taskNodeFromContext(ctx)
	if node != nil {
		node.setProgress(current, total)
		node.tree.reporter.sendNodeUpdate(node.id, nil, &current, &total)
	}
}

// StepTaskProgress increments the current progress on the current task node by step.
// When a sharedContext is present, sends a targeted update via the shared
// task system. Otherwise sends via the request-scoped progress channel.
// No-op if called outside a SubTask context.
func StepTaskProgress(ctx context.Context, step int) {
	if sc := sharedCtxFromContext(ctx); sc != nil && sc.node != nil {
		sc.node.mu.Lock()
		sc.node.current += step
		current := sc.node.current
		total := sc.node.total
		sc.node.mu.Unlock()
		sc.core.manager.sendUpdate(sc.node.id, nil, &current, &total)
		return
	}
	node := taskNodeFromContext(ctx)
	if node != nil {
		current, total := node.stepProgress(step)
		node.tree.reporter.sendNodeUpdate(node.id, nil, &current, &total)
	}
}

// OutputWriter returns an io.WriteCloser that creates a child task node
// and sends each Write as output attached to that node. The task node is
// marked completed when the writer is closed.
//
// When a sharedContext is present, the writer routes through the shared
// task system. Otherwise it uses the request-scoped progress channel.
func OutputWriter(ctx context.Context, title string) io.WriteCloser {
	if sc := sharedCtxFromContext(ctx); sc != nil {
		var node *sharedTaskNode
		if sc.node != nil {
			node = sc.node.subTask(sc.core, title)
		} else {
			node = sc.core.subTask(title)
		}
		return &sharedOutputWriter{core: sc.core, node: node}
	}

	tree := taskTreeFromContext(ctx)
	if tree == nil {
		return discardWriteCloser{}
	}

	parent := taskNodeFromContext(ctx)

	tree.mu.Lock()
	root := tree.ensureRoot()
	tree.mu.Unlock()

	if parent == nil {
		parent = root
	}

	child := &taskNode{
		tree:   tree,
		id:     tree.allocID(),
		title:  title,
		status: tasks.TaskNodeStatusCreated,
	}
	parent.addChild(child)
	tree.send()

	child.setStatus(tasks.TaskNodeStatusRunning)
	return &taskOutputWriter{
		tree: tree,
		node: child,
	}
}

// taskOutputWriter implements io.WriteCloser and sends written data as output
// attached to a specific task node via the request-scoped progress channel.
type taskOutputWriter struct {
	tree *taskTree
	node *taskNode
}

func (w *taskOutputWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		s := string(p)
		w.tree.reporter.sendNodeUpdate(w.node.id, &s, nil, nil)
	}
	return len(p), nil
}

func (w *taskOutputWriter) Close() error {
	w.node.setStatus(tasks.TaskNodeStatusCompleted)
	w.tree.send()
	return nil
}

// sharedOutputWriter implements io.WriteCloser for output within the shared
// task system.
type sharedOutputWriter struct {
	core *sharedTaskCore
	node *sharedTaskNode
}

func (w *sharedOutputWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		s := string(p)
		w.core.manager.sendUpdate(w.node.id, &s, nil, nil)
	}
	return len(p), nil
}

func (w *sharedOutputWriter) Close() error {
	w.node.mu.Lock()
	w.node.status = tasks.TaskNodeStatusCompleted
	w.node.mu.Unlock()
	w.core.manager.markDirty(w.core.id)
	return nil
}

// WriterProgress returns an io.WriteCloser that creates a child task node
// tracking bytes written as progress (current/total). If size <= 0, only
// current bytes are tracked without a total.
//
// When a sharedContext is present, the writer routes through the shared
// task system. Otherwise it uses the request-scoped progress channel.
func WriterProgress(ctx context.Context, title string, size int) io.WriteCloser {
	if sc := sharedCtxFromContext(ctx); sc != nil {
		var node *sharedTaskNode
		if sc.node != nil {
			node = sc.node.subTask(sc.core, title)
		} else {
			node = sc.core.subTask(title)
		}
		node.mu.Lock()
		node.total = size
		node.mu.Unlock()
		return &sharedProgressWriter{
			core:     sc.core,
			node:     node,
			total:    size,
			lastSend: time.Now(),
		}
	}

	tree := taskTreeFromContext(ctx)
	if tree == nil {
		return discardWriteCloser{}
	}

	parent := taskNodeFromContext(ctx)

	tree.mu.Lock()
	root := tree.ensureRoot()
	tree.mu.Unlock()

	if parent == nil {
		parent = root
	}

	child := &taskNode{
		tree:   tree,
		id:     tree.allocID(),
		title:  title,
		status: tasks.TaskNodeStatusCreated,
		total:  size,
	}
	parent.addChild(child)
	tree.send()

	child.setStatus(tasks.TaskNodeStatusRunning)
	return &taskProgressWriter{
		tree:     tree,
		node:     child,
		total:    size,
		lastSend: time.Now(),
	}
}

// taskProgressWriter implements io.WriteCloser and tracks bytes as progress
// via the request-scoped progress channel.
type taskProgressWriter struct {
	tree     *taskTree
	node     *taskNode
	written  int
	total    int
	lastSend time.Time
}

func (w *taskProgressWriter) Write(p []byte) (int, error) {
	w.written += len(p)
	w.node.setProgress(w.written, w.total)

	// Throttle progress updates to avoid flooding the client.
	if time.Since(w.lastSend) >= 100*time.Millisecond {
		current := w.written
		total := w.total
		w.node.tree.reporter.sendNodeUpdate(w.node.id, nil, &current, &total)
		w.lastSend = time.Now()
	}

	return len(p), nil
}

func (w *taskProgressWriter) Close() error {
	w.node.setProgress(w.written, w.total)
	w.node.setStatus(tasks.TaskNodeStatusCompleted)
	w.tree.send()
	return nil
}

// sharedProgressWriter implements io.WriteCloser for byte-tracking progress
// within the shared task system.
type sharedProgressWriter struct {
	core     *sharedTaskCore
	node     *sharedTaskNode
	written  int
	total    int
	lastSend time.Time
}

func (w *sharedProgressWriter) Write(p []byte) (int, error) {
	w.written += len(p)
	w.node.mu.Lock()
	w.node.current = w.written
	w.node.total = w.total
	w.node.mu.Unlock()

	// Throttle progress updates to avoid flooding the client.
	if time.Since(w.lastSend) >= 100*time.Millisecond {
		current := w.written
		total := w.total
		w.core.manager.sendUpdate(w.node.id, nil, &current, &total)
		w.lastSend = time.Now()
	}

	return len(p), nil
}

func (w *sharedProgressWriter) Close() error {
	w.node.mu.Lock()
	w.node.current = w.written
	w.node.total = w.total
	w.node.status = tasks.TaskNodeStatusCompleted
	w.node.mu.Unlock()
	w.core.manager.markDirty(w.core.id)
	return nil
}

// discardWriteCloser is a no-op WriteCloser for when no task tree is present.
type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                 { return nil }

// Task is a type-safe, generic wrapper around a request-scoped task node.
// M is the metadata type used with SetMeta.
// Created by StartTask and visible only to the requesting client via the
// progress stream.
type Task[M any] struct {
	node *taskNode
}

// ID returns the task's unique identifier.
func (t *Task[M]) ID() string {
	return t.node.id
}

// Progress updates the task's progress counters via a targeted per-node update.
func (t *Task[M]) Progress(current, total int) {
	t.node.setProgress(current, total)
	t.node.tree.reporter.sendNodeUpdate(t.node.id, nil, &current, &total)
}

// SetMeta sets typed metadata on the task.
func (t *Task[M]) SetMeta(v M) {
	t.node.mu.Lock()
	t.node.meta = v
	t.node.mu.Unlock()
	t.node.tree.send()
}

// Close marks the task as completed.
func (t *Task[M]) Close() {
	t.node.setStatus(tasks.TaskNodeStatusCompleted)
	t.node.tree.send()
}

// Fail marks the task as failed with the given error message.
func (t *Task[M]) Fail(message string) {
	t.node.setFailed(message)
	t.node.tree.send()
}

// Err fails the task with err.Error() if err is non-nil, or completes it if nil.
func (t *Task[M]) Err(err error) {
	if err != nil {
		t.Fail(err.Error())
	} else {
		t.Close()
	}
}

// StartTask creates a request-scoped task visible to the calling client via
// the progress stream. The returned context carries the task node so that
// SubTask, TaskProgress, and Output calls on it create child nodes under
// this task.
//
// When called inside a handler, the task lifecycle is managed automatically:
// returning nil completes the task, returning an error fails it.
func StartTask[M any](ctx context.Context, title string) (context.Context, *Task[M]) {
	tree := taskTreeFromContext(ctx)
	if tree == nil {
		return ctx, nil
	}

	tree.mu.Lock()
	root := tree.ensureRoot()
	tree.mu.Unlock()

	node := &taskNode{
		tree:   tree,
		id:     tree.allocID(),
		title:  title,
		status: tasks.TaskNodeStatusCreated,
	}
	root.addChild(node)

	// Populate the task slot so handleRequest can auto-manage lifecycle.
	if slot := taskSlotFromContext(ctx); slot != nil {
		slot.taskNode = node
	}

	ctx = withTaskNode(ctx, node)
	tree.send()

	node.setStatus(tasks.TaskNodeStatusRunning)
	return ctx, &Task[M]{node: node}
}
