package tasks

import (
	"context"
	"io"

	"github.com/marrasen/aprot"
)

// TaskOption configures a task created by StartTask.
type TaskOption func(*taskOptions)

type taskOptions struct {
	shared bool
}

// Shared makes the task visible to all connected clients (broadcast).
// Without this option, the task is only visible to the requesting client.
func Shared() TaskOption {
	return func(o *taskOptions) {
		o.shared = true
	}
}

// Task is a type-safe, generic wrapper around a task node.
// It works for both request-scoped and shared tasks.
type Task[M any] struct {
	node *taskNode
}

// ID returns the task's unique identifier.
func (t *Task[M]) ID() string {
	return t.node.id
}

// Progress updates the task's progress counters.
func (t *Task[M]) Progress(current, total int) {
	t.node.progress(current, total)
}

// SetMeta sets typed metadata on the task.
func (t *Task[M]) SetMeta(v M) {
	t.node.mu.Lock()
	t.node.meta = v
	t.node.mu.Unlock()
	t.node.delivery.sendSnapshot(nil)
}

// Output sends output text associated with this task.
func (t *Task[M]) Output(msg string) {
	t.node.output(msg)
}

// Close marks the task as completed.
func (t *Task[M]) Close() {
	if t.node.IsShared() {
		t.node.completeTop()
	} else {
		t.node.setStatus(TaskNodeStatusCompleted)
		t.node.delivery.sendSnapshot(nil)
	}
}

// Fail marks the task as failed with the given error message.
func (t *Task[M]) Fail(message string) {
	if t.node.IsShared() {
		t.node.failTop(message)
	} else {
		t.node.setFailed(message)
		t.node.delivery.sendSnapshot(nil)
	}
}

// Err fails the task with err.Error() if err is non-nil, or completes it if nil.
func (t *Task[M]) Err(err error) {
	if err != nil {
		t.Fail(err.Error())
	} else {
		t.Close()
	}
}

// SubTask creates a child node under this task.
func (t *Task[M]) SubTask(title string) *TaskSub[M] {
	child := t.node.createChild(title)
	return &TaskSub[M]{node: child}
}

// Context returns the task's context (for shared tasks, the cancellable task context).
func (t *Task[M]) Context() context.Context {
	if t.node.ctx != nil {
		return t.node.ctx
	}
	return context.Background()
}

// WithContext returns a new context that carries this task's delivery and node.
func (t *Task[M]) WithContext(ctx context.Context) context.Context {
	ctx = withDelivery(ctx, t.node.delivery)
	return withTaskNode(ctx, t.node)
}

// IsShared returns true if this task broadcasts to all clients.
func (t *Task[M]) IsShared() bool {
	return t.node.IsShared()
}

// TaskSub is a type-safe, generic child node of a Task.
type TaskSub[M any] struct {
	node *taskNode
}

// Close marks this sub-task as completed.
func (s *TaskSub[M]) Close() {
	s.node.closeNode()
}

// Fail marks this sub-task as failed with the given error message.
func (s *TaskSub[M]) Fail(message string) {
	s.node.failNode(message)
}

// Err fails the sub-task with err.Error() if err is non-nil, or completes it if nil.
func (s *TaskSub[M]) Err(err error) {
	if err != nil {
		s.Fail(err.Error())
	} else {
		s.Close()
	}
}

// SetMeta sets typed metadata on this sub-task node.
func (s *TaskSub[M]) SetMeta(v M) {
	s.node.mu.Lock()
	s.node.meta = v
	s.node.mu.Unlock()
	s.node.delivery.sendSnapshot(nil)
}

// SubTask creates a child node under this sub-task.
func (s *TaskSub[M]) SubTask(title string) *TaskSub[M] {
	child := s.node.createChild(title)
	return &TaskSub[M]{node: child}
}

// Progress updates the sub-task's progress.
func (s *TaskSub[M]) Progress(current, total int) {
	s.node.progress(current, total)
}

// SubTask creates a child task under the current task node, runs fn, and marks
// the sub-task as completed (or failed if fn returns an error).
func SubTask(ctx context.Context, title string, fn func(ctx context.Context) error) error {
	node := taskNodeFromContext(ctx)
	d := deliveryFromContext(ctx)

	if d == nil {
		return fn(ctx)
	}

	var parent *taskNode
	if node != nil {
		parent = node
	} else if rd, ok := d.(*requestDelivery); ok {
		parent = ensureRoot(rd)
	} else {
		// Shared delivery without a node — shouldn't normally happen,
		// but handle gracefully.
		return fn(ctx)
	}

	child := parent.createChild(title)
	childCtx := withTaskNode(ctx, child)
	err := fn(childCtx)

	if err != nil {
		child.failNode(err.Error())
	} else {
		child.closeNode()
	}

	return err
}

// Output sends a text output message attached to the nearest task node.
func Output(ctx context.Context, msg string) {
	node := taskNodeFromContext(ctx)
	if node != nil {
		node.output(msg)
		return
	}
	d := deliveryFromContext(ctx)
	if d == nil {
		return
	}
	// No node but have delivery — send with empty task ID for request-scoped.
	if !d.isShared() {
		d.sendOutput("", msg)
	}
}

// TaskProgress sets the progress (current/total) on the current task node.
func TaskProgress(ctx context.Context, current, total int) {
	node := taskNodeFromContext(ctx)
	if node != nil {
		node.progress(current, total)
	}
}

// StepTaskProgress increments the current progress on the current task node by step.
func StepTaskProgress(ctx context.Context, step int) {
	node := taskNodeFromContext(ctx)
	if node != nil {
		cur, tot := node.stepProgress(step)
		node.delivery.sendProgress(node.id, cur, tot)
	}
}

// OutputWriter returns an io.WriteCloser that creates a child task node
// and sends each Write as output attached to that node.
func OutputWriter(ctx context.Context, title string) io.WriteCloser {
	node := taskNodeFromContext(ctx)
	d := deliveryFromContext(ctx)

	if d == nil {
		return discardWriteCloser{}
	}

	var parent *taskNode
	if node != nil {
		parent = node
	} else if rd, ok := d.(*requestDelivery); ok {
		parent = ensureRoot(rd)
	} else {
		return discardWriteCloser{}
	}

	child := parent.createChild(title)
	return &outputWriter{node: child}
}

// WriterProgress returns an io.WriteCloser that creates a child task node
// tracking bytes written as progress (current/total).
func WriterProgress(ctx context.Context, title string, size int) io.WriteCloser {
	node := taskNodeFromContext(ctx)
	d := deliveryFromContext(ctx)

	if d == nil {
		return discardWriteCloser{}
	}

	var parent *taskNode
	if node != nil {
		parent = node
	} else if rd, ok := d.(*requestDelivery); ok {
		parent = ensureRoot(rd)
	} else {
		return discardWriteCloser{}
	}

	child := parent.createChild(title)
	child.mu.Lock()
	child.total = size
	child.mu.Unlock()
	return &progressWriter{node: child, total: size}
}

// StartTask creates a task. By default it is request-scoped (visible only to
// the calling client). Pass Shared() to make it visible to all clients.
func StartTask[M any](ctx context.Context, title string, opts ...TaskOption) (context.Context, *Task[M]) {
	var o taskOptions
	for _, opt := range opts {
		opt(&o)
	}

	if o.shared {
		return startSharedTask[M](ctx, title)
	}
	return startRequestTask[M](ctx, title)
}

func startRequestTask[M any](ctx context.Context, title string) (context.Context, *Task[M]) {
	d := deliveryFromContext(ctx)
	if d == nil {
		return ctx, nil
	}
	rd, ok := d.(*requestDelivery)
	if !ok {
		return ctx, nil
	}

	root := ensureRoot(rd)

	node := &taskNode{
		delivery: d,
		id:       d.allocID(),
		title:    title,
		status:   TaskNodeStatusCreated,
	}
	root.addChild(node)

	if slot := taskSlotFromContext(ctx); slot != nil {
		slot.node = node
	}

	ctx = withTaskNode(ctx, node)
	d.sendSnapshot(nil)

	node.setStatus(TaskNodeStatusRunning)
	return ctx, &Task[M]{node: node}
}

func startSharedTask[M any](ctx context.Context, title string) (context.Context, *Task[M]) {
	conn := aprot.Connection(ctx)
	if conn == nil {
		return ctx, nil
	}
	tm := taskManagerFromContext(ctx)
	if tm == nil {
		return ctx, nil
	}
	node := tm.create(title, conn.ID(), true, ctx)
	tm.broadcastNow() // first message shows CREATED
	node.mu.Lock()
	node.status = TaskNodeStatusRunning
	node.mu.Unlock()

	if slot := taskSlotFromContext(ctx); slot != nil {
		slot.node = node
	}

	// Use the task's own cancellable context, with delivery and node set.
	taskCtx := withDelivery(node.ctx, node.delivery)
	taskCtx = withTaskNode(taskCtx, node)

	return taskCtx, &Task[M]{node: node}
}

// SharedSubTask bridges the request-scoped and shared task systems.
func SharedSubTask(ctx context.Context, title string, fn func(ctx context.Context) error) error {
	// If already in a shared context (node is shared), route through SubTask.
	if node := taskNodeFromContext(ctx); node != nil && node.IsShared() {
		return SubTask(ctx, title, fn)
	}

	conn := aprot.Connection(ctx)
	var tm *taskManager
	if conn != nil {
		tm = taskManagerFromContext(ctx)
	}

	if tm == nil {
		return SubTask(ctx, title, fn)
	}

	node := tm.create(title, conn.ID(), false, ctx)
	tm.broadcastNow() // first message shows CREATED
	node.mu.Lock()
	node.status = TaskNodeStatusRunning
	node.mu.Unlock()

	childCtx := withDelivery(node.ctx, node.delivery)
	childCtx = withTaskNode(childCtx, node)

	err := SubTask(childCtx, title, fn)

	if err != nil {
		node.failTop(err.Error())
	} else {
		node.completeTop()
	}

	return err
}

// CancelSharedTask cancels a shared task by ID.
func CancelSharedTask(ctx context.Context, taskID string) error {
	tm := taskManagerFromContext(ctx)
	if tm == nil {
		return aprot.NewError(aprot.CodeInternalError, "tasks not enabled")
	}
	if !tm.cancelTask(taskID) {
		return aprot.NewError(aprot.CodeInvalidParams, "task not found: "+taskID)
	}
	return nil
}
