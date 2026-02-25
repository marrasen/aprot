package tasks

import (
	"sync"
	"sync/atomic"

	"github.com/marrasen/aprot"
)

// taskTree is the mutable state for a request's task hierarchy.
// It is created per-request and stored in the context.
type taskTree struct {
	sender aprot.RequestSender
	root   *taskNode
	mu     sync.Mutex
	nextID atomic.Int64
}

func newTaskTree(sender aprot.RequestSender) *taskTree {
	return &taskTree{
		sender: sender,
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
		msg := aprot.ProgressMessage{
			Type:  aprot.TypeProgress,
			ID:    t.sender.RequestID(),
			Tasks: nodes,
		}
		t.sender.SendJSON(msg)
	}
}

// allocID returns a unique task node ID within this tree.
func (t *taskTree) allocID() string {
	n := t.nextID.Add(1)
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
			status: TaskNodeStatusRunning,
		}
	}
	return t.root
}
