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
	current  int
	total    int
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
		Current: n.current,
		Total:   n.total,
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

func (n *taskNode) setProgress(current, total int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.current = current
	n.total = total
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
func SubTask(ctx context.Context, title string, fn func(ctx context.Context) error) error {
	tree := taskTreeFromContext(ctx)
	if tree == nil {
		// No task tree — just run the function directly.
		return fn(ctx)
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

	childCtx := withTaskNode(ctx, child)
	err := fn(childCtx)

	if err != nil {
		child.setStatus(TaskNodeStatusFailed)
	} else {
		child.setStatus(TaskNodeStatusCompleted)
	}
	tree.send()

	return err
}

// Output sends a text output message for the current request.
// This reuses the progress channel with the Output field.
func Output(ctx context.Context, msg string) {
	tree := taskTreeFromContext(ctx)
	if tree == nil {
		return
	}
	tree.reporter.sendOutput(msg)
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
