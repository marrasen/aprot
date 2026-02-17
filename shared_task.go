package aprot

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// TaskStateEvent is the push event broadcast to all clients when shared tasks change.
type TaskStateEvent struct {
	Tasks []SharedTaskState `json:"tasks"`
}

// TaskOutputEvent is the push event for shared task output lines.
type TaskOutputEvent struct {
	TaskID string `json:"taskId"`
	Output string `json:"output"`
}

// SharedTaskState is the wire representation of a shared task.
type SharedTaskState struct {
	ID       string         `json:"id"`
	ParentID string         `json:"parentId,omitempty"`
	Title    string         `json:"title"`
	Status   TaskNodeStatus `json:"status"`
	Current  int            `json:"current,omitempty"`
	Total    int            `json:"total,omitempty"`
	Meta     any            `json:"meta,omitempty"`
	Children []*TaskNode    `json:"children,omitempty"`
}

// TaskRef is the reference returned to the client from a handler
// when a shared task is created.
type TaskRef struct {
	TaskID string `json:"taskId"`
}

// CancelTaskRequest is the request payload for canceling a shared task.
type CancelTaskRequest struct {
	TaskID string `json:"taskId"`
}

// sharedTaskCore is the internal, non-generic core of a shared task.
// It is stored by taskManager and holds all mutable state.
type sharedTaskCore struct {
	id       string
	title    string
	status   TaskNodeStatus
	current  int
	total    int
	meta     any
	children []*sharedTaskNode
	mu       sync.Mutex
	manager  *taskManager
	cancel   context.CancelFunc
	ctx      context.Context
}

func (t *sharedTaskCore) progress(current, total int) {
	t.mu.Lock()
	t.current = current
	t.total = total
	t.mu.Unlock()
	t.manager.markDirty(t.id)
}

func (t *sharedTaskCore) setMeta(v any) {
	t.mu.Lock()
	t.meta = v
	t.mu.Unlock()
	t.manager.markDirty(t.id)
}

func (t *sharedTaskCore) output(msg string) {
	t.manager.sendOutput(t.id, msg)
}

func (t *sharedTaskCore) goRun(fn func(ctx context.Context)) {
	go func() {
		defer t.closeTask()
		fn(t.ctx)
	}()
}

func (t *sharedTaskCore) closeTask() {
	t.mu.Lock()
	if t.status != TaskNodeStatusRunning {
		t.mu.Unlock()
		return
	}
	t.status = TaskNodeStatusCompleted
	t.mu.Unlock()
	t.cancel()
	t.manager.markDirty(t.id)
	// Schedule removal after broadcasting the completed state.
	go func() {
		time.Sleep(200 * time.Millisecond)
		t.manager.remove(t.id)
	}()
}

func (t *sharedTaskCore) fail() {
	t.mu.Lock()
	if t.status != TaskNodeStatusRunning {
		t.mu.Unlock()
		return
	}
	t.status = TaskNodeStatusFailed
	t.mu.Unlock()
	t.cancel()
	t.manager.markDirty(t.id)
	go func() {
		time.Sleep(200 * time.Millisecond)
		t.manager.remove(t.id)
	}()
}

func (t *sharedTaskCore) subTask(title string) *sharedTaskNode {
	child := &sharedTaskNode{
		id:     t.manager.allocID(),
		title:  title,
		status: TaskNodeStatusRunning,
	}
	t.mu.Lock()
	t.children = append(t.children, child)
	t.mu.Unlock()
	t.manager.markDirty(t.id)
	return child
}

func (t *sharedTaskCore) snapshot() SharedTaskState {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := SharedTaskState{
		ID:      t.id,
		Title:   t.title,
		Status:  t.status,
		Current: t.current,
		Total:   t.total,
		Meta:    t.meta,
	}
	for _, child := range t.children {
		state.Children = append(state.Children, child.snapshot())
	}
	return state
}

// SharedTask is a type-safe, generic wrapper around sharedTaskCore.
// M is the metadata type used with SetMeta.
type SharedTask[M any] struct {
	core *sharedTaskCore
}

// ID returns the task's unique identifier.
func (t *SharedTask[M]) ID() string {
	return t.core.id
}

// Ref returns a TaskRef that can be returned from a handler to the client.
func (t *SharedTask[M]) Ref() *TaskRef {
	return &TaskRef{TaskID: t.core.id}
}

// Progress updates the shared task's progress.
func (t *SharedTask[M]) Progress(current, total int) {
	t.core.progress(current, total)
}

// SetMeta sets typed metadata on the shared task.
// The value is included in the JSON snapshot broadcast to clients.
func (t *SharedTask[M]) SetMeta(v M) {
	t.core.setMeta(v)
}

// Output sends output text associated with this task.
func (t *SharedTask[M]) Output(msg string) {
	t.core.output(msg)
}

// Go runs fn in a goroutine, automatically closing the task when fn returns.
func (t *SharedTask[M]) Go(fn func(ctx context.Context)) {
	t.core.goRun(fn)
}

// Close marks the task as completed and removes it from the manager.
func (t *SharedTask[M]) Close() {
	t.core.closeTask()
}

// Fail marks the task as failed.
func (t *SharedTask[M]) Fail() {
	t.core.fail()
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
	s.core.manager.markDirty(s.core.id)
}

// Fail marks this sub-task as failed.
func (s *SharedTaskSub[M]) Fail() {
	s.node.mu.Lock()
	s.node.status = TaskNodeStatusFailed
	s.node.mu.Unlock()
	s.core.manager.markDirty(s.core.id)
}

// SetMeta sets typed metadata on this sub-task node.
func (s *SharedTaskSub[M]) SetMeta(v M) {
	s.node.mu.Lock()
	s.node.meta = v
	s.node.mu.Unlock()
	s.core.manager.markDirty(s.core.id)
}

// SubTask creates a child node under this sub-task.
func (s *SharedTaskSub[M]) SubTask(title string) *SharedTaskSub[M] {
	child := &sharedTaskNode{
		id:     s.core.manager.allocID(),
		title:  title,
		status: TaskNodeStatusRunning,
	}
	s.node.mu.Lock()
	s.node.children = append(s.node.children, child)
	s.node.mu.Unlock()
	s.core.manager.markDirty(s.core.id)
	return &SharedTaskSub[M]{node: child, core: s.core}
}

// Progress updates the sub-task's progress.
func (s *SharedTaskSub[M]) Progress(current, total int) {
	s.node.mu.Lock()
	s.node.current = current
	s.node.total = total
	s.node.mu.Unlock()
	s.core.manager.markDirty(s.core.id)
}

// sharedTaskNode is the internal mutable child node of a SharedTask.
type sharedTaskNode struct {
	id       string
	title    string
	status   TaskNodeStatus
	current  int
	total    int
	meta     any
	children []*sharedTaskNode
	mu       sync.Mutex
}

func (n *sharedTaskNode) snapshot() *TaskNode {
	n.mu.Lock()
	defer n.mu.Unlock()
	node := &TaskNode{
		ID:      n.id,
		Title:   n.title,
		Status:  n.status,
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
// It batches state changes and broadcasts them to all clients.
type taskManager struct {
	server  *Server
	tasks   map[string]*sharedTaskCore
	dirty   map[string]bool
	mu      sync.Mutex
	nextID  atomic.Int64
	stopCh  chan struct{}
	stopped bool
}

func newTaskManager(server *Server) *taskManager {
	tm := &taskManager{
		server: server,
		tasks:  make(map[string]*sharedTaskCore),
		dirty:  make(map[string]bool),
		stopCh: make(chan struct{}),
	}
	go tm.flushLoop()
	return tm
}

func (tm *taskManager) allocID() string {
	n := tm.nextID.Add(1)
	return "st" + itoa(n)
}

// create creates a new sharedTaskCore and registers it.
func (tm *taskManager) create(title string, ctx context.Context) *sharedTaskCore {
	detached := context.WithoutCancel(ctx)
	taskCtx, cancel := context.WithCancel(detached)

	id := tm.allocID()
	task := &sharedTaskCore{
		id:      id,
		title:   title,
		status:  TaskNodeStatusRunning,
		manager: tm,
		cancel:  cancel,
		ctx:     taskCtx,
	}

	tm.mu.Lock()
	tm.tasks[id] = task
	tm.dirty[id] = true
	tm.mu.Unlock()

	return task
}

// remove removes a task from the manager.
func (tm *taskManager) remove(id string) {
	tm.mu.Lock()
	delete(tm.tasks, id)
	delete(tm.dirty, id)
	tm.mu.Unlock()
	// Broadcast the removal — send current state which no longer includes this task.
	tm.broadcastNow()
}

// markDirty flags a task for inclusion in the next batch broadcast.
func (tm *taskManager) markDirty(id string) {
	tm.mu.Lock()
	tm.dirty[id] = true
	tm.mu.Unlock()
}

// sendOutput broadcasts a task output event immediately.
func (tm *taskManager) sendOutput(taskID, output string) {
	event := TaskOutputEvent{
		TaskID: taskID,
		Output: output,
	}
	tm.server.Broadcast(event)
}

// cancel cancels a shared task by ID.
func (tm *taskManager) cancelTask(id string) bool {
	tm.mu.Lock()
	task, ok := tm.tasks[id]
	tm.mu.Unlock()
	if !ok {
		return false
	}
	task.fail()
	return true
}

// snapshotAll returns the current state of all active tasks.
func (tm *taskManager) snapshotAll() []SharedTaskState {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	states := make([]SharedTaskState, 0, len(tm.tasks))
	for _, task := range tm.tasks {
		states = append(states, task.snapshot())
	}
	return states
}

// broadcastNow sends the full task state to all clients immediately.
func (tm *taskManager) broadcastNow() {
	states := tm.snapshotAll()
	event := TaskStateEvent{Tasks: states}
	tm.server.Broadcast(event)
}

// flushLoop batches dirty task updates and broadcasts every 150ms.
func (tm *taskManager) flushLoop() {
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			tm.mu.Lock()
			if len(tm.dirty) == 0 {
				tm.mu.Unlock()
				continue
			}
			// Clear dirty set and broadcast current state.
			tm.dirty = make(map[string]bool)
			tm.mu.Unlock()
			tm.broadcastNow()

		case <-tm.stopCh:
			return
		}
	}
}

// stop shuts down the flush loop.
func (tm *taskManager) stop() {
	tm.mu.Lock()
	if !tm.stopped {
		tm.stopped = true
		close(tm.stopCh)
	}
	tm.mu.Unlock()
}

// ShareTask creates a new shared task visible to all clients.
// The returned SharedTask[M] is detached from the connection's lifecycle
// (uses context.WithoutCancel) and has its own cancellation context.
func ShareTask[M any](ctx context.Context, title string) *SharedTask[M] {
	conn := Connection(ctx)
	if conn == nil {
		return nil
	}
	tm := conn.server.taskManager
	if tm == nil {
		return nil
	}
	core := tm.create(title, ctx)
	return &SharedTask[M]{core: core}
}

// taskCancelHandler is an internal handler registered by EnableTasks()
// to allow clients to cancel shared tasks.
type taskCancelHandler struct {
	server *Server
}

func (h *taskCancelHandler) CancelTask(ctx context.Context, req *CancelTaskRequest) error {
	conn := Connection(ctx)
	if conn == nil {
		return ErrInternal(nil)
	}
	tm := conn.server.taskManager
	if tm == nil {
		return NewError(CodeInternalError, "tasks not enabled")
	}
	if !tm.cancelTask(req.TaskID) {
		return NewError(CodeInvalidParams, "task not found: "+req.TaskID)
	}
	return nil
}
