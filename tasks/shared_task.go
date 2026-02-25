package tasks

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marrasen/aprot"
)

// sharedContext is propagated through the context to route task operations
// through the shared task system.
type sharedContext struct {
	core *sharedTaskCore
	node *sharedTaskNode // nil = at core level
}

// sharedTaskCore is the internal, non-generic core of a shared task.
type sharedTaskCore struct {
	id          string
	title       string
	status      TaskNodeStatus
	error       string
	current     int
	total       int
	meta        any
	children    []*sharedTaskNode
	mu          sync.Mutex
	manager     *taskManager
	cancel      context.CancelFunc
	ctx         context.Context
	ownerConnID uint64
	topLevel    bool
}

func (t *sharedTaskCore) progress(current, total int) {
	t.mu.Lock()
	t.current = current
	t.total = total
	t.mu.Unlock()
	t.manager.sendUpdate(t.id, nil, &current, &total)
}

func (t *sharedTaskCore) setMeta(v any) {
	t.mu.Lock()
	t.meta = v
	t.mu.Unlock()
	t.manager.broadcastNow()
}

func (t *sharedTaskCore) output(msg string) {
	t.manager.sendUpdate(t.id, &msg, nil, nil)
}

func (t *sharedTaskCore) closeTask() {
	t.mu.Lock()
	if t.status != TaskNodeStatusRunning && t.status != TaskNodeStatusCreated {
		t.mu.Unlock()
		return
	}
	t.status = TaskNodeStatusCompleted
	t.mu.Unlock()
	t.cancel()
	t.manager.broadcastNow()
	go func() {
		time.Sleep(200 * time.Millisecond)
		t.manager.remove(t.id)
	}()
}

func (t *sharedTaskCore) fail(msg string) {
	t.mu.Lock()
	if t.status != TaskNodeStatusRunning && t.status != TaskNodeStatusCreated {
		t.mu.Unlock()
		return
	}
	t.status = TaskNodeStatusFailed
	t.error = msg
	t.mu.Unlock()
	t.cancel()
	t.manager.broadcastNow()
	go func() {
		time.Sleep(200 * time.Millisecond)
		t.manager.remove(t.id)
	}()
}

func (t *sharedTaskCore) subTask(title string) *sharedTaskNode {
	child := &sharedTaskNode{
		id:     t.manager.allocID(),
		title:  title,
		status: TaskNodeStatusCreated,
	}
	t.mu.Lock()
	t.children = append(t.children, child)
	t.mu.Unlock()
	t.manager.broadcastNow()
	child.mu.Lock()
	child.status = TaskNodeStatusRunning
	child.mu.Unlock()
	return child
}

func (t *sharedTaskCore) snapshot() SharedTaskState {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := SharedTaskState{
		ID:      t.id,
		Title:   t.title,
		Status:  t.status,
		Error:   t.error,
		Current: t.current,
		Total:   t.total,
		Meta:    t.meta,
	}
	for _, child := range t.children {
		state.Children = append(state.Children, child.snapshot())
	}
	return state
}

func (t *sharedTaskCore) snapshotForConn(connID uint64) SharedTaskState {
	state := t.snapshot()
	state.IsOwner = t.topLevel && (t.ownerConnID == connID)
	return state
}

// SharedTask is a type-safe, generic wrapper around sharedTaskCore.
type SharedTask[M any] struct {
	core *sharedTaskCore
}

// ID returns the task's unique identifier.
func (t *SharedTask[M]) ID() string {
	return t.core.id
}

// Progress updates the shared task's progress.
func (t *SharedTask[M]) Progress(current, total int) {
	t.core.progress(current, total)
}

// SetMeta sets typed metadata on the shared task.
func (t *SharedTask[M]) SetMeta(v M) {
	t.core.setMeta(v)
}

// Output sends output text associated with this task.
func (t *SharedTask[M]) Output(msg string) {
	t.core.output(msg)
}

// Close marks the task as completed and removes it from the manager.
func (t *SharedTask[M]) Close() {
	t.core.closeTask()
}

// Fail marks the task as failed with the given error message.
func (t *SharedTask[M]) Fail(message string) {
	t.core.fail(message)
}

// Err fails the task with err.Error() if err is non-nil, or completes it if nil.
func (t *SharedTask[M]) Err(err error) {
	if err != nil {
		t.Fail(err.Error())
	} else {
		t.Close()
	}
}

// SubTask creates a child node under this shared task.
func (t *SharedTask[M]) SubTask(title string) *SharedTaskSub[M] {
	node := t.core.subTask(title)
	return &SharedTaskSub[M]{node: node, core: t.core}
}

// Context returns the task's context.
func (t *SharedTask[M]) Context() context.Context {
	return t.core.ctx
}

// WithContext returns a new context that carries this task's shared context.
func (t *SharedTask[M]) WithContext(ctx context.Context) context.Context {
	return withSharedContext(ctx, &sharedContext{core: t.core})
}

// SharedTaskSub is a type-safe, generic child node of a SharedTask.
type SharedTaskSub[M any] struct {
	node *sharedTaskNode
	core *sharedTaskCore
}

// Complete marks this sub-task as completed.
func (s *SharedTaskSub[M]) Complete() {
	s.node.mu.Lock()
	s.node.status = TaskNodeStatusCompleted
	s.node.mu.Unlock()
	s.core.manager.broadcastNow()
}

// Fail marks this sub-task as failed with the given error message.
func (s *SharedTaskSub[M]) Fail(message string) {
	s.node.mu.Lock()
	s.node.status = TaskNodeStatusFailed
	s.node.error = message
	s.node.mu.Unlock()
	s.core.manager.broadcastNow()
}

// Err fails the sub-task with err.Error() if err is non-nil, or completes it if nil.
func (s *SharedTaskSub[M]) Err(err error) {
	if err != nil {
		s.Fail(err.Error())
	} else {
		s.Complete()
	}
}

// SetMeta sets typed metadata on this sub-task node.
func (s *SharedTaskSub[M]) SetMeta(v M) {
	s.node.mu.Lock()
	s.node.meta = v
	s.node.mu.Unlock()
	s.core.manager.broadcastNow()
}

// SubTask creates a child node under this sub-task.
func (s *SharedTaskSub[M]) SubTask(title string) *SharedTaskSub[M] {
	child := s.node.subTask(s.core, title)
	return &SharedTaskSub[M]{node: child, core: s.core}
}

// Progress updates the sub-task's progress.
func (s *SharedTaskSub[M]) Progress(current, total int) {
	s.node.mu.Lock()
	s.node.current = current
	s.node.total = total
	s.node.mu.Unlock()
	s.core.manager.sendUpdate(s.node.id, nil, &current, &total)
}

// sharedTaskNode is the internal mutable child node of a SharedTask.
type sharedTaskNode struct {
	id       string
	title    string
	status   TaskNodeStatus
	error    string
	current  int
	total    int
	meta     any
	children []*sharedTaskNode
	mu       sync.Mutex
}

func (n *sharedTaskNode) subTask(core *sharedTaskCore, title string) *sharedTaskNode {
	child := &sharedTaskNode{
		id:     core.manager.allocID(),
		title:  title,
		status: TaskNodeStatusCreated,
	}
	n.mu.Lock()
	n.children = append(n.children, child)
	n.mu.Unlock()
	core.manager.broadcastNow()
	child.mu.Lock()
	child.status = TaskNodeStatusRunning
	child.mu.Unlock()
	return child
}

func (n *sharedTaskNode) snapshot() *TaskNode {
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

// taskManager is the server-wide registry of active shared tasks.
type taskManager struct {
	server         *aprot.Server
	tasks          map[string]*sharedTaskCore
	mu             sync.Mutex
	nextID         atomic.Int64
	lastProgress   map[string]time.Time
	lastProgressMu sync.Mutex
}

func newTaskManager(server *aprot.Server) *taskManager {
	return &taskManager{
		server:       server,
		tasks:        make(map[string]*sharedTaskCore),
		lastProgress: make(map[string]time.Time),
	}
}

func (tm *taskManager) allocID() string {
	n := tm.nextID.Add(1)
	return "st" + itoa(n)
}

func (tm *taskManager) create(title string, connID uint64, topLevel bool, ctx context.Context) *sharedTaskCore {
	taskCtx, cancel := context.WithCancel(ctx)

	id := tm.allocID()
	task := &sharedTaskCore{
		id:          id,
		title:       title,
		status:      TaskNodeStatusCreated,
		manager:     tm,
		cancel:      cancel,
		ctx:         taskCtx,
		ownerConnID: connID,
		topLevel:    topLevel,
	}

	tm.mu.Lock()
	tm.tasks[id] = task
	tm.mu.Unlock()

	return task
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
	task, ok := tm.tasks[id]
	tm.mu.Unlock()
	if !ok {
		return false
	}
	task.fail("canceled")
	return true
}

func (tm *taskManager) snapshotAll() []SharedTaskState {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	states := make([]SharedTaskState, 0, len(tm.tasks))
	for _, task := range tm.tasks {
		states = append(states, task.snapshot())
	}
	return states
}

func (tm *taskManager) snapshotAllForConn(connID uint64) []SharedTaskState {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	states := make([]SharedTaskState, 0, len(tm.tasks))
	for _, task := range tm.tasks {
		states = append(states, task.snapshotForConn(connID))
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
