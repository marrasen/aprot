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

// SharedTask is a task visible to all connected clients.
// It is detached from the originating connection's lifecycle.
type SharedTask struct {
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

// ID returns the task's unique identifier.
func (t *SharedTask) ID() string {
	return t.id
}

// Ref returns a TaskRef that can be returned from a handler to the client.
func (t *SharedTask) Ref() *TaskRef {
	return &TaskRef{TaskID: t.id}
}

// Progress updates the shared task's progress.
func (t *SharedTask) Progress(current, total int) {
	t.mu.Lock()
	t.current = current
	t.total = total
	t.mu.Unlock()
	t.manager.markDirty(t.id)
}

// SetMeta sets arbitrary metadata on the shared task.
// The value is included in the JSON snapshot broadcast to clients.
func (t *SharedTask) SetMeta(v any) {
	t.mu.Lock()
	t.meta = v
	t.mu.Unlock()
	t.manager.markDirty(t.id)
}

// Output sends output text associated with this task.
func (t *SharedTask) Output(msg string) {
	t.manager.sendOutput(t.id, msg)
}

// Go runs fn in a goroutine, automatically closing the task when fn returns.
func (t *SharedTask) Go(fn func(ctx context.Context)) {
	go func() {
		defer t.Close()
		fn(t.ctx)
	}()
}

// Close marks the task as completed and removes it from the manager.
func (t *SharedTask) Close() {
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

// Fail marks the task as failed.
func (t *SharedTask) Fail() {
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

// SubTask creates a child node under this shared task.
func (t *SharedTask) SubTask(title string) *SharedTaskSub {
	child := &sharedTaskNode{
		id:     t.manager.allocID(),
		title:  title,
		status: TaskNodeStatusRunning,
	}
	t.mu.Lock()
	t.children = append(t.children, child)
	t.mu.Unlock()
	t.manager.markDirty(t.id)
	return &SharedTaskSub{node: child, task: t}
}

// Context returns the task's context.
func (t *SharedTask) Context() context.Context {
	return t.ctx
}

func (t *SharedTask) snapshot() SharedTaskState {
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

// SharedTaskSub is a child node of a SharedTask.
type SharedTaskSub struct {
	node *sharedTaskNode
	task *SharedTask
}

// Complete marks this sub-task as completed.
func (s *SharedTaskSub) Complete() {
	s.node.mu.Lock()
	s.node.status = TaskNodeStatusCompleted
	s.node.mu.Unlock()
	s.task.manager.markDirty(s.task.id)
}

// Fail marks this sub-task as failed.
func (s *SharedTaskSub) Fail() {
	s.node.mu.Lock()
	s.node.status = TaskNodeStatusFailed
	s.node.mu.Unlock()
	s.task.manager.markDirty(s.task.id)
}

// SetMeta sets arbitrary metadata on this sub-task node.
func (s *SharedTaskSub) SetMeta(v any) {
	s.node.mu.Lock()
	s.node.meta = v
	s.node.mu.Unlock()
	s.task.manager.markDirty(s.task.id)
}

// SubTask creates a child node under this sub-task.
func (s *SharedTaskSub) SubTask(title string) *SharedTaskSub {
	child := &sharedTaskNode{
		id:     s.task.manager.allocID(),
		title:  title,
		status: TaskNodeStatusRunning,
	}
	s.node.mu.Lock()
	s.node.children = append(s.node.children, child)
	s.node.mu.Unlock()
	s.task.manager.markDirty(s.task.id)
	return &SharedTaskSub{node: child, task: s.task}
}

// Progress updates the sub-task's progress.
func (s *SharedTaskSub) Progress(current, total int) {
	s.node.mu.Lock()
	s.node.current = current
	s.node.total = total
	s.node.mu.Unlock()
	s.task.manager.markDirty(s.task.id)
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
	tasks   map[string]*SharedTask
	dirty   map[string]bool
	mu      sync.Mutex
	nextID  atomic.Int64
	stopCh  chan struct{}
	stopped bool
}

func newTaskManager(server *Server) *taskManager {
	tm := &taskManager{
		server: server,
		tasks:  make(map[string]*SharedTask),
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

// create creates a new SharedTask and registers it.
func (tm *taskManager) create(title string, ctx context.Context) *SharedTask {
	detached := context.WithoutCancel(ctx)
	taskCtx, cancel := context.WithCancel(detached)

	id := tm.allocID()
	task := &SharedTask{
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
	task.Fail()
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
// The returned SharedTask is detached from the connection's lifecycle
// (uses context.WithoutCancel) and has its own cancellation context.
func ShareTask(ctx context.Context, title string) *SharedTask {
	conn := Connection(ctx)
	if conn == nil {
		return nil
	}
	tm := conn.server.taskManager
	if tm == nil {
		return nil
	}
	return tm.create(title, ctx)
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
