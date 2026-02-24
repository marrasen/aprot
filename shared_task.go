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
	Error    string         `json:"error,omitempty"`
	Current  int            `json:"current,omitempty"`
	Total    int            `json:"total,omitempty"`
	Meta     any            `json:"meta,omitempty"`
	Children []*TaskNode    `json:"children,omitempty"`
	IsOwner  bool           `json:"isOwner"`
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
	ownerConnID uint64 // connection ID of the client that created this task
	topLevel    bool   // true only for StartSharedTask; gates isOwner in snapshots
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

func (t *sharedTaskCore) fail(msg string) {
	t.mu.Lock()
	if t.status != TaskNodeStatusRunning {
		t.mu.Unlock()
		return
	}
	t.status = TaskNodeStatusFailed
	t.error = msg
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
// M is the metadata type used with SetMeta.
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
// The value is included in the JSON snapshot broadcast to clients.
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

// WithContext returns a new context derived from ctx that carries
// this task's shared context. Use this to propagate shared task
// awareness into other goroutines, so that SubTask calls inside
// them also create mirrored shared task nodes.
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
	s.core.manager.markDirty(s.core.id)
}

// Fail marks this sub-task as failed with the given error message.
func (s *SharedTaskSub[M]) Fail(message string) {
	s.node.mu.Lock()
	s.node.status = TaskNodeStatusFailed
	s.node.error = message
	s.node.mu.Unlock()
	s.core.manager.markDirty(s.core.id)
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
	s.core.manager.markDirty(s.core.id)
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
	s.core.manager.markDirty(s.core.id)
}

// sharedContext is propagated through the context to bridge SubTask with the shared task system.
// When SubTask detects it, it creates mirrored shared task nodes.
type sharedContext struct {
	core *sharedTaskCore
	node *sharedTaskNode // nil = at core level
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
		status: TaskNodeStatusRunning,
	}
	n.mu.Lock()
	n.children = append(n.children, child)
	n.mu.Unlock()
	core.manager.markDirty(core.id)
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
// connID is the connection ID of the client that created this task (0 if none).
// topLevel indicates this task was created via StartSharedTask and should
// report isOwner to the originating connection.
// The task context is derived from ctx. The caller controls whether the task
// survives parent cancellation by passing context.WithoutCancel(ctx) if desired.
func (tm *taskManager) create(title string, connID uint64, topLevel bool, ctx context.Context) *sharedTaskCore {
	taskCtx, cancel := context.WithCancel(ctx)

	id := tm.allocID()
	task := &sharedTaskCore{
		id:          id,
		title:       title,
		status:      TaskNodeStatusRunning,
		manager:     tm,
		cancel:      cancel,
		ctx:         taskCtx,
		ownerConnID: connID,
		topLevel:    topLevel,
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
	task.fail("canceled")
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

// snapshotAllForConn returns the current state of all active tasks with
// IsOwner set according to the given connection ID.
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
// Each connection receives a per-connection snapshot with the correct IsOwner flag.
func (tm *taskManager) broadcastNow() {
	tm.server.mu.RLock()
	conns := make([]*Conn, 0, len(tm.server.conns))
	for conn := range tm.server.conns {
		conns = append(conns, conn)
	}
	tm.server.mu.RUnlock()

	event := tm.server.registry.eventName(TaskStateEvent{})
	for _, conn := range conns {
		states := tm.snapshotAllForConn(conn.ID())
		conn.push(event, TaskStateEvent{Tasks: states})
	}
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

// StartSharedTask creates a new shared task visible to all clients.
// The returned context carries the shared task so that SubTask, Output,
// and TaskProgress calls on it automatically create mirrored nodes in
// the shared task system (dual-send to the requesting client and broadcast).
//
// The task uses the provided context for cancellation. If the client
// disconnects and the request context is canceled, the task is canceled too.
// To make a task survive disconnection, pass context.WithoutCancel(ctx).
//
// When called inside a handler, the task lifecycle is managed automatically:
// returning nil completes the task, returning an error fails it.
func StartSharedTask[M any](ctx context.Context, title string) (context.Context, *SharedTask[M]) {
	conn := Connection(ctx)
	if conn == nil {
		return ctx, nil
	}
	tm := conn.server.taskManager
	if tm == nil {
		return ctx, nil
	}
	core := tm.create(title, conn.ID(), true, ctx)

	// Populate the task slot so handleRequest can auto-manage lifecycle.
	if slot := taskSlotFromContext(ctx); slot != nil {
		slot.sharedCore = core
	}

	// Enrich context with sharedContext so SubTask/Output/TaskProgress dual-send.
	ctx = withSharedContext(ctx, &sharedContext{core: core})

	return ctx, &SharedTask[M]{core: core}
}

// SharedSubTask bridges the request-scoped (SubTask) and shared task systems.
// It creates a node in the request-scoped task tree (progress to the requester)
// AND in the shared task system (broadcast to all clients).
//
// If a sharedContext already exists on ctx (e.g. from a parent SharedSubTask or
// SharedTask.WithContext), it delegates to SubTask which handles dual-send.
//
// Otherwise, it creates a new sharedTaskCore (top-level shared task), attaches
// a sharedContext to the context, runs fn, and then closes or fails the core
// on return.
//
// If no connection or taskManager is available, it falls back to SubTask.
func SharedSubTask(ctx context.Context, title string, fn func(ctx context.Context) error) error {
	// If we're already inside a shared context, just delegate to SubTask
	// which will handle dual-send via the sharedContext on ctx.
	if sc := sharedCtxFromContext(ctx); sc != nil {
		return SubTask(ctx, title, fn)
	}

	// Try to create a new shared task core.
	conn := Connection(ctx)
	var tm *taskManager
	if conn != nil {
		tm = conn.server.taskManager
	}

	if tm == nil {
		// No shared task system available — fall back to SubTask.
		return SubTask(ctx, title, fn)
	}

	// Create a new shared task core (not top-level, so isOwner stays false).
	core := tm.create(title, conn.ID(), false, ctx)
	sc := &sharedContext{core: core}
	childCtx := withSharedContext(ctx, sc)

	err := SubTask(childCtx, title, fn)

	// Close or fail the shared task core based on the result.
	if err != nil {
		core.fail(err.Error())
	} else {
		core.closeTask()
	}

	return err
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
