package aprot

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// TaskNodeStatus represents the current state of a task node.
type TaskNodeStatus string

const (
	TaskNodeStatusRunning   TaskNodeStatus = "running"
	TaskNodeStatusCompleted TaskNodeStatus = "completed"
	TaskNodeStatusFailed    TaskNodeStatus = "failed"
)

// TaskNode is the JSON-serializable snapshot of a task sent to the client.
type TaskNode struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Status   TaskNodeStatus `json:"status"`
	Error    string         `json:"error,omitempty"`
	Current  int            `json:"current,omitempty"`
	Total    int            `json:"total,omitempty"`
	Meta     any            `json:"meta,omitempty"`
	Children []*TaskNode    `json:"children,omitempty"`
}

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
func (t *taskTree) snapshot() []*TaskNode {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.root == nil {
		return nil
	}
	return t.root.snapshotChildren()
}

// send sends the current task tree snapshot to the client.
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
	status   TaskNodeStatus
	error    string
	current  int
	total    int
	meta     any
	children []*taskNode
	mu       sync.Mutex
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

func (n *taskNode) stepProgress(step int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.current += step
}

// ensureRoot lazily creates the implicit root node.
func (t *taskTree) ensureRoot() *taskNode {
	if t.root == nil {
		t.root = &taskNode{
			tree:   t,
			id:     "root",
			title:  "",
			status: TaskNodeStatusRunning,
		}
	}
	return t.root
}

// SubTask creates a child task under the current task node, runs fn, and marks
// the sub-task as completed (or failed if fn returns an error).
// The child task node is stored in the context passed to fn, so nested SubTask
// calls create a proper hierarchy.
//
// When a sharedContext is present on ctx (set by SharedSubTask or
// SharedTask.WithContext), SubTask also creates a mirrored node in the shared
// task system, so progress is sent both to the requesting client and broadcast
// to all clients.
func SubTask(ctx context.Context, title string, fn func(ctx context.Context) error) error {
	tree := taskTreeFromContext(ctx)
	sc := sharedCtxFromContext(ctx)

	if tree == nil && sc == nil {
		// No task tree and no shared context — just run the function directly.
		return fn(ctx)
	}

	// --- request-scoped node ---
	var child *taskNode
	if tree != nil {
		parent := taskNodeFromContext(ctx)

		tree.mu.Lock()
		root := tree.ensureRoot()
		tree.mu.Unlock()

		if parent == nil {
			parent = root
		}

		child = &taskNode{
			tree:   tree,
			id:     tree.allocID(),
			title:  title,
			status: TaskNodeStatusRunning,
		}
		parent.addChild(child)
		tree.send()
	}

	// --- shared node ---
	var sharedNode *sharedTaskNode
	if sc != nil {
		if sc.node != nil {
			sharedNode = sc.node.subTask(sc.core, title)
		} else {
			sharedNode = sc.core.subTask(title)
		}
	}

	// Build child context with both nodes propagated.
	childCtx := ctx
	if child != nil {
		childCtx = withTaskNode(childCtx, child)
	}
	if sharedNode != nil {
		childCtx = withSharedContext(childCtx, &sharedContext{core: sc.core, node: sharedNode})
	}

	err := fn(childCtx)

	// --- finalize request-scoped node ---
	if child != nil {
		if err != nil {
			child.setFailed(err.Error())
		} else {
			child.setStatus(TaskNodeStatusCompleted)
		}
		tree.send()
	}

	// --- finalize shared node ---
	if sharedNode != nil {
		sharedNode.mu.Lock()
		if err != nil {
			sharedNode.status = TaskNodeStatusFailed
			sharedNode.error = err.Error()
		} else {
			sharedNode.status = TaskNodeStatusCompleted
		}
		sharedNode.mu.Unlock()
		sc.core.manager.markDirty(sc.core.id)
	}

	return err
}

// Output sends a text output message for the current request.
// This reuses the progress channel with the Output field.
// When a sharedContext is present, the output is also broadcast
// to all clients via the shared task system.
func Output(ctx context.Context, msg string) {
	tree := taskTreeFromContext(ctx)
	if tree != nil {
		tree.reporter.sendOutput(msg)
	}
	sc := sharedCtxFromContext(ctx)
	if sc != nil {
		sc.core.output(msg)
	}
}

// TaskProgress sets the progress (current/total) on the current task node.
// Updates both the request-scoped task tree and the shared task system (if present).
// No-op if called outside a SubTask context.
func TaskProgress(ctx context.Context, current, total int) {
	node := taskNodeFromContext(ctx)
	if node != nil {
		node.setProgress(current, total)
		node.tree.send()
	}

	sc := sharedCtxFromContext(ctx)
	if sc != nil && sc.node != nil {
		sc.node.mu.Lock()
		sc.node.current = current
		sc.node.total = total
		sc.node.mu.Unlock()
		sc.core.manager.markDirty(sc.core.id)
	}
}

// StepTaskProgress increments the current progress on the current task node by step.
// Updates both the request-scoped task tree and the shared task system (if present).
// No-op if called outside a SubTask context.
func StepTaskProgress(ctx context.Context, step int) {
	node := taskNodeFromContext(ctx)
	if node != nil {
		node.stepProgress(step)
		node.tree.send()
	}

	sc := sharedCtxFromContext(ctx)
	if sc != nil && sc.node != nil {
		sc.node.mu.Lock()
		sc.node.current += step
		sc.node.mu.Unlock()
		sc.core.manager.markDirty(sc.core.id)
	}
}

// OutputWriter returns an io.WriteCloser that creates a child task node
// and sends each line written to it as output. The task node is marked
// completed when the writer is closed.
func OutputWriter(ctx context.Context, title string) io.WriteCloser {
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
		status: TaskNodeStatusRunning,
	}
	parent.addChild(child)
	tree.send()

	return &taskOutputWriter{
		tree: tree,
		node: child,
	}
}

// taskOutputWriter implements io.WriteCloser and sends written data as output.
type taskOutputWriter struct {
	tree *taskTree
	node *taskNode
}

func (w *taskOutputWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		w.tree.reporter.sendOutput(string(p))
	}
	return len(p), nil
}

func (w *taskOutputWriter) Close() error {
	w.node.setStatus(TaskNodeStatusCompleted)
	w.tree.send()
	return nil
}

// WriterProgress returns an io.WriteCloser that creates a child task node
// tracking bytes written as progress (current/total). If size <= 0, only
// current bytes are tracked without a total.
func WriterProgress(ctx context.Context, title string, size int) io.WriteCloser {
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
		status: TaskNodeStatusRunning,
		total:  size,
	}
	parent.addChild(child)
	tree.send()

	return &taskProgressWriter{
		tree:     tree,
		node:     child,
		total:    size,
		lastSend: time.Now(),
	}
}

// taskProgressWriter implements io.WriteCloser and tracks bytes as progress.
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
		w.tree.send()
		w.lastSend = time.Now()
	}

	return len(p), nil
}

func (w *taskProgressWriter) Close() error {
	w.node.setProgress(w.written, w.total)
	w.node.setStatus(TaskNodeStatusCompleted)
	w.tree.send()
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

// Progress updates the task's progress counters.
func (t *Task[M]) Progress(current, total int) {
	t.node.setProgress(current, total)
	t.node.tree.send()
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
	t.node.setStatus(TaskNodeStatusCompleted)
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
		status: TaskNodeStatusRunning,
	}
	root.addChild(node)

	// Populate the task slot so handleRequest can auto-manage lifecycle.
	if slot := taskSlotFromContext(ctx); slot != nil {
		slot.taskNode = node
	}

	ctx = withTaskNode(ctx, node)
	tree.send()

	return ctx, &Task[M]{node: node}
}
